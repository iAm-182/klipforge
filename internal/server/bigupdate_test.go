package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"klipforge/internal/model"
)

func TestB1_BoardMoveActivityPeekAndReviewSLA(t *testing.T) {
	srv, st, h := setupPhase4TestServer(t)

	// 1. Agent moves candidate -> verifikasi_frame (200 OK)
	mv1 := httptest.NewRequest(http.MethodPost, "/api/board/move",
		bytes.NewBufferString(`{"projectId":"proj-test-windah","candidateId":"cand-1","toColumn":"verifikasi_frame"}`))
	mv1.Header.Set("X-Agent-Key", srv.AgentKey)
	rec1 := httptest.NewRecorder()
	h.ServeHTTP(rec1, mv1)
	if rec1.Code != http.StatusOK {
		t.Fatalf("expected 200 on board/move verifikasi_frame, got %d: %s", rec1.Code, rec1.Body.String())
	}

	// 2. Invalid board column -> 400 Bad Request
	mvBad := httptest.NewRequest(http.MethodPost, "/api/board/move",
		bytes.NewBufferString(`{"projectId":"proj-test-windah","candidateId":"cand-1","toColumn":"kolom_ngawur"}`))
	mvBad.Header.Set("X-Agent-Key", srv.AgentKey)
	recBad := httptest.NewRecorder()
	h.ServeHTTP(recBad, mvBad)
	if recBad.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 on invalid board column, got %d", recBad.Code)
	}

	// 3. Seed a completed clip owned by QC member Raka -> test self-approve block (403) + Owner approve (200)
	raka := &model.TeamMember{ID: "mem-raka", Name: "Raka", Role: "qc", AccessKey: "kf_usr_raka_test_key_0001", AgentKey: "kf_agt_raka_test_key_0001", Enabled: true, MaxActive: 3}
	_ = st.SaveTeamMember(raka)
	doneAt := time.Now().Add(-8 * time.Hour) // 8h old -> amber SLA
	_ = st.SaveJob(&model.RenderJob{
		ID:           "job-raka-1",
		ProjectID:    "proj-test-windah",
		CampaignID:   "windah-gtavi-motionklip",
		CampaignName: "Windah Basudara — GTA VI",
		Title:        "Raka Clip Nabrak",
		Status:       "done",
		ReviewStatus: "pending_review",
		BoardColumn:  "review",
		CreatedByID:  raka.ID,
		CreatedBy:    raka.Name,
		CreatedVia:   "web",
		Compliance:   model.ComplianceCheck{WatermarkApplied: true, OutroApplied: true, OutroClean: true, HasValueAdd: true, HashtagsReady: true},
		CreatedAt:    doneAt,
		CompletedAt:  &doneAt,
	})

	// Raka CANNOT self-approve his own clip via /review or /board/move!
	selfRev := httptest.NewRequest(http.MethodPost, "/api/jobs/job-raka-1/review",
		bytes.NewBufferString(`{"reviewStatus":"approved"}`))
	selfRev.Header.Set("X-Access-Key", raka.AccessKey)
	recSelf := httptest.NewRecorder()
	h.ServeHTTP(recSelf, selfRev)
	if recSelf.Code != http.StatusForbidden {
		t.Fatalf("expected 403 blocking QC self-approve, got %d: %s", recSelf.Code, recSelf.Body.String())
	}

	// Agent CANNOT move job to siap_posting (QC/Owner only -> 403)
	agApp := httptest.NewRequest(http.MethodPost, "/api/board/move",
		bytes.NewBufferString(`{"jobId":"job-raka-1","toColumn":"siap_posting"}`))
	agApp.Header.Set("X-Agent-Key", srv.AgentKey)
	recAgApp := httptest.NewRecorder()
	h.ServeHTTP(recAgApp, agApp)
	if recAgApp.Code != http.StatusForbidden {
		t.Fatalf("expected 403 when agent tries moving job to siap_posting, got %d", recAgApp.Code)
	}

	// Home metrics reports 8h-old pending review with amber SLA!
	hmReq := httptest.NewRequest(http.MethodGet, "/api/metrics/home", nil)
	hmReq.Header.Set("X-Agent-Key", srv.AgentKey)
	hmRec := httptest.NewRecorder()
	h.ServeHTTP(hmRec, hmReq)
	if hmRec.Code != http.StatusOK || !bytes.Contains(hmRec.Body.Bytes(), []byte(`"slaLevel":"amber"`)) {
		t.Fatalf("expected amber SLA in /api/metrics/home, got %d: %s", hmRec.Code, hmRec.Body.String())
	}

	// Owner approves via board/move -> siap_posting + auto-generates PublishKit + logs Activity!
	ownApp := httptest.NewRequest(http.MethodPost, "/api/board/move",
		bytes.NewBufferString(`{"jobId":"job-raka-1","toColumn":"siap_posting"}`))
	ownApp.Header.Set("X-Engine-Token", srv.EngineToken)
	recOwn := httptest.NewRecorder()
	h.ServeHTTP(recOwn, ownApp)
	if recOwn.Code != http.StatusOK {
		t.Fatalf("expected 200 on owner approve board/move, got %d: %s", recOwn.Code, recOwn.Body.String())
	}

	// Activity feed returns persisted events
	actReq := httptest.NewRequest(http.MethodGet, "/api/activity?limit=10", nil)
	actReq.Header.Set("X-Agent-Key", srv.AgentKey)
	actRec := httptest.NewRecorder()
	h.ServeHTTP(actRec, actReq)
	if actRec.Code != http.StatusOK || !bytes.Contains(actRec.Body.Bytes(), []byte("Siap Posting")) {
		t.Fatalf("expected persisted activity entry, got %d: %s", actRec.Code, actRec.Body.String())
	}

	// Peek metadata endpoint works for agent (GET /api/jobs/job-raka-1/peek?meta=1)
	pkReq := httptest.NewRequest(http.MethodGet, "/api/jobs/job-raka-1/peek?meta=1", nil)
	pkReq.Header.Set("X-Agent-Key", srv.AgentKey)
	pkRec := httptest.NewRecorder()
	h.ServeHTTP(pkRec, pkReq)
	if pkRec.Code != http.StatusOK {
		t.Fatalf("expected 200 on peek?meta=1, got %d: %s", pkRec.Code, pkRec.Body.String())
	}

	// Team metrics (safe non-secret) accessible by clipper/agent, while /api/team stays 403!
	tmReq := httptest.NewRequest(http.MethodGet, "/api/team/metrics", nil)
	tmReq.Header.Set("X-Agent-Key", srv.AgentKey)
	tmRec := httptest.NewRecorder()
	h.ServeHTTP(tmRec, tmReq)
	if tmRec.Code != http.StatusOK {
		t.Fatalf("expected 200 on /api/team/metrics, got %d", tmRec.Code)
	}
}

func TestB2_PublishKitAndApprovalOAuthGates(t *testing.T) {
	srv, st, h := setupPhase4TestServer(t)

	_ = st.SaveJob(&model.RenderJob{
		ID:           "job-pub-1",
		ProjectID:    "proj-test-windah",
		CampaignID:   "windah-gtavi-motionklip",
		CampaignName: "Windah Basudara — GTA VI",
		Title:        "Windah Nabrak Tembok GTA VI",
		Options:      model.RenderOptions{CandidateID: "cand-2", HookText: "GUA JAGO NYETIR — DETIK DETIK NABRAK!"},
		Status:       "done",
		ReviewStatus: "pending_review",
		Compliance:   model.ComplianceCheck{WatermarkApplied: true, OutroApplied: true, OutroClean: true, HasValueAdd: true, HashtagsReady: true},
		CreatedAt:    time.Now(),
	})

	// 1. Agent generates per-platform publish kit -> 200 OK (TikTok + Shorts <=100 chars + Reels + 4-check SOP)
	pkReq := httptest.NewRequest(http.MethodPost, "/api/jobs/job-pub-1/publish-kit", bytes.NewBufferString(`{}`))
	pkReq.Header.Set("X-Agent-Key", srv.AgentKey)
	pkRec := httptest.NewRecorder()
	h.ServeHTTP(pkRec, pkReq)
	if pkRec.Code != http.StatusOK {
		t.Fatalf("expected 200 on publish-kit, got %d: %s", pkRec.Code, pkRec.Body.String())
	}
	var pkResp struct {
		PublishKit model.PublishKit `json:"publishKit"`
	}
	_ = json.Unmarshal(pkRec.Body.Bytes(), &pkResp)
	if len([]rune(pkResp.PublishKit.Shorts.Title)) > 100 || pkResp.PublishKit.TikTok.Caption == "" || pkResp.PublishKit.Reels.Caption == "" {
		t.Fatalf("unexpected publish kit variants: %+v", pkResp.PublishKit)
	}
	if !pkResp.PublishKit.Checklist.ReadyToPost {
		t.Fatalf("expected all 4 SOP checklist marks ready, got %+v", pkResp.PublishKit.Checklist)
	}

	// 2. Forbidden word in publish caption -> 400 Bad Request!
	badPK := httptest.NewRequest(http.MethodPost, "/api/jobs/job-pub-1/publish-kit",
		bytes.NewBufferString(`{"tiktokCaption":"PROMO JUDOL WINDAH"}`))
	badPK.Header.Set("X-Agent-Key", srv.AgentKey)
	recBadPK := httptest.NewRecorder()
	h.ServeHTTP(recBadPK, badPK)
	if recBadPK.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for forbidden word in publish caption, got %d: %s", recBadPK.Code, recBadPK.Body.String())
	}

	// 3. Publish WITHOUT QC approval -> STRICT 409 Conflict!
	unappReq := httptest.NewRequest(http.MethodPost, "/api/jobs/job-pub-1/publish",
		bytes.NewBufferString(`{"platform":"youtube"}`))
	unappReq.Header.Set("X-Agent-Key", srv.AgentKey)
	unappRec := httptest.NewRecorder()
	h.ServeHTTP(unappRec, unappReq)
	if unappRec.Code != http.StatusConflict {
		t.Fatalf("expected 409 Conflict when publishing unapproved clip, got %d: %s", unappRec.Code, unappRec.Body.String())
	}

	// 4. Approve clip, then agent calls YouTube publish with NO OAuth token stored -> 401 Unauthorized!
	j, _ := st.GetJob("job-pub-1")
	j.ReviewStatus = "approved"
	_ = st.SaveJob(j)

	noOAuthReq := httptest.NewRequest(http.MethodPost, "/api/jobs/job-pub-1/publish",
		bytes.NewBufferString(`{"platform":"youtube"}`))
	noOAuthReq.Header.Set("X-Agent-Key", srv.AgentKey)
	noOAuthRec := httptest.NewRecorder()
	h.ServeHTTP(noOAuthRec, noOAuthReq)
	if noOAuthRec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 Unauthorized when YouTube OAuth token missing, got %d: %s", noOAuthRec.Code, noOAuthRec.Body.String())
	}

	// 5. Agent CANNOT access /api/publish/accounts (Owner-only -> 403)
	accReq := httptest.NewRequest(http.MethodGet, "/api/publish/accounts", nil)
	accReq.Header.Set("X-Agent-Key", srv.AgentKey)
	accRec := httptest.NewRecorder()
	h.ServeHTTP(accRec, accReq)
	if accRec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for agent GET /api/publish/accounts, got %d", accRec.Code)
	}
}

func TestB3_DraftSceneAndDiarizationEndpoints(t *testing.T) {
	srv, st, h := setupPhase4TestServer(t)

	_ = st.SaveJob(&model.RenderJob{
		ID:            "job-draft-1",
		ProjectID:     "proj-test-windah",
		CampaignID:    "windah-gtavi-motionklip",
		Title:         "Windah Nabrak CapCut Draft",
		FinalDuration: 22,
		Options:       model.RenderOptions{StartSec: 1400, EndSec: 1422, HookText: "GUA JAGO NYETIR"},
		Status:        "done",
		CreatedAt:     time.Now(),
	})

	// 1. Agent exports Jianying draft -> 200 OK with pinned schemaVersion + downloadable ZIP!
	dReq := httptest.NewRequest(http.MethodPost, "/api/jobs/job-draft-1/draft", nil)
	dReq.Header.Set("X-Agent-Key", srv.AgentKey)
	dRec := httptest.NewRecorder()
	h.ServeHTTP(dRec, dReq)
	if dRec.Code != http.StatusOK {
		t.Fatalf("expected 200 on POST /api/jobs/job-draft-1/draft, got %d: %s", dRec.Code, dRec.Body.String())
	}
	if !bytes.Contains(dRec.Body.Bytes(), []byte("jianying-draft-5.9.0")) {
		t.Fatalf("expected pinned schemaVersion in draft response: %s", dRec.Body.String())
	}
	dlReq := httptest.NewRequest(http.MethodGet, "/api/jobs/job-draft-1/draft/download", nil)
	dlReq.Header.Set("X-Agent-Key", srv.AgentKey)
	dlRec := httptest.NewRecorder()
	h.ServeHTTP(dlRec, dlReq)
	if dlRec.Code != http.StatusOK || dlRec.Body.Len() < 100 {
		t.Fatalf("expected 200 ZIP download, got %d len=%d", dlRec.Code, dlRec.Body.Len())
	}

	// 2. Agent snaps candidate boundaries via POST /api/projects/{id}/detect-scenes
	scReq := httptest.NewRequest(http.MethodPost, "/api/projects/proj-test-windah/detect-scenes",
		bytes.NewBufferString(`{"cuts":[1398.5, 1420.5, 2261.0, 2319.5]}`))
	scReq.Header.Set("X-Agent-Key", srv.AgentKey)
	scRec := httptest.NewRecorder()
	h.ServeHTTP(scRec, scReq)
	if scRec.Code != http.StatusOK || !bytes.Contains(scRec.Body.Bytes(), []byte(`"snappedCount":2`)) {
		t.Fatalf("expected 2 snapped candidates from detect-scenes, got %d: %s", scRec.Code, scRec.Body.String())
	}

	// 3. Agent runs diarization via POST /api/projects/{id}/diarize -> returns Windah/NPC turns + verified id_ID TTS info
	srv.cacheMu.Lock()
	srv.rawSignals["proj-test-windah"] = &cachedSignals{
		Transcript: []model.TranscriptCue{
			{Start: 1401, End: 1405, Text: "gua jago nyetir guys lihat nih"},
			{Start: 1406, End: 1410, Text: "hey what's the problem you sure about that"},
			{Start: 1411, End: 1416, Text: "anjir nabrak polisi wkwkwk"},
		},
	}
	srv.cacheMu.Unlock()

	diReq := httptest.NewRequest(http.MethodPost, "/api/projects/proj-test-windah/diarize", nil)
	diReq.Header.Set("X-Agent-Key", srv.AgentKey)
	diRec := httptest.NewRecorder()
	h.ServeHTTP(diRec, diReq)
	if diRec.Code != http.StatusOK || !bytes.Contains(diRec.Body.Bytes(), []byte("SPEAKER_0")) || !bytes.Contains(diRec.Body.Bytes(), []byte("SPEAKER_1")) {
		t.Fatalf("expected SPEAKER_0 + SPEAKER_1 turns from /diarize, got %d: %s", diRec.Code, diRec.Body.String())
	}
}
