package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"klipforge/internal/deps"
	"klipforge/internal/events"
	"klipforge/internal/model"
	"klipforge/internal/store"
)

func newTestDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "klipforge-srv-test-*")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	t.Cleanup(func() {
		// Background audio synthesis may briefly hold a wav handle on Windows; best-effort cleanup.
		_ = os.RemoveAll(dir)
	})
	return dir
}

func TestServerSecurity_AuthAndCORS(t *testing.T) {
	tmp := newTestDir(t)
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
		{http.MethodGet, "/api/team"},
		{http.MethodPost, "/api/team"},
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

func TestServerTeamMode_AccessKey_AgentKey_RBAC_And_Quota(t *testing.T) {
	tmp := newTestDir(t)
	st, err := store.NewStore(tmp)
	if err != nil {
		t.Fatalf("NewStore failed: %v", err)
	}
	srv := New(st, deps.NewManager(tmp), events.NewBroker())
	h := srv.Handler()

	// 1. Owner creates a Clipper (Budi) and a QC Reviewer (Raka)
	createMember := func(name, role string, maxActive int) model.TeamMember {
		t.Helper()
		body, _ := json.Marshal(map[string]interface{}{"name": name, "role": role, "maxActive": maxActive})
		req := httptest.NewRequest(http.MethodPost, "/api/team", bytes.NewReader(body))
		req.Header.Set("X-Engine-Token", srv.EngineToken)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusCreated {
			t.Fatalf("create %s failed: HTTP %d %s", name, rec.Code, rec.Body.String())
		}
		var resp struct {
			Member model.TeamMember `json:"member"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatalf("decode member: %v", err)
		}
		if len(resp.Member.AccessKey) < 20 || len(resp.Member.AgentKey) < 20 {
			t.Fatalf("expected generated accessKey & agentKey for %s", name)
		}
		return resp.Member
	}

	budi := createMember("Budi", "clipper", 2)
	raka := createMember("Raka", "qc", 3)

	// 2. Budi logs in via X-Access-Key -> /api/auth/me returns Budi (clipper)
	meReq := httptest.NewRequest(http.MethodGet, "/api/auth/me", nil)
	meReq.Header.Set("X-Access-Key", budi.AccessKey)
	meRec := httptest.NewRecorder()
	h.ServeHTTP(meRec, meReq)
	if meRec.Code != http.StatusOK {
		t.Fatalf("Budi /api/auth/me failed: %d", meRec.Code)
	}
	var budiPrincipal model.Principal
	_ = json.Unmarshal(meRec.Body.Bytes(), &budiPrincipal)
	if budiPrincipal.Name != "Budi" || budiPrincipal.Role != "clipper" || budiPrincipal.Via != "web" {
		t.Fatalf("unexpected Budi principal: %+v", budiPrincipal)
	}

	// 3. Budi's personal Muse uses Budi's AgentKey -> /api/auth/me returns Name=Budi, Role=agent, Via=agent!
	museReq := httptest.NewRequest(http.MethodGet, "/api/auth/me", nil)
	museReq.Header.Set("X-Agent-Key", budi.AgentKey)
	museRec := httptest.NewRecorder()
	h.ServeHTTP(museRec, museReq)
	if museRec.Code != http.StatusOK {
		t.Fatalf("Budi Muse /api/auth/me failed: %d", museRec.Code)
	}
	var musePrincipal model.Principal
	_ = json.Unmarshal(museRec.Body.Bytes(), &musePrincipal)
	if musePrincipal.ID != budi.ID || musePrincipal.Name != "Budi" || musePrincipal.Role != "agent" || musePrincipal.Via != "agent" {
		t.Fatalf("unexpected Budi Muse principal: %+v", musePrincipal)
	}

	// 4. Clipper RBAC: Budi CANNOT access /api/team, CANNOT change settings, CANNOT delete campaign, CANNOT self-approve review!
	budiForbidden := []struct{ m, p string }{
		{http.MethodGet, "/api/team"},
		{http.MethodPost, "/api/team"},
		{http.MethodPut, "/api/settings"},
		{http.MethodPost, "/api/storage/cleanup"},
		{http.MethodDelete, "/api/campaigns/windah-gtavi-motionklip"},
		{http.MethodDelete, "/api/projects/proj-1"},
		{http.MethodPost, "/api/jobs/job-budi-1/review"},
	}
	for _, tc := range budiForbidden {
		req := httptest.NewRequest(tc.m, tc.p, bytes.NewBufferString(`{"reviewStatus":"approved"}`))
		req.Header.Set("X-Access-Key", budi.AccessKey)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden {
			t.Fatalf("expected 403 for clipper Budi %s %s, got %d", tc.m, tc.p, rec.Code)
		}
	}

	// 5. Job ownership & QC review: seed 2 jobs (one by Budi, one by Owner)
	_ = st.SaveJob(&model.RenderJob{ID: "job-budi-1", Title: "Budi Clip", Status: "done", CreatedByID: budi.ID, CreatedBy: "Budi", CreatedAt: time.Now()})
	_ = st.SaveJob(&model.RenderJob{ID: "job-owner-1", Title: "Owner Clip", Status: "done", CreatedByID: "owner", CreatedBy: "Owner", CreatedAt: time.Now()})

	// Budi CANNOT delete Owner's job (403)
	delOwnerReq := httptest.NewRequest(http.MethodDelete, "/api/jobs/job-owner-1", nil)
	delOwnerReq.Header.Set("X-Access-Key", budi.AccessKey)
	delOwnerRec := httptest.NewRecorder()
	h.ServeHTTP(delOwnerRec, delOwnerReq)
	if delOwnerRec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 when clipper deletes another member's job, got %d", delOwnerRec.Code)
	}

	// QC Raka CAN approve Budi's job -> sets ReviewedBy = "Raka", but Raka CANNOT touch /api/team or /api/settings!
	revReq := httptest.NewRequest(http.MethodPost, "/api/jobs/job-budi-1/review", bytes.NewBufferString(`{"reviewStatus":"approved"}`))
	revReq.Header.Set("X-Access-Key", raka.AccessKey)
	revRec := httptest.NewRecorder()
	h.ServeHTTP(revRec, revReq)
	if revRec.Code != http.StatusOK {
		t.Fatalf("expected 200 when QC approves job, got %d: %s", revRec.Code, revRec.Body.String())
	}
	approvedJob, _ := st.GetJob("job-budi-1")
	if approvedJob.ReviewStatus != "approved" || approvedJob.ReviewedBy != "Raka" {
		t.Fatalf("expected approved by Raka, got %+v", approvedJob)
	}

	// Once approved, even Budi CANNOT delete his own approved clip (403)!
	delApprovedReq := httptest.NewRequest(http.MethodDelete, "/api/jobs/job-budi-1", nil)
	delApprovedReq.Header.Set("X-Access-Key", budi.AccessKey)
	delApprovedRec := httptest.NewRecorder()
	h.ServeHTTP(delApprovedRec, delApprovedReq)
	if delApprovedRec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 when deleting approved clip as clipper, got %d", delApprovedRec.Code)
	}

	// 6. Fair-Queue Limit (Budi has maxActive=2; seed 2 active jobs -> Budi AND Budi's Muse both get 429!)
	_ = st.SaveJob(&model.RenderJob{ID: "job-budi-a1", Status: "rendering", CreatedByID: budi.ID, CreatedAt: time.Now()})
	_ = st.SaveJob(&model.RenderJob{ID: "job-budi-a2", Status: "queued", CreatedByID: budi.ID, CreatedAt: time.Now()})
	qReq := httptest.NewRequest(http.MethodPost, "/api/jobs/render", bytes.NewBufferString(`{"projectId":"p1","startSec":0,"endSec":10}`))
	qReq.Header.Set("X-Agent-Key", budi.AgentKey)
	qRec := httptest.NewRecorder()
	h.ServeHTTP(qRec, qReq)
	if qRec.Code != http.StatusTooManyRequests {
		t.Fatalf("expected 429 fair-queue limit for Budi's Muse, got %d", qRec.Code)
	}

	// 7. Instant Revocation: Owner disables Budi -> BOTH AccessKey AND AgentKey immediately get 401!
	disReq := httptest.NewRequest(http.MethodPut, "/api/team/"+budi.ID, bytes.NewBufferString(`{"enabled":false}`))
	disReq.Header.Set("X-Engine-Token", srv.EngineToken)
	disRec := httptest.NewRecorder()
	h.ServeHTTP(disRec, disReq)
	if disRec.Code != http.StatusOK {
		t.Fatalf("disable Budi failed: %d", disRec.Code)
	}
	for _, pair := range [][2]string{{"X-Access-Key", budi.AccessKey}, {"X-Agent-Key", budi.AgentKey}} {
		rRev := httptest.NewRequest(http.MethodGet, "/api/status", nil)
		rRev.Header.Set(pair[0], pair[1])
		recRev := httptest.NewRecorder()
		h.ServeHTTP(recRev, rRev)
		if recRev.Code != http.StatusUnauthorized {
			t.Fatalf("expected instant 401 after disabling Budi (%s), got %d", pair[0], recRev.Code)
		}
	}
}

func TestJobsPaginationSummaryAndSingleGet(t *testing.T) {
	tmp := newTestDir(t)
	st, err := store.NewStore(tmp)
	if err != nil {
		t.Fatalf("NewStore failed: %v", err)
	}
	srv := New(st, deps.NewManager(tmp), events.NewBroker())
	h := srv.Handler()

	now := time.Now()
	_ = st.SaveJob(&model.RenderJob{ID: "j1", Title: "Clip 1", Status: "done", Progress: 100, PostCaption: "giant caption payload", CreatedAt: now.Add(-3 * time.Minute)})
	_ = st.SaveJob(&model.RenderJob{ID: "j2", Title: "Clip 2", Status: "rendering", Progress: 45, Stage: "Encoding", CreatedAt: now.Add(-2 * time.Minute)})
	_ = st.SaveJob(&model.RenderJob{ID: "j3", Title: "Clip 3", Status: "error", Error: "timeout", CreatedAt: now.Add(-1 * time.Minute)})

	// 1. ?status=done&summary=1
	req1 := httptest.NewRequest(http.MethodGet, "/api/jobs?status=done&summary=1&limit=10", nil)
	req1.Header.Set("X-Agent-Key", srv.AgentKey)
	rec1 := httptest.NewRecorder()
	h.ServeHTTP(rec1, req1)
	if rec1.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec1.Code)
	}
	if rec1.Header().Get("X-Total-Count") != "1" {
		t.Fatalf("expected X-Total-Count=1, got %q", rec1.Header().Get("X-Total-Count"))
	}
	if bytes.Contains(rec1.Body.Bytes(), []byte("giant caption payload")) {
		t.Fatalf("summary=1 should strip heavy postCaption/options fields!")
	}

	// 2. ?limit=2&offset=1
	req2 := httptest.NewRequest(http.MethodGet, "/api/jobs?limit=2&offset=1", nil)
	req2.Header.Set("X-Engine-Token", srv.EngineToken)
	rec2 := httptest.NewRecorder()
	h.ServeHTTP(rec2, req2)
	var page []model.RenderJob
	_ = json.Unmarshal(rec2.Body.Bytes(), &page)
	if len(page) != 2 || rec2.Header().Get("X-Total-Count") != "3" {
		t.Fatalf("expected 2 jobs with X-Total-Count=3, got len=%d total=%q", len(page), rec2.Header().Get("X-Total-Count"))
	}

	// 3. Single job GET /api/jobs/j2
	req3 := httptest.NewRequest(http.MethodGet, "/api/jobs/j2", nil)
	req3.Header.Set("X-Agent-Key", srv.AgentKey)
	rec3 := httptest.NewRecorder()
	h.ServeHTTP(rec3, req3)
	if rec3.Code != http.StatusOK {
		t.Fatalf("expected 200 on GET /api/jobs/j2, got %d", rec3.Code)
	}
	var got model.RenderJob
	_ = json.Unmarshal(rec3.Body.Bytes(), &got)
	if got.ID != "j2" || got.Progress != 45 {
		t.Fatalf("unexpected single job response: %+v", got)
	}
}
