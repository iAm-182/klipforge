package server

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"time"

	"klipforge/internal/model"
)

var validBoardColumns = map[string]bool{
	"kandidat":         true,
	"verifikasi_frame": true,
	"render":           true,
	"review":           true,
	"siap_posting":     true,
	"diposting":        true,
}

func normalizeBoardColumn(col string) string {
	c := strings.ToLower(strings.TrimSpace(col))
	c = strings.ReplaceAll(c, "-", "_")
	c = strings.ReplaceAll(c, " ", "_")
	switch c {
	case "kandidat", "candidate", "candidates":
		return "kandidat"
	case "verifikasi_frame", "verifikasi", "verify_frame", "verify_frames":
		return "verifikasi_frame"
	case "render", "rendering":
		return "render"
	case "review", "pending_review":
		return "review"
	case "siap_posting", "ready", "approved":
		return "siap_posting"
	case "diposting", "posted", "published":
		return "diposting"
	default:
		return c
	}
}

func effectiveJobColumn(j *model.RenderJob) string {
	if j == nil {
		return "kandidat"
	}
	if j.BoardColumn != "" && validBoardColumns[j.BoardColumn] {
		return j.BoardColumn
	}
	if j.Status == "queued" || j.Status == "downloading" || j.Status == "rendering" {
		return "render"
	}
	if j.ReviewStatus == "approved" {
		for _, pr := range j.PublishRecords {
			if pr.Status == "published" {
				return "diposting"
			}
		}
		return "siap_posting"
	}
	if j.ReviewStatus == "rejected" {
		return "kandidat"
	}
	if j.Status == "done" {
		return "review"
	}
	return "render"
}

func effectiveCandidateColumn(c model.ClipCandidate) string {
	if c.BoardColumn != "" && validBoardColumns[c.BoardColumn] {
		return c.BoardColumn
	}
	if c.StartFrameURL != "" || c.PeakFrameURL != "" {
		return "verifikasi_frame"
	}
	return "kandidat"
}

func (s *Server) recordActivity(p model.Principal, action, targetType, targetID, summary, detail, level string) {
	if s.Store == nil {
		return
	}
	ent := s.Store.AppendActivity(model.ActivityEntry{
		Timestamp:  time.Now(),
		ActorID:    p.ID,
		ActorName:  p.Name,
		ActorVia:   p.Via,
		Action:     action,
		TargetType: targetType,
		TargetID:   targetID,
		Summary:    summary,
		Detail:     detail,
		Level:      level,
	})
	if s.Broker != nil {
		s.Broker.Publish("activity", ent)
	}
}

// handleListActivity returns persisted activity entries (newest first), optionally filtered by ?limit= and ?member=.
func (s *Server) handleListActivity(w http.ResponseWriter, r *http.Request) {
	limit := 50
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		if v, err := strconv.Atoi(raw); err == nil && v > 0 {
			limit = v
		}
	}
	member := strings.TrimSpace(r.URL.Query().Get("member"))
	items := s.Store.ListActivity(limit, member)
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"count": len(items),
		"items": items,
	})
}

// handleJobPeek serves the live mid-render JPEG peek thumbnail (or JSON metadata when ?meta=1).
func (s *Server) handleJobPeek(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	job, ok := s.Store.GetJob(id)
	if !ok {
		writeErr(w, http.StatusNotFound, "Job tidak ditemukan")
		return
	}

	peekPath := job.PeekFramePath
	if peekPath == "" {
		candPath := s.Worker.PeekFramePath(job.ID)
		if fi, err := os.Stat(candPath); err == nil && fi.Size() > 256 {
			peekPath = candPath
		}
	}
	// On-demand grab from completed MP4 if no peek file exists yet
	if (peekPath == "" || !pathExists(peekPath)) && job.OutputPath != "" && pathExists(job.OutputPath) {
		ff := s.Deps.GetStatus().FFmpegPath
		if ff != "" {
			dest := s.Worker.PeekFramePath(job.ID)
			seek := 1.0
			if job.FinalDuration > 4 {
				seek = job.FinalDuration * 0.3
			}
			cctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
			defer cancel()
			_ = exec.CommandContext(cctx, ff,
				"-y", "-hide_banner", "-loglevel", "error",
				"-ss", fmt.Sprintf("%.2f", seek),
				"-i", job.OutputPath,
				"-frames:v", "1", "-vf", "scale=360:-2", "-q:v", "4", dest,
			).Run()
			if fi, err := os.Stat(dest); err == nil && fi.Size() > 256 {
				peekPath = dest
				job.PeekFramePath = dest
				job.PeekFrameURL = fmt.Sprintf("/api/jobs/%s/peek", job.ID)
				_ = s.Store.SaveJob(job)
			}
		}
	}

	hasFrame := peekPath != "" && pathExists(peekPath)
	if r.URL.Query().Get("meta") == "1" || strings.EqualFold(r.URL.Query().Get("meta"), "true") {
		elapsed := 0.0
		if job.StartedAt != nil {
			if job.CompletedAt != nil {
				elapsed = job.CompletedAt.Sub(*job.StartedAt).Seconds()
			} else {
				elapsed = time.Since(*job.StartedAt).Seconds()
			}
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"jobId":      job.ID,
			"status":     job.Status,
			"stage":      job.Stage,
			"progress":   job.Progress,
			"workerId":   job.WorkerID,
			"elapsedSec": math.Round(elapsed*10) / 10,
			"hasFrame":   hasFrame,
			"peekUrl":    fmt.Sprintf("/api/jobs/%s/peek", job.ID),
		})
		return
	}

	if !hasFrame {
		writeErr(w, http.StatusNotFound, "Frame peek belum tersedia untuk job ini")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	http.ServeFile(w, r, peekPath)
}

func pathExists(p string) bool {
	if p == "" {
		return false
	}
	fi, err := os.Stat(p)
	return err == nil && !fi.IsDir()
}

// BoardCard represents a unified Kanban card (either a ClipCandidate or a RenderJob).
type BoardCard struct {
	ID                string                   `json:"id"`
	Kind              string                   `json:"kind"` // "candidate" | "job"
	Column            string                   `json:"column"`
	ProjectID         string                   `json:"projectId"`
	ProjectTitle      string                   `json:"projectTitle,omitempty"`
	CampaignID        string                   `json:"campaignId,omitempty"`
	CandidateID       string                   `json:"candidateId,omitempty"`
	JobID             string                   `json:"jobId,omitempty"`
	Title             string                   `json:"title"`
	ArcLabel          string                   `json:"arcLabel,omitempty"`
	MomentType        string                   `json:"momentType,omitempty"`
	ViralityScore     float64                  `json:"viralityScore,omitempty"`
	ViralityBreakdown *model.ViralityBreakdown `json:"viralityBreakdown,omitempty"`
	ThumbnailURL      string                   `json:"thumbnailUrl,omitempty"`
	StartFrameURL     string                   `json:"startFrameUrl,omitempty"`
	PeakFrameURL      string                   `json:"peakFrameUrl,omitempty"`
	OwnerID           string                   `json:"ownerId,omitempty"`
	OwnerName         string                   `json:"ownerName,omitempty"`
	CreatedVia        string                   `json:"createdVia,omitempty"`
	Status            string                   `json:"status,omitempty"`
	Progress          float64                  `json:"progress,omitempty"`
	ReviewStatus      string                   `json:"reviewStatus,omitempty"`
	Reason            string                   `json:"reason,omitempty"`
	AgeHours          float64                  `json:"ageHours"`
	UpdatedAt         time.Time                `json:"updatedAt"`
	Verdict           *model.CandidateVerdict  `json:"verdict,omitempty"`
	ChecklistReady    bool                     `json:"checklistReady,omitempty"`
}

// handleGetBoard returns all 6 Kanban columns, cards, WIP meters per member, and filter choices.
func (s *Server) handleGetBoard(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	memberFilter := strings.TrimSpace(q.Get("member"))
	projectFilter := strings.TrimSpace(q.Get("projectId"))
	momentFilter := strings.ToLower(strings.TrimSpace(q.Get("momentType")))

	now := time.Now()
	jobs := s.Store.ListJobs()
	projects := s.Store.ListProjects()

	// Track which candidate IDs already have an active/non-rejected job so we don't show duplicate cards
	candHasJob := make(map[string]bool)
	activeByMember := make(map[string]int)
	var cards []BoardCard

	projMap := make(map[string]*model.Project, len(projects))
	for _, pr := range projects {
		projMap[pr.ID] = pr
	}

	for _, j := range jobs {
		cid := j.CreatedByID
		if cid == "" {
			cid = "owner"
		}
		if j.Status == "queued" || j.Status == "downloading" || j.Status == "rendering" {
			activeByMember[cid]++
		}
		if j.Options.CandidateID != "" && j.ReviewStatus != "rejected" && j.Status != "canceled" {
			candHasJob[j.ProjectID+"::"+j.Options.CandidateID] = true
		}

		col := effectiveJobColumn(j)
		var matchedCand *model.ClipCandidate
		if pr, ok := projMap[j.ProjectID]; ok && j.Options.CandidateID != "" {
			for idx := range pr.Candidates {
				if pr.Candidates[idx].ID == j.Options.CandidateID {
					cp := pr.Candidates[idx]
					matchedCand = &cp
					break
				}
			}
		}

		mt := ""
		arcLbl := ""
		var vScore float64
		var vBreak *model.ViralityBreakdown
		var verdict *model.CandidateVerdict
		startF, peakF := "", ""
		if matchedCand != nil {
			mt = matchedCand.MomentType
			arcLbl = matchedCand.ArcReason
			vScore = matchedCand.ViralityScore
			vBreak = matchedCand.ViralityBreakdown
			verdict = matchedCand.Verdict
			startF = matchedCand.StartFrameURL
			peakF = matchedCand.PeakFrameURL
		}
		if mt == "" && j.AutoEdit != nil {
			mt = j.AutoEdit.MomentType
		}

		thumb := j.CoverFrameURL
		if thumb == "" {
			thumb = j.PeekFrameURL
		}
		if thumb == "" && peakF != "" {
			thumb = peakF
		}
		if thumb == "" && startF != "" {
			thumb = startF
		}

		ownerName := j.CreatedBy
		if ownerName == "" {
			ownerName = "Owner"
		}
		via := j.CreatedVia
		if via == "" {
			via = "web"
		}

		if memberFilter != "" && memberFilter != "all" && cid != memberFilter {
			continue
		}
		if projectFilter != "" && projectFilter != "all" && j.ProjectID != projectFilter {
			continue
		}
		if momentFilter != "" && momentFilter != "all" && strings.ToLower(mt) != momentFilter {
			continue
		}

		ts := j.CreatedAt
		if j.CompletedAt != nil {
			ts = *j.CompletedAt
		}
		readyPost := j.PublishKit != nil && j.PublishKit.Checklist.ReadyToPost

		cards = append(cards, BoardCard{
			ID:                "job:" + j.ID,
			Kind:              "job",
			Column:            col,
			ProjectID:         j.ProjectID,
			ProjectTitle:      j.ProjectTitle,
			CampaignID:        j.CampaignID,
			CandidateID:       j.Options.CandidateID,
			JobID:             j.ID,
			Title:             j.Title,
			ArcLabel:          arcLbl,
			MomentType:        mt,
			ViralityScore:     vScore,
			ViralityBreakdown: vBreak,
			ThumbnailURL:      thumb,
			StartFrameURL:     startF,
			PeakFrameURL:      peakF,
			OwnerID:           cid,
			OwnerName:         ownerName,
			CreatedVia:        via,
			Status:            j.Status,
			Progress:          j.Progress,
			ReviewStatus:      j.ReviewStatus,
			Reason:            j.RejectReason,
			AgeHours:          math.Round(now.Sub(ts).Hours()*10) / 10,
			UpdatedAt:         ts,
			Verdict:           verdict,
			ChecklistReady:    readyPost,
		})
	}

	// Add non-dropped project candidates that don't yet have an active job card
	for _, pr := range projects {
		if projectFilter != "" && projectFilter != "all" && pr.ID != projectFilter {
			continue
		}
		s.enrichProjectAutoEdit(pr)
		for _, c := range pr.Candidates {
			if c.Verdict != nil && strings.EqualFold(c.Verdict.Verdict, "drop") {
				continue
			}
			if candHasJob[pr.ID+"::"+c.ID] {
				continue
			}
			ownerID := c.OwnerID
			if ownerID == "" {
				ownerID = pr.CreatedByID
			}
			if ownerID == "" {
				ownerID = "owner"
			}
			ownerName := c.OwnerName
			if ownerName == "" {
				ownerName = pr.CreatedBy
			}
			if ownerName == "" {
				ownerName = "Owner"
			}
			if memberFilter != "" && memberFilter != "all" && ownerID != memberFilter {
				continue
			}
			if momentFilter != "" && momentFilter != "all" && strings.ToLower(c.MomentType) != momentFilter {
				continue
			}

			col := effectiveCandidateColumn(c)
			thumb := c.CoverFrameURL
			if thumb == "" {
				thumb = c.PeakFrameURL
			}
			if thumb == "" {
				thumb = c.StartFrameURL
			}
			ts := pr.UpdatedAt
			if c.BoardUpdatedAt != nil {
				ts = *c.BoardUpdatedAt
			}
			via := "web"
			if c.Verdict != nil && c.Verdict.Via != "" {
				via = c.Verdict.Via
			}
			title := c.HookText
			if strings.TrimSpace(title) == "" {
				title = c.Title
			}
			cards = append(cards, BoardCard{
				ID:                "cand:" + pr.ID + ":" + c.ID,
				Kind:              "candidate",
				Column:            col,
				ProjectID:         pr.ID,
				ProjectTitle:      pr.Title,
				CampaignID:        pr.CampaignID,
				CandidateID:       c.ID,
				Title:             title,
				ArcLabel:          c.ArcReason,
				MomentType:        c.MomentType,
				ViralityScore:     c.ViralityScore,
				ViralityBreakdown: c.ViralityBreakdown,
				ThumbnailURL:      thumb,
				StartFrameURL:     c.StartFrameURL,
				PeakFrameURL:      c.PeakFrameURL,
				OwnerID:           ownerID,
				OwnerName:         ownerName,
				CreatedVia:        via,
				Reason:            c.BoardReason,
				AgeHours:          math.Round(now.Sub(ts).Hours()*10) / 10,
				UpdatedAt:         ts,
				Verdict:           c.Verdict,
			})
		}
	}

	limitsByMember := map[string]int{"owner": 99}
	for _, m := range s.Store.ListTeam() {
		lim := m.MaxActive
		if lim <= 0 {
			lim = 3
		}
		limitsByMember[m.ID] = lim
	}

	columns := []map[string]string{
		{"id": "kandidat", "label": "Kandidat"},
		{"id": "verifikasi_frame", "label": "Verifikasi Frame"},
		{"id": "render", "label": "Render"},
		{"id": "review", "label": "Review"},
		{"id": "siap_posting", "label": "Siap Posting"},
		{"id": "diposting", "label": "Diposting"},
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"columns":        columns,
		"cards":          cards,
		"activeByMember": activeByMember,
		"limitsByMember": limitsByMember,
	})
}

// handleBoardMove moves a candidate or job across Kanban columns and triggers the matching real action.
// Agent-allowed (subject to least-privilege rules: agents cannot QC-approve/reject or bypass Fair Queue).
func (s *Server) handleBoardMove(w http.ResponseWriter, r *http.Request) {
	var req struct {
		JobID       string `json:"jobId"`
		CandidateID string `json:"candidateId"`
		ProjectID   string `json:"projectId"`
		ToColumn    string `json:"toColumn"`
		Reason      string `json:"reason"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "Format JSON board/move tidak valid")
		return
	}
	toCol := normalizeBoardColumn(req.ToColumn)
	if !validBoardColumns[toCol] {
		writeErr(w, http.StatusBadRequest, fmt.Sprintf("Kolom tujuan tidak valid: %q (pilih: kandidat, verifikasi_frame, render, review, siap_posting, diposting)", req.ToColumn))
		return
	}
	if strings.TrimSpace(req.JobID) == "" && strings.TrimSpace(req.CandidateID) == "" {
		writeErr(w, http.StatusBadRequest, "Wajib sertakan jobId atau candidateId")
		return
	}

	p := getPrincipal(r)
	now := time.Now()

	// Case 1: Moving a RenderJob card
	if jid := strings.TrimSpace(req.JobID); jid != "" {
		job, ok := s.Store.GetJob(jid)
		if !ok {
			writeErr(w, http.StatusNotFound, "Job tidak ditemukan")
			return
		}
		switch toCol {
		case "siap_posting":
			if p.Role != "owner" && p.Role != "admin" && p.Role != "qc" {
				writeErr(w, http.StatusForbidden, "Hanya Owner atau QC yang boleh menyetujui klip ke Siap Posting")
				return
			}
			if p.Role == "qc" && job.CreatedByID != "" && job.CreatedByID == p.ID {
				writeErr(w, http.StatusForbidden, "Self-approve dilarang: QC tidak boleh menyetujui klip miliknya sendiri")
				return
			}
			job.ReviewStatus = "approved"
			job.ReviewedBy = p.Name
			job.ReviewedAt = &now
			job.BoardColumn = "siap_posting"
			if strings.TrimSpace(req.Reason) != "" {
				job.ReviewNote = strings.TrimSpace(req.Reason)
			}
			if job.PublishKit == nil {
				if kit, err := s.buildPublishKitForJob(job, nil); err == nil {
					job.PublishKit = kit
				}
			}
			_ = s.Store.SaveJob(job)
			s.Broker.Publish("job_update", job)
			s.recordActivity(p, "review_approved", "job", job.ID,
				fmt.Sprintf("%s menyetujui klip %s ke Siap Posting", p.Name, job.Title), req.Reason, "success")
			writeJSON(w, http.StatusOK, map[string]interface{}{"ok": true, "toColumn": toCol, "job": job})
			return

		case "kandidat":
			// Reject from Review back to Kandidat requires QC/Owner + reason
			if effectiveJobColumn(job) == "review" || job.ReviewStatus == "pending_review" {
				if p.Role != "owner" && p.Role != "admin" && p.Role != "qc" {
					writeErr(w, http.StatusForbidden, "Hanya Owner atau QC yang boleh menolak klip dari Review")
					return
				}
				reason := strings.TrimSpace(req.Reason)
				if reason == "" {
					writeErr(w, http.StatusBadRequest, "Alasan penolakan (reason) wajib diisi saat mengembalikan klip ke Kandidat")
					return
				}
				job.ReviewStatus = "rejected"
				job.ReviewedBy = p.Name
				job.ReviewedAt = &now
				job.RejectReason = reason
				job.ReviewNote = reason
			}
			job.BoardColumn = "kandidat"
			_ = s.Store.SaveJob(job)
			s.Broker.Publish("job_update", job)
			// Also attach reject reason on the originating candidate if present
			if job.ProjectID != "" && job.Options.CandidateID != "" {
				if proj, ok := s.Store.GetProject(job.ProjectID); ok {
					cands := make([]model.ClipCandidate, len(proj.Candidates))
					copy(cands, proj.Candidates)
					for i := range cands {
						if cands[i].ID == job.Options.CandidateID {
							cands[i].BoardColumn = "kandidat"
							cands[i].BoardReason = job.RejectReason
							cands[i].BoardUpdatedAt = &now
							break
						}
					}
					proj.Candidates = cands
					_ = s.Store.SaveProject(proj)
					s.Broker.Publish("project_update", proj)
				}
			}
			s.recordActivity(p, "review_rejected", "job", job.ID,
				fmt.Sprintf("%s mengembalikan klip %s ke Kandidat", p.Name, job.Title), req.Reason, "warn")
			writeJSON(w, http.StatusOK, map[string]interface{}{"ok": true, "toColumn": toCol, "job": job})
			return

		case "diposting":
			if job.ReviewStatus != "approved" {
				writeErr(w, http.StatusConflict, "Klip harus berstatus Approved (Siap Posting) sebelum dipindah ke Diposting")
				return
			}
			job.BoardColumn = "diposting"
			_ = s.Store.SaveJob(job)
			s.Broker.Publish("job_update", job)
			s.recordActivity(p, "board_move", "job", job.ID,
				fmt.Sprintf("%s menandai klip %s sebagai Diposting", p.Name, job.Title), "", "success")
			writeJSON(w, http.StatusOK, map[string]interface{}{"ok": true, "toColumn": toCol, "job": job})
			return

		default:
			job.BoardColumn = toCol
			if toCol == "review" && job.ReviewStatus == "" {
				job.ReviewStatus = "pending_review"
			}
			_ = s.Store.SaveJob(job)
			s.Broker.Publish("job_update", job)
			s.recordActivity(p, "board_move", "job", job.ID,
				fmt.Sprintf("%s memindahkan klip %s ke %s", p.Name, job.Title, toCol), req.Reason, "info")
			writeJSON(w, http.StatusOK, map[string]interface{}{"ok": true, "toColumn": toCol, "job": job})
			return
		}
	}

	// Case 2: Moving a ClipCandidate card
	proj, candIdx := s.findProjectCandidate(strings.TrimSpace(req.ProjectID), strings.TrimSpace(req.CandidateID))
	if proj == nil || candIdx < 0 {
		writeErr(w, http.StatusNotFound, "Kandidat tidak ditemukan")
		return
	}

	cands := make([]model.ClipCandidate, len(proj.Candidates))
	copy(cands, proj.Candidates)
	cand := &cands[candIdx]
	cand.BoardColumn = toCol
	cand.BoardUpdatedAt = &now
	if strings.TrimSpace(req.Reason) != "" {
		cand.BoardReason = strings.TrimSpace(req.Reason)
	}
	if cand.OwnerID == "" {
		cand.OwnerID = p.ID
		cand.OwnerName = p.Name
	}

	switch toCol {
	case "verifikasi_frame":
		proj.Candidates = cands
		_ = s.Store.SaveProject(proj)
		s.Broker.Publish("project_update", proj)
		go s.captureCandidateFrames(proj.ID, 5)
		s.recordActivity(p, "board_move", "candidate", cand.ID,
			fmt.Sprintf("%s memverifikasi frame kandidat %s", p.Name, cand.Title), "", "info")
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"ok":          true,
			"toColumn":    toCol,
			"action":      "verify_frames_triggered",
			"projectId":   proj.ID,
			"candidateId": cand.ID,
			"candidate":   *cand,
		})
		return

	case "render":
		if err := s.checkMemberQueueLimit(p); err != nil {
			writeErr(w, http.StatusTooManyRequests, err.Error())
			return
		}
		camp, _ := s.Store.GetCampaign(proj.CampaignID)
		opts := buildDefaultRenderOptsFromCandidate(proj, *cand, camp)
		job, err := s.Worker.EnqueueWithReview(opts, "pending_review", "")
		if err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		job.BoardColumn = "render"
		s.tagJobWithPrincipal(job, p)
		proj.Candidates = cands
		_ = s.Store.SaveProject(proj)
		s.Broker.Publish("project_update", proj)
		s.recordActivity(p, "render_queued", "job", job.ID,
			fmt.Sprintf("%s merender klip %s (%.0f dtk)", p.Name, job.Title, cand.EndSec-cand.StartSec), "", "info")
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"ok":          true,
			"toColumn":    toCol,
			"action":      "render_queued",
			"projectId":   proj.ID,
			"candidateId": cand.ID,
			"job":         job,
		})
		return

	case "siap_posting", "diposting":
		writeErr(w, http.StatusConflict, "Kandidat belum dirender dan belum disetujui QC — pindahkan ke Render dulu")
		return

	default:
		proj.Candidates = cands
		_ = s.Store.SaveProject(proj)
		s.Broker.Publish("project_update", proj)
		s.recordActivity(p, "board_move", "candidate", cand.ID,
			fmt.Sprintf("%s memindahkan kandidat %s ke %s", p.Name, cand.Title, toCol), req.Reason, "info")
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"ok":          true,
			"toColumn":    toCol,
			"projectId":   proj.ID,
			"candidateId": cand.ID,
			"candidate":   *cand,
		})
		return
	}
}

func (s *Server) findProjectCandidate(projectID, candID string) (*model.Project, int) {
	if projectID != "" {
		if pr, ok := s.Store.GetProject(projectID); ok {
			for i, c := range pr.Candidates {
				if c.ID == candID {
					return pr, i
				}
			}
		}
		return nil, -1
	}
	for _, pr := range s.Store.ListProjects() {
		for i, c := range pr.Candidates {
			if c.ID == candID {
				return pr, i
			}
		}
	}
	return nil, -1
}

func buildDefaultRenderOptsFromCandidate(proj *model.Project, cand model.ClipCandidate, camp *model.Campaign) model.RenderOptions {
	layout := model.LayoutConfig{AspectRatio: "9:16", Resolution: "1080p", CropMode: "face_track"}
	subs := model.SubtitleStyle{Enabled: true, Preset: "karaoke", FontName: "Impact", FontSize: 58, Position: "bottom", MaxWordsPerRow: 4, Uppercase: true}
	afx := model.AudioFXConfig{BGMVolume: 0.18, AutoDuck: true, SFXAutoPreset: "auto_by_moment"}
	vfx := model.VideoFXConfig{ColorPreset: "vivid_gaming", MotionPreset: "peak_punch", FadeIn: true, Speed: 1.0, Sharpen: true}
	useWM, useOutro, audioNorm := true, true, true
	campID := proj.CampaignID
	if camp != nil {
		campID = camp.ID
		if camp.DefaultLayout.AspectRatio != "" {
			layout = camp.DefaultLayout
		}
		if camp.DefaultSubtitles.Preset != "" {
			subs = camp.DefaultSubtitles
		}
		afx = camp.DefaultAudioFX
		if afx.SFXAutoPreset == "" {
			afx.SFXAutoPreset = "auto_by_moment"
		}
		vfx = camp.DefaultVideoFX
		useWM = camp.Watermark.Enabled
		useOutro = camp.Outro.Enabled
		audioNorm = camp.DefaultAudioNorm
	}
	vfx.PeakOffsetSec = math.Max(1.0, cand.PeakSec-cand.StartSec)
	title := strings.TrimSpace(cand.HookText)
	if title == "" {
		title = cand.Title
	}
	return model.RenderOptions{
		ProjectID:    proj.ID,
		CampaignID:   campID,
		CandidateID:  cand.ID,
		Title:        title,
		StartSec:     cand.StartSec,
		EndSec:       cand.EndSec,
		HookText:     cand.HookText,
		Layout:       layout,
		Subtitles:    subs,
		AudioFX:      afx,
		VideoFX:      vfx,
		AudioNorm:    audioNorm,
		UseWatermark: useWM,
		UseOutro:     useOutro,
	}
}

// handleBatchReviewJobs allows QC/Owner to approve multiple clips in one tap from Review batch mode.
func (s *Server) handleBatchReviewJobs(w http.ResponseWriter, r *http.Request) {
	p := getPrincipal(r)
	if p.Role != "owner" && p.Role != "admin" && p.Role != "qc" {
		writeErr(w, http.StatusForbidden, "Hanya Owner atau QC yang boleh menjalankan batch review")
		return
	}
	var req struct {
		JobIDs       []string `json:"jobIds"`
		ReviewStatus string   `json:"reviewStatus"`
		ReviewNote   string   `json:"reviewNote"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || len(req.JobIDs) == 0 {
		writeErr(w, http.StatusBadRequest, "jobIds wajib diisi")
		return
	}
	if req.ReviewStatus == "" {
		req.ReviewStatus = "approved"
	}
	if req.ReviewStatus != "approved" && req.ReviewStatus != "rejected" {
		writeErr(w, http.StatusBadRequest, "reviewStatus harus approved atau rejected")
		return
	}
	now := time.Now()
	var updated []string
	var skipped []string
	for _, id := range req.JobIDs {
		job, ok := s.Store.GetJob(id)
		if !ok {
			skipped = append(skipped, id+": tidak ditemukan")
			continue
		}
		if p.Role == "qc" && req.ReviewStatus == "approved" && job.CreatedByID != "" && job.CreatedByID == p.ID {
			skipped = append(skipped, id+": self-approve diblokir")
			continue
		}
		job.ReviewStatus = req.ReviewStatus
		job.ReviewedBy = p.Name
		job.ReviewedAt = &now
		if req.ReviewNote != "" {
			job.ReviewNote = req.ReviewNote
		}
		if req.ReviewStatus == "approved" {
			job.BoardColumn = "siap_posting"
			if job.PublishKit == nil {
				if kit, err := s.buildPublishKitForJob(job, nil); err == nil {
					job.PublishKit = kit
				}
			}
		} else {
			job.BoardColumn = "kandidat"
			job.RejectReason = req.ReviewNote
		}
		_ = s.Store.SaveJob(job)
		s.Broker.Publish("job_update", job)
		updated = append(updated, id)
	}
	s.recordActivity(p, "review_batch", "job", strings.Join(updated, ","),
		fmt.Sprintf("%s menyetujui batch %d klip", p.Name, len(updated)), req.ReviewNote, "success")
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"ok":      true,
		"updated": updated,
		"skipped": skipped,
	})
}

// handleTeamMetrics returns non-secret presence, turnaround, approval rate, active jobs, and 7-day sparklines for the Tim screen.
func (s *Server) handleTeamMetrics(w http.ResponseWriter, r *http.Request) {
	now := time.Now()
	todayStr := now.Format("2006-01-02")
	jobs := s.Store.ListJobs()
	members := s.Store.ListTeam()

	type activeJobInfo struct {
		ID       string  `json:"id"`
		Title    string  `json:"title"`
		Status   string  `json:"status"`
		Stage    string  `json:"stage"`
		Progress float64 `json:"progress"`
		Via      string  `json:"via"`
		WorkerID int     `json:"workerId"`
	}
	type memberCard struct {
		ID                string          `json:"id"`
		Name              string          `json:"name"`
		Role              string          `json:"role"`
		Online            bool            `json:"online"`
		LastSeenAt        *time.Time      `json:"lastSeenAt,omitempty"`
		MaxActive         int             `json:"maxActive"`
		ActiveCount       int             `json:"activeCount"`
		JobsToday         int             `json:"jobsToday"`
		TotalDone         int             `json:"totalDone"`
		ApprovedCount     int             `json:"approvedCount"`
		RejectedCount     int             `json:"rejectedCount"`
		ApprovalRate      float64         `json:"approvalRate"`
		AvgTurnaroundMin  float64         `json:"avgTurnaroundMin"`
		AgentJobs         int             `json:"agentJobs"`
		Sparkline7d       []int           `json:"sparkline7d"`
		ActiveJobs        []activeJobInfo `json:"activeJobs"`
	}

	byID := make(map[string]*memberCard)
	order := []string{"owner"}
	byID["owner"] = &memberCard{
		ID:          "owner",
		Name:        "Owner",
		Role:        "owner",
		Online:      true,
		MaxActive:   10,
		Sparkline7d: make([]int, 7),
	}
	for _, m := range members {
		if !m.Enabled {
			continue
		}
		online := m.LastSeenAt != nil && now.Sub(*m.LastSeenAt) < 15*time.Minute
		lim := m.MaxActive
		if lim <= 0 {
			lim = 3
		}
		byID[m.ID] = &memberCard{
			ID:          m.ID,
			Name:        m.Name,
			Role:        m.Role,
			Online:      online,
			LastSeenAt:  m.LastSeenAt,
			MaxActive:   lim,
			Sparkline7d: make([]int, 7),
		}
		order = append(order, m.ID)
	}

	turnaroundSum := make(map[string]float64)
	turnaroundCnt := make(map[string]int)

	for _, j := range jobs {
		cid := j.CreatedByID
		if cid == "" || cid == "owner-agent" {
			cid = "owner"
		}
		mc, ok := byID[cid]
		if !ok {
			mc = &memberCard{
				ID:          cid,
				Name:        j.CreatedBy,
				Role:        "clipper",
				MaxActive:   3,
				Sparkline7d: make([]int, 7),
			}
			if mc.Name == "" {
				mc.Name = cid
			}
			byID[cid] = mc
			order = append(order, cid)
		}
		if j.CreatedAt.Format("2006-01-02") == todayStr {
			mc.JobsToday++
		}
		if j.Status == "done" {
			mc.TotalDone++
		}
		if j.CreatedVia == "agent" {
			mc.AgentJobs++
		}
		if j.ReviewStatus == "approved" {
			mc.ApprovedCount++
			if j.CompletedAt != nil && j.ReviewedAt != nil && j.ReviewedAt.After(*j.CompletedAt) {
				turnaroundSum[cid] += j.ReviewedAt.Sub(*j.CompletedAt).Minutes()
				turnaroundCnt[cid]++
			}
		} else if j.ReviewStatus == "rejected" {
			mc.RejectedCount++
		}
		if j.Status == "queued" || j.Status == "downloading" || j.Status == "rendering" {
			mc.ActiveCount++
			mc.ActiveJobs = append(mc.ActiveJobs, activeJobInfo{
				ID:       j.ID,
				Title:    j.Title,
				Status:   j.Status,
				Stage:    j.Stage,
				Progress: j.Progress,
				Via:      j.CreatedVia,
				WorkerID: j.WorkerID,
			})
		}
		// 7-day sparkline bucket (index 6 = today, index 0 = 6 days ago)
		daysAgo := int(now.Truncate(24*time.Hour).Sub(j.CreatedAt.Truncate(24*time.Hour)).Hours() / 24)
		if daysAgo >= 0 && daysAgo < 7 {
			mc.Sparkline7d[6-daysAgo]++
		}
	}

	out := make([]*memberCard, 0, len(order))
	for _, id := range order {
		mc := byID[id]
		reviewed := mc.ApprovedCount + mc.RejectedCount
		if reviewed > 0 {
			mc.ApprovalRate = math.Round(float64(mc.ApprovedCount)/float64(reviewed)*1000) / 10
		}
		if turnaroundCnt[id] > 0 {
			mc.AvgTurnaroundMin = math.Round(turnaroundSum[id]/float64(turnaroundCnt[id])*10) / 10
		}
		out = append(out, mc)
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"members": out,
		"now":     now,
	})
}

// handleHomeMetrics computes Beranda's 4 KPI tiles with 7-day deltas, live active render cards, and oldest-first pending reviews.
func (s *Server) handleHomeMetrics(w http.ResponseWriter, r *http.Request) {
	now := time.Now()
	cut7 := now.AddDate(0, 0, -7)
	cut14 := now.AddDate(0, 0, -14)
	jobs := s.Store.ListJobs()

	var rendCur, rendPrev int
	var appCur, appPrev int
	var postCur, postPrev int
	var durSumCur, durSumPrev float64
	var durCntCur, durCntPrev int

	type activeItem struct {
		ID            string  `json:"id"`
		Title         string  `json:"title"`
		Status        string  `json:"status"`
		Stage         string  `json:"stage"`
		Progress      float64 `json:"progress"`
		Speed         string  `json:"speed,omitempty"`
		WorkerID      int     `json:"workerId"`
		QueuePosition int     `json:"queuePosition"`
		ElapsedSec    float64 `json:"elapsedSec"`
		PeekURL       string  `json:"peekUrl"`
		CreatedBy     string  `json:"createdBy,omitempty"`
		CreatedVia    string  `json:"createdVia,omitempty"`
	}
	type pendingItem struct {
		ID           string  `json:"id"`
		Title        string  `json:"title"`
		CampaignName string  `json:"campaignName"`
		CreatedBy    string  `json:"createdBy,omitempty"`
		CreatedByID  string  `json:"createdById,omitempty"`
		CreatedVia   string  `json:"createdVia,omitempty"`
		AgeHours     float64 `json:"ageHours"`
		SLALevel     string  `json:"slaLevel"` // plain (<6h), amber (6-24h), red (>24h)
		CreatedAt    time.Time `json:"createdAt"`
	}

	var active []activeItem
	var pending []pendingItem

	queueIdx := 0
	for _, j := range jobs {
		inCur := j.CreatedAt.After(cut7)
		inPrev := !inCur && j.CreatedAt.After(cut14)

		if j.Status == "done" {
			if inCur {
				rendCur++
			} else if inPrev {
				rendPrev++
			}
			if j.StartedAt != nil && j.CompletedAt != nil && j.CompletedAt.After(*j.StartedAt) {
				d := j.CompletedAt.Sub(*j.StartedAt).Seconds()
				if inCur {
					durSumCur += d
					durCntCur++
				} else if inPrev {
					durSumPrev += d
					durCntPrev++
				}
			}
		}
		if j.ReviewStatus == "approved" {
			if inCur {
				appCur++
			} else if inPrev {
				appPrev++
			}
		}
		if effectiveJobColumn(j) == "diposting" {
			if inCur {
				postCur++
			} else if inPrev {
				postPrev++
			}
		}

		if j.Status == "queued" || j.Status == "downloading" || j.Status == "rendering" {
			queueIdx++
			elapsed := time.Since(j.CreatedAt).Seconds()
			if j.StartedAt != nil {
				elapsed = time.Since(*j.StartedAt).Seconds()
			}
			active = append(active, activeItem{
				ID:            j.ID,
				Title:         j.Title,
				Status:        j.Status,
				Stage:         j.Stage,
				Progress:      j.Progress,
				Speed:         j.Speed,
				WorkerID:      j.WorkerID,
				QueuePosition: queueIdx,
				ElapsedSec:    math.Round(elapsed),
				PeekURL:       fmt.Sprintf("/api/jobs/%s/peek", j.ID),
				CreatedBy:     j.CreatedBy,
				CreatedVia:    j.CreatedVia,
			})
		}

		if j.Status == "done" && (j.ReviewStatus == "" || j.ReviewStatus == "pending_review") {
			ref := j.CreatedAt
			if j.CompletedAt != nil {
				ref = *j.CompletedAt
			}
			ageH := math.Round(now.Sub(ref).Hours()*10) / 10
			sla := "plain"
			if ageH >= 24 {
				sla = "red"
			} else if ageH >= 6 {
				sla = "amber"
			}
			pending = append(pending, pendingItem{
				ID:           j.ID,
				Title:        j.Title,
				CampaignName: j.CampaignName,
				CreatedBy:    j.CreatedBy,
				CreatedByID:  j.CreatedByID,
				CreatedVia:   j.CreatedVia,
				AgeHours:     ageH,
				SLALevel:     sla,
				CreatedAt:    ref,
			})
		}
	}

	// Sort pending reviews OLDEST FIRST (SLA priority!)
	sort.Slice(pending, func(i, j int) bool {
		return pending[i].CreatedAt.Before(pending[j].CreatedAt)
	})

	avgCur := 0.0
	if durCntCur > 0 {
		avgCur = math.Round(durSumCur/float64(durCntCur)*10) / 10
	}
	avgPrev := 0.0
	if durCntPrev > 0 {
		avgPrev = math.Round(durSumPrev/float64(durCntPrev)*10) / 10
	}

	p := getPrincipal(r)
	myActive := s.Store.CountActiveJobsByMember(p.ID)
	myLimit := p.MaxActive
	if myLimit <= 0 {
		if p.Role == "owner" || p.Role == "admin" {
			myLimit = 10
		} else {
			myLimit = 3
		}
	}

	cfg := s.Store.GetSettings()
	engineState := "bangun"
	if cfg.CooldownUntil != nil && cfg.CooldownUntil.After(now) {
		engineState = "cooldown"
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"engineState":   engineState,
		"cooldownUntil": cfg.CooldownUntil,
		"myQueue":       map[string]int{"active": myActive, "limit": myLimit},
		"kpis": map[string]interface{}{
			"rendered7d":   map[string]interface{}{"value": rendCur, "delta": rendCur - rendPrev},
			"approved7d":   map[string]interface{}{"value": appCur, "delta": appCur - appPrev},
			"posted7d":     map[string]interface{}{"value": postCur, "delta": postCur - postPrev},
			"avgRenderSec": map[string]interface{}{"value": avgCur, "delta": math.Round((avgCur-avgPrev)*10) / 10},
		},
		"activeJobs":     active,
		"pendingReviews": pending,
	})
}
