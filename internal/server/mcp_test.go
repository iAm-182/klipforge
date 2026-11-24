package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestMCP_StreamableHTTP_RBACAndTools(t *testing.T) {
	srv, _, h := setupPhase4TestServer(t)

	// 1. Unauthenticated POST /api/mcp -> 401
	unauth := httptest.NewRequest(http.MethodPost, "/api/mcp",
		bytes.NewBufferString(`{"jsonrpc":"2.0","id":1,"method":"initialize"}`))
	recUnauth := httptest.NewRecorder()
	h.ServeHTTP(recUnauth, unauth)
	if recUnauth.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 for unauthenticated /api/mcp, got %d", recUnauth.Code)
	}

	// Helper to post JSON-RPC as Agent (X-Agent-Key)
	callMCP := func(body string) (int, map[string]interface{}) {
		req := httptest.NewRequest(http.MethodPost, "/api/mcp", bytes.NewBufferString(body))
		req.Header.Set("X-Agent-Key", srv.AgentKey)
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		var out map[string]interface{}
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return rec.Code, out
	}

	// 2. initialize -> 200 with klipforge-mcp serverInfo
	c1, r1 := callMCP(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26"}}`)
	if c1 != http.StatusOK {
		t.Fatalf("initialize expected 200, got %d", c1)
	}
	res1, _ := r1["result"].(map[string]interface{})
	sInfo, _ := res1["serverInfo"].(map[string]interface{})
	if sInfo["name"] != "klipforge-mcp" {
		t.Fatalf("unexpected serverInfo: %+v", sInfo)
	}

	// 3. tools/list -> 18 tools, ZERO destructive/admin/review tools
	c2, r2 := callMCP(`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`)
	if c2 != http.StatusOK {
		t.Fatalf("tools/list expected 200, got %d", c2)
	}
	res2, _ := r2["result"].(map[string]interface{})
	tools, _ := res2["tools"].([]interface{})
	if len(tools) != 18 {
		t.Fatalf("expected 18 MCP tools, got %d", len(tools))
	}
	for _, raw := range tools {
		tm, _ := raw.(map[string]interface{})
		name, _ := tm["name"].(string)
		lower := strings.ToLower(name)
		if strings.Contains(lower, "delete") || strings.Contains(lower, "review") || strings.Contains(lower, "approve") || strings.Contains(lower, "team") || strings.Contains(lower, "refresh_cookie") {
			t.Fatalf("least-privilege violation: forbidden tool exposed in tools/list: %s", name)
		}
	}

	// 4. tools/call -> klipforge_home_metrics, klipforge_cookie_status, klipforge_list_board, klipforge_detect_facecam
	for _, tc := range []string{
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"klipforge_home_metrics","arguments":{}}}`,
		`{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"klipforge_cookie_status","arguments":{}}}`,
		`{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"klipforge_list_board","arguments":{}}}`,
		`{"jsonrpc":"2.0","id":6,"method":"tools/call","params":{"name":"klipforge_detect_facecam","arguments":{"projectId":"proj-test-windah"}}}`,
	} {
		code, resp := callMCP(tc)
		if code != http.StatusOK || resp["error"] != nil {
			t.Fatalf("tool call failed (%s): code=%d resp=%+v", tc, code, resp)
		}
		res, _ := resp["result"].(map[string]interface{})
		if isErr, _ := res["isError"].(bool); isErr {
			t.Fatalf("unexpected tool isError=true for %s: %+v", tc, res)
		}
	}

	// 5. Unknown tool -> JSON-RPC -32601 error
	cBad, rBad := callMCP(`{"jsonrpc":"2.0","id":7,"method":"tools/call","params":{"name":"klipforge_delete_everything","arguments":{}}}`)
	if cBad != http.StatusOK || rBad["error"] == nil {
		t.Fatalf("expected JSON-RPC error for unknown tool, got code=%d resp=%+v", cBad, rBad)
	}
	errObj, _ := rBad["error"].(map[string]interface{})
	if int(errObj["code"].(float64)) != -32601 {
		t.Fatalf("expected -32601 for unknown tool, got %+v", errObj)
	}
}
