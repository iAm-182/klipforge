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

	// 3. tools/list -> 23 tools, ZERO destructive/admin/review tools (read-only klipforge_team_metrics allowed)
	c2, r2 := callMCP(`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`)
	if c2 != http.StatusOK {
		t.Fatalf("tools/list expected 200, got %d", c2)
	}
	res2, _ := r2["result"].(map[string]interface{})
	tools, _ := res2["tools"].([]interface{})
	if len(tools) != 23 {
		t.Fatalf("expected 23 MCP tools, got %d", len(tools))
	}
	for _, raw := range tools {
		tm, _ := raw.(map[string]interface{})
		name, _ := tm["name"].(string)
		lower := strings.ToLower(name)
		if strings.Contains(lower, "delete") || strings.Contains(lower, "review") || strings.Contains(lower, "approve") ||
			(strings.Contains(lower, "team") && lower != "klipforge_team_metrics") ||
			strings.Contains(lower, "refresh_cookie") || strings.Contains(lower, "publish_account") {
			t.Fatalf("least-privilege violation: forbidden tool exposed in tools/list: %s", name)
		}
	}

	// 4. tools/call -> core + new tools (including search_transcript, activity, team_metrics)
	for _, tc := range []string{
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"klipforge_home_metrics","arguments":{}}}`,
		`{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"klipforge_cookie_status","arguments":{}}}`,
		`{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"klipforge_list_board","arguments":{}}}`,
		`{"jsonrpc":"2.0","id":6,"method":"tools/call","params":{"name":"klipforge_detect_facecam","arguments":{"projectId":"proj-test-windah"}}}`,
		`{"jsonrpc":"2.0","id":7,"method":"tools/call","params":{"name":"klipforge_search_transcript","arguments":{"projectId":"proj-test-windah","query":"gta"}}}`,
		`{"jsonrpc":"2.0","id":8,"method":"tools/call","params":{"name":"klipforge_activity","arguments":{"limit":10}}}`,
		`{"jsonrpc":"2.0","id":9,"method":"tools/call","params":{"name":"klipforge_team_metrics","arguments":{}}}`,
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

	// 5. Path-escape safety: weird characters in projectId must NEVER panic
	cEsc, rEsc := callMCP(`{"jsonrpc":"2.0","id":10,"method":"tools/call","params":{"name":"klipforge_get_job","arguments":{"jobId":"../bad job?x=1#frag"}}}`)
	if cEsc != http.StatusOK || rEsc["error"] != nil {
		t.Fatalf("expected graceful JSON-RPC result on bad jobId, got code=%d resp=%+v", cEsc, rEsc)
	}

	// 6. JSON-RPC 2.0 batch array + GET /api/mcp + DELETE /api/mcp
	batchReq := httptest.NewRequest(http.MethodPost, "/api/mcp", bytes.NewBufferString(`[{"jsonrpc":"2.0","id":11,"method":"ping"},{"jsonrpc":"2.0","method":"notifications/initialized"}]`))
	batchReq.Header.Set("X-Agent-Key", srv.AgentKey)
	batchRec := httptest.NewRecorder()
	h.ServeHTTP(batchRec, batchReq)
	if batchRec.Code != http.StatusOK {
		t.Fatalf("expected 200 for MCP batch, got %d", batchRec.Code)
	}
	var batchOut []map[string]interface{}
	if err := json.Unmarshal(batchRec.Body.Bytes(), &batchOut); err != nil || len(batchOut) != 1 {
		t.Fatalf("expected 1 batch response (notification omitted), got err=%v out=%+v", err, batchOut)
	}

	getReq := httptest.NewRequest(http.MethodGet, "/api/mcp", nil)
	getReq.Header.Set("X-Agent-Key", srv.AgentKey)
	getRec := httptest.NewRecorder()
	h.ServeHTTP(getRec, getReq)
	if getRec.Code != http.StatusOK {
		t.Fatalf("expected 200 for GET /api/mcp, got %d", getRec.Code)
	}

	delReq := httptest.NewRequest(http.MethodDelete, "/api/mcp", nil)
	delReq.Header.Set("X-Agent-Key", srv.AgentKey)
	delRec := httptest.NewRecorder()
	h.ServeHTTP(delRec, delReq)
	if delRec.Code != http.StatusNoContent {
		t.Fatalf("expected 204 for DELETE /api/mcp, got %d", delRec.Code)
	}

	// 7. Unknown tool -> JSON-RPC -32601 error
	cBad, rBad := callMCP(`{"jsonrpc":"2.0","id":12,"method":"tools/call","params":{"name":"klipforge_delete_everything","arguments":{}}}`)
	if cBad != http.StatusOK || rBad["error"] == nil {
		t.Fatalf("expected JSON-RPC error for unknown tool, got code=%d resp=%+v", cBad, rBad)
	}
	errObj, _ := rBad["error"].(map[string]interface{})
	if int(errObj["code"].(float64)) != -32601 {
		t.Fatalf("expected -32601 for unknown tool, got %+v", errObj)
	}
}
