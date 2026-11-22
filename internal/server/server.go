package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"klipforge/internal/analyzer"
	"klipforge/internal/audiofx"
	"klipforge/internal/autopilot"
	"klipforge/internal/deps"
	"klipforge/internal/events"
	"klipforge/internal/model"
	"klipforge/internal/renderer"
	"klipforge/internal/store"
	"klipforge/internal/youtube"
	"klipforge/web"
)

// Server wires all REST endpoints, SSE streaming, media serving, and embedded dashboard UI.
type Server struct {
	Store     *store.Store
	Deps      *deps.Manager
	YT        *youtube.Client
	Audio     *audiofx.Library
	Broker    *events.Broker
	Worker    *renderer.WorkerPool
	Autopilot *autopilot.Scheduler

	// Cache raw signals in memory for instant re-analysis without re-fetching YouTube
	cacheMu    sync.RWMutex
	rawSignals map[string]*cachedSignals
}

type cachedSignals struct {
	Heatmap     []youtube.HeatmapPoint
	Chats       []youtube.ChatMessage
	AudioEnergy []float64
	Transcript  []model.TranscriptCue
	StreamURL   string
}

func New(st *store.Store, d *deps.Manager, broker *events.Broker) *Server {
	yt := youtube.NewClient(d)
	audioLib := audiofx.NewLibrary(st.DataDir, d)
	wp := renderer.NewWorkerPool(st, d, yt, audioLib, broker)
	s := &Server{
		Store:      st,
		Deps:       d,
		YT:         yt,
		Audio:      audioLib,
		Broker:     broker,
		Worker:     wp,
		rawSignals: make(map[string]*cachedSignals),
	}
	s.Autopilot = autopilot.NewScheduler(st, yt, wp, broker, s.analyzeSyncFast)
	go s.recoverIncompleteProjects()
	return s
}

// recoverIncompleteProjects finishes any cached project that already downloaded subs/preview on disk before restart.
func (s *Server) recoverIncompleteProjects() {
	for _, p := range s.Store.ListProjects() {
		if len(p.Candidates) > 0 {
			continue
		}
		dir := s.Store.ProjectDir(p.ID)
		if p.PreviewFrame == "" {
			if _, err := os.Stat(filepath.Join(dir, "preview.jpg")); err == nil {
				p.PreviewFrame = filepath.Join(dir, "preview.jpg")
			}
		}
		if p.SubtitlePath == "" {
			if matches, _ := filepath.Glob(filepath.Join(dir, "subs*.vtt")); len(matches) > 0 {
				p.SubtitlePath = matches[0]
			}
		}
		if p.SubtitlePath != "" {
			cs := s.loadCachedSignals(p)
			if len(cs.Transcript) > 0 || len(cs.Heatmap) > 0 || len(cs.Chats) > 0 {
				res := analyzer.DetectHype(analyzer.AnalysisInput{
					DurationSec: p.DurationSec,
					Heatmap:     cs.Heatmap,
					Chats:       cs.Chats,
					AudioEnergy: cs.AudioEnergy,
					Transcript:  cs.Transcript,
					Chapters:    p.Chapters,
					Settings:    p.DetectConfig,
				})
				p.Timeline = res.Timeline
				p.Candidates = res.Candidates
				p.Signals = res.Signals
				p.Status = "ready"
				p.StatusMessage = fmt.Sprintf("Siap! Ditemukan %d momen hits dari transkrip & sinyal tersimpan.", len(res.Candidates))
				_ = s.Store.SaveProject(p)
				s.Broker.Publish("project_update", p)
			}
		}
	}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	// Health, SSE & Settings
	mux.HandleFunc("GET /api/status", s.handleStatus)
	mux.HandleFunc("POST /api/deps/refresh", s.handleRefreshDeps)
	mux.Handle("GET /api/events", s.Broker)
	mux.HandleFunc("GET /api/settings", s.handleGetSettings)
	mux.HandleFunc("PUT /api/settings", s.handleSaveSettings)
	mux.HandleFunc("POST /api/open-output-folder", s.handleOpenOutputFolder)

	// Campaigns & Asset uploads (Watermark / Outro / Intro)
	mux.HandleFunc("GET /api/campaigns", s.handleListCampaigns)
	mux.HandleFunc("POST /api/campaigns", s.handleCreateCampaign)
	mux.HandleFunc("PUT /api/campaigns/{id}", s.handleUpdateCampaign)
	mux.HandleFunc("DELETE /api/campaigns/{id}", s.handleDeleteCampaign)
	mux.HandleFunc("POST /api/campaigns/{id}/upload/{assetType}", s.handleUploadCampaignAsset)
	mux.HandleFunc("GET /api/assets/{filename}", s.handleServeAsset)

	// Projects (YouTube Analyze / Full Download / Re-Analyze / Frame Grab)
	mux.HandleFunc("GET /api/projects", s.handleListProjects)
	mux.HandleFunc("GET /api/projects/{id}", s.handleGetProject)
	mux.HandleFunc("DELETE /api/projects/{id}", s.handleDeleteProject)
	mux.HandleFunc("POST /api/projects/analyze", s.handleAnalyzeProject)
	mux.HandleFunc("POST /api/projects/{id}/reanalyze", s.handleReanalyzeProject)
	mux.HandleFunc("POST /api/projects/{id}/download-full", s.handleDownloadFull)
	mux.HandleFunc("POST /api/projects/{id}/grab-frame", s.handleGrabFrame)
	mux.HandleFunc("GET /api/projects/{id}/preview-frame", s.handleServePreviewFrame)
	mux.HandleFunc("GET /api/projects/{id}/video", s.handleServeProjectVideo)

	// Render Jobs, Clips & Human-in-the-Loop Review
	mux.HandleFunc("GET /api/jobs", s.handleListJobs)
	mux.HandleFunc("POST /api/jobs/render", s.handleRenderSingle)
	mux.HandleFunc("POST /api/jobs/batch", s.handleRenderBatch)
	mux.HandleFunc("POST /api/jobs/{id}/review", s.handleReviewJob)
	mux.HandleFunc("POST /api/jobs/{id}/cancel", s.handleCancelJob)
	mux.HandleFunc("POST /api/jobs/{id}/retry", s.handleRetryJob)
	mux.HandleFunc("DELETE /api/jobs/{id}", s.handleDeleteJob)
	mux.HandleFunc("GET /api/jobs/{id}/video", s.handleServeJobVideo)

	// Autonomous Scheduler / Autopilot (n8n-style HITL workflow)
	mux.HandleFunc("GET /api/autopilot", s.handleListAutopilots)
	mux.HandleFunc("POST /api/autopilot", s.handleCreateAutopilot)
	mux.HandleFunc("PUT /api/autopilot/{id}", s.handleUpdateAutopilot)
	mux.HandleFunc("DELETE /api/autopilot/{id}", s.handleDeleteAutopilot)
	mux.HandleFunc("POST /api/autopilot/{id}/run", s.handleRunAutopilot)

	// Audio Library (BGM & SFX starter pack + custom uploads)
	mux.HandleFunc("GET /api/audio", s.handleListAudio)
	mux.HandleFunc("POST /api/audio/upload/{kind}", s.handleUploadAudio)
	mux.HandleFunc("DELETE /api/audio/{id}", s.handleDeleteAudio)
	mux.HandleFunc("GET /api/audio/file/{filename}", s.handleServeAudio)
	mux.HandleFunc("POST /api/audio/regenerate", s.handleRegenerateAudio)

	// Embedded Web Dashboard UI
	subFS, _ := fs.Sub(web.FS, ".")
	fileServer := http.FileServer(http.FS(subFS))
	mux.Handle("/", fileServer)

	return corsMiddleware(mux)
}

func corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, status int, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(data)
}

func writeErr(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

// --- Status & Settings ---

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"deps":      s.Deps.GetStatus(),
		"settings":  s.Store.GetSettings(),
		"campaigns": len(s.Store.ListCampaigns()),
		"projects":  len(s.Store.ListProjects()),
		"jobs":      len(s.Store.ListJobs()),
	})
}

func (s *Server) handleRefreshDeps(w http.ResponseWriter, r *http.Request) {
	st := s.Deps.Refresh()
	writeJSON(w, http.StatusOK, st)
}

func (s *Server) handleGetSettings(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.Store.GetSettings())
}

func (s *Server) handleSaveSettings(w http.ResponseWriter, r *http.Request) {
	var cfg model.AppSettings
	if err := json.NewDecoder(r.Body).Decode(&cfg); err != nil {
		writeErr(w, http.StatusBadRequest, "Format JSON settings tidak valid")
		return
	}
	if err := s.Store.SaveSettings(cfg); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, s.Store.GetSettings())
}

func (s *Server) handleOpenOutputFolder(w http.ResponseWriter, r *http.Request) {
	dir := s.Store.GetSettings().OutputDir
	_ = os.MkdirAll(dir, 0o755)
	if runtime.GOOS == "windows" {
		_ = exec.Command("explorer.exe", dir).Start()
	} else if runtime.GOOS == "darwin" {
		_ = exec.Command("open", dir).Start()
	}
	writeJSON(w, http.StatusOK, map[string]string{"path": dir})
}

// --- Campaigns ---

func (s *Server) handleListCampaigns(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.Store.ListCampaigns())
}

func (s *Server) handleCreateCampaign(w http.ResponseWriter, r *http.Request) {
	var camp model.Campaign
	if err := json.NewDecoder(r.Body).Decode(&camp); err != nil {
		writeErr(w, http.StatusBadRequest, "Format JSON campaign tidak valid")
		return
	}
	if strings.TrimSpace(camp.Name) == "" {
		writeErr(w, http.StatusBadRequest, "Nama campaign wajib diisi")
		return
	}
	if camp.ID == "" {
		camp.ID = fmt.Sprintf("camp-%d", time.Now().UnixNano())
	}
	if camp.DefaultLayout.AspectRatio == "" {
		camp.DefaultLayout = model.LayoutConfig{
			AspectRatio: "9:16",
			Resolution:  "1080p",
			CropMode:    "split_custom",
			SplitRatio:  0.62,
			GameplayBox: model.CropBox{X: 0.14, Y: 0.0, W: 0.72, H: 1.0},
			FacecamBox:  model.CropBox{X: 0.72, Y: 0.62, W: 0.28, H: 0.38},
		}
	}
	if err := s.Store.SaveCampaign(&camp); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, camp)
}

func (s *Server) handleUpdateCampaign(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	existing, ok := s.Store.GetCampaign(id)
	if !ok {
		writeErr(w, http.StatusNotFound, "Campaign tidak ditemukan")
		return
	}
	var updated model.Campaign
	if err := json.NewDecoder(r.Body).Decode(&updated); err != nil {
		writeErr(w, http.StatusBadRequest, "Format JSON tidak valid")
		return
	}
	updated.ID = existing.ID
	// Preserve uploaded file paths if client did not clear them intentionally
	if updated.Watermark.FilePath == "" && existing.Watermark.FilePath != "" && updated.Watermark.FileName == existing.Watermark.FileName {
		updated.Watermark.FilePath = existing.Watermark.FilePath
	}
	if updated.Outro.FilePath == "" && existing.Outro.FilePath != "" && updated.Outro.FileName == existing.Outro.FileName {
		updated.Outro.FilePath = existing.Outro.FilePath
		updated.Outro.Duration = existing.Outro.Duration
	}
	if updated.Intro.FilePath == "" && existing.Intro.FilePath != "" && updated.Intro.FileName == existing.Intro.FileName {
		updated.Intro.FilePath = existing.Intro.FilePath
		updated.Intro.Duration = existing.Intro.Duration
	}
	if err := s.Store.SaveCampaign(&updated); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, updated)
}

func (s *Server) handleDeleteCampaign(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.Store.DeleteCampaign(id); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"deleted": true})
}

func (s *Server) handleUploadCampaignAsset(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	assetType := r.PathValue("assetType")
	if assetType != "watermark" && assetType != "outro" && assetType != "intro" {
		writeErr(w, http.StatusBadRequest, "Tipe asset harus watermark, outro, atau intro")
		return
	}

	camp, ok := s.Store.GetCampaign(id)
	if !ok {
		writeErr(w, http.StatusNotFound, "Campaign tidak ditemukan")
		return
	}

	if err := r.ParseMultipartForm(256 << 20); err != nil { // up to 256MB
		writeErr(w, http.StatusBadRequest, "Gagal membaca file upload: "+err.Error())
		return
	}
	file, header, err := r.FormFile("file")
	if err != nil {
		writeErr(w, http.StatusBadRequest, "Field 'file' tidak ditemukan")
		return
	}
	defer file.Close()

	ext := strings.ToLower(filepath.Ext(header.Filename))
	if ext == "" {
		ext = ".bin"
	}
	savedName := fmt.Sprintf("%s_%s_%d%s", id, assetType, time.Now().Unix(), ext)
	destPath := filepath.Join(s.Store.AssetsDir, savedName)

	dst, err := os.Create(destPath)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if _, err := io.Copy(dst, file); err != nil {
		dst.Close()
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	dst.Close()

	switch assetType {
	case "watermark":
		camp.Watermark.Enabled = true
		camp.Watermark.FilePath = destPath
		camp.Watermark.FileName = savedName
	case "outro":
		camp.Outro.Enabled = true
		camp.Outro.FilePath = destPath
		camp.Outro.FileName = savedName
		camp.Outro.KeepRaw = true
		if info, err := renderer.ProbeMedia(r.Context(), s.Deps.GetStatus().FFprobePath, destPath); err == nil {
			camp.Outro.Duration = info.Duration
		}
	case "intro":
		camp.Intro.Enabled = true
		camp.Intro.FilePath = destPath
		camp.Intro.FileName = savedName
		camp.Intro.KeepRaw = true
		if info, err := renderer.ProbeMedia(r.Context(), s.Deps.GetStatus().FFprobePath, destPath); err == nil {
			camp.Intro.Duration = info.Duration
		}
	}

	if err := s.Store.SaveCampaign(camp); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.Broker.Log("campaign", "success", fmt.Sprintf("Upload %s berhasil untuk campaign '%s' (%s)", assetType, camp.Name, header.Filename))
	writeJSON(w, http.StatusOK, camp)
}

func (s *Server) handleServeAsset(w http.ResponseWriter, r *http.Request) {
	name := filepath.Base(r.PathValue("filename"))
	fullPath := filepath.Join(s.Store.AssetsDir, name)
	if _, err := os.Stat(fullPath); err != nil {
		http.NotFound(w, r)
		return
	}
	http.ServeFile(w, r, fullPath)
}

// --- Projects & Multi-Signal Analysis ---

func (s *Server) handleListProjects(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.Store.ListProjects())
}

func (s *Server) handleGetProject(w http.ResponseWriter, r *http.Request) {
	proj, ok := s.Store.GetProject(r.PathValue("id"))
	if !ok {
		writeErr(w, http.StatusNotFound, "Project tidak ditemukan")
		return
	}
	writeJSON(w, http.StatusOK, proj)
}

func (s *Server) handleDeleteProject(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	s.cacheMu.Lock()
	delete(s.rawSignals, id)
	s.cacheMu.Unlock()
	_ = s.Store.DeleteProject(id)
	writeJSON(w, http.StatusOK, map[string]bool{"deleted": true})
}

func (s *Server) handleAnalyzeProject(w http.ResponseWriter, r *http.Request) {
	var req struct {
		URL           string                   `json:"url"`
		CampaignID    string                   `json:"campaignId"`
		DownloadFull  bool                     `json:"downloadFull"`
		IncludeChat   *bool                    `json:"includeChat"`
		IncludeAudio  bool                     `json:"includeAudio"`
		DetectConfig  *model.DetectionSettings `json:"detectConfig"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "Body JSON tidak valid")
		return
	}
	videoURL := strings.TrimSpace(req.URL)
	if videoURL == "" {
		writeErr(w, http.StatusBadRequest, "Link YouTube wajib diisi")
		return
	}
	if !s.Deps.GetStatus().Ready {
		writeErr(w, http.StatusServiceUnavailable, "Dependency belum lengkap (ffmpeg / yt-dlp)")
		return
	}

	videoID := youtube.ExtractVideoID(videoURL)
	projID := fmt.Sprintf("proj-%s-%d", videoID, time.Now().Unix())
	if videoID == "" {
		projID = fmt.Sprintf("proj-%d", time.Now().UnixNano())
	}

	detectCfg := model.DefaultDetectionSettings()
	if req.DetectConfig != nil {
		detectCfg = *req.DetectConfig
	}

	includeChat := true
	if req.IncludeChat != nil {
		includeChat = *req.IncludeChat
	}

	proj := &model.Project{
		ID:            projID,
		VideoID:       videoID,
		SourceURL:     videoURL,
		Title:         "Memindai video YouTube...",
		Status:        "scanning",
		StatusMessage: "Mengambil metadata & Most Replayed Heatmap...",
		CampaignID:    req.CampaignID,
		DetectConfig:  detectCfg,
		CreatedAt:     time.Now(),
		UpdatedAt:     time.Now(),
	}
	if err := s.Store.SaveProject(proj); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.Broker.Publish("project_update", proj)

	// Run multi-signal ingestion concurrently in background so HTTP responds immediately
	go s.runAnalysisPipeline(proj.ID, req.DownloadFull, includeChat, req.IncludeAudio)

	writeJSON(w, http.StatusAccepted, proj)
}

// analyzeSyncFast runs Phase-1 fast scan synchronously (used by Autopilot scheduler & n8n triggers).
func (s *Server) analyzeSyncFast(ctx context.Context, videoURL, campaignID string) (*model.Project, error) {
	videoID := youtube.ExtractVideoID(videoURL)
	projID := fmt.Sprintf("proj-%s-%d", videoID, time.Now().Unix())
	if videoID == "" {
		projID = fmt.Sprintf("proj-%d", time.Now().UnixNano())
	}
	proj := &model.Project{
		ID:            projID,
		VideoID:       videoID,
		SourceURL:     videoURL,
		Title:         "Memindai video YouTube...",
		Status:        "scanning",
		StatusMessage: "Autopilot memindai metadata, heatmap & transkrip...",
		CampaignID:    campaignID,
		DetectConfig:  model.DefaultDetectionSettings(),
		CreatedAt:     time.Now(),
		UpdatedAt:     time.Now(),
	}
	if err := s.Store.SaveProject(proj); err != nil {
		return nil, err
	}
	s.Broker.Publish("project_update", proj)
	s.runAnalysisPipeline(proj.ID, false, false, false)

	res, ok := s.Store.GetProject(proj.ID)
	if !ok || res.Status == "error" {
		msg := "gagal analisis"
		if res != nil {
			msg = res.StatusMessage
		}
		return nil, fmt.Errorf("%s", msg)
	}
	return res, nil
}

func (s *Server) runAnalysisPipeline(projectID string, downloadFull, includeChat, includeAudio bool) {
	ctx := context.Background()
	proj, ok := s.Store.GetProject(projectID)
	if !ok {
		return
	}
	projDir := s.Store.ProjectDir(projectID)
	settings := s.Store.GetSettings()

	update := func(status, msg string) {
		proj.Status = status
		proj.StatusMessage = msg
		_ = s.Store.SaveProject(proj)
		s.Broker.Publish("project_update", proj)
		s.Broker.Log("analyzer", "info", msg)
	}

	// ============ PHASE 1: ULTRA-FAST SCAN (~8-15 detik!) ============
	// Metadata + Heatmap + HD Preview Frame + Indonesian/English Auto-Captions (NO slow blocking live_chat!)
	update("scanning", "Fase 1 (Cepat): Mengambil metadata YouTube, heatmap & chapters...")
	meta, err := s.YT.FetchMetadata(ctx, proj.SourceURL, settings)
	if err != nil {
		update("error", err.Error())
		return
	}

	proj.VideoID = meta.ID
	proj.Title = meta.Title
	proj.Channel = meta.Channel
	proj.ChannelURL = meta.ChannelURL
	proj.ThumbnailURL = meta.Thumbnail
	proj.DurationSec = meta.Duration
	proj.ViewCount = meta.ViewCount
	proj.UploadDate = meta.UploadDate
	proj.IsLiveVOD = meta.WasLive
	proj.Chapters = meta.Chapters
	_ = s.Store.SaveProject(proj)
	s.Broker.Publish("project_update", proj)

	if len(meta.Heatmap) > 0 {
		if b, err := json.Marshal(meta.Heatmap); err == nil {
			_ = os.WriteFile(filepath.Join(projDir, "heatmap.json"), b, 0o644)
		}
	}

	update("scanning", "Mengambil preview frame HD & transkrip ucapan (cepat ~5-10 detik)...")
	var wg sync.WaitGroup
	var subCues []model.TranscriptCue
	var audioEnergy []float64

	// Preview frame grabber
	wg.Add(1)
	go func() {
		defer wg.Done()
		seek := meta.Duration * 0.20
		if len(meta.Heatmap) > 0 {
			bestV := -1.0
			for _, h := range meta.Heatmap {
				if h.StartTime >= 15 && h.Value > bestV {
					bestV = h.Value
					seek = h.StartTime
				}
			}
		}
		framePath := filepath.Join(projDir, "preview.jpg")
		if err := s.YT.GrabPreviewFrame(ctx, meta.StreamURL, meta.Thumbnail, seek, framePath); err == nil {
			proj.PreviewFrame = framePath
		}
	}()

	// Subtitles / Auto-captions (fast ~5s even on 4-hour streams -> enables instant Transcript Hype!)
	if meta.SubLang != "" {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if subPath, err := s.YT.DownloadSubtitles(ctx, proj.SourceURL, projDir, meta.SubLang, meta.IsAutoSub, settings); err == nil && subPath != "" {
				proj.SubtitlePath = subPath
				if cues, err := youtube.ParseSubtitleFile(subPath); err == nil {
					subCues = cues
					s.Broker.Log("analyzer", "success", fmt.Sprintf("Transkrip siap: %d baris ucapan!", len(cues)))
				}
			}
		}()
	}

	wg.Wait()

	// Optional Full Video Download OR Audio-only Peak Analysis
	if downloadFull {
		update("downloading", "Mengunduh video full MP4 (untuk scrub lokal + deteksi audio keras + render instan)...")
		localMP4 := filepath.Join(projDir, "source.mp4")
		err := s.YT.DownloadFullVideo(ctx, proj.SourceURL, localMP4, settings, func(pct float64, line string) {
			if pct >= 0 {
				proj.StatusMessage = fmt.Sprintf("Mengunduh video full (%.0f%%)...", pct)
				s.Broker.Publish("project_update", proj)
			}
		})
		if err == nil {
			proj.LocalVideoPath = localMP4
			_ = s.YT.GrabFrameFromLocalFile(ctx, localMP4, meta.Duration*0.20, filepath.Join(projDir, "preview.jpg"))
			proj.PreviewFrame = filepath.Join(projDir, "preview.jpg")

			update("scanning", "Menganalisis puncak volume & teriakan audio via FFmpeg...")
			if energy, err := analyzer.AnalyzeAudioEnergy(ctx, s.Deps.GetStatus().FFmpegPath, localMP4, meta.Duration); err == nil {
				audioEnergy = energy
				saveAudioCache(projDir, energy)
			}
		} else {
			s.Broker.Log("analyzer", "warn", "Download full gagal, lanjut mode Quick Scan: "+err.Error())
		}
	} else if includeAudio || (len(meta.Heatmap) == 0 && len(subCues) == 0) {
		update("scanning", "Menganalisis puncak audio untuk deteksi teriakan/momen seru...")
		if audioPath, err := s.YT.DownloadAudioOnly(ctx, proj.SourceURL, filepath.Join(projDir, "audio.m4a"), settings); err == nil {
			if energy, err := analyzer.AnalyzeAudioEnergy(ctx, s.Deps.GetStatus().FFmpegPath, audioPath, meta.Duration); err == nil {
				audioEnergy = energy
				saveAudioCache(projDir, energy)
			}
			_ = os.Remove(audioPath)
		}
	}

	// Publish Phase-1 results IMMEDIATELY so user can start previewing & clipping right away!
	res := analyzer.DetectHype(analyzer.AnalysisInput{
		DurationSec: proj.DurationSec,
		Heatmap:     meta.Heatmap,
		AudioEnergy: audioEnergy,
		Transcript:  subCues,
		Chapters:    proj.Chapters,
		Settings:    proj.DetectConfig,
	})

	cs := &cachedSignals{
		Heatmap:     meta.Heatmap,
		AudioEnergy: audioEnergy,
		Transcript:  subCues,
		StreamURL:   meta.StreamURL,
	}
	s.cacheMu.Lock()
	s.rawSignals[projectID] = cs
	s.cacheMu.Unlock()

	proj.Timeline = res.Timeline
	proj.Candidates = res.Candidates
	proj.Signals = res.Signals
	proj.Status = "ready"
	proj.StatusMessage = fmt.Sprintf("Siap! Ditemukan %d momen hits.", len(res.Candidates))
	_ = s.Store.SaveProject(proj)
	s.Broker.Publish("project_update", proj)
	s.Broker.Log("analyzer", "success", fmt.Sprintf("Fase 1 selesai cepat: '%s' -> %d kandidat klip siap preview!", proj.Title, len(res.Candidates)))

	// ============ PHASE 2: NON-BLOCKING BACKGROUND LIVE CHAT (Smart Guard!) ============
	// Live chat replay on viral 4-hour streams (14M+ views) has 10,000+ fragments & takes 20+ mins.
	// So we only auto-download live chat in background for streams <= 90 mins (with strict 90s process-tree kill!).
	if includeChat && meta.HasLiveChat {
		if meta.Duration > 5400 {
			s.Broker.Log("analyzer", "info", fmt.Sprintf("VOD berdurasi %.0f menit (sangat panjang) — Live Chat dilewati otomatis agar tidak lemot; deteksi memakai Heatmap + Transkrip Hype!", meta.Duration/60))
			return
		}
		go func() {
			s.Broker.Log("analyzer", "info", "Fase 2 (Background): Memperkaya skor dengan YouTube Live Chat Replay (maks 90 dtk)...")
			chatCtx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
			defer cancel()
			chatPath, err := s.YT.DownloadLiveChat(chatCtx, proj.SourceURL, projDir, settings)
			if err != nil || chatPath == "" {
				s.Broker.Log("analyzer", "warn", "Background Live Chat selesai/timeout — hasil Fase 1 tetap aktif.")
				return
			}
			msgs, err := youtube.ParseLiveChatFile(chatPath)
			if err != nil || len(msgs) < 20 {
				return
			}
			latestProj, ok := s.Store.GetProject(projectID)
			if !ok {
				return
			}
			latestProj.LiveChatPath = chatPath
			s.cacheMu.Lock()
			if cur, ok := s.rawSignals[projectID]; ok {
				cur.Chats = msgs
			}
			s.cacheMu.Unlock()

			upgraded := analyzer.DetectHype(analyzer.AnalysisInput{
				DurationSec: latestProj.DurationSec,
				Heatmap:     cs.Heatmap,
				Chats:       msgs,
				AudioEnergy: cs.AudioEnergy,
				Transcript:  cs.Transcript,
				Chapters:    latestProj.Chapters,
				Settings:    latestProj.DetectConfig,
			})
			latestProj.Timeline = upgraded.Timeline
			latestProj.Candidates = upgraded.Candidates
			latestProj.Signals = upgraded.Signals
			latestProj.StatusMessage = fmt.Sprintf("Diperkaya dengan %d pesan Live Chat! %d momen hits ditemukan.", len(msgs), len(upgraded.Candidates))
			_ = s.Store.SaveProject(latestProj)
			s.Broker.Publish("project_update", latestProj)
			s.Broker.Log("analyzer", "success", fmt.Sprintf("Fase 2 Live Chat selesai: %d pesan ditambahkan!", len(msgs)))
		}()
	}
}

func (s *Server) loadCachedSignals(proj *model.Project) *cachedSignals {
	s.cacheMu.RLock()
	cs, ok := s.rawSignals[proj.ID]
	s.cacheMu.RUnlock()
	if ok {
		return cs
	}

	// Rehydrate from disk files so re-analysis works even after restart!
	projDir := s.Store.ProjectDir(proj.ID)
	cs = &cachedSignals{}
	if b, err := os.ReadFile(filepath.Join(projDir, "heatmap.json")); err == nil {
		_ = json.Unmarshal(b, &cs.Heatmap)
	}
	if proj.LiveChatPath != "" {
		if msgs, err := youtube.ParseLiveChatFile(proj.LiveChatPath); err == nil {
			cs.Chats = msgs
		}
	}
	if proj.SubtitlePath != "" {
		if cues, err := youtube.ParseSubtitleFile(proj.SubtitlePath); err == nil {
			cs.Transcript = cues
		}
	}
	if b, err := os.ReadFile(filepath.Join(projDir, "audio_energy.json")); err == nil {
		_ = json.Unmarshal(b, &cs.AudioEnergy)
	}

	s.cacheMu.Lock()
	s.rawSignals[proj.ID] = cs
	s.cacheMu.Unlock()
	return cs
}

func (s *Server) handleReanalyzeProject(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	proj, ok := s.Store.GetProject(id)
	if !ok {
		writeErr(w, http.StatusNotFound, "Project tidak ditemukan")
		return
	}
	var cfg model.DetectionSettings
	if err := json.NewDecoder(r.Body).Decode(&cfg); err != nil {
		writeErr(w, http.StatusBadRequest, "Format settings tidak valid")
		return
	}

	cs := s.loadCachedSignals(proj)
	res := analyzer.DetectHype(analyzer.AnalysisInput{
		DurationSec: proj.DurationSec,
		Heatmap:     cs.Heatmap,
		Chats:       cs.Chats,
		AudioEnergy: cs.AudioEnergy,
		Transcript:  cs.Transcript,
		Chapters:    proj.Chapters,
		Settings:    cfg,
	})

	proj.DetectConfig = cfg
	proj.Timeline = res.Timeline
	proj.Candidates = res.Candidates
	proj.Signals = res.Signals
	proj.StatusMessage = fmt.Sprintf("Re-analisis selesai: %d momen terdeteksi.", len(res.Candidates))
	_ = s.Store.SaveProject(proj)
	s.Broker.Publish("project_update", proj)
	writeJSON(w, http.StatusOK, proj)
}

func (s *Server) handleDownloadFull(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	proj, ok := s.Store.GetProject(id)
	if !ok {
		writeErr(w, http.StatusNotFound, "Project tidak ditemukan")
		return
	}
	proj.Status = "downloading"
	proj.StatusMessage = "Memulai download video full MP4..."
	_ = s.Store.SaveProject(proj)
	s.Broker.Publish("project_update", proj)

	go func() {
		ctx := context.Background()
		projDir := s.Store.ProjectDir(proj.ID)
		localMP4 := filepath.Join(projDir, "source.mp4")
		settings := s.Store.GetSettings()

		err := s.YT.DownloadFullVideo(ctx, proj.SourceURL, localMP4, settings, func(pct float64, line string) {
			if pct >= 0 {
				proj.StatusMessage = fmt.Sprintf("Downloading video full (%.0f%%)...", pct)
				s.Broker.Publish("project_update", proj)
			}
		})
		if err != nil {
			proj.Status = "ready"
			proj.StatusMessage = "Download full gagal: " + err.Error()
			_ = s.Store.SaveProject(proj)
			s.Broker.Publish("project_update", proj)
			return
		}

		proj.LocalVideoPath = localMP4
		proj.Status = "scanning"
		proj.StatusMessage = "Video selesai diunduh! Menganalisis puncak audio (teriakan/tawa)..."
		_ = s.Store.SaveProject(proj)
		s.Broker.Publish("project_update", proj)

		cs := s.loadCachedSignals(proj)
		if energy, err := analyzer.AnalyzeAudioEnergy(ctx, s.Deps.GetStatus().FFmpegPath, localMP4, proj.DurationSec); err == nil {
			cs.AudioEnergy = energy
			saveAudioCache(projDir, energy)
		}

		res := analyzer.DetectHype(analyzer.AnalysisInput{
			DurationSec: proj.DurationSec,
			Heatmap:     cs.Heatmap,
			Chats:       cs.Chats,
			AudioEnergy: cs.AudioEnergy,
			Transcript:  cs.Transcript,
			Chapters:    proj.Chapters,
			Settings:    proj.DetectConfig,
		})
		proj.Timeline = res.Timeline
		proj.Candidates = res.Candidates
		proj.Signals = res.Signals
		proj.Status = "ready"
		proj.StatusMessage = fmt.Sprintf("Full video + Audio Hype siap! %d momen terdeteksi.", len(res.Candidates))
		_ = s.Store.SaveProject(proj)
		s.Broker.Publish("project_update", proj)
	}()

	writeJSON(w, http.StatusAccepted, proj)
}

func (s *Server) handleGrabFrame(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	proj, ok := s.Store.GetProject(id)
	if !ok {
		writeErr(w, http.StatusNotFound, "Project tidak ditemukan")
		return
	}
	var req struct {
		SeekSec float64 `json:"seekSec"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)

	framePath := filepath.Join(s.Store.ProjectDir(proj.ID), "preview.jpg")
	if proj.LocalVideoPath != "" {
		if err := s.YT.GrabFrameFromLocalFile(r.Context(), proj.LocalVideoPath, req.SeekSec, framePath); err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
	} else {
		cs := s.loadCachedSignals(proj)
		streamURL := cs.StreamURL
		if streamURL == "" {
			if meta, err := s.YT.FetchMetadata(r.Context(), proj.SourceURL, s.Store.GetSettings()); err == nil {
				streamURL = meta.StreamURL
				cs.StreamURL = streamURL
			}
		}
		if err := s.YT.GrabPreviewFrame(r.Context(), streamURL, proj.ThumbnailURL, req.SeekSec, framePath); err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
	}
	proj.PreviewFrame = framePath
	_ = s.Store.SaveProject(proj)
	writeJSON(w, http.StatusOK, map[string]string{
		"url": fmt.Sprintf("/api/projects/%s/preview-frame?t=%d", proj.ID, time.Now().UnixNano()),
	})
}

func (s *Server) handleServePreviewFrame(w http.ResponseWriter, r *http.Request) {
	proj, ok := s.Store.GetProject(r.PathValue("id"))
	if !ok || proj.PreviewFrame == "" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	http.ServeFile(w, r, proj.PreviewFrame)
}

func (s *Server) handleServeProjectVideo(w http.ResponseWriter, r *http.Request) {
	proj, ok := s.Store.GetProject(r.PathValue("id"))
	if !ok || proj.LocalVideoPath == "" {
		http.NotFound(w, r)
		return
	}
	http.ServeFile(w, r, proj.LocalVideoPath)
}

// --- Render Jobs ---

func (s *Server) handleListJobs(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.Store.ListJobs())
}

func (s *Server) handleRenderSingle(w http.ResponseWriter, r *http.Request) {
	var opts model.RenderOptions
	if err := json.NewDecoder(r.Body).Decode(&opts); err != nil {
		writeErr(w, http.StatusBadRequest, "Format render options tidak valid")
		return
	}
	job, err := s.Worker.Enqueue(opts)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, job)
}

func (s *Server) handleRenderBatch(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Items []model.RenderOptions `json:"items"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || len(req.Items) == 0 {
		writeErr(w, http.StatusBadRequest, "Minimal 1 klip diperlukan untuk batch auto-clip")
		return
	}
	var jobs []*model.RenderJob
	for _, item := range req.Items {
		job, err := s.Worker.Enqueue(item)
		if err != nil {
			continue
		}
		jobs = append(jobs, job)
	}
	writeJSON(w, http.StatusAccepted, jobs)
}

func (s *Server) handleCancelJob(w http.ResponseWriter, r *http.Request) {
	s.Worker.Cancel(r.PathValue("id"))
	writeJSON(w, http.StatusOK, map[string]bool{"canceled": true})
}

func (s *Server) handleRetryJob(w http.ResponseWriter, r *http.Request) {
	oldJob, ok := s.Store.GetJob(r.PathValue("id"))
	if !ok {
		writeErr(w, http.StatusNotFound, "Job tidak ditemukan")
		return
	}
	newJob, err := s.Worker.Enqueue(oldJob.Options)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, newJob)
}

func (s *Server) handleDeleteJob(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	s.Worker.Cancel(id)
	_ = s.Store.DeleteJob(id)
	writeJSON(w, http.StatusOK, map[string]bool{"deleted": true})
}

func (s *Server) handleServeJobVideo(w http.ResponseWriter, r *http.Request) {
	job, ok := s.Store.GetJob(r.PathValue("id"))
	if !ok || job.OutputPath == "" {
		http.NotFound(w, r)
		return
	}
	if r.URL.Query().Get("download") == "1" {
		w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, job.OutputFile))
	}
	http.ServeFile(w, r, job.OutputPath)
}

func saveAudioCache(projDir string, energy []float64) {
	if b, err := json.Marshal(energy); err == nil {
		_ = os.WriteFile(filepath.Join(projDir, "audio_energy.json"), b, 0o644)
	}
}

// --- Audio Library (BGM & SFX) ---

func (s *Server) handleListAudio(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.Audio.List())
}

func (s *Server) handleUploadAudio(w http.ResponseWriter, r *http.Request) {
	kind := r.PathValue("kind")
	if kind != "bgm" && kind != "sfx" {
		writeErr(w, http.StatusBadRequest, "Tipe audio harus bgm atau sfx")
		return
	}
	if err := r.ParseMultipartForm(64 << 20); err != nil {
		writeErr(w, http.StatusBadRequest, "Gagal membaca upload: "+err.Error())
		return
	}
	file, header, err := r.FormFile("file")
	if err != nil {
		writeErr(w, http.StatusBadRequest, "Field 'file' tidak ditemukan")
		return
	}
	defer file.Close()

	name := r.FormValue("name")
	category := r.FormValue("category")
	asset, err := s.Audio.AddUpload(kind, name, category, header.Filename, file, 0)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if info, err := renderer.ProbeMedia(r.Context(), s.Deps.GetStatus().FFprobePath, asset.FilePath); err == nil && info.Duration > 0 {
		asset.Duration = info.Duration
	}
	s.Broker.Log("audio", "success", fmt.Sprintf("Upload %s berhasil: %s", strings.ToUpper(kind), asset.Name))
	writeJSON(w, http.StatusCreated, asset)
}

func (s *Server) handleDeleteAudio(w http.ResponseWriter, r *http.Request) {
	if err := s.Audio.Delete(r.PathValue("id")); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"deleted": true})
}

func (s *Server) handleServeAudio(w http.ResponseWriter, r *http.Request) {
	name := filepath.Base(r.PathValue("filename"))
	fullPath := filepath.Join(s.Audio.AudioDir, name)
	if _, err := os.Stat(fullPath); err != nil {
		http.NotFound(w, r)
		return
	}
	http.ServeFile(w, r, fullPath)
}

func (s *Server) handleRegenerateAudio(w http.ResponseWriter, r *http.Request) {
	s.Audio.EnsureBuiltIns()
	writeJSON(w, http.StatusOK, s.Audio.List())
}

// --- Human-in-the-Loop (HITL) Review & Autopilot Scheduler ---

func (s *Server) handleReviewJob(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	job, ok := s.Store.GetJob(id)
	if !ok {
		writeErr(w, http.StatusNotFound, "Job tidak ditemukan")
		return
	}
	var req struct {
		ReviewStatus       string `json:"reviewStatus"` // approved, rejected, pending_review
		ReviewNote         string `json:"reviewNote"`
		PostCaption        string `json:"postCaption"`
		DeleteFileOnReject bool   `json:"deleteFileOnReject"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "Format JSON review tidak valid")
		return
	}
	if req.ReviewStatus != "approved" && req.ReviewStatus != "rejected" && req.ReviewStatus != "pending_review" {
		writeErr(w, http.StatusBadRequest, "reviewStatus harus approved, rejected, atau pending_review")
		return
	}
	job.ReviewStatus = req.ReviewStatus
	if req.ReviewNote != "" {
		job.ReviewNote = req.ReviewNote
	}
	if strings.TrimSpace(req.PostCaption) != "" {
		job.PostCaption = req.PostCaption
	}
	if req.ReviewStatus == "rejected" && req.DeleteFileOnReject && job.OutputPath != "" {
		_ = os.Remove(job.OutputPath)
		job.OutputPath = ""
	}
	if err := s.Store.SaveJob(job); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.Broker.Publish("job_update", job)
	s.Broker.Log("review", "success", fmt.Sprintf("Human Review [%s]: %s", strings.ToUpper(req.ReviewStatus), job.Title))

	// Fire optional n8n Approve Webhook when human approves a clip!
	if req.ReviewStatus == "approved" && job.AutopilotID != "" {
		if rule, ok := s.Store.GetAutopilot(job.AutopilotID); ok && rule.ApproveWebhookURL != "" {
			autopilot.FireApproveWebhook(rule.ApproveWebhookURL, job)
		}
	}
	writeJSON(w, http.StatusOK, job)
}

func (s *Server) handleListAutopilots(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.Store.ListAutopilots())
}

func (s *Server) handleCreateAutopilot(w http.ResponseWriter, r *http.Request) {
	var rule model.AutopilotRule
	if err := json.NewDecoder(r.Body).Decode(&rule); err != nil {
		writeErr(w, http.StatusBadRequest, "Format JSON autopilot tidak valid")
		return
	}
	if strings.TrimSpace(rule.Name) == "" || strings.TrimSpace(rule.ChannelURL) == "" {
		writeErr(w, http.StatusBadRequest, "Nama rule & Channel URL wajib diisi")
		return
	}
	if rule.ID == "" {
		rule.ID = fmt.Sprintf("auto-%d", time.Now().UnixNano())
	}
	if err := s.Store.SaveAutopilot(&rule); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.Broker.Publish("autopilot_update", &rule)
	writeJSON(w, http.StatusCreated, rule)
}

func (s *Server) handleUpdateAutopilot(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	ex, ok := s.Store.GetAutopilot(id)
	if !ok {
		writeErr(w, http.StatusNotFound, "Autopilot rule tidak ditemukan")
		return
	}
	var updated model.AutopilotRule
	if err := json.NewDecoder(r.Body).Decode(&updated); err != nil {
		writeErr(w, http.StatusBadRequest, "Format JSON tidak valid")
		return
	}
	updated.ID = ex.ID
	if len(updated.ProcessedVideoIDs) == 0 && len(ex.ProcessedVideoIDs) > 0 {
		updated.ProcessedVideoIDs = ex.ProcessedVideoIDs
	}
	if updated.Enabled && updated.NextRunAt == nil {
		next := time.Now().Add(time.Duration(updated.IntervalMinutes) * time.Minute)
		updated.NextRunAt = &next
	}
	if err := s.Store.SaveAutopilot(&updated); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.Broker.Publish("autopilot_update", &updated)
	writeJSON(w, http.StatusOK, updated)
}

func (s *Server) handleDeleteAutopilot(w http.ResponseWriter, r *http.Request) {
	_ = s.Store.DeleteAutopilot(r.PathValue("id"))
	writeJSON(w, http.StatusOK, map[string]bool{"deleted": true})
}

func (s *Server) handleRunAutopilot(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	rule, ok := s.Store.GetAutopilot(id)
	if !ok {
		writeErr(w, http.StatusNotFound, "Autopilot rule tidak ditemukan")
		return
	}
	go s.Autopilot.RunRule(id)
	writeJSON(w, http.StatusAccepted, map[string]interface{}{
		"started": true,
		"rule":    rule,
	})
}
