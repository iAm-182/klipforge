package server

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"klipforge/internal/analyzer"
	"klipforge/internal/model"
	"klipforge/internal/renderer"
	"klipforge/internal/youtube"
)

// handleScanLiveChat fetches YouTube Live Chat Replay (fast parallel Innertube seeker -> yt-dlp fallback)
// even on 3–5 hour VODs, re-runs DetectHype with trailing reaction lag + unique-chatter anti-spam + Visual Gag Boost,
// and preserves existing agent verdicts & verification frames.
func (s *Server) handleScanLiveChat(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	proj, ok := s.Store.GetProject(id)
	if !ok {
		writeErr(w, http.StatusNotFound, "Project tidak ditemukan")
		return
	}

	var req struct {
		Sync bool `json:"sync"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)

	p := getPrincipal(r)

	runScan := func(ctx context.Context) (int, string, *model.Project, error) {
		projDir := s.Store.ProjectDir(proj.ID)
		settings := s.Store.GetSettings()

		chatPath, _, engine, err := s.YT.DownloadLiveChatSmart(ctx, proj.SourceURL, proj.VideoID, proj.DurationSec, projDir, settings, nil)
		if err != nil || chatPath == "" {
			if err == nil {
				err = fmt.Errorf("live chat replay tidak tersedia untuk video ini")
			}
			return 0, engine, nil, err
		}

		msgs, err := youtube.ParseLiveChatFile(chatPath)
		if err != nil || len(msgs) < 5 {
			if err == nil {
				err = fmt.Errorf("live chat terlalu sedikit (%d pesan) untuk dianalisis", len(msgs))
			}
			return len(msgs), engine, nil, err
		}

		cs := s.loadCachedSignals(proj)
		s.cacheMu.Lock()
		cs.Chats = msgs
		heatSnap, audioSnap, transSnap := cs.Heatmap, cs.AudioEnergy, cs.Transcript
		s.cacheMu.Unlock()

		latestProj, ok := s.Store.GetProject(proj.ID)
		if !ok {
			return len(msgs), engine, nil, fmt.Errorf("project sudah dihapus")
		}
		latestProj.LiveChatPath = chatPath

		upgraded := analyzer.DetectHype(analyzer.AnalysisInput{
			DurationSec:    latestProj.DurationSec,
			Heatmap:        heatSnap,
			Chats:          msgs,
			AudioEnergy:    audioSnap,
			Transcript:     transSnap,
			Chapters:       latestProj.Chapters,
			Settings:       latestProj.DetectConfig,
			ForbiddenWords: s.campaignForbiddenWords(latestProj.CampaignID),
		})
		latestProj.Timeline = upgraded.Timeline
		latestProj.Candidates = mergePreservedCandidateMeta(latestProj.Candidates, upgraded.Candidates)
		s.enrichProjectAutoEdit(latestProj)
		latestProj.Signals = upgraded.Signals
		latestProj.Status = "ready"
		latestProj.StatusMessage = fmt.Sprintf("💬 Live Chat Replay siap (%s): %d pesan dianalisis -> %d momen hits!", engine, len(msgs), len(latestProj.Candidates))
		_ = s.Store.SaveProject(latestProj)
		s.Broker.Publish("project_update", latestProj)
		s.Broker.Log("analyzer", "success", latestProj.StatusMessage)
		s.recordActivity(p, "livechat_scan", "project", latestProj.ID,
			fmt.Sprintf("%s memperkaya %s dengan %d pesan Live Chat (%s)", p.Name, latestProj.Title, len(msgs), engine),
			fmt.Sprintf("%d kandidat", len(latestProj.Candidates)), "info")
		return len(msgs), engine, latestProj, nil
	}

	if req.Sync {
		ctx, cancel := context.WithTimeout(r.Context(), 150*time.Second)
		defer cancel()
		count, engine, latest, err := runScan(ctx)
		if err != nil {
			writeErr(w, http.StatusUnprocessableEntity, "Scan Live Chat gagal: "+err.Error())
			return
		}
		topN := 8
		if len(latest.Candidates) < topN {
			topN = len(latest.Candidates)
		}
		type candBrief struct {
			ID          string   `json:"id"`
			StartSec    float64  `json:"startSec"`
			EndSec      float64  `json:"endSec"`
			PeakSec     float64  `json:"peakSec"`
			Score       float64  `json:"score"`
			Title       string   `json:"title"`
			Reasons     []string `json:"reasons,omitempty"`
			SampleChats []string `json:"sampleChats,omitempty"`
		}
		briefs := make([]candBrief, 0, topN)
		for i := 0; i < topN; i++ {
			c := latest.Candidates[i]
			briefs = append(briefs, candBrief{
				ID:          c.ID,
				StartSec:    c.StartSec,
				EndSec:      c.EndSec,
				PeakSec:     c.PeakSec,
				Score:       c.Score,
				Title:       c.Title,
				Reasons:     c.Reasons,
				SampleChats: c.SampleChats,
			})
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"ok":              true,
			"projectId":       latest.ID,
			"engine":          engine,
			"chatMessages":    count,
			"hasLiveChat":     latest.Signals.HasLiveChat,
			"signals":         latest.Signals,
			"candidatesCount": len(latest.Candidates),
			"topCandidates":   briefs,
			"statusMessage":   latest.StatusMessage,
		})
		return
	}

	// Async background scan for non-blocking Studio UI button
	proj.Status = "scanning"
	proj.StatusMessage = "💬 Mengambil Live Chat Replay YouTube secara paralel (cepat untuk VOD panjang)..."
	_ = s.Store.SaveProject(proj)
	s.Broker.Publish("project_update", proj)

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
		defer cancel()
		if _, _, _, err := runScan(ctx); err != nil {
			if latest, ok := s.Store.GetProject(proj.ID); ok {
				latest.Status = "ready"
				latest.StatusMessage = "Live Chat tidak tersedia / timeout: " + err.Error()
				_ = s.Store.SaveProject(latest)
				s.Broker.Publish("project_update", latest)
			}
			s.Broker.Log("analyzer", "warn", "Scan Live Chat gagal: "+err.Error())
		}
	}()

	writeJSON(w, http.StatusAccepted, proj)
}

// handleGetTranscriptWindows returns AutoClip-style one-pass compact `id|time|text` transcript windows
// enriched with inline [SPEAKER_X], [CHAT:N], [AUD:N], and synthetic [VISUAL_CHAT_SPIKE] lines.
func (s *Server) handleGetTranscriptWindows(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	proj, ok := s.Store.GetProject(id)
	if !ok {
		writeErr(w, http.StatusNotFound, "Project tidak ditemukan")
		return
	}

	q := r.URL.Query()
	winIdx, _ := strconv.Atoi(q.Get("window"))
	winMin, _ := strconv.ParseFloat(q.Get("windowMin"), 64)
	overlapSec, _ := strconv.ParseFloat(q.Get("overlapSec"), 64)
	maxChars, _ := strconv.Atoi(q.Get("maxChars"))

	cs := s.loadCachedSignals(proj)
	s.cacheMu.RLock()
	cues := cs.Transcript
	s.cacheMu.RUnlock()
	if len(cues) == 0 {
		cues = s.loadProjectCuesPreferDiarized(proj)
	}

	res := analyzer.BuildCompactTranscriptWindows(analyzer.CompactTranscriptInput{
		DurationSec:   proj.DurationSec,
		Cues:          cues,
		Timeline:      proj.Timeline,
		WindowMinutes: winMin,
		OverlapSec:    overlapSec,
		MaxChars:      maxChars,
		WindowIndex:   winIdx,
	})

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"ok":           true,
		"projectId":    proj.ID,
		"title":        proj.Title,
		"durationSec":  proj.DurationSec,
		"hasLiveChat":  proj.Signals.HasLiveChat,
		"hasAudio":     proj.Signals.HasAudio,
		"cueCount":     len(cues),
		"totalWindows": res.TotalWindows,
		"windowIndex":  res.WindowIndex,
		"window":       res.Window,
		"allWindows":   res.AllWindows,
		"format":       res.Format,
		"compactText":  res.CompactText,
	})
}

// handleSampleStoryboard extracts key frames across a range or full VOD and stitches them into
// ONE labeled contact-sheet JPEG (works on local MP4 OR remote YouTube stream URL in Audio-First mode).
func (s *Server) handleSampleStoryboard(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	proj, ok := s.Store.GetProject(id)
	if !ok {
		writeErr(w, http.StatusNotFound, "Project tidak ditemukan")
		return
	}

	var req struct {
		StartSec      float64   `json:"startSec"`
		EndSec        float64   `json:"endSec"`
		Timestamps    []float64 `json:"timestamps"`
		Count         int       `json:"count"`
		Cols          int       `json:"cols"`
		ThumbW        int       `json:"thumbW"`
		IncludeBase64 bool      `json:"includeBase64"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)

	projDir := s.Store.ProjectDir(proj.ID)

	// Load optional cached scene cuts for better timestamp placement
	var sceneCuts []float64
	if raw, err := os.ReadFile(filepath.Join(projDir, "scenes.json")); err == nil {
		var sc analyzer.SceneCutResult
		if json.Unmarshal(raw, &sc) == nil {
			sceneCuts = sc.Cuts
		}
	}

	timestamps := req.Timestamps
	if len(timestamps) == 0 {
		timestamps = renderer.PickStoryboardTimestamps(proj.DurationSec, req.StartSec, req.EndSec, req.Count, proj.Timeline, sceneCuts)
	}
	if len(timestamps) > 16 {
		timestamps = timestamps[:16]
	}

	// Resolve media source: local MP4 first, else cached/fresh YouTube stream URL (Audio-First compatible!)
	mediaSrc := ""
	usingRemote := false
	cs := s.loadCachedSignals(proj)
	if proj.LocalVideoPath != "" && pathExists(proj.LocalVideoPath) {
		mediaSrc = proj.LocalVideoPath
	} else {
		s.cacheMu.RLock()
		mediaSrc = cs.StreamURL
		s.cacheMu.RUnlock()
		usingRemote = true
		if mediaSrc == "" && proj.SourceURL != "" {
			if meta, err := s.YT.FetchMetadata(r.Context(), proj.SourceURL, s.Store.GetSettings()); err == nil && meta.StreamURL != "" {
				mediaSrc = meta.StreamURL
				s.cacheMu.Lock()
				cs.StreamURL = mediaSrc
				s.cacheMu.Unlock()
			}
		}
	}
	if mediaSrc == "" {
		writeErr(w, http.StatusBadRequest, "Sumber video / stream URL belum tersedia untuk membuat storyboard")
		return
	}

	fileName := fmt.Sprintf("sb_%d.jpg", time.Now().UnixNano())
	destJPG := filepath.Join(projDir, "storyboards", fileName)
	ffmpegPath := s.Deps.GetStatus().FFmpegPath

	res, err := renderer.GenerateStoryboardContactSheet(r.Context(), ffmpegPath, mediaSrc, timestamps, req.Cols, req.ThumbW, destJPG)
	if err != nil && usingRemote && proj.SourceURL != "" {
		// Cached YouTube CDN stream URL may have expired; refresh once and retry!
		if meta, merr := s.YT.FetchMetadata(r.Context(), proj.SourceURL, s.Store.GetSettings()); merr == nil && meta.StreamURL != "" && meta.StreamURL != mediaSrc {
			mediaSrc = meta.StreamURL
			s.cacheMu.Lock()
			cs.StreamURL = mediaSrc
			s.cacheMu.Unlock()
			res, err = renderer.GenerateStoryboardContactSheet(r.Context(), ffmpegPath, mediaSrc, timestamps, req.Cols, req.ThumbW, destJPG)
		}
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "Gagal membuat storyboard: "+err.Error())
		return
	}

	p := getPrincipal(r)
	s.recordActivity(p, "storyboard_sample", "project", proj.ID,
		fmt.Sprintf("%s membuat storyboard visual %dx%d (%d frame) untuk %s", p.Name, res.Cols, res.Rows, len(res.Frames), proj.Title),
		res.FileName, "info")

	out := map[string]interface{}{
		"ok":        true,
		"projectId": proj.ID,
		"fileName":  res.FileName,
		"imageUrl":  fmt.Sprintf("/api/projects/%s/storyboard/%s", proj.ID, res.FileName),
		"width":     res.Width,
		"height":    res.Height,
		"cols":      res.Cols,
		"rows":      res.Rows,
		"frames":    res.Frames,
	}
	if req.IncludeBase64 {
		if raw, rerr := os.ReadFile(res.FilePath); rerr == nil && len(raw) > 0 {
			out["mimeType"] = "image/jpeg"
			out["base64"] = base64.StdEncoding.EncodeToString(raw)
		}
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleServeStoryboard(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	fn := filepath.Base(strings.TrimSpace(r.PathValue("filename")))
	if fn == "" || fn == "." || fn == ".." || !strings.HasSuffix(strings.ToLower(fn), ".jpg") {
		http.NotFound(w, r)
		return
	}
	p := filepath.Join(s.Store.ProjectDir(id), "storyboards", fn)
	if !pathExists(p) {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "image/jpeg")
	w.Header().Set("Cache-Control", "public, max-age=3600")
	http.ServeFile(w, r, p)
}
