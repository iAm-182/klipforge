package server

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"klipforge/internal/analyzer"
	"klipforge/internal/audiofx"
	"klipforge/internal/autopilot"
	"klipforge/internal/deps"
	"klipforge/internal/events"
	"klipforge/internal/model"
	"klipforge/internal/notify"
	"klipforge/internal/renderer"
	"klipforge/internal/store"
	"klipforge/internal/youtube"
	"klipforge/web"
)

// Server wires all REST endpoints, SSE streaming, media serving, and embedded dashboard UI.
type Server struct {
	Store       *store.Store
	Deps        *deps.Manager
	YT          *youtube.Client
	Audio       *audiofx.Library
	Broker      *events.Broker
	Worker      *renderer.WorkerPool
	Autopilot   *autopilot.Scheduler
	EngineToken string
	AgentKey    string

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

func loadOrCreateSecretFile(baseDir, envName, fileName, label string) string {
	if envVal := strings.TrimSpace(os.Getenv(envName)); envVal != "" {
		return envVal
	}
	p := filepath.Join(baseDir, fileName)
	if b, err := os.ReadFile(p); err == nil {
		if tok := strings.TrimSpace(string(b)); len(tok) >= 16 {
			return tok
		}
	}
	buf := make([]byte, 24)
	if _, err := rand.Read(buf); err != nil {
		return ""
	}
	tok := hex.EncodeToString(buf)
	_ = os.WriteFile(p, []byte(tok), 0o600)
	log.Printf("[Security] Generated new %s in %s", label, p)
	return tok
}

func New(st *store.Store, d *deps.Manager, broker *events.Broker) *Server {
	yt := youtube.NewClient(d)
	audioLib := audiofx.NewLibrary(st.DataDir, d)
	wp := renderer.NewWorkerPool(st, d, yt, audioLib, broker)
	engTok := loadOrCreateSecretFile(st.BaseDir, "KLIPFORGE_ENGINE_TOKEN", ".engine-token", "Engine Token")
	agentKey := loadOrCreateSecretFile(st.BaseDir, "KLIPFORGE_AGENT_KEY", ".agent-key", "Agent Key")
	s := &Server{
		Store:       st,
		Deps:        d,
		YT:          yt,
		Audio:       audioLib,
		Broker:      broker,
		Worker:      wp,
		EngineToken: engTok,
		AgentKey:    agentKey,
		rawSignals:  make(map[string]*cachedSignals),
	}
	s.Autopilot = autopilot.NewScheduler(st, yt, wp, broker, s.analyzeSyncFast)
	broker.SetActivitySink(func(source, level, message string) {
		if strings.TrimSpace(message) == "" {
			return
		}
		st.AppendActivity(model.ActivityEntry{
			ActorName:  "Engine",
			ActorVia:   "system",
			Action:     source,
			Summary:    message,
			Level:      level,
		})
	})
	go s.recoverIncompleteProjects()
	s.syncTeamToEdgeAsync()
	if wh := strings.TrimSpace(st.GetSettings().NotifyWebhookURL); wh != "" {
		go notify.Send(wh, "engine_start", "🟢 KlipForge Engine Menyala", "Engine lokal siap menerima job & auto-catchup Autopilot aktif.")
	}
	return s
}

func (s *Server) campaignForbiddenWords(campaignID string) []string {
	if campaignID == "" {
		return nil
	}
	if camp, ok := s.Store.GetCampaign(campaignID); ok {
		return camp.ForbiddenWords
	}
	return nil
}

// recoverIncompleteProjects finishes cached projects and upgrades older projects with TranscriptHype curve & unique hooks.
func (s *Server) recoverIncompleteProjects() {
	for _, p := range s.Store.ListProjects() {
		dupHooks := 0
		if len(p.Candidates) >= 3 {
			firstHook := p.Candidates[0].HookText
			for _, c := range p.Candidates {
				if c.HookText == firstHook && firstHook != "" {
					dupHooks++
				}
			}
		}
		needsUpgrade := len(p.Candidates) == 0 || (p.Signals.HasSubtitles && !p.Signals.HasTranscriptHype) || dupHooks >= 3
		if !needsUpgrade {
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
					DurationSec:    p.DurationSec,
					Heatmap:        cs.Heatmap,
					Chats:          cs.Chats,
					AudioEnergy:    cs.AudioEnergy,
					Transcript:     cs.Transcript,
					Chapters:       p.Chapters,
					Settings:       p.DetectConfig,
					ForbiddenWords: s.campaignForbiddenWords(p.CampaignID),
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

	// Health, Auth, Storage, SSE & Settings
	mux.HandleFunc("GET /api/auth/check", s.handleAuthCheck)
	mux.HandleFunc("POST /api/auth/unlock", s.handleAuthUnlock)
	mux.HandleFunc("GET /api/auth/me", s.handleAuthMe)
	mux.HandleFunc("GET /api/status", s.handleStatus)
	mux.HandleFunc("POST /api/deps/refresh", s.handleRefreshDeps)
	mux.HandleFunc("POST /api/deps/update-ytdlp", s.handleUpdateYtDlp)
	mux.HandleFunc("POST /api/storage/cleanup", s.handleStorageCleanup)
	mux.Handle("GET /api/events", s.Broker)
	mux.HandleFunc("GET /api/settings", s.handleGetSettings)
	mux.HandleFunc("PUT /api/settings", s.handleSaveSettings)
	mux.HandleFunc("POST /api/settings/cookies", s.handleUploadCookiesFile)
	mux.HandleFunc("POST /api/open-output-folder", s.handleOpenOutputFolder)

	// Team Mode Management (Owner-only: personal AccessKey + personal Muse AgentKey per member)
	mux.HandleFunc("GET /api/team", s.handleListTeam)
	mux.HandleFunc("GET /api/team/metrics", s.handleTeamMetrics)
	mux.HandleFunc("POST /api/team", s.handleCreateTeamMember)
	mux.HandleFunc("PUT /api/team/{id}", s.handleUpdateTeamMember)
	mux.HandleFunc("POST /api/team/{id}/rotate", s.handleRotateTeamKeys)
	mux.HandleFunc("DELETE /api/team/{id}", s.handleDeleteTeamMember)
	mux.HandleFunc("POST /api/team/sync-edge", s.handleSyncTeamEdge)

	// Kanban Board, Activity Feed & Home Metrics (B1)
	mux.HandleFunc("GET /api/activity", s.handleListActivity)
	mux.HandleFunc("GET /api/metrics/home", s.handleHomeMetrics)
	mux.HandleFunc("GET /api/board", s.handleGetBoard)
	mux.HandleFunc("POST /api/board/move", s.handleBoardMove)

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
	mux.HandleFunc("GET /api/projects/{id}/search", s.handleSearchTranscript)
	mux.HandleFunc("POST /api/projects/{id}/scan-audio", s.handleScanAudioOnly)
	mux.HandleFunc("POST /api/projects/{id}/download-full", s.handleDownloadFull)
	mux.HandleFunc("POST /api/projects/{id}/grab-frame", s.handleGrabFrame)
	mux.HandleFunc("POST /api/projects/{id}/verify-frames", s.handleVerifyCandidateFrames)
	mux.HandleFunc("POST /api/projects/{id}/detect-scenes", s.handleDetectProjectScenes)
	mux.HandleFunc("POST /api/projects/{id}/diarize", s.handleDiarizeProject)
	mux.HandleFunc("POST /api/projects/{id}/candidates/{candId}/verdict", s.handleCandidateVerdict)
	mux.HandleFunc("GET /api/projects/{id}/candidate-frame/{candId}/{kind}", s.handleServeCandidateFrame)
	mux.HandleFunc("GET /api/projects/{id}/preview-frame", s.handleServePreviewFrame)
	mux.HandleFunc("GET /api/projects/{id}/video", s.handleServeProjectVideo)

	// Render Jobs, Clips & Human-in-the-Loop Review
	mux.HandleFunc("GET /api/jobs", s.handleListJobs)
	mux.HandleFunc("GET /api/jobs/{id}", s.handleGetJob)
	mux.HandleFunc("POST /api/jobs/render", s.handleRenderSingle)
	mux.HandleFunc("POST /api/jobs/batch", s.handleRenderBatch)
	mux.HandleFunc("POST /api/jobs/compile", s.handleRenderCompile)
	mux.HandleFunc("POST /api/jobs/edit-script", s.handleEditScript)
	mux.HandleFunc("POST /api/jobs/{id}/post-edit", s.handlePostEditJob)
	mux.HandleFunc("POST /api/jobs/{id}/review", s.handleReviewJob)
	mux.HandleFunc("POST /api/jobs/review/batch", s.handleBatchReviewJobs)
	mux.HandleFunc("GET /api/jobs/{id}/peek", s.handleJobPeek)
	mux.HandleFunc("GET /api/jobs/{id}/cover", s.handleServeJobCover)
	mux.HandleFunc("POST /api/jobs/{id}/draft", s.handleExportJianyingDraft)
	mux.HandleFunc("GET /api/jobs/{id}/draft/download", s.handleDownloadJianyingDraft)
	mux.HandleFunc("POST /api/jobs/{id}/publish-kit", s.handleGeneratePublishKit)
	mux.HandleFunc("POST /api/jobs/{id}/publish", s.handlePublishJob)
	mux.HandleFunc("POST /api/jobs/{id}/analytics/sync", s.handleSyncJobAnalytics)
	mux.HandleFunc("POST /api/jobs/{id}/cancel", s.handleCancelJob)

	// Publish Accounts Management (Owner-only OAuth / SAU session configs)
	mux.HandleFunc("GET /api/publish/accounts", s.handleListPublishAccounts)
	mux.HandleFunc("POST /api/publish/accounts", s.handleSavePublishAccount)
	mux.HandleFunc("DELETE /api/publish/accounts/{id}", s.handleDeletePublishAccount)
	mux.HandleFunc("POST /api/jobs/{id}/retry", s.handleRetryJob)
	mux.HandleFunc("DELETE /api/jobs/{id}", s.handleDeleteJob)
	mux.HandleFunc("GET /api/jobs/{id}/video", s.handleServeJobVideo)

	// Autonomous Scheduler / Autopilot (n8n-style HITL workflow)
	mux.HandleFunc("GET /api/autopilot", s.handleListAutopilots)
	mux.HandleFunc("POST /api/autopilot", s.handleCreateAutopilot)
	mux.HandleFunc("PUT /api/autopilot/{id}", s.handleUpdateAutopilot)
	mux.HandleFunc("DELETE /api/autopilot/{id}", s.handleDeleteAutopilot)
	mux.HandleFunc("POST /api/autopilot/{id}/run", s.handleRunAutopilot)

	// Audio Library (36+ SFX & 15+ BGM starter pack + custom uploads + Smart Presets)
	mux.HandleFunc("GET /api/audio", s.handleListAudio)
	mux.HandleFunc("POST /api/audio/upload/{kind}", s.handleUploadAudio)
	mux.HandleFunc("DELETE /api/audio/{id}", s.handleDeleteAudio)
	mux.HandleFunc("GET /api/audio/file/{filename}", s.handleServeAudio)
	mux.HandleFunc("POST /api/audio/regenerate", s.handleRegenerateAudio)
	mux.HandleFunc("GET /api/audio/presets", s.handleListAudioPresets)
	mux.HandleFunc("POST /api/audio/presets", s.handleSaveAudioPreset)
	mux.HandleFunc("DELETE /api/audio/presets/{id}", s.handleDeleteAudioPreset)
	mux.HandleFunc("GET /api/audio/trending", s.handleListTrendingAudio)
	mux.HandleFunc("POST /api/audio/trending/import", s.handleImportTrendingAudio)
	mux.HandleFunc("GET /api/audio/moment-map", s.handleGetMomentEditMap)

	// Embedded Web Dashboard UI
	subFS, _ := fs.Sub(web.FS, ".")
	fileServer := http.FileServer(http.FS(subFS))
	mux.Handle("/", fileServer)

	return s.corsMiddleware(s.authMiddleware(mux))
}

func constantTimeMatch(expected, provided string) bool {
	if expected == "" {
		return false
	}
	p := strings.TrimSpace(provided)
	if len(p) != len(expected) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(p), []byte(expected)) == 1
}

func (s *Server) isValidToken(provided string) bool {
	if s.EngineToken == "" {
		return true
	}
	return constantTimeMatch(s.EngineToken, provided)
}

func (s *Server) isValidAgentKey(provided string) bool {
	return constantTimeMatch(s.AgentKey, provided)
}

// authRole resolves request privilege: "admin" (EngineToken), "agent" (scoped X-Agent-Key), or "" (unauthorized).
func (s *Server) authRole(r *http.Request) string {
	if s.EngineToken == "" {
		return "admin"
	}
	if s.isValidToken(r.Header.Get("X-Engine-Token")) {
		return "admin"
	}
	if ck, err := r.Cookie("kf_token"); err == nil && s.isValidToken(ck.Value) {
		return "admin"
	}
	if q := r.URL.Query().Get("token"); q != "" && s.isValidToken(q) {
		return "admin"
	}
	if ak := r.Header.Get("X-Agent-Key"); ak != "" && s.isValidAgentKey(ak) {
		return "agent"
	}
	return ""
}

// isAgentAllowed enforces least-privilege scope for assistant agents (X-Agent-Key):
// ALLOWED: Read (GET), Transcript Search, Analyze/Reanalyze/Scan-Audio/Grab-Frame, Render/Compile/Post-Edit/Retry.
// DENIED (403): ALL DELETE, ALL PUT (settings/campaigns/autopilot), storage cleanup, uploads, review/reject deletes.
func isAgentAllowed(method, path string) bool {
	if method == http.MethodGet || method == http.MethodHead {
		return true
	}
	if method != http.MethodPost {
		return false
	}
	allowedExact := map[string]bool{
		"/api/projects/analyze":      true,
		"/api/jobs/render":           true,
		"/api/jobs/batch":            true,
		"/api/jobs/compile":          true,
		"/api/jobs/edit-script":      true,
		"/api/audio/trending/import": true,
		"/api/board/move":            true,
	}
	if allowedExact[path] {
		return true
	}
	if strings.HasPrefix(path, "/api/projects/") {
		if strings.HasSuffix(path, "/reanalyze") ||
			strings.HasSuffix(path, "/scan-audio") ||
			strings.HasSuffix(path, "/verify-frames") ||
			strings.HasSuffix(path, "/grab-frame") ||
			strings.HasSuffix(path, "/detect-scenes") ||
			strings.HasSuffix(path, "/diarize") ||
			(strings.Contains(path, "/candidates/") && strings.HasSuffix(path, "/verdict")) {
			return true
		}
	}
	if strings.HasPrefix(path, "/api/jobs/") {
		if strings.HasSuffix(path, "/post-edit") ||
			strings.HasSuffix(path, "/retry") ||
			strings.HasSuffix(path, "/publish-kit") ||
			strings.HasSuffix(path, "/publish") ||
			strings.HasSuffix(path, "/draft") ||
			strings.HasSuffix(path, "/analytics/sync") {
			return true
		}
	}
	return false
}

func (s *Server) authMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodOptions {
			next.ServeHTTP(w, r)
			return
		}
		// Protect EVERY /api/* route with Owner EngineToken, Team AccessKey, or Scoped AgentKey (Owner/Member Muse)
		if strings.HasPrefix(r.URL.Path, "/api/") &&
			r.URL.Path != "/api/auth/unlock" &&
			r.URL.Path != "/api/auth/check" {
			principal, ok := s.resolvePrincipal(r)
			if !ok {
				writeErr(w, http.StatusUnauthorized, "Unauthorized: Access Key, Engine Token, atau Agent Key diperlukan")
				return
			}
			if !isRoleAllowed(principal, r.Method, r.URL.Path) {
				if principal.Role == "agent" {
					writeErr(w, http.StatusForbidden, "Forbidden: X-Agent-Key hanya diizinkan untuk baca, cari transkrip, analisis, dan render (tidak boleh hapus/ubah config/review/tim)")
				} else {
					writeErr(w, http.StatusForbidden, fmt.Sprintf("Forbidden: Role '%s' (%s) tidak memiliki izin untuk aksi ini", principal.Role, principal.Name))
				}
				return
			}
			if principal.ID != "" && principal.ID != "owner" && principal.ID != "owner-agent" {
				go s.Store.TouchTeamMemberLastSeen(principal.ID)
			}
			r = r.WithContext(withPrincipal(r.Context(), principal))
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) corsMiddleware(next http.Handler) http.Handler {
	allowedOrigins := map[string]bool{
		"https://klip.breakthematrix.id": true,
		"http://127.0.0.1:8787":          true,
		"http://localhost:8787":          true,
	}
	if extra := strings.TrimSpace(os.Getenv("KLIPFORGE_EXTRA_ORIGIN")); extra != "" {
		for _, o := range strings.Split(extra, ",") {
			if t := strings.TrimSpace(o); t != "" {
				allowedOrigins[t] = true
			}
		}
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if origin != "" && allowedOrigins[origin] {
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Vary", "Origin")
			w.Header().Set("Access-Control-Allow-Credentials", "true")
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, X-Engine-Token, X-Access-Key, X-Agent-Key")
		}
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

// --- Auth, Status, Storage & Settings ---

func (s *Server) handleAuthCheck(w http.ResponseWriter, r *http.Request) {
	p, ok := s.resolvePrincipal(r)
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"tokenRequired": s.EngineToken != "",
		"authenticated": ok,
		"role":          p.Role,
		"user":          p,
	})
}

func (s *Server) handleAuthMe(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, getPrincipal(r))
}

func (s *Server) handleAuthUnlock(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "Format JSON tidak valid")
		return
	}
	tok := strings.TrimSpace(req.Token)
	if s.isValidToken(tok) {
		http.SetCookie(w, &http.Cookie{
			Name:     "kf_token",
			Value:    s.EngineToken,
			Path:     "/",
			HttpOnly: true,
			SameSite: http.SameSiteStrictMode,
			MaxAge:   86400 * 30,
		})
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"ok":   true,
			"role": "owner",
			"user": model.Principal{ID: "owner", Name: "Owner", Role: "owner", Via: "token"},
		})
		return
	}
	if m, ok := s.Store.FindTeamByAccessKey(tok); ok {
		http.SetCookie(w, &http.Cookie{
			Name:     "kf_access_key",
			Value:    m.AccessKey,
			Path:     "/",
			HttpOnly: true,
			SameSite: http.SameSiteStrictMode,
			MaxAge:   86400 * 30,
		})
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"ok":   true,
			"role": m.Role,
			"user": model.Principal{ID: m.ID, Name: m.Name, Role: m.Role, Via: "web", MaxActive: m.MaxActive},
		})
		return
	}
	writeErr(w, http.StatusUnauthorized, "Engine Token atau Team Access Key salah")
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	p := getPrincipal(r)
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"deps":     s.Deps.GetStatus(),
		"settings": s.Store.GetSettings(),
		"disk":     s.Store.DiskStats(),
		"user":     p,
		"security": map[string]interface{}{
			"engineTokenEnabled": s.EngineToken != "",
			"agentKeyEnabled":    s.AgentKey != "",
			"teamCount":          len(s.Store.ListTeam()),
			"role":               p.Role,
		},
		"campaigns": len(s.Store.ListCampaigns()),
		"projects":  len(s.Store.ListProjects()),
		"jobs":      len(s.Store.ListJobs()),
	})
}

func (s *Server) handleRefreshDeps(w http.ResponseWriter, r *http.Request) {
	st := s.Deps.Refresh()
	writeJSON(w, http.StatusOK, st)
}

func (s *Server) handleUpdateYtDlp(w http.ResponseWriter, r *http.Request) {
	s.Broker.Log("system", "info", "Memeriksa update yt-dlp terbaru (yt-dlp -U)...")
	oldVer, newVer, out, err := s.Deps.UpdateYtDlp(r.Context())
	if err != nil {
		s.Broker.Log("system", "warn", fmt.Sprintf("Update yt-dlp selesai dengan catatan: %s", out))
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"ok":         false,
			"oldVersion": oldVer,
			"newVersion": newVer,
			"output":     out,
			"error":      err.Error(),
			"deps":       s.Deps.GetStatus(),
		})
		return
	}
	s.Broker.Log("system", "success", fmt.Sprintf("yt-dlp up-to-date: %s -> %s", oldVer, newVer))
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"ok":         true,
		"oldVersion": oldVer,
		"newVersion": newVer,
		"output":     out,
		"deps":       s.Deps.GetStatus(),
	})
}

func (s *Server) handleStorageCleanup(w http.ResponseWriter, r *http.Request) {
	var req struct {
		OlderThanDays int `json:"olderThanDays"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	deleted, freed := s.Store.CleanupStorage(req.OlderThanDays)
	s.Broker.Log("system", "success", fmt.Sprintf("Pembersihan storage selesai: %d item dihapus (%.1f MB bebas).", deleted, float64(freed)/(1024*1024)))
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"deleted":    deleted,
		"freedBytes": freed,
		"disk":       s.Store.DiskStats(),
	})
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
		// Auto-detect full-frame 9:16 transparent template overlay (e.g. official MotionKlip 1080x1920 PNG!)
		if info, err := renderer.ProbeMedia(r.Context(), s.Deps.GetStatus().FFprobePath, destPath); err == nil {
			if info.Height > 0 && info.Width > 0 && float64(info.Height)/float64(info.Width) >= 1.4 {
				camp.Watermark.Position = "full-frame"
				camp.Watermark.Scale = 1.0
				camp.Watermark.Opacity = 1.0
				camp.Watermark.MarginPx = 0
				s.Broker.Log("campaign", "info", fmt.Sprintf("Watermark Full-Frame 9:16 (%dx%d) terdeteksi — otomatis pakai mode Full-Frame 1:1 presisi!", info.Width, info.Height))
			}
		}
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

func (s *Server) enrichProjectAutoEdit(proj *model.Project) {
	if proj == nil || s.Audio == nil || len(proj.Candidates) == 0 {
		return
	}
	cands := make([]model.ClipCandidate, len(proj.Candidates))
	copy(cands, proj.Candidates)
	for i := range cands {
		c := &cands[i]
		if strings.TrimSpace(c.MomentType) == "" {
			c.MomentType = analyzer.ClassifyMomentType(c.Transcript, c.ArcReason, c.SampleChats)
		}
		dec := s.Audio.ResolveAutoEditForMoment(c.MomentType, "", "")
		c.AutoEdit = &dec
	}
	proj.Candidates = cands
}

func (s *Server) handleGetProject(w http.ResponseWriter, r *http.Request) {
	proj, ok := s.Store.GetProject(r.PathValue("id"))
	if !ok {
		writeErr(w, http.StatusNotFound, "Project tidak ditemukan")
		return
	}
	s.enrichProjectAutoEdit(proj)
	incDrop := r.URL.Query().Get("includeDropped")
	includeDropped := incDrop == "1" || strings.EqualFold(incDrop, "true")
	if !includeDropped && len(proj.Candidates) > 0 {
		filtered := make([]model.ClipCandidate, 0, len(proj.Candidates))
		for _, c := range proj.Candidates {
			if c.Verdict != nil && strings.EqualFold(c.Verdict.Verdict, "drop") {
				continue
			}
			filtered = append(filtered, c)
		}
		proj.Candidates = filtered
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
		URL          string                   `json:"url"`
		CampaignID   string                   `json:"campaignId"`
		DownloadFull bool                     `json:"downloadFull"`
		AudioFirst   bool                     `json:"audioFirst"`
		IncludeChat  *bool                    `json:"includeChat"`
		IncludeAudio bool                     `json:"includeAudio"`
		DetectConfig *model.DetectionSettings `json:"detectConfig"`
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

	mode := "quick"
	if req.DownloadFull {
		mode = "full"
	} else if req.AudioFirst {
		mode = "audio_first"
	}

	p := getPrincipal(r)
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
		CreatedByID:   p.ID,
		CreatedBy:     p.Name,
		AnalysisMode:  mode,
	}
	if err := s.Store.SaveProject(proj); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.Broker.Publish("project_update", proj)

	// Run multi-signal ingestion concurrently in background so HTTP responds immediately
	go s.runAnalysisPipeline(proj.ID, req.DownloadFull, includeChat, req.IncludeAudio || req.AudioFirst, req.AudioFirst)

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
		AnalysisMode:  "audio_first",
	}
	if err := s.Store.SaveProject(proj); err != nil {
		return nil, err
	}
	s.Broker.Publish("project_update", proj)
	s.runAnalysisPipeline(proj.ID, false, false, false, false)

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

func (s *Server) runAnalysisPipeline(projectID string, downloadFull, includeChat, includeAudio, audioFirst bool) {
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
	} else if audioFirst || includeAudio || (len(meta.Heatmap) == 0 && len(subCues) == 0) {
		if audioFirst {
			update("scanning", "🎧 Audio-First Mode: Mengunduh audio ringan (<=64kbps) tanpa video full agar hemat disk ~95%...")
		} else {
			update("scanning", "Menganalisis puncak audio untuk deteksi teriakan/momen seru...")
		}
		if audioPath, err := s.YT.DownloadAudioOnly(ctx, proj.SourceURL, filepath.Join(projDir, "audio.m4a"), settings); err == nil {
			if fi, err := os.Stat(audioPath); err == nil {
				proj.AudioBytesUsed = fi.Size()
				// Estimate 1080p full VOD size (~3.5 Mbps ≈ 437 KB/sec) so disk savings are measurable!
				if meta.Duration > 0 {
					proj.EstFullBytes = int64(meta.Duration * 437500)
				}
				s.Broker.Log("analyzer", "success", fmt.Sprintf("🎧 Audio-First: pakai %.1f MB audio vs estimasi %.0f MB full video (hemat ~%.0f%% disk!)",
					float64(proj.AudioBytesUsed)/(1024*1024),
					float64(proj.EstFullBytes)/(1024*1024),
					100.0*(1.0-float64(proj.AudioBytesUsed)/float64(maxInt64(proj.EstFullBytes, 1)))))
			}
			if energy, err := analyzer.AnalyzeAudioEnergy(ctx, s.Deps.GetStatus().FFmpegPath, audioPath, meta.Duration); err == nil {
				audioEnergy = energy
				saveAudioCache(projDir, energy)
			}
			_ = os.Remove(audioPath)
		}
	}

	// Publish Phase-1 results IMMEDIATELY so user can start previewing & clipping right away!
	res := analyzer.DetectHype(analyzer.AnalysisInput{
		DurationSec:    proj.DurationSec,
		Heatmap:        meta.Heatmap,
		AudioEnergy:    audioEnergy,
		Transcript:     subCues,
		Chapters:       proj.Chapters,
		Settings:       proj.DetectConfig,
		ForbiddenWords: s.campaignForbiddenWords(proj.CampaignID),
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
	s.enrichProjectAutoEdit(proj)
	proj.Signals = res.Signals
	proj.Status = "ready"
	proj.StatusMessage = fmt.Sprintf("Siap! Ditemukan %d momen arc hits.", len(res.Candidates))
	_ = s.Store.SaveProject(proj)
	s.Broker.Publish("project_update", proj)
	s.Broker.Log("analyzer", "success", fmt.Sprintf("Fase 1 selesai cepat: '%s' -> %d kandidat arc siap review!", proj.Title, len(res.Candidates)))

	// Capture Start + Peak verification frames for top 5 candidates in background (for human visual review BEFORE rendering!)
	go s.captureCandidateFrames(proj.ID, 5)

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
		DurationSec:    proj.DurationSec,
		Heatmap:        cs.Heatmap,
		Chats:          cs.Chats,
		AudioEnergy:    cs.AudioEnergy,
		Transcript:     cs.Transcript,
		Chapters:       proj.Chapters,
		Settings:       cfg,
		ForbiddenWords: s.campaignForbiddenWords(proj.CampaignID),
	})

	proj.DetectConfig = cfg
	proj.Timeline = res.Timeline
	proj.Candidates = res.Candidates
	s.enrichProjectAutoEdit(proj)
	proj.Signals = res.Signals
	proj.StatusMessage = fmt.Sprintf("Re-analisis selesai: %d momen terdeteksi.", len(res.Candidates))
	_ = s.Store.SaveProject(proj)
	s.Broker.Publish("project_update", proj)
	writeJSON(w, http.StatusOK, proj)
}

func (s *Server) handleSearchTranscript(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	proj, ok := s.Store.GetProject(id)
	if !ok {
		writeErr(w, http.StatusNotFound, "Project tidak ditemukan")
		return
	}
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	cs := s.loadCachedSignals(proj)
	hits := analyzer.SearchTranscript(cs.Transcript, q, 40)
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"query": q,
		"total": len(hits),
		"hits":  hits,
	})
}

func (s *Server) handleRenderCompile(w http.ResponseWriter, r *http.Request) {
	p := getPrincipal(r)
	if err := s.checkMemberQueueLimit(p); err != nil {
		writeErr(w, http.StatusTooManyRequests, err.Error())
		return
	}
	var opts model.CompileOptions
	if err := json.NewDecoder(r.Body).Decode(&opts); err != nil {
		writeErr(w, http.StatusBadRequest, "Format JSON compile tidak valid")
		return
	}
	job, err := s.Worker.EnqueueCompilation(opts)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	s.tagJobWithPrincipal(job, p)
	writeJSON(w, http.StatusAccepted, job)
}

func (s *Server) handleScanAudioOnly(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	proj, ok := s.Store.GetProject(id)
	if !ok {
		writeErr(w, http.StatusNotFound, "Project tidak ditemukan")
		return
	}
	proj.Status = "scanning"
	proj.StatusMessage = "Mengunduh audio ringan (<=64kbps) untuk deteksi teriakan & tawa keras..."
	_ = s.Store.SaveProject(proj)
	s.Broker.Publish("project_update", proj)

	go func() {
		ctx := context.Background()
		projDir := s.Store.ProjectDir(proj.ID)
		settings := s.Store.GetSettings()

		var mediaPath string
		var cleanup bool
		if proj.LocalVideoPath != "" {
			mediaPath = proj.LocalVideoPath
		} else {
			audioPath, err := s.YT.DownloadAudioOnly(ctx, proj.SourceURL, filepath.Join(projDir, "fast_audio.m4a"), settings)
			if err != nil {
				proj.Status = "ready"
				proj.StatusMessage = "Scan audio gagal: " + err.Error()
				_ = s.Store.SaveProject(proj)
				s.Broker.Publish("project_update", proj)
				return
			}
			mediaPath = audioPath
			cleanup = true
		}

		proj.StatusMessage = "Menganalisis puncak teriakan, tawa & ledakan suara (FFmpeg RMS)..."
		s.Broker.Publish("project_update", proj)

		energy, err := analyzer.AnalyzeAudioEnergy(ctx, s.Deps.GetStatus().FFmpegPath, mediaPath, proj.DurationSec)
		if cleanup {
			_ = os.Remove(mediaPath)
		}
		if err != nil || len(energy) == 0 {
			proj.Status = "ready"
			proj.StatusMessage = "Gagal analisis energi audio."
			_ = s.Store.SaveProject(proj)
			s.Broker.Publish("project_update", proj)
			return
		}

		saveAudioCache(projDir, energy)
		cs := s.loadCachedSignals(proj)
		cs.AudioEnergy = energy

		res := analyzer.DetectHype(analyzer.AnalysisInput{
			DurationSec:    proj.DurationSec,
			Heatmap:        cs.Heatmap,
			Chats:          cs.Chats,
			AudioEnergy:    energy,
			Transcript:     cs.Transcript,
			Chapters:       proj.Chapters,
			Settings:       proj.DetectConfig,
			ForbiddenWords: s.campaignForbiddenWords(proj.CampaignID),
		})
		proj.Timeline = res.Timeline
		proj.Candidates = res.Candidates
		proj.Signals = res.Signals
		proj.Status = "ready"
		proj.StatusMessage = fmt.Sprintf("🔊 Audio Scream + Transcript siap! %d momen paling pecah ditemukan.", len(res.Candidates))
		_ = s.Store.SaveProject(proj)
		s.Broker.Publish("project_update", proj)
		s.Broker.Log("analyzer", "success", proj.StatusMessage)
	}()

	writeJSON(w, http.StatusAccepted, proj)
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

func maxInt64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

// captureCandidateFrames grabs 1 verification frame at ArcStartSec and 1 at PeakSec for the top N candidates
// so human reviewers can verify visual context on frames BEFORE rendering (never auto-renders!).
func (s *Server) captureCandidateFrames(projectID string, topN int) {
	proj, ok := s.Store.GetProject(projectID)
	if !ok || len(proj.Candidates) == 0 {
		return
	}
	if topN <= 0 || topN > len(proj.Candidates) {
		topN = len(proj.Candidates)
	}
	if topN > 8 {
		topN = 8
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	cs := s.loadCachedSignals(proj)
	streamURL := cs.StreamURL
	if proj.LocalVideoPath == "" && streamURL == "" {
		if meta, err := s.YT.FetchMetadata(ctx, proj.SourceURL, s.Store.GetSettings()); err == nil {
			streamURL = meta.StreamURL
			cs.StreamURL = streamURL
		}
	}

	framesDir := filepath.Join(s.Store.ProjectDir(proj.ID), "frames")
	_ = os.MkdirAll(framesDir, 0o755)
	changed := false

	grabAt := func(sec float64, dest string) bool {
		if proj.LocalVideoPath != "" {
			return s.YT.GrabFrameFromLocalFile(ctx, proj.LocalVideoPath, sec, dest) == nil
		}
		if streamURL != "" {
			return s.YT.GrabPreviewFrame(ctx, streamURL, "", sec, dest) == nil
		}
		return false
	}

	for i := 0; i < topN; i++ {
		c := &proj.Candidates[i]
		startSec := c.ArcStartSec
		if startSec <= 0 {
			startSec = c.StartSec
		}
		peakSec := c.PeakSec
		if peakSec <= 0 {
			peakSec = (c.StartSec + c.EndSec) / 2
		}

		startPath := filepath.Join(framesDir, c.ID+"_start.jpg")
		peakPath := filepath.Join(framesDir, c.ID+"_peak.jpg")

		if grabAt(startSec, startPath) {
			c.StartFramePath = startPath
			c.StartFrameURL = fmt.Sprintf("/api/projects/%s/candidate-frame/%s/start", proj.ID, c.ID)
			changed = true
		}
		if grabAt(peakSec, peakPath) {
			c.PeakFramePath = peakPath
			c.PeakFrameURL = fmt.Sprintf("/api/projects/%s/candidate-frame/%s/peak", proj.ID, c.ID)
			changed = true
		}
	}

	if changed {
		// Re-fetch latest project to avoid clobbering concurrent updates, then copy frame URLs/paths
		if latest, ok := s.Store.GetProject(projectID); ok {
			frameByID := make(map[string]model.ClipCandidate, topN)
			for i := 0; i < topN; i++ {
				frameByID[proj.Candidates[i].ID] = proj.Candidates[i]
			}
			for i := range latest.Candidates {
				if fc, ok := frameByID[latest.Candidates[i].ID]; ok {
					latest.Candidates[i].StartFramePath = fc.StartFramePath
					latest.Candidates[i].StartFrameURL = fc.StartFrameURL
					latest.Candidates[i].PeakFramePath = fc.PeakFramePath
					latest.Candidates[i].PeakFrameURL = fc.PeakFrameURL
				}
			}
			_ = s.Store.SaveProject(latest)
			s.Broker.Publish("project_update", latest)
			s.Broker.Log("analyzer", "success", fmt.Sprintf("Frame verifikasi (Start + Peak) siap untuk Top %d kandidat!", topN))
		}
	}
}

func (s *Server) handleVerifyCandidateFrames(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	proj, ok := s.Store.GetProject(id)
	if !ok {
		writeErr(w, http.StatusNotFound, "Project tidak ditemukan")
		return
	}
	var req struct {
		TopN int `json:"topN"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	if req.TopN <= 0 {
		req.TopN = 5
	}
	go s.captureCandidateFrames(proj.ID, req.TopN)
	writeJSON(w, http.StatusAccepted, map[string]interface{}{"ok": true, "projectId": proj.ID, "topN": req.TopN})
}

func (s *Server) handleServeCandidateFrame(w http.ResponseWriter, r *http.Request) {
	projID := r.PathValue("id")
	candID := filepath.Base(r.PathValue("candId"))
	kind := r.PathValue("kind")
	if kind != "start" && kind != "peak" {
		http.NotFound(w, r)
		return
	}
	p := filepath.Join(s.Store.ProjectDir(projID), "frames", candID+"_"+kind+".jpg")
	if _, err := os.Stat(p); err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Cache-Control", "public, max-age=3600")
	http.ServeFile(w, r, p)
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
	all := s.Store.ListJobs()
	q := r.URL.Query()

	// Optional ?status=done or ?status=queued,rendering filter
	statusFilter := make(map[string]bool)
	if rawStatus := strings.TrimSpace(q.Get("status")); rawStatus != "" {
		for _, part := range strings.Split(rawStatus, ",") {
			if t := strings.TrimSpace(strings.ToLower(part)); t != "" {
				statusFilter[t] = true
			}
		}
	}
	memberFilter := strings.TrimSpace(q.Get("member"))

	filtered := make([]*model.RenderJob, 0, len(all))
	for _, j := range all {
		if len(statusFilter) > 0 && !statusFilter[strings.ToLower(j.Status)] {
			continue
		}
		if memberFilter != "" {
			cid := j.CreatedByID
			if cid == "" {
				cid = "owner"
			}
			if cid != memberFilter {
				continue
			}
		}
		filtered = append(filtered, j)
	}

	total := len(filtered)
	w.Header().Set("X-Total-Count", strconv.Itoa(total))

	// Pagination: ?offset=0 & ?limit=50 (default 50, max 200 to prevent truncated multi-MB responses!)
	offset := 0
	if rawOff := strings.TrimSpace(q.Get("offset")); rawOff != "" {
		if v, err := strconv.Atoi(rawOff); err == nil && v > 0 {
			offset = v
		}
	}
	limit := 50
	if rawLim := strings.TrimSpace(q.Get("limit")); rawLim != "" {
		if v, err := strconv.Atoi(rawLim); err == nil {
			if v <= 0 {
				limit = 200
			} else if v > 200 {
				limit = 200
			} else {
				limit = v
			}
		}
	}
	if offset > len(filtered) {
		offset = len(filtered)
	}
	end := offset + limit
	if end > len(filtered) {
		end = len(filtered)
	}
	page := filtered[offset:end]

	// Compact truncation-proof ?summary=1 mode (ideal for Muse polling & fast mobile loads!)
	if q.Get("summary") == "1" || strings.EqualFold(q.Get("summary"), "true") {
		summaries := make([]model.JobSummary, 0, len(page))
		for _, j := range page {
			summaries = append(summaries, model.JobSummary{
				ID:            j.ID,
				JobType:       j.JobType,
				ProjectID:     j.ProjectID,
				CampaignName:  j.CampaignName,
				Title:         j.Title,
				Status:        j.Status,
				Stage:         j.Stage,
				Progress:      j.Progress,
				Speed:         j.Speed,
				OutputFile:    j.OutputFile,
				FileSizeBytes: j.FileSizeBytes,
				FinalDuration: j.FinalDuration,
				ReviewStatus:  j.ReviewStatus,
				CreatedByID:   j.CreatedByID,
				CreatedBy:     j.CreatedBy,
				CreatedVia:    j.CreatedVia,
				Error:         j.Error,
				CreatedAt:     j.CreatedAt,
				CompletedAt:   j.CompletedAt,
			})
		}
		writeJSON(w, http.StatusOK, summaries)
		return
	}

	writeJSON(w, http.StatusOK, page)
}

func (s *Server) handleGetJob(w http.ResponseWriter, r *http.Request) {
	job, ok := s.Store.GetJob(r.PathValue("id"))
	if !ok {
		writeErr(w, http.StatusNotFound, "Job tidak ditemukan")
		return
	}
	writeJSON(w, http.StatusOK, job)
}

func (s *Server) handleRenderSingle(w http.ResponseWriter, r *http.Request) {
	p := getPrincipal(r)
	if err := s.checkMemberQueueLimit(p); err != nil {
		writeErr(w, http.StatusTooManyRequests, err.Error())
		return
	}
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
	s.tagJobWithPrincipal(job, p)
	writeJSON(w, http.StatusAccepted, job)
}

func (s *Server) handleRenderBatch(w http.ResponseWriter, r *http.Request) {
	p := getPrincipal(r)
	if err := s.checkMemberQueueLimit(p); err != nil {
		writeErr(w, http.StatusTooManyRequests, err.Error())
		return
	}
	var req struct {
		Items []model.RenderOptions `json:"items"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || len(req.Items) == 0 {
		writeErr(w, http.StatusBadRequest, "Minimal 1 klip diperlukan untuk batch auto-clip")
		return
	}
	var jobs []*model.RenderJob
	for _, item := range req.Items {
		if err := s.checkMemberQueueLimit(p); err != nil {
			break
		}
		job, err := s.Worker.Enqueue(item)
		if err != nil {
			continue
		}
		s.tagJobWithPrincipal(job, p)
		jobs = append(jobs, job)
	}
	if len(jobs) == 0 {
		writeErr(w, http.StatusTooManyRequests, "Batas antrean aktif anggota tercapai atau tidak ada item valid")
		return
	}
	writeJSON(w, http.StatusAccepted, jobs)
}

func (s *Server) handleCancelJob(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	p := getPrincipal(r)
	if p.Role == "clipper" {
		if job, ok := s.Store.GetJob(id); ok && job.CreatedByID != "" && job.CreatedByID != p.ID {
			writeErr(w, http.StatusForbidden, "Clipper hanya boleh membatalkan job miliknya sendiri")
			return
		}
	}
	s.Worker.Cancel(id)
	writeJSON(w, http.StatusOK, map[string]bool{"canceled": true})
}

func (s *Server) handlePostEditJob(w http.ResponseWriter, r *http.Request) {
	p := getPrincipal(r)
	if err := s.checkMemberQueueLimit(p); err != nil {
		writeErr(w, http.StatusTooManyRequests, err.Error())
		return
	}
	id := r.PathValue("id")
	var opts model.PostEditOptions
	if err := json.NewDecoder(r.Body).Decode(&opts); err != nil {
		writeErr(w, http.StatusBadRequest, "Format JSON edit manual tidak valid")
		return
	}
	opts.SourceJobID = id
	job, err := s.Worker.EnqueuePostEdit(opts)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	s.tagJobWithPrincipal(job, p)
	writeJSON(w, http.StatusAccepted, job)
}

func (s *Server) handleRetryJob(w http.ResponseWriter, r *http.Request) {
	p := getPrincipal(r)
	if err := s.checkMemberQueueLimit(p); err != nil {
		writeErr(w, http.StatusTooManyRequests, err.Error())
		return
	}
	oldJob, ok := s.Store.GetJob(r.PathValue("id"))
	if !ok {
		writeErr(w, http.StatusNotFound, "Job tidak ditemukan")
		return
	}
	var (
		newJob *model.RenderJob
		err    error
	)
	if oldJob.JobType == "compilation" && oldJob.CompileOpts != nil {
		newJob, err = s.Worker.EnqueueCompilation(*oldJob.CompileOpts)
	} else if oldJob.JobType == "post_edit" && oldJob.PostEditOpts != nil {
		newJob, err = s.Worker.EnqueuePostEdit(*oldJob.PostEditOpts)
	} else {
		newJob, err = s.Worker.Enqueue(oldJob.Options)
	}
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	s.tagJobWithPrincipal(newJob, p)
	writeJSON(w, http.StatusAccepted, newJob)
}

func (s *Server) handleDeleteJob(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	p := getPrincipal(r)
	if p.Role == "clipper" || p.Role == "qc" {
		if job, ok := s.Store.GetJob(id); ok {
			if job.ReviewStatus == "approved" {
				writeErr(w, http.StatusForbidden, "Klip yang sudah Approved hanya boleh dihapus oleh Owner")
				return
			}
			if p.Role == "clipper" && job.CreatedByID != p.ID {
				writeErr(w, http.StatusForbidden, "Clipper hanya boleh menghapus klip miliknya sendiri")
				return
			}
		}
	}
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

func (s *Server) handleListAudioPresets(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.Audio.ListPresets())
}

func (s *Server) handleSaveAudioPreset(w http.ResponseWriter, r *http.Request) {
	var p model.AudioPreset
	if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
		writeErr(w, http.StatusBadRequest, "Format preset audio tidak valid")
		return
	}
	saved, err := s.Audio.SavePreset(p)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, saved)
}

func (s *Server) handleDeleteAudioPreset(w http.ResponseWriter, r *http.Request) {
	if err := s.Audio.DeletePreset(r.PathValue("id")); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"deleted": true})
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

func (s *Server) handleGetMomentEditMap(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"path": s.Audio.MomentMapPath(),
		"map":  s.Audio.GetMomentEditMap(),
	})
}

func (s *Server) handleListTrendingAudio(w http.ResponseWriter, r *http.Request) {
	cat := r.URL.Query().Get("category")
	q := r.URL.Query().Get("q")
	refresh := r.URL.Query().Get("refresh") == "1"
	items, err := s.Audio.FetchTrendingSounds(r.Context(), cat, q, refresh, s.Store.GetSettings())
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"category": cat,
		"query":    q,
		"count":    len(items),
		"items":    items,
	})
}

func (s *Server) handleImportTrendingAudio(w http.ResponseWriter, r *http.Request) {
	var req struct {
		URL            string  `json:"url"`
		Name           string  `json:"name"`
		Kind           string  `json:"kind"` // "bgm" or "sfx"
		Category       string  `json:"category"`
		MaxDurationSec float64 `json:"maxDurationSec"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "Format JSON import sound tidak valid")
		return
	}

	p := getPrincipal(r)
	settings := s.Store.GetSettings()
	const agentDailyImportLimit = 5
	const agentMaxDurationSec = 30.0

	if p.Role == "agent" {
		if settings.DisableAgentTrendingImport {
			writeErr(w, http.StatusForbidden, "Forbidden: Owner menonaktifkan import trending sound untuk Agent")
			return
		}
		if !s.Audio.IsKnownRadarURL(req.URL) {
			writeErr(w, http.StatusForbidden, "Forbidden: Agent hanya boleh mengimpor sound dari daftar radar GET /api/audio/trending (URL arbitrer khusus pengguna manusia)")
			return
		}
		if used, _, allowed := s.Store.CheckAgentTrendQuota(p.ID, agentDailyImportLimit); !allowed {
			writeErr(w, http.StatusTooManyRequests, fmt.Sprintf("Batas harian import sound untuk agent tercapai (%d/%d hari ini untuk %s)", used, agentDailyImportLimit, p.Name))
			return
		}
		if req.MaxDurationSec <= 0 {
			if strings.EqualFold(strings.TrimSpace(req.Kind), "sfx") {
				req.MaxDurationSec = 12.0
			} else {
				req.MaxDurationSec = agentMaxDurationSec
			}
		} else if req.MaxDurationSec > agentMaxDurationSec {
			req.MaxDurationSec = agentMaxDurationSec
		}
	}

	asset, err := s.Audio.ImportSoundFromURL(r.Context(), req.URL, req.Name, req.Kind, req.Category, req.MaxDurationSec, settings)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	probedDur := 0.0
	if info, err := renderer.ProbeMedia(r.Context(), s.Deps.GetStatus().FFprobePath, asset.FilePath); err == nil && info.Duration > 0 {
		probedDur = info.Duration
		asset.Duration = info.Duration
	}
	if updated, ok := s.Audio.SetAttribution(asset.ID, p.ID, p.Name, p.Via, probedDur); ok {
		asset = updated
	}
	if p.Role == "agent" {
		s.Store.IncrementAgentTrendImportsToday(p.ID)
	}
	s.Broker.Log("audio", "success", fmt.Sprintf("🔥 Import Sound Trend (%s) oleh %s (%s): %s (%.1fs)", strings.ToUpper(asset.Kind), p.Name, p.Via, asset.Name, asset.Duration))
	writeJSON(w, http.StatusCreated, asset)
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
		RejectReason       string `json:"rejectReason"`
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
	p := getPrincipal(r)
	if p.Role == "qc" && req.ReviewStatus == "approved" && job.CreatedByID != "" && job.CreatedByID == p.ID {
		writeErr(w, http.StatusForbidden, "Self-approve dilarang: QC tidak boleh menyetujui klip miliknya sendiri")
		return
	}
	nowRev := time.Now()
	job.ReviewStatus = req.ReviewStatus
	job.ReviewedBy = p.Name
	job.ReviewedAt = &nowRev
	if req.ReviewNote != "" {
		job.ReviewNote = req.ReviewNote
	}
	if req.RejectReason != "" {
		job.RejectReason = req.RejectReason
		if job.ReviewNote == "" {
			job.ReviewNote = req.RejectReason
		}
	}
	if strings.TrimSpace(req.PostCaption) != "" {
		job.PostCaption = req.PostCaption
	}
	switch req.ReviewStatus {
	case "approved":
		job.BoardColumn = "siap_posting"
		if job.PublishKit == nil {
			if kit, err := s.buildPublishKitForJob(job, nil); err == nil {
				job.PublishKit = kit
			}
		}
		s.recordActivity(p, "review_approved", "job", job.ID,
			fmt.Sprintf("%s menyetujui klip %s", p.Name, job.Title), job.ReviewNote, "success")
	case "rejected":
		job.BoardColumn = "kandidat"
		s.recordActivity(p, "review_rejected", "job", job.ID,
			fmt.Sprintf("%s menolak klip %s", p.Name, job.Title), job.RejectReason, "warn")
	case "pending_review":
		job.BoardColumn = "review"
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
