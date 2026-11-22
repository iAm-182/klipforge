package server

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	"klipforge/internal/deps"
	"klipforge/internal/events"
	"klipforge/internal/store"
)

func TestServerSecurity_AuthAndCORS(t *testing.T) {
	tmp := t.TempDir()
	st, err := store.NewStore(tmp)
	if err != nil {
		t.Fatalf("NewStore failed: %v", err)
	}
	srv := New(st, deps.NewManager(tmp), events.NewBroker())
	if len(srv.EngineToken) < 16 {
		t.Fatalf("expected auto-generated EngineToken >= 16 chars, got %q", srv.EngineToken)
	}
	h := srv.Handler()

	// 1. No token -> 401 Unauthorized
	req1 := httptest.NewRequest(http.MethodGet, "/api/status", nil)
	rec1 := httptest.NewRecorder()
	h.ServeHTTP(rec1, req1)
	if rec1.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 without token, got %d", rec1.Code)
	}

	// 2. Wrong token -> 401 Unauthorized
	req2 := httptest.NewRequest(http.MethodGet, "/api/status", nil)
	req2.Header.Set("X-Engine-Token", "wrong-token-123456")
	rec2 := httptest.NewRecorder()
	h.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 with wrong token, got %d", rec2.Code)
	}

	// 3. Valid X-Engine-Token -> 200 OK
	req3 := httptest.NewRequest(http.MethodGet, "/api/status", nil)
	req3.Header.Set("X-Engine-Token", srv.EngineToken)
	rec3 := httptest.NewRecorder()
	h.ServeHTTP(rec3, req3)
	if rec3.Code != http.StatusOK {
		t.Fatalf("expected 200 with valid token, got %d", rec3.Code)
	}

	// 4. Disallowed CORS origin must NOT receive Access-Control-Allow-Origin
	req4 := httptest.NewRequest(http.MethodGet, "/api/status", nil)
	req4.Header.Set("Origin", "https://evil.example.com")
	req4.Header.Set("X-Engine-Token", srv.EngineToken)
	rec4 := httptest.NewRecorder()
	h.ServeHTTP(rec4, req4)
	if got := rec4.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Fatalf("expected empty CORS header for evil origin, got %q", got)
	}

	// 5. Allowed CORS origin receives exact origin (not *)
	req5 := httptest.NewRequest(http.MethodGet, "/api/status", nil)
	req5.Header.Set("Origin", "https://klip.breakthematrix.id")
	req5.Header.Set("X-Engine-Token", srv.EngineToken)
	rec5 := httptest.NewRecorder()
	h.ServeHTTP(rec5, req5)
	if got := rec5.Header().Get("Access-Control-Allow-Origin"); got != "https://klip.breakthematrix.id" {
		t.Fatalf("expected exact allowed origin, got %q", got)
	}

	// 6. Localhost unlock sets HttpOnly cookie that grants access
	unlockReq := httptest.NewRequest(http.MethodPost, "/api/auth/unlock", bytes.NewBufferString(`{"token":"`+srv.EngineToken+`"}`))
	unlockRec := httptest.NewRecorder()
	h.ServeHTTP(unlockRec, unlockReq)
	if unlockRec.Code != http.StatusOK {
		t.Fatalf("expected 200 from /api/auth/unlock, got %d", unlockRec.Code)
	}
	cookies := unlockRec.Result().Cookies()
	if len(cookies) == 0 || cookies[0].Name != "kf_token" {
		t.Fatalf("expected kf_token cookie from unlock")
	}
	req6 := httptest.NewRequest(http.MethodGet, "/api/status", nil)
	req6.AddCookie(cookies[0])
	rec6 := httptest.NewRecorder()
	h.ServeHTTP(rec6, req6)
	if rec6.Code != http.StatusOK {
		t.Fatalf("expected 200 using kf_token cookie, got %d", rec6.Code)
	}

	// 7. Scoped X-Agent-Key (Muse assistant): allowed read/search/render, DENIED 403 on delete/settings/cleanup!
	if len(srv.AgentKey) < 16 || srv.AgentKey == srv.EngineToken {
		t.Fatalf("expected distinct auto-generated AgentKey >= 16 chars")
	}

	// Wrong agent key -> 401
	aWrong := httptest.NewRequest(http.MethodGet, "/api/status", nil)
	aWrong.Header.Set("X-Agent-Key", "bad-agent-key")
	recAW := httptest.NewRecorder()
	h.ServeHTTP(recAW, aWrong)
	if recAW.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 for wrong X-Agent-Key, got %d", recAW.Code)
	}

	// Valid agent key on GET /api/status & GET /api/campaigns -> 200 OK
	for _, p := range []string{"/api/status", "/api/campaigns", "/api/projects", "/api/jobs"} {
		aGet := httptest.NewRequest(http.MethodGet, p, nil)
		aGet.Header.Set("X-Agent-Key", srv.AgentKey)
		recAG := httptest.NewRecorder()
		h.ServeHTTP(recAG, aGet)
		if recAG.Code != http.StatusOK {
			t.Fatalf("expected 200 for X-Agent-Key GET %s, got %d", p, recAG.Code)
		}
	}

	// Valid agent key on destructive / admin endpoints -> STRICT 403 Forbidden!
	forbiddenCases := []struct {
		method string
		path   string
	}{
		{http.MethodDelete, "/api/campaigns/windah-gtavi-motionklip"},
		{http.MethodDelete, "/api/projects/proj-123"},
		{http.MethodDelete, "/api/jobs/job-123"},
		{http.MethodPut, "/api/settings"},
		{http.MethodPost, "/api/storage/cleanup"},
		{http.MethodPost, "/api/jobs/job-123/review"},
		{http.MethodPost, "/api/open-output-folder"},
		{http.MethodPost, "/api/deps/update-ytdlp"},
	}
	for _, tc := range forbiddenCases {
		reqF := httptest.NewRequest(tc.method, tc.path, bytes.NewBufferString(`{}`))
		reqF.Header.Set("X-Agent-Key", srv.AgentKey)
		recF := httptest.NewRecorder()
		h.ServeHTTP(recF, reqF)
		if recF.Code != http.StatusForbidden {
			t.Fatalf("expected 403 Forbidden for X-Agent-Key %s %s, got %d", tc.method, tc.path, recF.Code)
		}
	}
}
