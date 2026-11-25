package autopilot

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"klipforge/internal/audiofx"
	"klipforge/internal/deps"
	"klipforge/internal/events"
	"klipforge/internal/model"
	"klipforge/internal/renderer"
	"klipforge/internal/store"
	"klipforge/internal/youtube"
)

func newAutopilotTestDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "klipforge-autopilot-test-*")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	t.Cleanup(func() {
		_ = os.RemoveAll(dir)
	})
	return dir
}

func TestAutopilotScoreBan_SelectVerifiedCandidatesRejectsUnverifiedHighScores(t *testing.T) {
	cands := []model.ClipCandidate{
		{ID: "c1", Rank: 1, Title: "Score 99 Unverified", Score: 99, StartSec: 10, EndSec: 40, PeakSec: 25},
		{ID: "c2", Rank: 2, Title: "Score 95 Drop", Score: 95, StartSec: 50, EndSec: 80, PeakSec: 65, Verdict: &model.CandidateVerdict{Verdict: "drop"}},
		{ID: "c3", Rank: 3, Title: "Score 92 Maybe", Score: 92, StartSec: 90, EndSec: 120, PeakSec: 105, Verdict: &model.CandidateVerdict{Verdict: "maybe"}},
		{ID: "c4", Rank: 4, Title: "Score 88 Unverified", Score: 88, StartSec: 130, EndSec: 160, PeakSec: 145},
		{ID: "c5", Rank: 5, Title: "Score 84 Empty Verdict", Score: 84, StartSec: 170, EndSec: 200, PeakSec: 185, Verdict: &model.CandidateVerdict{Verdict: ""}},
	}

	got := SelectVerifiedCandidates(cands, 5)
	if len(got) != 0 {
		t.Fatalf("Score-Ban violation: expected 0 verified candidates for unverified/drop/maybe list, got %d (%+v)", len(got), got)
	}
}

func TestAutopilotScoreBan_SelectVerifiedCandidatesOnlyKeepAndLimit(t *testing.T) {
	cands := []model.ClipCandidate{
		{ID: "c-high-unverified", Rank: 1, Title: "High Score Unverified", Score: 99, StartSec: 10, EndSec: 40},
		{ID: "c-keep-1", Rank: 2, Title: "Verified Low Score", Score: 41, StartSec: 50, EndSec: 80, Verdict: &model.CandidateVerdict{Verdict: "keep"}},
		{ID: "c-drop", Rank: 3, Title: "Dropped High Score", Score: 93, StartSec: 90, EndSec: 120, Verdict: &model.CandidateVerdict{Verdict: "drop"}},
		{ID: "c-keep-2", Rank: 4, Title: "Verified Case Space", Score: 52, StartSec: 130, EndSec: 160, Verdict: &model.CandidateVerdict{Verdict: " KEEP "}},
		{ID: "c-maybe", Rank: 5, Title: "Maybe High Score", Score: 89, StartSec: 170, EndSec: 200, Verdict: &model.CandidateVerdict{Verdict: "maybe"}},
		{ID: "c-keep-3", Rank: 6, Title: "Verified Third", Score: 60, StartSec: 210, EndSec: 240, Verdict: &model.CandidateVerdict{Verdict: "keep"}},
	}

	limited := SelectVerifiedCandidates(cands, 2)
	if len(limited) != 2 {
		t.Fatalf("expected limit=2 verified candidates, got %d", len(limited))
	}
	if limited[0].ID != "c-keep-1" || limited[1].ID != "c-keep-2" {
		t.Fatalf("expected [c-keep-1, c-keep-2], got [%s, %s]", limited[0].ID, limited[1].ID)
	}

	unlimited := SelectVerifiedCandidates(cands, 0)
	if len(unlimited) != 3 {
		t.Fatalf("expected limit=0 to return all 3 keep candidates, got %d", len(unlimited))
	}
	if unlimited[0].ID != "c-keep-1" || unlimited[1].ID != "c-keep-2" || unlimited[2].ID != "c-keep-3" {
		t.Fatalf("unexpected order/set for limit=0: %+v", unlimited)
	}
}

func TestAutopilotScoreBan_RunRuleZeroAutoRenderWhenUnverified(t *testing.T) {
	tmp := newAutopilotTestDir(t)
	st, err := store.NewStore(tmp)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	cfg := st.GetSettings()
	cfg.MinFreeDiskGB = 0.001
	_ = st.SaveSettings(cfg)
	d := deps.NewManager(tmp)
	yt := youtube.NewClient(d)
	audioLib := audiofx.NewLibrary(st.DataDir, d)
	broker := events.NewBroker()
	wp := renderer.NewWorkerPool(st, d, yt, audioLib, broker)

	rule := &model.AutopilotRule{
		ID:               "rule-scoreban-1",
		Name:             "Windah Watcher",
		Enabled:          true,
		ChannelURL:       "https://www.youtube.com/@WindahBasudara/streams",
		CampaignID:       "windah-gtavi-motionklip",
		IntervalMinutes:  60,
		MaxVideosPerRun:  1,
		TopClipsPerVideo: 5,
		CreatedAt:        time.Now(),
	}
	if err := st.SaveAutopilot(rule); err != nil {
		t.Fatalf("SaveAutopilot: %v", err)
	}

	sc := &Scheduler{
		Store:   st,
		YT:      yt,
		Worker:  wp,
		Broker:  broker,
		running: make(map[string]bool),
		fetchChannel: func(ctx context.Context, channelURL string, limit int, settings model.AppSettings) ([]model.ChannelVideo, error) {
			return []model.ChannelVideo{
				{ID: "vid-unverified-1", Title: "Windah GTA VI Stream", URL: "https://www.youtube.com/watch?v=vid-unverified-1", Duration: 3600},
			}, nil
		},
		Analyze: func(ctx context.Context, videoURL, campaignID string) (*model.Project, error) {
			proj := &model.Project{
				ID:          "proj-auto-unverified",
				VideoID:     "vid-unverified-1",
				SourceURL:   "",
				Title:       "Windah GTA VI Stream",
				DurationSec: 3600,
				CampaignID:  campaignID,
				Status:      "ready",
				Candidates: []model.ClipCandidate{
					{ID: "c1", Rank: 1, Title: "Auto Score 98", Score: 98, StartSec: 100, EndSec: 130, PeakSec: 115},
					{ID: "c2", Rank: 2, Title: "Auto Score 94", Score: 94, StartSec: 200, EndSec: 230, PeakSec: 215},
					{ID: "c3", Rank: 3, Title: "Auto Score 91", Score: 91, StartSec: 300, EndSec: 330, PeakSec: 315},
					{ID: "c4", Rank: 4, Title: "Auto Score 89", Score: 89, StartSec: 400, EndSec: 430, PeakSec: 415},
					{ID: "c5", Rank: 5, Title: "Auto Score 86", Score: 86, StartSec: 500, EndSec: 530, PeakSec: 515},
				},
				CreatedAt: time.Now(),
			}
			_ = st.SaveProject(proj)
			return proj, nil
		},
	}

	sc.RunRule(rule.ID)

	if jobs := st.ListJobs(); len(jobs) != 0 {
		for _, j := range jobs {
			wp.Cancel(j.ID)
		}
		t.Fatalf("Score-Ban violation: autopilot must queue 0 jobs for unverified candidates, queued %d", len(jobs))
	}

	updated, ok := st.GetAutopilot(rule.ID)
	if !ok {
		t.Fatalf("autopilot rule disappeared")
	}
	if !strings.Contains(updated.LastStatus, "Score-Ban") || !strings.Contains(updated.LastStatus, "0 klip") {
		t.Fatalf("expected Score-Ban 0-clip status message, got %q", updated.LastStatus)
	}
	if len(updated.ProcessedVideoIDs) != 1 || updated.ProcessedVideoIDs[0] != "vid-unverified-1" {
		t.Fatalf("expected video recorded as processed, got %v", updated.ProcessedVideoIDs)
	}
}

func TestAutopilotScoreBan_RunRuleQueuesOnlyKeepVerified(t *testing.T) {
	tmp := newAutopilotTestDir(t)
	st, err := store.NewStore(tmp)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	cfg := st.GetSettings()
	cfg.MinFreeDiskGB = 0.001
	_ = st.SaveSettings(cfg)
	d := deps.NewManager(tmp)
	yt := youtube.NewClient(d)
	audioLib := audiofx.NewLibrary(st.DataDir, d)
	broker := events.NewBroker()
	wp := renderer.NewWorkerPool(st, d, yt, audioLib, broker)

	rule := &model.AutopilotRule{
		ID:               "rule-scoreban-2",
		Name:             "Windah Verified Watcher",
		Enabled:          true,
		ChannelURL:       "https://www.youtube.com/@WindahBasudara/streams",
		CampaignID:       "windah-gtavi-motionklip",
		IntervalMinutes:  60,
		MaxVideosPerRun:  1,
		TopClipsPerVideo: 5,
		CreatedAt:        time.Now(),
	}
	if err := st.SaveAutopilot(rule); err != nil {
		t.Fatalf("SaveAutopilot: %v", err)
	}

	sc := &Scheduler{
		Store:   st,
		YT:      yt,
		Worker:  wp,
		Broker:  broker,
		running: make(map[string]bool),
		fetchChannel: func(ctx context.Context, channelURL string, limit int, settings model.AppSettings) ([]model.ChannelVideo, error) {
			return []model.ChannelVideo{
				{ID: "vid-verified-1", Title: "Windah GTA VI Verified", URL: "https://www.youtube.com/watch?v=vid-verified-1", Duration: 3600},
			}, nil
		},
		Analyze: func(ctx context.Context, videoURL, campaignID string) (*model.Project, error) {
			proj := &model.Project{
				ID:          "proj-auto-verified",
				VideoID:     "vid-verified-1",
				SourceURL:   "",
				Title:       "Windah GTA VI Verified",
				DurationSec: 3600,
				CampaignID:  campaignID,
				Status:      "ready",
				Candidates: []model.ClipCandidate{
					{ID: "c-high-1", Rank: 1, Title: "Unverified 99", Score: 99, StartSec: 100, EndSec: 130, PeakSec: 115},
					{ID: "c-drop-2", Rank: 2, Title: "Dropped 96", Score: 96, StartSec: 200, EndSec: 230, PeakSec: 215, Verdict: &model.CandidateVerdict{Verdict: "drop"}},
					{ID: "c-keep-3", Rank: 3, Title: "Real Funny Keep", Score: 58, StartSec: 300, EndSec: 335, PeakSec: 318, Verdict: &model.CandidateVerdict{Verdict: "keep", Funny: 5}},
					{ID: "c-maybe-4", Rank: 4, Title: "Maybe 90", Score: 90, StartSec: 400, EndSec: 430, PeakSec: 415, Verdict: &model.CandidateVerdict{Verdict: "maybe"}},
				},
				CreatedAt: time.Now(),
			}
			_ = st.SaveProject(proj)
			return proj, nil
		},
	}

	sc.RunRule(rule.ID)

	jobs := st.ListJobs()
	for _, j := range jobs {
		wp.Cancel(j.ID)
	}
	if len(jobs) != 1 {
		t.Fatalf("expected exactly 1 job queued for the single keep-verified candidate, got %d", len(jobs))
	}
	if jobs[0].Options.CandidateID != "c-keep-3" {
		t.Fatalf("expected candidate c-keep-3 to be queued, got %q", jobs[0].Options.CandidateID)
	}
	if jobs[0].ReviewStatus != "pending_review" || jobs[0].AutopilotID != rule.ID {
		t.Fatalf("expected pending_review + autopilotId=%s, got review=%q autopilot=%q", rule.ID, jobs[0].ReviewStatus, jobs[0].AutopilotID)
	}
}
