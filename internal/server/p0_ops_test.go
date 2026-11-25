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

func setupP0OpsTestServer(t *testing.T) (*Server, *store.Store, http.Handler, *model.TeamMember, *model.TeamMember) {
	t.Helper()
	tmp := newTestDir(t)
	st, err := store.NewStore(tmp)
	if err != nil {
		t.Fatalf("NewStore failed: %v", err)
	}
	cfg := st.GetSettings()
	cfg.MinFreeDiskGB = 0.001
	if err := st.SaveSettings(cfg); err != nil {
		t.Fatalf("SaveSettings: %v", err)
	}

	qc := &model.TeamMember{
		ID:        "mem-qc-1",
		Name:      "Rina QC",
		Role:      "qc",
		AccessKey: "kf_usr_qc_test_key_0001",
		AgentKey:  "kf_agt_qc_test_key_0001",
		Enabled:   true,
		MaxActive: 3,
		CreatedAt: time.Now(),
	}
	clipper := &model.TeamMember{
		ID:        "mem-clip-1",
		Name:      "Budi Clipper",
		Role:      "clipper",
		AccessKey: "kf_usr_clip_test_key_0002",
		AgentKey:  "kf_agt_clip_test_key_0002",
		Enabled:   true,
		MaxActive: 3,
		CreatedAt: time.Now(),
	}
	if err := st.SaveTeamMember(qc); err != nil {
		t.Fatalf("SaveTeamMember qc: %v", err)
	}
	if err := st.SaveTeamMember(clipper); err != nil {
		t.Fatalf("SaveTeamMember clipper: %v", err)
	}

	outDir := st.GetSettings().OutputDir
	subDir := filepath.Join(outDir, "Windah_Basudara_GTA_VI")
	if err := os.MkdirAll(subDir, 0o755); err != nil {
		t.Fatalf("MkdirAll subDir: %v", err)
	}
	writeFakeMP4 := func(relPath string, content string) string {
		full := filepath.Join(outDir, relPath)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatalf("MkdirAll %s: %v", full, err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatalf("WriteFile %s: %v", full, err)
		}
		return full
	}

	now := time.Now()
	pPosted := writeFakeMP4(filepath.Join("Windah_Basudara_GTA_VI", "posted.mp4"), strings.Repeat("A", 1024))
	pPubOld := writeFakeMP4(filepath.Join("Windah_Basudara_GTA_VI", "pub_old.mp4"), strings.Repeat("B", 2048))
	pPubNew := writeFakeMP4(filepath.Join("Windah_Basudara_GTA_VI", "pub_new.mp4"), strings.Repeat("C", 3072))
	pPending := writeFakeMP4(filepath.Join("Windah_Basudara_GTA_VI", "pending.mp4"), strings.Repeat("D", 512))
	pRej1 := writeFakeMP4(filepath.Join("Windah_Basudara_GTA_VI", "rej1.mp4"), strings.Repeat("E", 512))
	pRej2 := writeFakeMP4(filepath.Join("Windah_Basudara_GTA_VI", "rej2.mp4"), strings.Repeat("F", 512))
	pRej3 := writeFakeMP4(filepath.Join("Windah_Basudara_GTA_VI", "rej3.mp4"), strings.Repeat("G", 512))

	// Orphan MP4 + non-MP4 file on disk
	writeFakeMP4(filepath.Join("orphan_subdir", "stray_clip.mp4"), strings.Repeat("Z", 4096))
	writeFakeMP4("notes.txt", "not a video")

	jobs := []*model.RenderJob{
		{
			ID:            "job-posted",
			ProjectID:     "proj-transcript-test",
			CampaignID:    "windah-gtavi-motionklip",
			CampaignName:  "Windah GTA VI",
			Title:         "Red Hulk Smash 16:9",
			Status:        "done",
			ReviewStatus:  "approved",
			BoardColumn:   "diposting",
			OutputPath:    pPosted,
			OutputFile:    "posted.mp4",
			FileSizeBytes: 1024,
			FinalDuration: 28.5,
			Options:       model.RenderOptions{Layout: model.LayoutConfig{AspectRatio: "16:9"}},
			Compliance:    model.ComplianceCheck{WatermarkApplied: true, OutroClean: true},
			PublishRecords: []model.PublishRecord{
				{
					Platform:     "tiktok",
					Method:       "manual",
					Status:       "published",
					PublishedURL: "https://www.tiktok.com/@windah/video/111",
					UpdatedAt:    now.Add(-2 * time.Hour),
				},
			},
			CreatedAt: now.Add(-4 * time.Hour),
		},
		{
			ID:            "job-pub-old",
			ProjectID:     "proj-transcript-test",
			CampaignID:    "windah-gtavi-motionklip",
			CampaignName:  "Windah GTA VI",
			Title:         "Mancing Dapet Ikan Loncat",
			Status:        "done",
			ReviewStatus:  "approved",
			BoardColumn:   "siap_posting",
			OutputPath:    pPubOld,
			OutputFile:    "pub_old.mp4",
			FileSizeBytes: 2048,
			FinalDuration: 34.2,
			Options:       model.RenderOptions{Layout: model.LayoutConfig{AspectRatio: "16:9"}},
			Compliance:    model.ComplianceCheck{WatermarkApplied: true, OutroClean: true},
			PostCaption:   "Mancing ikan loncat bikin ngakak!",
			HashtagLine:   "#WindahBasudara #GTAVI",
			CoverFrameURL: "/api/jobs/job-pub-old/cover",
			PeekFrameURL:  "/api/jobs/job-pub-old/peek",
			CreatedAt:     now.Add(-3 * time.Hour),
		},
		{
			ID:            "job-pub-new",
			ProjectID:     "proj-transcript-test",
			CampaignID:    "windah-gtavi-motionklip",
			CampaignName:  "Windah GTA VI",
			Title:         "Sepeda Mental Ragdoll 16:9",
			Status:        "done",
			ReviewStatus:  "approved",
			BoardColumn:   "siap_posting",
			OutputPath:    pPubNew,
			OutputFile:    "pub_new.mp4",
			FileSizeBytes: 3072,
			FinalDuration: 22.0,
			Compliance:    model.ComplianceCheck{WatermarkApplied: true, OutroClean: false},
			PostCaption:   "Sepeda mental ragdoll!",
			HashtagLine:   "#WindahBasudara",
			CreatedAt:     now.Add(-1 * time.Hour),
		},
		{
			ID:            "job-pending",
			ProjectID:     "proj-transcript-test",
			Title:         "Pending Review Clip",
			Status:        "done",
			ReviewStatus:  "pending_review",
			OutputPath:    pPending,
			OutputFile:    "pending.mp4",
			FileSizeBytes: 512,
			CreatedAt:     now.Add(-50 * time.Minute),
		},
		{
			ID:            "job-rej-1",
			ProjectID:     "proj-transcript-test",
			Title:         "Rejected Clip 1",
			Status:        "done",
			ReviewStatus:  "rejected",
			RejectReason:  "jelek",
			OutputPath:    pRej1,
			OutputFile:    "rej1.mp4",
			FileSizeBytes: 512,
			CreatedAt:     now.Add(-40 * time.Minute),
		},
		{
			ID:            "job-rej-2",
			ProjectID:     "proj-transcript-test",
			Title:         "Rejected Clip 2",
			Status:        "done",
			ReviewStatus:  "rejected",
			RejectReason:  "Jelek",
			OutputPath:    pRej2,
			OutputFile:    "rej2.mp4",
			FileSizeBytes: 512,
			CreatedAt:     now.Add(-30 * time.Minute),
		},
		{
			ID:            "job-rej-3",
			ProjectID:     "proj-transcript-test",
			Title:         "Rejected Clip 3",
			Status:        "done",
			ReviewStatus:  "rejected",
			RejectReason:  "momentnya gak pas",
			OutputPath:    pRej3,
			OutputFile:    "rej3.mp4",
			FileSizeBytes: 512,
			CreatedAt:     now.Add(-20 * time.Minute),
		},
		{
			ID:           "job-missing",
			ProjectID:    "proj-transcript-test",
			Title:        "Missing File Approved",
			Status:       "done",
			ReviewStatus: "approved",
			OutputPath:   filepath.Join(outDir, "nonexistent_deleted.mp4"),
			OutputFile:   "nonexistent_deleted.mp4",
			CreatedAt:    now.Add(-15 * time.Minute),
		},
		{
			ID:        "job-failed",
			ProjectID: "proj-transcript-test",
			Title:     "Errored Render",
			Status:    "error",
			Error:     "ffmpeg exited 1",
			CreatedAt: now.Add(-10 * time.Minute),
		},
	}
	for _, j := range jobs {
		if err := st.SaveJob(j); err != nil {
			t.Fatalf("SaveJob %s: %v", j.ID, err)
		}
	}

	proj := &model.Project{
		ID:          "proj-transcript-test",
		VideoID:     "vidTrans001",
		Title:       "Windah GTA VI Full Stream",
		DurationSec: 4000,
		Status:      "ready",
		CreatedAt:   now.Add(-5 * time.Hour),
	}
	if err := st.SaveProject(proj); err != nil {
		t.Fatalf("SaveProject: %v", err)
	}
	cues := []model.TranscriptCue{
		{Start: 10.0, End: 14.5, Text: "Halo guys balik lagi di GTA VI", Speaker: "SPEAKER_0"},
		{Start: 62.0, End: 68.0, Text: "Waduh Red Hulk datang guys!", Speaker: "SPEAKER_0"},
		{Start: 125.0, End: 132.0, Text: "Awas mobilnya nabrak tembok!", Speaker: "SPEAKER_1"},
		{Start: 185.0, End: 192.0, Text: "Hulk smash mobil polisi mental!", Speaker: "SPEAKER_0"},
		{Start: 3665.0, End: 3672.0, Text: "Udah sejam lebih masih mancing ikan loncat", Speaker: "SPEAKER_0"},
	}
	rawCues, _ := json.Marshal(cues)
	projDir := st.ProjectDir(proj.ID)
	if err := os.WriteFile(filepath.Join(projDir, "diarized_cues.json"), rawCues, 0o644); err != nil {
		t.Fatalf("WriteFile diarized_cues.json: %v", err)
	}

	srv := New(st, deps.NewManager(tmp), events.NewBroker())
	t.Cleanup(srv.Close)
	return srv, st, srv.Handler(), qc, clipper
}

func TestP0Ops_InventoryAuditAndOrphans(t *testing.T) {
	srv, st, h, _, _ := setupP0OpsTestServer(t)

	// Both Owner and Agent can read GET /api/inventory
	for _, hdr := range []struct {
		name string
		k, v string
	}{
		{"owner", "X-Engine-Token", srv.EngineToken},
		{"agent", "X-Agent-Key", srv.AgentKey},
	} {
		t.Run(hdr.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/api/inventory", nil)
			req.Header.Set(hdr.k, hdr.v)
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != http.StatusOK {
				t.Fatalf("expected 200 from GET /api/inventory (%s), got %d: %s", hdr.name, rec.Code, rec.Body.String())
			}

			var resp struct {
				OK        bool   `json:"ok"`
				DataScope string `json:"dataScope"`
				Summary   struct {
					TotalJobs           int   `json:"totalJobs"`
					Publishable         int   `json:"publishable"`
					Posted              int   `json:"posted"`
					PendingReview       int   `json:"pendingReview"`
					RejectedRecoverable int   `json:"rejectedRecoverable"`
					Rejected            int   `json:"rejected"`
					BrokenMissingFile   int   `json:"brokenMissingFile"`
					MissingCount        int   `json:"missingCount"`
					Failed              int   `json:"failed"`
					OrphanDiskMp4s      int   `json:"orphanDiskMp4s"`
					OrphanCount         int   `json:"orphanCount"`
					TotalDiskBytes      int64 `json:"totalDiskBytes"`
				} `json:"summary"`
				RejectionReasons []struct {
					Reason string `json:"reason"`
					Count  int    `json:"count"`
				} `json:"rejectionReasons"`
				Orphans []struct {
					Path      string `json:"path"`
					FileName  string `json:"fileName"`
					SizeBytes int64  `json:"sizeBytes"`
				} `json:"orphans"`
				Items []struct {
					ID         string `json:"id"`
					Category   string `json:"category"`
					FileExists bool   `json:"fileExists"`
				} `json:"items"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
				t.Fatalf("decode /api/inventory: %v", err)
			}
			if !resp.OK || resp.DataScope != "IN-SAMPLE" {
				t.Fatalf("expected ok=true dataScope=IN-SAMPLE, got ok=%v scope=%q", resp.OK, resp.DataScope)
			}
			s := resp.Summary
			if s.TotalJobs != 9 || s.Publishable != 2 || s.Posted != 1 || s.PendingReview != 1 ||
				s.RejectedRecoverable != 3 || s.Rejected != 3 ||
				s.BrokenMissingFile != 1 || s.MissingCount != 1 ||
				s.Failed != 1 || s.OrphanDiskMp4s != 1 || s.OrphanCount != 1 {
				t.Fatalf("unexpected inventory summary: %+v", s)
			}
			// 1024+2048+3072+512+512+512+512 + 4096 = 12288 bytes
			if s.TotalDiskBytes != 12288 {
				t.Fatalf("expected totalDiskBytes=12288, got %d", s.TotalDiskBytes)
			}
			if len(resp.RejectionReasons) != 2 || resp.RejectionReasons[0].Count != 2 || !strings.EqualFold(resp.RejectionReasons[0].Reason, "jelek") {
				t.Fatalf("expected rejectionReasons sorted descending ([jelek:2, momentnya gak pas:1]), got %+v", resp.RejectionReasons)
			}
			if len(resp.Orphans) != 1 || resp.Orphans[0].FileName != "stray_clip.mp4" || resp.Orphans[0].SizeBytes != 4096 {
				t.Fatalf("expected 1 orphan stray_clip.mp4 (4096B), got %+v", resp.Orphans)
			}
			if len(resp.Items) != 9 {
				t.Fatalf("expected 9 inventory items, got %d", len(resp.Items))
			}
			catByID := map[string]string{}
			for _, it := range resp.Items {
				catByID[it.ID] = it.Category
			}
			wantCats := map[string]string{
				"job-posted":  "posted",
				"job-pub-old": "publishable",
				"job-pub-new": "publishable",
				"job-pending": "pending_review",
				"job-rej-1":   "rejected_recoverable",
				"job-missing": "broken_missing_file",
				"job-failed":  "failed",
			}
			for id, want := range wantCats {
				if catByID[id] != want {
					t.Fatalf("job %s: expected category %q, got %q", id, want, catByID[id])
				}
			}
			// Read-only invariant: store job count untouched
			if len(st.ListJobs()) != 9 {
				t.Fatalf("inventory audit mutated jobs count: %d", len(st.ListJobs()))
			}
		})
	}
}

func TestP0Ops_PublishableQueueFIFOAndFields(t *testing.T) {
	srv, st, h, _, _ := setupP0OpsTestServer(t)

	// Seed recorded views for job-pub-old to verify totalRecordedViews aggregation
	if _, err := st.AddViewsLedgerEntry(model.ViewsLedgerEntry{
		JobID:    "job-pub-old",
		Platform: "tiktok",
		Views:    2500,
	}); err != nil {
		t.Fatalf("AddViewsLedgerEntry 1: %v", err)
	}
	if _, err := st.AddViewsLedgerEntry(model.ViewsLedgerEntry{
		JobID:    "job-pub-old",
		Platform: "youtube",
		Views:    1700,
	}); err != nil {
		t.Fatalf("AddViewsLedgerEntry 2: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/publishable", nil)
	req.Header.Set("X-Engine-Token", srv.EngineToken)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 from GET /api/publishable, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp struct {
		OK                bool `json:"ok"`
		Count             int  `json:"count"`
		ManualPostingOnly bool `json:"manualPostingOnly"`
		Items             []struct {
			ID                 string   `json:"id"`
			JobID              string   `json:"jobId"`
			Title              string   `json:"title"`
			AspectRatio        string   `json:"aspectRatio"`
			Format             string   `json:"format"`
			DownloadURL        string   `json:"downloadUrl"`
			VideoURL           string   `json:"videoUrl"`
			CoverURL           string   `json:"coverUrl"`
			CoverFrameURL      string   `json:"coverFrameUrl"`
			PeekURL            string   `json:"peekUrl"`
			PostCaption        string   `json:"postCaption"`
			HashtagLine        string   `json:"hashtagLine"`
			WatermarkApplied   bool     `json:"watermarkApplied"`
			OutroClean         bool     `json:"outroClean"`
			ReadyToPost        bool     `json:"readyToPost"`
			PostedPlatforms    []string `json:"postedPlatforms"`
			RemainingPlatforms []string `json:"remainingPlatforms"`
			TotalRecordedViews int64    `json:"totalRecordedViews"`
		} `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode /api/publishable: %v", err)
	}
	if !resp.OK || !resp.ManualPostingOnly || resp.Count != 3 || len(resp.Items) != 3 {
		t.Fatalf("expected ok=true manualPostingOnly=true count=3, got %+v", resp)
	}

	// Strictly FIFO (oldest CreatedAt first): job-posted (-4h), job-pub-old (-3h), job-pub-new (-1h)
	gotOrder := []string{resp.Items[0].ID, resp.Items[1].ID, resp.Items[2].ID}
	wantOrder := []string{"job-posted", "job-pub-old", "job-pub-new"}
	for i := range wantOrder {
		if gotOrder[i] != wantOrder[i] {
			t.Fatalf("expected FIFO order %v, got %v", wantOrder, gotOrder)
		}
	}

	// job-posted has tiktok posted -> remaining youtube, instagram
	if len(resp.Items[0].PostedPlatforms) != 1 || resp.Items[0].PostedPlatforms[0] != "tiktok" {
		t.Fatalf("expected job-posted postedPlatforms=[tiktok], got %v", resp.Items[0].PostedPlatforms)
	}
	if len(resp.Items[0].RemainingPlatforms) != 2 || resp.Items[0].RemainingPlatforms[0] != "youtube" || resp.Items[0].RemainingPlatforms[1] != "instagram" {
		t.Fatalf("expected job-posted remainingPlatforms=[youtube instagram], got %v", resp.Items[0].RemainingPlatforms)
	}

	// job-pub-old checks
	it1 := resp.Items[1]
	if it1.TotalRecordedViews != 4200 {
		t.Fatalf("expected job-pub-old totalRecordedViews=4200, got %d", it1.TotalRecordedViews)
	}
	if it1.AspectRatio != "16:9" || it1.Format != "16:9" {
		t.Fatalf("expected aspectRatio/format=16:9, got %q/%q", it1.AspectRatio, it1.Format)
	}
	if it1.DownloadURL != "/api/jobs/job-pub-old/video?download=1" || it1.VideoURL != "/api/jobs/job-pub-old/video" {
		t.Fatalf("unexpected URLs: download=%q video=%q", it1.DownloadURL, it1.VideoURL)
	}
	if it1.CoverURL != "/api/jobs/job-pub-old/cover" || it1.CoverFrameURL != "/api/jobs/job-pub-old/cover" || it1.PeekURL != "/api/jobs/job-pub-old/peek" {
		t.Fatalf("unexpected cover/peek URLs: cover=%q coverFrame=%q peek=%q", it1.CoverURL, it1.CoverFrameURL, it1.PeekURL)
	}
	if !it1.WatermarkApplied || !it1.OutroClean || !it1.ReadyToPost || it1.PostCaption == "" || it1.HashtagLine == "" {
		t.Fatalf("unexpected compliance/caption fields on job-pub-old: %+v", it1)
	}

	// job-pub-new infers "16:9" from title when Layout.AspectRatio is empty
	if resp.Items[2].AspectRatio != "16:9" || resp.Items[2].Format != "16:9" {
		t.Fatalf("expected job-pub-new inferred aspectRatio=16:9 from title, got %q/%q", resp.Items[2].AspectRatio, resp.Items[2].Format)
	}
}

func TestP0Ops_MarkPostedFlowGuardsAndRBAC(t *testing.T) {
	srv, st, h, qc, clipper := setupP0OpsTestServer(t)

	postMark := func(jobID, hdrKey, hdrVal, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/api/jobs/"+jobID+"/mark-posted", bytes.NewBufferString(body))
		if hdrKey != "" {
			req.Header.Set(hdrKey, hdrVal)
		}
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}

	// 1. Unauthenticated -> 401
	if rec := postMark("job-pub-old", "", "", `{"platform":"tiktok"}`); rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 unauthenticated, got %d", rec.Code)
	}
	// 2. Agent -> 403 Forbidden
	if rec := postMark("job-pub-old", "X-Agent-Key", srv.AgentKey, `{"platform":"tiktok"}`); rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for agent on mark-posted, got %d: %s", rec.Code, rec.Body.String())
	}
	// 3. Clipper -> 403 Forbidden
	if rec := postMark("job-pub-old", "X-Access-Key", clipper.AccessKey, `{"platform":"tiktok"}`); rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for clipper on mark-posted, got %d: %s", rec.Code, rec.Body.String())
	}
	// 4. Not found -> 404
	if rec := postMark("job-does-not-exist", "X-Engine-Token", srv.EngineToken, `{"platform":"tiktok"}`); rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for unknown job, got %d", rec.Code)
	}
	// 5. Not approved -> 409 Conflict
	if rec := postMark("job-pending", "X-Engine-Token", srv.EngineToken, `{"platform":"tiktok"}`); rec.Code != http.StatusConflict {
		t.Fatalf("expected 409 for pending_review job, got %d: %s", rec.Code, rec.Body.String())
	}
	// 6. Missing file -> 409 Conflict
	if rec := postMark("job-missing", "X-Engine-Token", srv.EngineToken, `{"platform":"tiktok"}`); rec.Code != http.StatusConflict {
		t.Fatalf("expected 409 for missing file job, got %d: %s", rec.Code, rec.Body.String())
	}
	// 7. Invalid platform -> 400 Bad Request
	if rec := postMark("job-pub-old", "X-Engine-Token", srv.EngineToken, `{"platform":"facebook"}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for invalid platform, got %d: %s", rec.Code, rec.Body.String())
	}

	// 8. Valid mark-posted by QC with views/likes/url -> 200 OK + boardColumn=diposting + ledger entry created!
	rec := postMark("job-pub-old", "X-Access-Key", qc.AccessKey,
		`{"platform":"tiktok","publishedUrl":"https://www.tiktok.com/@windah/video/999","views":1500,"likes":120,"note":"Manual upload jam 7"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 for QC mark-posted, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		OK          bool                    `json:"ok"`
		JobID       string                  `json:"jobId"`
		Platform    string                  `json:"platform"`
		Method      string                  `json:"method"`
		Status      string                  `json:"status"`
		BoardColumn string                  `json:"boardColumn"`
		LedgerEntry *model.ViewsLedgerEntry `json:"ledgerEntry"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode mark-posted response: %v", err)
	}
	if !resp.OK || resp.JobID != "job-pub-old" || resp.Platform != "tiktok" || resp.Method != "manual" || resp.Status != "published" || resp.BoardColumn != "diposting" {
		t.Fatalf("unexpected mark-posted response: %+v", resp)
	}
	if resp.LedgerEntry == nil || resp.LedgerEntry.Views != 1500 || resp.LedgerEntry.Likes != 120 || resp.LedgerEntry.SampleSplit != "IN-SAMPLE" || resp.LedgerEntry.RecordedBy != qc.Name {
		t.Fatalf("unexpected ledgerEntry from mark-posted: %+v", resp.LedgerEntry)
	}

	// Verify persisted job state
	savedJob, ok := st.GetJob("job-pub-old")
	if !ok || savedJob.BoardColumn != "diposting" || len(savedJob.PublishRecords) != 1 || savedJob.PublishRecords[0].Method != "manual" || savedJob.PublishRecords[0].Status != "published" {
		t.Fatalf("job not persisted properly after mark-posted: %+v", savedJob)
	}

	// 9. Valid mark-posted by Owner without URL/metrics -> 200 OK with nil ledgerEntry
	rec2 := postMark("job-pub-new", "X-Engine-Token", srv.EngineToken, `{"platform":"youtube","note":"uploaded manually"}`)
	if rec2.Code != http.StatusOK {
		t.Fatalf("expected 200 for Owner mark-posted without metrics, got %d: %s", rec2.Code, rec2.Body.String())
	}
	var resp2 struct {
		OK          bool                    `json:"ok"`
		LedgerEntry *model.ViewsLedgerEntry `json:"ledgerEntry"`
	}
	if err := json.Unmarshal(rec2.Body.Bytes(), &resp2); err != nil {
		t.Fatalf("decode mark-posted response 2: %v", err)
	}
	if !resp2.OK || resp2.LedgerEntry != nil {
		t.Fatalf("expected nil ledgerEntry when no url/metrics passed, got %+v", resp2.LedgerEntry)
	}
}

func TestP0Ops_ViewsLedgerCRUDFilterAndRBAC(t *testing.T) {
	srv, _, h, qc, clipper := setupP0OpsTestServer(t)

	// 1. Agent POST /api/ledger/views -> 403 Forbidden
	reqA := httptest.NewRequest(http.MethodPost, "/api/ledger/views", bytes.NewBufferString(`{"jobId":"job-pub-old","platform":"tiktok","views":500}`))
	reqA.Header.Set("X-Agent-Key", srv.AgentKey)
	recA := httptest.NewRecorder()
	h.ServeHTTP(recA, reqA)
	if recA.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for agent POST /api/ledger/views, got %d: %s", recA.Code, recA.Body.String())
	}

	// 2. Agent GET /api/ledger/views -> 200 OK
	reqAGet := httptest.NewRequest(http.MethodGet, "/api/ledger/views", nil)
	reqAGet.Header.Set("X-Agent-Key", srv.AgentKey)
	recAGet := httptest.NewRecorder()
	h.ServeHTTP(recAGet, reqAGet)
	if recAGet.Code != http.StatusOK {
		t.Fatalf("expected 200 for agent GET /api/ledger/views, got %d: %s", recAGet.Code, recAGet.Body.String())
	}

	// 3. Clipper POST /api/ledger/views -> 201 Created + enriched from Job!
	reqC := httptest.NewRequest(http.MethodPost, "/api/ledger/views", bytes.NewBufferString(`{"jobId":"job-pub-old","platform":"tiktok","views":5000,"likes":300}`))
	reqC.Header.Set("X-Access-Key", clipper.AccessKey)
	recC := httptest.NewRecorder()
	h.ServeHTTP(recC, reqC)
	if recC.Code != http.StatusCreated {
		t.Fatalf("expected 201 for clipper POST /api/ledger/views, got %d: %s", recC.Code, recC.Body.String())
	}
	var addResp1 struct {
		OK      bool                     `json:"ok"`
		Entry   model.ViewsLedgerEntry   `json:"entry"`
		Summary model.ViewsLedgerSummary `json:"summary"`
	}
	if err := json.Unmarshal(recC.Body.Bytes(), &addResp1); err != nil {
		t.Fatalf("decode add ledger 1: %v", err)
	}
	if !addResp1.OK || addResp1.Entry.Title != "Mancing Dapet Ikan Loncat" || addResp1.Entry.ProjectID != "proj-transcript-test" ||
		addResp1.Entry.Format != "16:9" || addResp1.Entry.SampleSplit != "IN-SAMPLE" || addResp1.Entry.RecordedBy != clipper.Name {
		t.Fatalf("expected enriched ledger entry, got %+v", addResp1.Entry)
	}

	// 4. QC POST /api/ledger/views (HELD-OUT) -> 201 Created
	reqQ := httptest.NewRequest(http.MethodPost, "/api/ledger/views", bytes.NewBufferString(`{"jobId":"job-pub-new","platform":"youtube","views":12000,"likes":800,"sampleSplit":"HELD-OUT"}`))
	reqQ.Header.Set("X-Access-Key", qc.AccessKey)
	recQ := httptest.NewRecorder()
	h.ServeHTTP(recQ, reqQ)
	if recQ.Code != http.StatusCreated {
		t.Fatalf("expected 201 for QC POST /api/ledger/views, got %d: %s", recQ.Code, recQ.Body.String())
	}

	// 5. Validation error (invalid platform / negative views) -> 400 Bad Request
	reqBad := httptest.NewRequest(http.MethodPost, "/api/ledger/views", bytes.NewBufferString(`{"jobId":"job-pub-old","platform":"twitch","views":10}`))
	reqBad.Header.Set("X-Engine-Token", srv.EngineToken)
	recBad := httptest.NewRecorder()
	h.ServeHTTP(recBad, reqBad)
	if recBad.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for invalid ledger entry, got %d: %s", recBad.Code, recBad.Body.String())
	}

	// 6. GET /api/ledger/views (unfiltered + filtered)
	reqAll := httptest.NewRequest(http.MethodGet, "/api/ledger/views", nil)
	reqAll.Header.Set("X-Engine-Token", srv.EngineToken)
	recAll := httptest.NewRecorder()
	h.ServeHTTP(recAll, reqAll)
	if recAll.Code != http.StatusOK {
		t.Fatalf("expected 200 GET /api/ledger/views, got %d", recAll.Code)
	}
	var listAll struct {
		OK      bool                     `json:"ok"`
		Entries []model.ViewsLedgerEntry `json:"entries"`
		Summary model.ViewsLedgerSummary `json:"summary"`
	}
	if err := json.Unmarshal(recAll.Body.Bytes(), &listAll); err != nil {
		t.Fatalf("decode GET /api/ledger/views: %v", err)
	}
	if !listAll.OK || len(listAll.Entries) != 2 || listAll.Summary.TotalViews != 17000 || listAll.Summary.DataScopeNote == "" {
		t.Fatalf("unexpected ledger list/summary: %+v", listAll)
	}
	if listAll.Summary.BySplit["IN-SAMPLE"].Entries != 1 || listAll.Summary.BySplit["HELD-OUT"].Entries != 1 {
		t.Fatalf("unexpected bySplit breakdown: %+v", listAll.Summary.BySplit)
	}

	reqFilt := httptest.NewRequest(http.MethodGet, "/api/ledger/views?platform=youtube&split=HELD-OUT&jobId=job-pub-new", nil)
	reqFilt.Header.Set("X-Engine-Token", srv.EngineToken)
	recFilt := httptest.NewRecorder()
	h.ServeHTTP(recFilt, reqFilt)
	var listFilt struct {
		Entries []model.ViewsLedgerEntry `json:"entries"`
	}
	_ = json.Unmarshal(recFilt.Body.Bytes(), &listFilt)
	if len(listFilt.Entries) != 1 || listFilt.Entries[0].JobID != "job-pub-new" {
		t.Fatalf("expected 1 filtered ledger entry for job-pub-new, got %+v", listFilt.Entries)
	}
}

func TestP0Ops_TranscriptBrowserPaginationAndFilters(t *testing.T) {
	srv, _, h, _, _ := setupP0OpsTestServer(t)

	// 1. Unknown project -> 404
	req404 := httptest.NewRequest(http.MethodGet, "/api/projects/no-such-proj/transcript", nil)
	req404.Header.Set("X-Engine-Token", srv.EngineToken)
	rec404 := httptest.NewRecorder()
	h.ServeHTTP(rec404, req404)
	if rec404.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for unknown project transcript, got %d", rec404.Code)
	}

	// 2. Paginated read as Agent: offset=0, limit=2
	req1 := httptest.NewRequest(http.MethodGet, "/api/projects/proj-transcript-test/transcript?offset=0&limit=2", nil)
	req1.Header.Set("X-Agent-Key", srv.AgentKey)
	rec1 := httptest.NewRecorder()
	h.ServeHTTP(rec1, req1)
	if rec1.Code != http.StatusOK {
		t.Fatalf("expected 200 GET transcript, got %d: %s", rec1.Code, rec1.Body.String())
	}
	type cueResp struct {
		Index    int     `json:"index"`
		Start    float64 `json:"start"`
		End      float64 `json:"end"`
		TimeText string  `json:"timeText"`
		Text     string  `json:"text"`
		Speaker  string  `json:"speaker"`
	}
	var page1 struct {
		OK        bool      `json:"ok"`
		ProjectID string    `json:"projectId"`
		Total     int       `json:"total"`
		Offset    int       `json:"offset"`
		Limit     int       `json:"limit"`
		HasMore   bool      `json:"hasMore"`
		Cues      []cueResp `json:"cues"`
	}
	if err := json.Unmarshal(rec1.Body.Bytes(), &page1); err != nil {
		t.Fatalf("decode transcript page1: %v", err)
	}
	if !page1.OK || page1.ProjectID != "proj-transcript-test" || page1.Total != 5 || page1.Offset != 0 || page1.Limit != 2 || !page1.HasMore || len(page1.Cues) != 2 {
		t.Fatalf("unexpected page1: %+v", page1)
	}
	if page1.Cues[0].Index != 0 || page1.Cues[0].TimeText != "00:10" || page1.Cues[0].Speaker != "SPEAKER_0" {
		t.Fatalf("unexpected cue[0]: %+v", page1.Cues[0])
	}
	if page1.Cues[1].Index != 1 || page1.Cues[1].TimeText != "01:02" {
		t.Fatalf("unexpected cue[1]: %+v", page1.Cues[1])
	}

	// 3. Last page: offset=4, limit=2 -> 1 item, hour formatting, hasMore=false
	reqLast := httptest.NewRequest(http.MethodGet, "/api/projects/proj-transcript-test/transcript?offset=4&limit=2", nil)
	reqLast.Header.Set("X-Engine-Token", srv.EngineToken)
	recLast := httptest.NewRecorder()
	h.ServeHTTP(recLast, reqLast)
	var pageLast struct {
		Total   int       `json:"total"`
		HasMore bool      `json:"hasMore"`
		Cues    []cueResp `json:"cues"`
	}
	_ = json.Unmarshal(recLast.Body.Bytes(), &pageLast)
	if pageLast.Total != 5 || pageLast.HasMore || len(pageLast.Cues) != 1 || pageLast.Cues[0].Index != 4 || pageLast.Cues[0].TimeText != "1:01:05" {
		t.Fatalf("unexpected last page: %+v", pageLast)
	}

	// 4. Time range filter: fromSec=65&toSec=150 -> cues[1] (62..68, End>=65) and cues[2] (125..132, Start<=150)
	reqRange := httptest.NewRequest(http.MethodGet, "/api/projects/proj-transcript-test/transcript?fromSec=65&toSec=150", nil)
	reqRange.Header.Set("X-Engine-Token", srv.EngineToken)
	recRange := httptest.NewRecorder()
	h.ServeHTTP(recRange, reqRange)
	var pageRange struct {
		Total int       `json:"total"`
		Cues  []cueResp `json:"cues"`
	}
	_ = json.Unmarshal(recRange.Body.Bytes(), &pageRange)
	if pageRange.Total != 2 || len(pageRange.Cues) != 2 || pageRange.Cues[0].Index != 1 || pageRange.Cues[1].Index != 2 {
		t.Fatalf("expected cues[1,2] for fromSec=65&toSec=150, got %+v", pageRange)
	}

	// 5. Case-insensitive text filter q=HULK -> cues[1] and cues[3]
	reqQ := httptest.NewRequest(http.MethodGet, "/api/projects/proj-transcript-test/transcript?q=HULK", nil)
	reqQ.Header.Set("X-Engine-Token", srv.EngineToken)
	recQ := httptest.NewRecorder()
	h.ServeHTTP(recQ, reqQ)
	var pageQ struct {
		Total int       `json:"total"`
		Cues  []cueResp `json:"cues"`
	}
	_ = json.Unmarshal(recQ.Body.Bytes(), &pageQ)
	if pageQ.Total != 2 || len(pageQ.Cues) != 2 || pageQ.Cues[0].Index != 1 || pageQ.Cues[1].Index != 3 {
		t.Fatalf("expected cues[1,3] for q=HULK, got %+v", pageQ)
	}

	// 6. Limit clamping (> 200 -> 200)
	reqLim := httptest.NewRequest(http.MethodGet, "/api/projects/proj-transcript-test/transcript?limit=999", nil)
	reqLim.Header.Set("X-Engine-Token", srv.EngineToken)
	recLim := httptest.NewRecorder()
	h.ServeHTTP(recLim, reqLim)
	var pageLim struct {
		Limit int `json:"limit"`
	}
	_ = json.Unmarshal(recLim.Body.Bytes(), &pageLim)
	if pageLim.Limit != 200 {
		t.Fatalf("expected limit clamped to 200, got %d", pageLim.Limit)
	}
}
