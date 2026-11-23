package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"klipforge/internal/deps"
	"klipforge/internal/events"
	"klipforge/internal/model"
	"klipforge/internal/store"
)

func setupPhase4TestServer(t *testing.T) (*Server, *store.Store, http.Handler) {
	t.Helper()
	tmp := newTestDir(t)
	st, err := store.NewStore(tmp)
	if err != nil {
		t.Fatalf("NewStore failed: %v", err)
	}
	// Seed a campaign with forbidden words & a mock project with candidates
	camp, _ := st.GetCampaign("windah-gtavi-motionklip")
	camp.ForbiddenWords = []string{"judol", "slot", "terlarang"}
	_ = st.SaveCampaign(camp)

	_ = st.SaveProject(&model.Project{
		ID:          "proj-test-windah",
		VideoID:     "45r3OyaxvFE",
		SourceURL:   "https://www.youtube.com/watch?v=45r3OyaxvFE",
		Title:       "Windah GTA VI Test",
		DurationSec: 5000,
		CampaignID:  "windah-gtavi-motionklip",
		Status:      "ready",
		Candidates: []model.ClipCandidate{
			{
				ID:         "cand-1",
				Rank:       1,
				Title:      "Baku Tembak",
				StartSec:   2263,
				EndSec:     2318,
				PeakSec:    2312,
				MomentType: "fail",
				Transcript: "dikit lagi mati kena polisi baku tembak",
				Selected:   true,
			},
			{
				ID:         "cand-2",
				Rank:       2,
				Title:      "Nabrak Tembok",
				StartSec:   1400,
				EndSec:     1422,
				PeakSec:    1412,
				MomentType: "crash",
				Transcript: "gua jago nyetir nabrak",
				Selected:   true,
			},
		},
		CreatedAt: time.Now(),
	})

	srv := New(st, deps.NewManager(tmp), events.NewBroker())
	return srv, st, srv.Handler()
}

func TestPhase4_2_EditScriptEndpoint(t *testing.T) {
	srv, _, h := setupPhase4TestServer(t)

	// 1. Agent calls POST /api/jobs/edit-script -> HTTP 200 with appliedPlan echoed!
	// Seg 1 has explicit endSfxId + clamped zoom + clamped speedRamp + unknown bgmAssetId skipped;
	// Seg 2 has momentType="crash" with NO explicit SFX/BGM so auto_by_moment picks sfx-metal-pipe + bgm-gaming-pulse!
	payload := map[string]interface{}{
		"title":      "Windah GTA VI CapCut Edit",
		"projectId":  "proj-test-windah",
		"campaignId": "windah-gtavi-motionklip",
		"layout":     map[string]interface{}{"aspectRatio": "9:16", "cropMode": "face_track"},
		"subtitles":  map[string]interface{}{"enabled": true, "preset": "karaoke"},
		"audioFx":    map[string]interface{}{"sfxAutoPreset": "auto_by_moment", "bgmVolume": 0.18, "autoDuck": true},
		"segments": []map[string]interface{}{
			{
				"startSec":    2263,
				"endSec":      2318,
				"label":       "baku tembak",
				"momentType":  "fail",
				"speed":       1.0,
				"speedRamp":   map[string]interface{}{"atSec": 50, "fromSpeed": 1.0, "toSpeed": 0.5},
				"zoom":        map[string]interface{}{"scale": 3.0, "atSec": 52, "durationSec": 1.5},
				"textOverlay": map[string]interface{}{"text": "DIKIT LAGI MATI", "position": "top", "style": "bold_yellow"},
				"filter":      "vivid_gaming",
				"transition":  "fade",
				"endSfxId":    "sfx-record-scratch",
				"bgmAssetId":  "bgm-does-not-exist",
			},
			{
				"startSec":   1400,
				"endSec":     1422,
				"label":      "nabrak",
				"momentType": "crash",
				"transition": "zoom",
			},
		},
	}
	body, _ := json.Marshal(payload)
	req := httptest.NewRequest(http.MethodPost, "/api/jobs/edit-script", bytes.NewReader(body))
	req.Header.Set("X-Agent-Key", srv.AgentKey)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected HTTP 200 from POST /api/jobs/edit-script, got %d: %s", rec.Code, rec.Body.String())
	}

	var job model.RenderJob
	if err := json.Unmarshal(rec.Body.Bytes(), &job); err != nil {
		t.Fatalf("decode edit-script response: %v", err)
	}
	if job.AppliedPlan == nil || len(job.AppliedPlan.Segments) != 2 {
		t.Fatalf("expected appliedPlan with 2 segments, got %+v", job.AppliedPlan)
	}
	// Cancel queued job immediately so background worker won't try downloading YouTube in unit tests
	srv.Worker.Cancel(job.ID)

	seg0 := job.AppliedPlan.Segments[0]
	if seg0.EndSFXID != "sfx-record-scratch" {
		t.Fatalf("expected explicit endSfxId sfx-record-scratch to win on seg0, got %q", seg0.EndSFXID)
	}
	if seg0.Zoom == nil || seg0.Zoom.Scale != 1.60 || len(seg0.Clamped) == 0 {
		t.Fatalf("expected zoom scale 3.0 clamped to 1.60 with note, got zoom=%+v clamped=%v", seg0.Zoom, seg0.Clamped)
	}
	if len(seg0.Skipped) == 0 {
		t.Fatalf("expected unknown bgmAssetId to be skipped cleanly in seg0.Skipped")
	}
	// Unknown bgmAssetId was skipped, so auto_by_moment(fail) fills bgm-funny-bouncy!
	if seg0.BGMAssetID != "bgm-funny-bouncy" {
		t.Fatalf("expected auto_by_moment(fail) BGM fallback bgm-funny-bouncy, got %q", seg0.BGMAssetID)
	}

	seg1 := job.AppliedPlan.Segments[1]
	if seg1.EndSFXID != "sfx-metal-pipe" || seg1.BGMAssetID != "bgm-gaming-pulse" {
		t.Fatalf("expected auto_by_moment(crash) -> (sfx-metal-pipe, bgm-gaming-pulse), got (%q, %q)", seg1.EndSFXID, seg1.BGMAssetID)
	}
	if seg1.AutoEdit == nil || seg1.AutoEdit.MomentType != "crash" {
		t.Fatalf("expected seg1.AutoEdit populated, got %+v", seg1.AutoEdit)
	}

	// 2. Negative case: forbidden word in textOverlay MUST be rejected with HTTP 400!
	badPayload := map[string]interface{}{
		"title":      "Bad Clip",
		"projectId":  "proj-test-windah",
		"campaignId": "windah-gtavi-motionklip",
		"segments": []map[string]interface{}{
			{
				"startSec":    2263,
				"endSec":      2280,
				"label":       "ok",
				"textOverlay": map[string]interface{}{"text": "PROMO JUDOL GACOR", "position": "top"},
			},
		},
	}
	badBody, _ := json.Marshal(badPayload)
	reqBad := httptest.NewRequest(http.MethodPost, "/api/jobs/edit-script", bytes.NewReader(badBody))
	reqBad.Header.Set("X-Agent-Key", srv.AgentKey)
	recBad := httptest.NewRecorder()
	h.ServeHTTP(recBad, reqBad)
	if recBad.Code != http.StatusBadRequest {
		t.Fatalf("expected HTTP 400 for forbidden word in textOverlay, got %d: %s", recBad.Code, recBad.Body.String())
	}
}

func TestPhase4_4_CandidateVerdictStore(t *testing.T) {
	srv, st, h := setupPhase4TestServer(t)
	jobsBefore := len(st.ListJobs())

	// 1. Agent posts "keep" verdict -> 200 OK, stored with verdictBy, NEVER auto-renders!
	vKeep := bytes.NewBufferString(`{"verdict":"keep","funny":5,"note":"pecah banget pas ketembak","momentType":"fail"}`)
	req1 := httptest.NewRequest(http.MethodPost, "/api/projects/proj-test-windah/candidates/cand-1/verdict", vKeep)
	req1.Header.Set("X-Agent-Key", srv.AgentKey)
	rec1 := httptest.NewRecorder()
	h.ServeHTTP(rec1, req1)
	if rec1.Code != http.StatusOK {
		t.Fatalf("expected 200 on verdict keep, got %d: %s", rec1.Code, rec1.Body.String())
	}
	if len(st.ListJobs()) != jobsBefore {
		t.Fatalf("keep verdict MUST NOT auto-render! jobs before=%d after=%d", jobsBefore, len(st.ListJobs()))
	}

	// 2. Agent posts "drop" verdict on cand-2 -> hidden by default on GET /api/projects/{id}, visible with ?includeDropped=1!
	vDrop := bytes.NewBufferString(`{"verdict":"drop","funny":1,"note":"basi"}`)
	req2 := httptest.NewRequest(http.MethodPost, "/api/projects/proj-test-windah/candidates/cand-2/verdict", vDrop)
	req2.Header.Set("X-Agent-Key", srv.AgentKey)
	rec2 := httptest.NewRecorder()
	h.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusOK {
		t.Fatalf("expected 200 on verdict drop, got %d: %s", rec2.Code, rec2.Body.String())
	}

	// Default GET hides dropped candidate
	g1 := httptest.NewRequest(http.MethodGet, "/api/projects/proj-test-windah", nil)
	g1.Header.Set("X-Agent-Key", srv.AgentKey)
	rg1 := httptest.NewRecorder()
	h.ServeHTTP(rg1, g1)
	if rg1.Code != http.StatusOK {
		t.Fatalf("GET project failed: %d", rg1.Code)
	}
	var projDefault model.Project
	_ = json.Unmarshal(rg1.Body.Bytes(), &projDefault)
	if len(projDefault.Candidates) != 1 || projDefault.Candidates[0].ID != "cand-1" {
		t.Fatalf("expected default GET to hide drop candidate (len=1 cand-1), got len=%d", len(projDefault.Candidates))
	}
	if projDefault.Candidates[0].Verdict == nil || projDefault.Candidates[0].Verdict.Verdict != "keep" || projDefault.Candidates[0].Verdict.Funny != 5 {
		t.Fatalf("expected keep verdict with funny=5 on cand-1, got %+v", projDefault.Candidates[0].Verdict)
	}
	if projDefault.Candidates[0].Verdict.Via != "agent" || projDefault.Candidates[0].Verdict.VerdictBy == "" {
		t.Fatalf("expected verdictBy attribution, got %+v", projDefault.Candidates[0].Verdict)
	}

	// GET with ?includeDropped=1 returns both
	g2 := httptest.NewRequest(http.MethodGet, "/api/projects/proj-test-windah?includeDropped=1", nil)
	g2.Header.Set("X-Agent-Key", srv.AgentKey)
	rg2 := httptest.NewRecorder()
	h.ServeHTTP(rg2, g2)
	var projAll model.Project
	_ = json.Unmarshal(rg2.Body.Bytes(), &projAll)
	if len(projAll.Candidates) != 2 {
		t.Fatalf("expected includeDropped=1 to return 2 candidates, got %d", len(projAll.Candidates))
	}

	// 3. Invalid verdict -> 400
	vBad := bytes.NewBufferString(`{"verdict":"auto_render"}`)
	reqBad := httptest.NewRequest(http.MethodPost, "/api/projects/proj-test-windah/candidates/cand-1/verdict", vBad)
	reqBad.Header.Set("X-Agent-Key", srv.AgentKey)
	recBad := httptest.NewRecorder()
	h.ServeHTTP(recBad, reqBad)
	if recBad.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for invalid verdict value, got %d", recBad.Code)
	}
}
