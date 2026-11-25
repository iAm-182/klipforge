package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"klipforge/internal/deps"
	"klipforge/internal/events"
	"klipforge/internal/model"
	"klipforge/internal/store"
)

func setupScoreBanTestServer(t *testing.T) (*Server, *store.Store, http.Handler) {
	t.Helper()
	tmp := newTestDir(t)
	st, err := store.NewStore(tmp)
	if err != nil {
		t.Fatalf("NewStore failed: %v", err)
	}
	cfg := st.GetSettings()
	cfg.MinFreeDiskGB = 0.001
	_ = st.SaveSettings(cfg)

	proj := &model.Project{
		ID:          "proj-scoreban",
		VideoID:     "sbTest001",
		SourceURL:   "",
		Title:       "Windah GTA VI Score-Ban Test",
		DurationSec: 5000,
		CampaignID:  "windah-gtavi-motionklip",
		Status:      "ready",
		Candidates: []model.ClipCandidate{
			{
				ID:       "cand-unverified",
				Rank:     1,
				Title:    "High Score Unverified",
				StartSec: 100,
				EndSec:   130,
				PeakSec:  115,
				Score:    98,
			},
			{
				ID:       "cand-drop",
				Rank:     2,
				Title:    "Dropped Candidate",
				StartSec: 200,
				EndSec:   230,
				PeakSec:  215,
				Score:    95,
				Verdict:  &model.CandidateVerdict{Verdict: "drop", Funny: 1, VerdictBy: "Agent"},
			},
			{
				ID:       "cand-maybe",
				Rank:     3,
				Title:    "Maybe Candidate",
				StartSec: 300,
				EndSec:   330,
				PeakSec:  315,
				Score:    92,
				Verdict:  &model.CandidateVerdict{Verdict: "maybe", Funny: 3, VerdictBy: "Agent"},
			},
			{
				ID:       "cand-keep-no-frame",
				Rank:     4,
				Title:    "Keep Without Frame",
				StartSec: 400,
				EndSec:   430,
				PeakSec:  415,
				Score:    70,
				Verdict:  &model.CandidateVerdict{Verdict: "keep", Funny: 5, VerdictBy: "Agent"},
			},
			{
				ID:            "cand-keep-start-frame",
				Rank:          5,
				Title:         "Keep With Start Frame",
				StartSec:      500,
				EndSec:        530,
				PeakSec:       515,
				Score:         65,
				StartFrameURL: "/api/projects/proj-scoreban/candidate-frame/cand-keep-start-frame/start",
				Verdict:       &model.CandidateVerdict{Verdict: "keep", Funny: 5, VerdictBy: "Agent"},
			},
			{
				ID:           "cand-keep-peak-frame",
				Rank:         6,
				Title:        "Keep With Peak Frame",
				StartSec:     600,
				EndSec:       630,
				PeakSec:      615,
				Score:        60,
				PeakFrameURL: "/api/projects/proj-scoreban/candidate-frame/cand-keep-peak-frame/peak",
				Verdict:      &model.CandidateVerdict{Verdict: "keep", Funny: 5, VerdictBy: "Agent"},
			},
		},
		CreatedAt: time.Now(),
	}
	if err := st.SaveProject(proj); err != nil {
		t.Fatalf("SaveProject: %v", err)
	}

	srv := New(st, deps.NewManager(tmp), events.NewBroker())
	t.Cleanup(srv.Close)
	return srv, st, srv.Handler()
}

func TestScoreBan_AgentUnverifiedCandidateBlocked409(t *testing.T) {
	srv, st, h := setupScoreBanTestServer(t)

	for _, candID := range []string{"cand-unverified", "cand-drop", "cand-maybe"} {
		t.Run(candID, func(t *testing.T) {
			jobsBefore := len(st.ListJobs())
			body, _ := json.Marshal(map[string]interface{}{
				"projectId":   "proj-scoreban",
				"candidateId": candID,
				"title":       "Test Blocked",
				"startSec":    100,
				"endSec":      130,
			})
			req := httptest.NewRequest(http.MethodPost, "/api/jobs/render", bytes.NewReader(body))
			req.Header.Set("X-Agent-Key", srv.AgentKey)
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)

			if rec.Code == http.StatusAccepted {
				var j model.RenderJob
				if json.Unmarshal(rec.Body.Bytes(), &j) == nil && j.ID != "" {
					srv.Worker.Cancel(j.ID)
				}
			}
			if rec.Code != http.StatusConflict {
				t.Fatalf("expected 409 Conflict for agent rendering %s, got %d: %s", candID, rec.Code, rec.Body.String())
			}
			if !strings.Contains(rec.Body.String(), "Score-Ban") || !strings.Contains(rec.Body.String(), "I-05") {
				t.Fatalf("expected Score-Ban (I-05) error message, got: %s", rec.Body.String())
			}
			if len(st.ListJobs()) != jobsBefore {
				t.Fatalf("blocked render must not enqueue a job: before=%d after=%d", jobsBefore, len(st.ListJobs()))
			}
		})
	}
}

func TestScoreBan_AgentKeepWithoutVisualProofBlocked409(t *testing.T) {
	srv, st, h := setupScoreBanTestServer(t)
	jobsBefore := len(st.ListJobs())

	body, _ := json.Marshal(map[string]interface{}{
		"projectId":   "proj-scoreban",
		"candidateId": "cand-keep-no-frame",
		"title":       "Keep But No Frame",
		"startSec":    400,
		"endSec":      430,
	})
	req := httptest.NewRequest(http.MethodPost, "/api/jobs/render", bytes.NewReader(body))
	req.Header.Set("X-Agent-Key", srv.AgentKey)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code == http.StatusAccepted {
		var j model.RenderJob
		if json.Unmarshal(rec.Body.Bytes(), &j) == nil && j.ID != "" {
			srv.Worker.Cancel(j.ID)
		}
	}
	if rec.Code != http.StatusConflict {
		t.Fatalf("expected 409 Conflict when keep candidate lacks frame/storyboard proof, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "frame/storyboard") {
		t.Fatalf("expected frame/storyboard proof message, got: %s", rec.Body.String())
	}
	if len(st.ListJobs()) != jobsBefore {
		t.Fatalf("blocked render must not enqueue a job")
	}
}

func TestScoreBan_AgentKeepWithVisualProofAllowed202(t *testing.T) {
	srv, st, h := setupScoreBanTestServer(t)

	// 1. StartFrameURL present -> 202
	t.Run("startFrameUrl", func(t *testing.T) {
		body, _ := json.Marshal(map[string]interface{}{
			"projectId":   "proj-scoreban",
			"candidateId": "cand-keep-start-frame",
			"title":       "Verified With Start Frame",
			"startSec":    500,
			"endSec":      530,
		})
		req := httptest.NewRequest(http.MethodPost, "/api/jobs/render", bytes.NewReader(body))
		req.Header.Set("X-Agent-Key", srv.AgentKey)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusAccepted {
			t.Fatalf("expected 202 Accepted for keep+startFrameUrl, got %d: %s", rec.Code, rec.Body.String())
		}
		var job model.RenderJob
		if err := json.Unmarshal(rec.Body.Bytes(), &job); err == nil && job.ID != "" {
			srv.Worker.Cancel(job.ID)
		}
	})

	// 2. PeakFrameURL present -> 202
	t.Run("peakFrameUrl", func(t *testing.T) {
		body, _ := json.Marshal(map[string]interface{}{
			"projectId":   "proj-scoreban",
			"candidateId": "cand-keep-peak-frame",
			"title":       "Verified With Peak Frame",
			"startSec":    600,
			"endSec":      630,
		})
		req := httptest.NewRequest(http.MethodPost, "/api/jobs/render", bytes.NewReader(body))
		req.Header.Set("X-Agent-Key", srv.AgentKey)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusAccepted {
			t.Fatalf("expected 202 Accepted for keep+peakFrameUrl, got %d: %s", rec.Code, rec.Body.String())
		}
		var job model.RenderJob
		if err := json.Unmarshal(rec.Body.Bytes(), &job); err == nil && job.ID != "" {
			srv.Worker.Cancel(job.ID)
		}
	})

	// 3. Explicit frameVerified=true flag -> 202
	t.Run("frameVerifiedFlag", func(t *testing.T) {
		body, _ := json.Marshal(map[string]interface{}{
			"projectId":     "proj-scoreban",
			"candidateId":   "cand-keep-no-frame",
			"frameVerified": true,
			"title":         "Verified With FrameVerified Flag",
			"startSec":      400,
			"endSec":        430,
		})
		req := httptest.NewRequest(http.MethodPost, "/api/jobs/render", bytes.NewReader(body))
		req.Header.Set("X-Agent-Key", srv.AgentKey)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusAccepted {
			t.Fatalf("expected 202 Accepted for keep+frameVerified=true, got %d: %s", rec.Code, rec.Body.String())
		}
		var job model.RenderJob
		if err := json.Unmarshal(rec.Body.Bytes(), &job); err == nil && job.ID != "" {
			srv.Worker.Cancel(job.ID)
		}
	})

	// 4. Project storyboards/*.jpg exists on disk -> 202
	t.Run("projectStoryboardOnDisk", func(t *testing.T) {
		sbDir := filepath.Join(st.ProjectDir("proj-scoreban"), "storyboards")
		if err := os.MkdirAll(sbDir, 0o755); err != nil {
			t.Fatalf("MkdirAll storyboards: %v", err)
		}
		if err := os.WriteFile(filepath.Join(sbDir, "sb_12345.jpg"), []byte("fakejpg"), 0o644); err != nil {
			t.Fatalf("WriteFile storyboard: %v", err)
		}

		body, _ := json.Marshal(map[string]interface{}{
			"projectId":   "proj-scoreban",
			"candidateId": "cand-keep-no-frame",
			"title":       "Verified With Storyboard",
			"startSec":    400,
			"endSec":      430,
		})
		req := httptest.NewRequest(http.MethodPost, "/api/jobs/render", bytes.NewReader(body))
		req.Header.Set("X-Agent-Key", srv.AgentKey)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusAccepted {
			t.Fatalf("expected 202 Accepted when project storyboard exists, got %d: %s", rec.Code, rec.Body.String())
		}
		var job model.RenderJob
		if err := json.Unmarshal(rec.Body.Bytes(), &job); err == nil && job.ID != "" {
			srv.Worker.Cancel(job.ID)
		}
	})
}

func TestScoreBan_ManualRangeCutAlwaysAllowed(t *testing.T) {
	srv, _, h := setupScoreBanTestServer(t)

	cases := []struct {
		name      string
		headerKey string
		headerVal string
		payload   map[string]interface{}
	}{
		{
			name:      "owner_manual_cut",
			headerKey: "X-Engine-Token",
			headerVal: srv.EngineToken,
			payload: map[string]interface{}{
				"projectId":       "proj-scoreban",
				"candidateId":     "",
				"requireVerified": true,
				"title":           "Manual Human Cut",
				"startSec":        700,
				"endSec":          735,
			},
		},
		{
			name:      "agent_manual_cut",
			headerKey: "X-Agent-Key",
			headerVal: srv.AgentKey,
			payload: map[string]interface{}{
				"projectId": "proj-scoreban",
				"title":     "Manual Timestamp Cut",
				"startSec":  800,
				"endSec":    835,
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body, _ := json.Marshal(tc.payload)
			req := httptest.NewRequest(http.MethodPost, "/api/jobs/render", bytes.NewReader(body))
			req.Header.Set(tc.headerKey, tc.headerVal)
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != http.StatusAccepted {
				t.Fatalf("manual range cut must always be allowed (202), got %d: %s", rec.Code, rec.Body.String())
			}
			var job model.RenderJob
			if err := json.Unmarshal(rec.Body.Bytes(), &job); err == nil && job.ID != "" {
				srv.Worker.Cancel(job.ID)
			}
		})
	}
}

func TestScoreBan_OwnerRequireVerifiedFlagAndDefaultAllowed(t *testing.T) {
	srv, _, h := setupScoreBanTestServer(t)

	// 1. Owner with requireVerified=true on unverified candidate -> 409 Conflict
	bodyBlocked, _ := json.Marshal(map[string]interface{}{
		"projectId":       "proj-scoreban",
		"candidateId":     "cand-unverified",
		"requireVerified": true,
		"title":           "Owner Enforced Guardrail",
		"startSec":        100,
		"endSec":          130,
	})
	req1 := httptest.NewRequest(http.MethodPost, "/api/jobs/render", bytes.NewReader(bodyBlocked))
	req1.Header.Set("X-Engine-Token", srv.EngineToken)
	rec1 := httptest.NewRecorder()
	h.ServeHTTP(rec1, req1)
	if rec1.Code == http.StatusAccepted {
		var j model.RenderJob
		if json.Unmarshal(rec1.Body.Bytes(), &j) == nil && j.ID != "" {
			srv.Worker.Cancel(j.ID)
		}
	}
	if rec1.Code != http.StatusConflict {
		t.Fatalf("expected 409 Conflict for owner with requireVerified=true on unverified candidate, got %d: %s", rec1.Code, rec1.Body.String())
	}

	// 2. Owner WITHOUT requireVerified on candidate -> 202 Accepted (human UI remains unblocked)
	bodyAllowed, _ := json.Marshal(map[string]interface{}{
		"projectId":   "proj-scoreban",
		"candidateId": "cand-unverified",
		"title":       "Owner UI Direct Render",
		"startSec":    100,
		"endSec":      130,
	})
	req2 := httptest.NewRequest(http.MethodPost, "/api/jobs/render", bytes.NewReader(bodyAllowed))
	req2.Header.Set("X-Engine-Token", srv.EngineToken)
	rec2 := httptest.NewRecorder()
	h.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusAccepted {
		t.Fatalf("expected 202 Accepted for owner without requireVerified, got %d: %s", rec2.Code, rec2.Body.String())
	}
	var job model.RenderJob
	if err := json.Unmarshal(rec2.Body.Bytes(), &job); err == nil && job.ID != "" {
		srv.Worker.Cancel(job.ID)
	}
}

func TestScoreBan_UnknownCandidateIDReturns400(t *testing.T) {
	srv, _, h := setupScoreBanTestServer(t)

	body, _ := json.Marshal(map[string]interface{}{
		"projectId":   "proj-scoreban",
		"candidateId": "cand-does-not-exist",
		"title":       "Ghost Candidate",
		"startSec":    100,
		"endSec":      130,
	})
	req := httptest.NewRequest(http.MethodPost, "/api/jobs/render", bytes.NewReader(body))
	req.Header.Set("X-Agent-Key", srv.AgentKey)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code == http.StatusAccepted {
		var j model.RenderJob
		if json.Unmarshal(rec.Body.Bytes(), &j) == nil && j.ID != "" {
			srv.Worker.Cancel(j.ID)
		}
	}
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 Bad Request for unknown candidateId, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestScoreBan_BatchAllBlocked409AndMixedSkipsBlocked(t *testing.T) {
	srv, st, h := setupScoreBanTestServer(t)

	// 1. ALL items rejected by guardrail -> 409 Conflict + 0 jobs enqueued
	t.Run("allRejectedReturns409", func(t *testing.T) {
		jobsBefore := len(st.ListJobs())
		payload := map[string]interface{}{
			"items": []map[string]interface{}{
				{"projectId": "proj-scoreban", "candidateId": "cand-unverified", "title": "Bad 1", "startSec": 100, "endSec": 130},
				{"projectId": "proj-scoreban", "candidateId": "cand-keep-no-frame", "title": "Bad 2", "startSec": 400, "endSec": 430},
			},
		}
		body, _ := json.Marshal(payload)
		req := httptest.NewRequest(http.MethodPost, "/api/jobs/batch", bytes.NewReader(body))
		req.Header.Set("X-Agent-Key", srv.AgentKey)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)

		if rec.Code == http.StatusAccepted {
			var jobs []model.RenderJob
			if json.Unmarshal(rec.Body.Bytes(), &jobs) == nil {
				for _, j := range jobs {
					srv.Worker.Cancel(j.ID)
				}
			}
		}
		if rec.Code != http.StatusConflict {
			t.Fatalf("expected 409 Conflict when all batch items blocked by guardrail, got %d: %s", rec.Code, rec.Body.String())
		}
		if len(st.ListJobs()) != jobsBefore {
			t.Fatalf("expected 0 jobs enqueued when all batch items rejected, got delta=%d", len(st.ListJobs())-jobsBefore)
		}
	})

	// 2. Mixed batch (1 blocked + 1 verified) -> 202 Accepted with ONLY the verified item
	t.Run("mixedBatchSkipsBlockedCleanly", func(t *testing.T) {
		payload := map[string]interface{}{
			"items": []map[string]interface{}{
				{"projectId": "proj-scoreban", "candidateId": "cand-unverified", "title": "Blocked Unverified", "startSec": 100, "endSec": 130},
				{"projectId": "proj-scoreban", "candidateId": "cand-keep-start-frame", "title": "Allowed Verified", "startSec": 500, "endSec": 530},
			},
		}
		body, _ := json.Marshal(payload)
		req := httptest.NewRequest(http.MethodPost, "/api/jobs/batch", bytes.NewReader(body))
		req.Header.Set("X-Agent-Key", srv.AgentKey)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)

		if rec.Code != http.StatusAccepted {
			t.Fatalf("expected 202 Accepted for mixed batch, got %d: %s", rec.Code, rec.Body.String())
		}
		var jobs []model.RenderJob
		if err := json.Unmarshal(rec.Body.Bytes(), &jobs); err != nil {
			t.Fatalf("decode batch response: %v", err)
		}
		for _, j := range jobs {
			srv.Worker.Cancel(j.ID)
		}
		if len(jobs) != 1 || jobs[0].Options.CandidateID != "cand-keep-start-frame" {
			t.Fatalf("expected exactly 1 verified job (cand-keep-start-frame), got %d: %+v", len(jobs), jobs)
		}
	})
}
