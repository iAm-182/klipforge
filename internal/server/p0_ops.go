package server

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"klipforge/internal/analyzer"
	"klipforge/internal/model"
	"klipforge/internal/store"
)

type inventorySummary struct {
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
}

type inventoryReasonCount struct {
	Reason string `json:"reason"`
	Count  int    `json:"count"`
}

type inventoryOrphan struct {
	Path         string    `json:"path"`
	FileName     string    `json:"fileName"`
	RelativePath string    `json:"relativePath"`
	SizeBytes    int64     `json:"sizeBytes"`
	ModifiedAt   time.Time `json:"modifiedAt"`
}

type inventoryItem struct {
	ID              string    `json:"id"`
	JobID           string    `json:"jobId"`
	ProjectID       string    `json:"projectId,omitempty"`
	CampaignID      string    `json:"campaignId,omitempty"`
	CampaignName    string    `json:"campaignName,omitempty"`
	Title           string    `json:"title"`
	Status          string    `json:"status"`
	ReviewStatus    string    `json:"reviewStatus"`
	RejectReason    string    `json:"rejectReason,omitempty"`
	BoardColumn     string    `json:"boardColumn,omitempty"`
	Category        string    `json:"category"`
	FileExists      bool      `json:"fileExists"`
	OutputPath      string    `json:"outputPath,omitempty"`
	OutputFile      string    `json:"outputFile,omitempty"`
	FileSizeBytes   int64     `json:"fileSizeBytes"`
	FinalDuration   float64   `json:"finalDuration"`
	AspectRatio     string    `json:"aspectRatio"`
	PostedPlatforms []string  `json:"postedPlatforms,omitempty"`
	CreatedAt       time.Time `json:"createdAt"`
}

type publishableQueueItem struct {
	ID                 string            `json:"id"`
	JobID              string            `json:"jobId"`
	ProjectID          string            `json:"projectId,omitempty"`
	CampaignID         string            `json:"campaignId,omitempty"`
	CampaignName       string            `json:"campaignName,omitempty"`
	Title              string            `json:"title"`
	Status             string            `json:"status"`
	ReviewStatus       string            `json:"reviewStatus"`
	BoardColumn        string            `json:"boardColumn,omitempty"`
	OutputFile         string            `json:"outputFile,omitempty"`
	FileSizeBytes      int64             `json:"fileSizeBytes"`
	FinalDuration      float64           `json:"finalDuration"`
	AspectRatio        string            `json:"aspectRatio"`
	Format             string            `json:"format"`
	DownloadURL        string            `json:"downloadUrl"`
	VideoURL           string            `json:"videoUrl"`
	CoverURL           string            `json:"coverUrl,omitempty"`
	CoverFrameURL      string            `json:"coverFrameUrl,omitempty"`
	PeekURL            string            `json:"peekUrl,omitempty"`
	PeekFrameURL       string            `json:"peekFrameUrl,omitempty"`
	PostCaption        string            `json:"postCaption"`
	HashtagLine        string            `json:"hashtagLine"`
	PublishKit         *model.PublishKit `json:"publishKit,omitempty"`
	WatermarkApplied   bool              `json:"watermarkApplied"`
	OutroClean         bool              `json:"outroClean"`
	ReadyToPost        bool              `json:"readyToPost"`
	PostedPlatforms    []string          `json:"postedPlatforms"`
	RemainingPlatforms []string          `json:"remainingPlatforms"`
	TotalRecordedViews int64             `json:"totalRecordedViews"`
	CreatedAt          time.Time         `json:"createdAt"`
	ReviewedAt         *time.Time        `json:"reviewedAt,omitempty"`
}

type transcriptBrowserCue struct {
	Index    int     `json:"index"`
	Start    float64 `json:"start"`
	End      float64 `json:"end"`
	TimeText string  `json:"timeText"`
	Text     string  `json:"text"`
	Speaker  string  `json:"speaker,omitempty"`
}

var manualPostPlatforms = []string{"tiktok", "youtube", "instagram"}

func isValidManualPlatform(p string) bool {
	return p == "tiktok" || p == "youtube" || p == "instagram"
}

func canonicalDiskKey(p string) string {
	trimmed := strings.TrimSpace(p)
	if trimmed == "" {
		return ""
	}
	abs, err := filepath.Abs(filepath.Clean(trimmed))
	if err != nil {
		abs = filepath.Clean(trimmed)
	}
	if runtime.GOOS == "windows" {
		return strings.ToLower(abs)
	}
	return abs
}

func inferJobAspectRatio(j *model.RenderJob) string {
	if j == nil {
		return "9:16"
	}
	if ar := strings.TrimSpace(j.Options.Layout.AspectRatio); ar != "" {
		return ar
	}
	if j.CompileOpts != nil {
		if ar := strings.TrimSpace(j.CompileOpts.Layout.AspectRatio); ar != "" {
			return ar
		}
	}
	if strings.Contains(j.Title, "16:9") {
		return "16:9"
	}
	return "9:16"
}

func uniquePublishedPlatforms(records []model.PublishRecord) []string {
	seen := make(map[string]bool)
	var extras []string
	for _, pr := range records {
		if !strings.EqualFold(strings.TrimSpace(pr.Status), "published") {
			continue
		}
		plat := strings.ToLower(strings.TrimSpace(pr.Platform))
		if plat == "" || seen[plat] {
			continue
		}
		seen[plat] = true
		if !isValidManualPlatform(plat) {
			extras = append(extras, plat)
		}
	}
	out := make([]string, 0, len(seen))
	for _, plat := range manualPostPlatforms {
		if seen[plat] {
			out = append(out, plat)
		}
	}
	sort.Strings(extras)
	out = append(out, extras...)
	return out
}

func remainingPostPlatforms(posted []string) []string {
	done := make(map[string]bool, len(posted))
	for _, p := range posted {
		done[strings.ToLower(strings.TrimSpace(p))] = true
	}
	rem := make([]string, 0, len(manualPostPlatforms))
	for _, p := range manualPostPlatforms {
		if !done[p] {
			rem = append(rem, p)
		}
	}
	return rem
}

// handleGetInventory performs a 100% read-only audit of all jobs vs MP4 files on disk
// and detects orphan .mp4 files under OutputDir (I-02).
func (s *Server) handleGetInventory(w http.ResponseWriter, r *http.Request) {
	jobs := s.Store.ListJobs()
	summary := inventorySummary{
		TotalJobs: len(jobs),
	}

	referencedPaths := make(map[string]bool, len(jobs))
	seenDiskFiles := make(map[string]bool, len(jobs))
	reasonCounts := make(map[string]int)
	reasonLabels := make(map[string]string)
	items := make([]inventoryItem, 0, len(jobs))

	for _, j := range jobs {
		fileExists := pathExists(j.OutputPath)
		diskSize := j.FileSizeBytes
		if fileExists {
			if fi, err := os.Stat(j.OutputPath); err == nil && !fi.IsDir() {
				diskSize = fi.Size()
			}
			if key := canonicalDiskKey(j.OutputPath); key != "" {
				referencedPaths[key] = true
				if !seenDiskFiles[key] {
					seenDiskFiles[key] = true
					summary.TotalDiskBytes += diskSize
				}
			}
		}

		postedPlats := uniquePublishedPlatforms(j.PublishRecords)
		isPosted := len(postedPlats) > 0

		category := "failed"
		switch {
		case isPosted:
			category = "posted"
			summary.Posted++
		case (j.Status == "done" || j.ReviewStatus == "approved") && !fileExists:
			category = "broken_missing_file"
			summary.BrokenMissingFile++
		case j.Status == "done" && j.ReviewStatus == "approved" && fileExists:
			category = "publishable"
			summary.Publishable++
		case j.Status == "done" && (j.ReviewStatus == "" || j.ReviewStatus == "pending_review") && fileExists:
			category = "pending_review"
			summary.PendingReview++
		case j.Status == "done" && j.ReviewStatus == "rejected" && fileExists:
			category = "rejected_recoverable"
			summary.RejectedRecoverable++
		default:
			category = "failed"
			summary.Failed++
		}

		if j.ReviewStatus == "rejected" {
			reason := strings.TrimSpace(j.RejectReason)
			if reason == "" {
				reason = strings.TrimSpace(j.ReviewNote)
			}
			if reason == "" {
				reason = "(tanpa alasan)"
			}
			lk := strings.ToLower(reason)
			if _, ok := reasonLabels[lk]; !ok {
				reasonLabels[lk] = reason
			}
			reasonCounts[lk]++
		}

		items = append(items, inventoryItem{
			ID:              j.ID,
			JobID:           j.ID,
			ProjectID:       j.ProjectID,
			CampaignID:      j.CampaignID,
			CampaignName:    j.CampaignName,
			Title:           j.Title,
			Status:          j.Status,
			ReviewStatus:    j.ReviewStatus,
			RejectReason:    j.RejectReason,
			BoardColumn:     j.BoardColumn,
			Category:        category,
			FileExists:      fileExists,
			OutputPath:      j.OutputPath,
			OutputFile:      j.OutputFile,
			FileSizeBytes:   diskSize,
			FinalDuration:   j.FinalDuration,
			AspectRatio:     inferJobAspectRatio(j),
			PostedPlatforms: postedPlats,
			CreatedAt:       j.CreatedAt,
		})
	}

	outDir := strings.TrimSpace(s.Store.GetSettings().OutputDir)
	if outDir == "" {
		outDir = s.Store.OutputDir
	}
	orphans := make([]inventoryOrphan, 0)
	if outDir != "" {
		_ = filepath.WalkDir(outDir, func(path string, d fs.DirEntry, err error) error {
			if err != nil || d == nil || d.IsDir() {
				return nil
			}
			if !strings.HasSuffix(strings.ToLower(d.Name()), ".mp4") {
				return nil
			}
			key := canonicalDiskKey(path)
			if key == "" {
				return nil
			}
			var size int64
			var modTime time.Time
			if fi, statErr := d.Info(); statErr == nil {
				size = fi.Size()
				modTime = fi.ModTime()
			}
			if !seenDiskFiles[key] {
				seenDiskFiles[key] = true
				summary.TotalDiskBytes += size
			}
			if !referencedPaths[key] {
				absPath, aerr := filepath.Abs(path)
				if aerr != nil {
					absPath = path
				}
				rel, rerr := filepath.Rel(outDir, path)
				if rerr != nil {
					rel = d.Name()
				}
				orphans = append(orphans, inventoryOrphan{
					Path:         absPath,
					FileName:     d.Name(),
					RelativePath: filepath.ToSlash(rel),
					SizeBytes:    size,
					ModifiedAt:   modTime,
				})
			}
			return nil
		})
	}
	sort.Slice(orphans, func(i, j int) bool {
		return orphans[i].RelativePath < orphans[j].RelativePath
	})

	summary.Rejected = summary.RejectedRecoverable
	summary.MissingCount = summary.BrokenMissingFile
	summary.OrphanDiskMp4s = len(orphans)
	summary.OrphanCount = len(orphans)

	rejectionReasons := make([]inventoryReasonCount, 0, len(reasonCounts))
	for lk, cnt := range reasonCounts {
		rejectionReasons = append(rejectionReasons, inventoryReasonCount{
			Reason: reasonLabels[lk],
			Count:  cnt,
		})
	}
	sort.Slice(rejectionReasons, func(i, j int) bool {
		if rejectionReasons[i].Count != rejectionReasons[j].Count {
			return rejectionReasons[i].Count > rejectionReasons[j].Count
		}
		return strings.ToLower(rejectionReasons[i].Reason) < strings.ToLower(rejectionReasons[j].Reason)
	})

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"ok":               true,
		"dataScope":        "IN-SAMPLE",
		"outputDir":        outDir,
		"summary":          summary,
		"rejectionReasons": rejectionReasons,
		"orphans":          orphans,
		"items":            items,
	})
}

// handleListPublishable returns the FIFO manual posting queue: ONLY jobs where
// status == "done" && reviewStatus == "approved" && pathExists(OutputPath) (I-03).
func (s *Server) handleListPublishable(w http.ResponseWriter, r *http.Request) {
	all := s.Store.ListJobs()
	eligible := make([]*model.RenderJob, 0, len(all))
	for _, j := range all {
		if j.Status == "done" && j.ReviewStatus == "approved" && pathExists(j.OutputPath) {
			eligible = append(eligible, j)
		}
	}
	// FIFO: oldest CreatedAt first so oldest approved clips get posted first
	sort.Slice(eligible, func(i, j int) bool {
		if !eligible[i].CreatedAt.Equal(eligible[j].CreatedAt) {
			return eligible[i].CreatedAt.Before(eligible[j].CreatedAt)
		}
		return eligible[i].ID < eligible[j].ID
	})

	items := make([]publishableQueueItem, 0, len(eligible))
	for _, j := range eligible {
		posted := uniquePublishedPlatforms(j.PublishRecords)
		remaining := remainingPostPlatforms(posted)

		var totalViews int64
		for _, ent := range s.Store.ListViewsLedger(j.ID, "", "") {
			totalViews += ent.Views
		}

		sizeBytes := j.FileSizeBytes
		if sizeBytes <= 0 {
			if fi, err := os.Stat(j.OutputPath); err == nil && !fi.IsDir() {
				sizeBytes = fi.Size()
			}
		}

		aspect := inferJobAspectRatio(j)
		coverURL := j.CoverFrameURL
		if coverURL == "" && j.CoverFramePath != "" && pathExists(j.CoverFramePath) {
			coverURL = fmt.Sprintf("/api/jobs/%s/cover", j.ID)
		}
		peekURL := j.PeekFrameURL
		if peekURL == "" {
			peekURL = fmt.Sprintf("/api/jobs/%s/peek", j.ID)
		}

		readyToPost := true
		if j.PublishKit != nil {
			readyToPost = j.PublishKit.Checklist.ReadyToPost
		}

		items = append(items, publishableQueueItem{
			ID:                 j.ID,
			JobID:              j.ID,
			ProjectID:          j.ProjectID,
			CampaignID:         j.CampaignID,
			CampaignName:       j.CampaignName,
			Title:              j.Title,
			Status:             j.Status,
			ReviewStatus:       j.ReviewStatus,
			BoardColumn:        j.BoardColumn,
			OutputFile:         j.OutputFile,
			FileSizeBytes:      sizeBytes,
			FinalDuration:      j.FinalDuration,
			AspectRatio:        aspect,
			Format:             aspect,
			DownloadURL:        fmt.Sprintf("/api/jobs/%s/video?download=1", j.ID),
			VideoURL:           fmt.Sprintf("/api/jobs/%s/video", j.ID),
			CoverURL:           coverURL,
			CoverFrameURL:      coverURL,
			PeekURL:            peekURL,
			PeekFrameURL:       peekURL,
			PostCaption:        j.PostCaption,
			HashtagLine:        j.HashtagLine,
			PublishKit:         j.PublishKit,
			WatermarkApplied:   j.Compliance.WatermarkApplied,
			OutroClean:         j.Compliance.OutroClean,
			ReadyToPost:        readyToPost,
			PostedPlatforms:    posted,
			RemainingPlatforms: remaining,
			TotalRecordedViews: totalViews,
			CreatedAt:          j.CreatedAt,
			ReviewedAt:         j.ReviewedAt,
		})
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"ok":                true,
		"count":             len(items),
		"manualPostingOnly": true,
		"items":             items,
	})
}

// handleMarkJobPosted tracks a human manual post on TikTok/YouTube/Instagram (zero auto-post!).
// Strictly restricted to Owner and QC roles; blocks unapproved jobs or missing MP4 files with 409 Conflict.
func (s *Server) handleMarkJobPosted(w http.ResponseWriter, r *http.Request) {
	p := getPrincipal(r)
	if p.Role != "owner" && p.Role != "admin" && p.Role != "qc" {
		writeErr(w, http.StatusForbidden, "Forbidden: hanya Owner atau QC yang boleh menandai klip diposting manual")
		return
	}

	id := r.PathValue("id")
	job, ok := s.Store.GetJob(id)
	if !ok {
		writeErr(w, http.StatusNotFound, "Job tidak ditemukan")
		return
	}
	if job.ReviewStatus != "approved" {
		writeErr(w, http.StatusConflict, "Konflik (409): klip harus disetujui QC (reviewStatus=approved) sebelum ditandai diposting")
		return
	}
	if !pathExists(job.OutputPath) {
		writeErr(w, http.StatusConflict, "Konflik (409): file MP4 hasil render tidak ditemukan di disk")
		return
	}

	var req struct {
		Platform         string `json:"platform"`
		PublishedURL     string `json:"publishedUrl"`
		PostURL          string `json:"postUrl"`
		Note             string `json:"note"`
		Views            *int64 `json:"views"`
		Likes            *int64 `json:"likes"`
		Comments         *int64 `json:"comments"`
		Shares           *int64 `json:"shares"`
		SampleSplit      string `json:"sampleSplit"`
		CohortTag        string `json:"cohortTag"`
		HookType         string `json:"hookType"`
		TriggerSource    string `json:"triggerSource"`
		Views24h         *int64 `json:"views24h"`
		Views7d          *int64 `json:"views7d"`
		ChannelState     string `json:"channelState"`
		PostedDayTime    string `json:"postedDayTime"`
		ThumbnailTs      string `json:"thumbnailTs"`
		CueSelectionUsed bool   `json:"cueSelectionUsed"`
		H1TawaFacecam    bool   `json:"h1TawaFacecam"`
		H1VisualKejadian bool   `json:"h1VisualKejadian"`
		H1ArcUtuh        bool   `json:"h1ArcUtuh"`
		H1HookMutePass   bool   `json:"h1HookMutePass"`
		RecordLedger     *bool  `json:"recordLedger"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "Format JSON mark-posted tidak valid")
		return
	}

	platform := strings.ToLower(strings.TrimSpace(req.Platform))
	if !isValidManualPlatform(platform) {
		writeErr(w, http.StatusBadRequest, "platform harus salah satu dari: tiktok, youtube, instagram")
		return
	}

	var views, likes, comments, shares, views24h, views7d int64
	if req.Views != nil {
		if *req.Views < 0 {
			writeErr(w, http.StatusBadRequest, "views harus >= 0")
			return
		}
		views = *req.Views
	}
	if req.Views24h != nil {
		if *req.Views24h < 0 {
			writeErr(w, http.StatusBadRequest, "views24h harus >= 0")
			return
		}
		views24h = *req.Views24h
	}
	if req.Views7d != nil {
		if *req.Views7d < 0 {
			writeErr(w, http.StatusBadRequest, "views7d harus >= 0")
			return
		}
		views7d = *req.Views7d
	}
	if req.Likes != nil {
		if *req.Likes < 0 {
			writeErr(w, http.StatusBadRequest, "likes harus >= 0")
			return
		}
		likes = *req.Likes
	}
	if req.Comments != nil {
		if *req.Comments < 0 {
			writeErr(w, http.StatusBadRequest, "comments harus >= 0")
			return
		}
		comments = *req.Comments
	}
	if req.Shares != nil {
		if *req.Shares < 0 {
			writeErr(w, http.StatusBadRequest, "shares harus >= 0")
			return
		}
		shares = *req.Shares
	}

	split := strings.ToUpper(strings.TrimSpace(req.SampleSplit))
	if split == "" {
		split = "IN-SAMPLE"
	}
	if split != "IN-SAMPLE" && split != "HELD-OUT" {
		writeErr(w, http.StatusBadRequest, "sampleSplit harus IN-SAMPLE atau HELD-OUT")
		return
	}

	cohortTag, err := store.NormalizeCohortTag(req.CohortTag)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	hookType, err := store.NormalizeHookType(req.HookType)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	triggerSource, err := store.NormalizeTriggerSource(req.TriggerSource)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}

	publishedURL := strings.TrimSpace(req.PublishedURL)
	if publishedURL == "" {
		publishedURL = strings.TrimSpace(req.PostURL)
	}
	note := strings.TrimSpace(req.Note)
	now := time.Now()

	newRec := model.PublishRecord{
		Platform:     platform,
		Method:       "manual",
		Status:       "published",
		PublishedURL: publishedURL,
		UpdatedAt:    now,
	}

	updated, ok := s.Store.UpdateJob(job.ID, func(j *model.RenderJob) {
		replaced := false
		for i := range j.PublishRecords {
			if strings.EqualFold(j.PublishRecords[i].Platform, platform) {
				j.PublishRecords[i] = newRec
				replaced = true
				break
			}
		}
		if !replaced {
			j.PublishRecords = append(j.PublishRecords, newRec)
		}
		j.BoardColumn = "diposting"
	})
	if !ok {
		writeErr(w, http.StatusNotFound, "Job tidak ditemukan")
		return
	}

	s.Broker.Publish("job_update", updated)
	s.recordActivity(p, "manual_posted", "job", updated.ID,
		fmt.Sprintf("%s menandai klip '%s' diposting manual ke %s", p.Name, updated.Title, strings.ToUpper(platform)),
		publishedURL, "success")

	hasExplicitMetric := req.Views != nil || req.Likes != nil || req.Comments != nil || req.Shares != nil || req.Views24h != nil || req.Views7d != nil
	hasPositiveMetric := views > 0 || likes > 0 || comments > 0 || shares > 0 || views24h > 0 || views7d > 0
	hasV12Metadata := strings.TrimSpace(req.CohortTag) != "" || hookType != "" || triggerSource != "" ||
		strings.TrimSpace(req.ChannelState) != "" || strings.TrimSpace(req.PostedDayTime) != "" ||
		strings.TrimSpace(req.ThumbnailTs) != "" || req.CueSelectionUsed ||
		req.H1TawaFacecam || req.H1VisualKejadian || req.H1ArcUtuh || req.H1HookMutePass
	allowLedger := req.RecordLedger == nil || *req.RecordLedger

	var ledgerEntry *model.ViewsLedgerEntry
	if allowLedger && (hasPositiveMetric || hasExplicitMetric || publishedURL != "" || hasV12Metadata) {
		saved, err := s.Store.AddViewsLedgerEntry(model.ViewsLedgerEntry{
			JobID:            updated.ID,
			ProjectID:        updated.ProjectID,
			Title:            updated.Title,
			Platform:         platform,
			PostURL:          publishedURL,
			PostedAt:         now,
			RecordedAt:       now,
			Views:            views,
			Views24h:         views24h,
			Views7d:          views7d,
			Likes:            likes,
			Comments:         comments,
			Shares:           shares,
			Format:           inferJobAspectRatio(updated),
			SampleSplit:      split,
			CohortTag:        cohortTag,
			HookType:         hookType,
			TriggerSource:    triggerSource,
			ChannelState:     req.ChannelState,
			PostedDayTime:    req.PostedDayTime,
			ThumbnailTs:      req.ThumbnailTs,
			CueSelectionUsed: req.CueSelectionUsed,
			H1TawaFacecam:    req.H1TawaFacecam,
			H1VisualKejadian: req.H1VisualKejadian,
			H1ArcUtuh:        req.H1ArcUtuh,
			H1HookMutePass:   req.H1HookMutePass,
			Note:             note,
			RecordedBy:       p.Name,
		})
		if err != nil {
			writeErr(w, http.StatusInternalServerError, "Klip ditandai diposting tetapi gagal menyimpan views ledger: "+err.Error())
			return
		}
		ledgerEntry = saved
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"ok":             true,
		"jobId":          updated.ID,
		"platform":       platform,
		"method":         "manual",
		"status":         "published",
		"boardColumn":    updated.BoardColumn,
		"publishRecords": updated.PublishRecords,
		"ledgerEntry":    ledgerEntry,
	})
}

// handleListViewsLedger returns manual post performance entries + summary (I-04).
func (s *Server) handleListViewsLedger(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	jobID := strings.TrimSpace(q.Get("jobId"))
	platform := strings.TrimSpace(q.Get("platform"))
	split := strings.TrimSpace(q.Get("split"))
	if split == "" {
		split = strings.TrimSpace(q.Get("sampleSplit"))
	}

	entries := s.Store.ListViewsLedger(jobID, platform, split)
	if entries == nil {
		entries = []*model.ViewsLedgerEntry{}
	}
	summary := store.SummarizeViewsLedger(entries)

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"ok":      true,
		"count":   len(entries),
		"entries": entries,
		"summary": summary,
	})
}

// handleAddViewsLedger persists a new manual views observation to data/views_ledger.json (I-04).
// Allowed for owner, qc, and clipper; strictly denied (403) for agent.
func (s *Server) handleAddViewsLedger(w http.ResponseWriter, r *http.Request) {
	p := getPrincipal(r)
	if p.Role == "agent" {
		writeErr(w, http.StatusForbidden, "Forbidden: Agent tidak diizinkan menulis views ledger (hanya baca)")
		return
	}

	var entry model.ViewsLedgerEntry
	if err := json.NewDecoder(r.Body).Decode(&entry); err != nil {
		writeErr(w, http.StatusBadRequest, "Format JSON views ledger tidak valid")
		return
	}

	entry.JobID = strings.TrimSpace(entry.JobID)
	if entry.JobID != "" {
		if job, ok := s.Store.GetJob(entry.JobID); ok {
			if strings.TrimSpace(entry.Title) == "" {
				entry.Title = job.Title
			}
			if strings.TrimSpace(entry.ProjectID) == "" {
				entry.ProjectID = job.ProjectID
			}
			if strings.TrimSpace(entry.Format) == "" {
				entry.Format = inferJobAspectRatio(job)
			}
		}
	}
	entry.RecordedBy = p.Name

	saved, err := s.Store.AddViewsLedgerEntry(entry)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}

	s.recordActivity(p, "views_ledger_add", "job", saved.JobID,
		fmt.Sprintf("%s mencatat %d views (%s, %s) untuk %s", p.Name, saved.Views, strings.ToUpper(saved.Platform), saved.SampleSplit, saved.Title),
		saved.Note, "info")

	allEntries := s.Store.ListViewsLedger("", "", "")
	summary := store.SummarizeViewsLedger(allEntries)

	writeJSON(w, http.StatusCreated, map[string]interface{}{
		"ok":      true,
		"entry":   saved,
		"summary": summary,
	})
}

// handleGetProjectTranscript returns paginated, filterable chronological transcript cues
// for the interactive Studio Transcript Browser (GET /api/projects/{id}/transcript).
func (s *Server) handleGetProjectTranscript(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	proj, ok := s.Store.GetProject(id)
	if !ok {
		writeErr(w, http.StatusNotFound, "Project tidak ditemukan")
		return
	}

	q := r.URL.Query()
	offset := 0
	if raw := strings.TrimSpace(q.Get("offset")); raw != "" {
		if v, err := strconv.Atoi(raw); err == nil && v > 0 {
			offset = v
		}
	}
	limit := 60
	if raw := strings.TrimSpace(q.Get("limit")); raw != "" {
		if v, err := strconv.Atoi(raw); err == nil && v > 0 {
			limit = v
		}
	}
	if limit > 200 {
		limit = 200
	}

	var fromSec float64
	hasFrom := false
	if raw := strings.TrimSpace(q.Get("fromSec")); raw != "" {
		if v, err := strconv.ParseFloat(raw, 64); err == nil {
			fromSec = v
			hasFrom = true
		}
	}
	var toSec float64
	hasTo := false
	if raw := strings.TrimSpace(q.Get("toSec")); raw != "" {
		if v, err := strconv.ParseFloat(raw, 64); err == nil {
			toSec = v
			hasTo = true
		}
	}
	qLower := strings.ToLower(strings.TrimSpace(q.Get("q")))

	cs := s.loadCachedSignals(proj)
	s.cacheMu.RLock()
	cues := cs.Transcript
	s.cacheMu.RUnlock()
	if len(cues) == 0 {
		cues = s.loadProjectCuesPreferDiarized(proj)
	}

	filtered := make([]transcriptBrowserCue, 0, len(cues))
	for i, c := range cues {
		if hasFrom && c.End < fromSec {
			continue
		}
		if hasTo && c.Start > toSec {
			continue
		}
		if qLower != "" && !strings.Contains(strings.ToLower(c.Text), qLower) {
			continue
		}
		filtered = append(filtered, transcriptBrowserCue{
			Index:    i,
			Start:    c.Start,
			End:      c.End,
			TimeText: analyzer.FormatMMSS(c.Start),
			Text:     c.Text,
			Speaker:  c.Speaker,
		})
	}

	total := len(filtered)
	if offset > total {
		offset = total
	}
	end := offset + limit
	if end > total {
		end = total
	}
	page := filtered[offset:end]
	if page == nil {
		page = []transcriptBrowserCue{}
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"ok":        true,
		"projectId": proj.ID,
		"total":     total,
		"offset":    offset,
		"limit":     limit,
		"hasMore":   end < total,
		"cues":      page,
	})
}
