package server

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"klipforge/internal/model"
)

type ctxKey string

const principalCtxKey ctxKey = "kf_principal"

func withPrincipal(ctx context.Context, p model.Principal) context.Context {
	return context.WithValue(ctx, principalCtxKey, p)
}

func getPrincipal(r *http.Request) model.Principal {
	if p, ok := r.Context().Value(principalCtxKey).(model.Principal); ok {
		return p
	}
	return model.Principal{ID: "owner", Name: "Owner", Role: "owner", Via: "token"}
}

func randomPrefixedKey(prefix string) string {
	buf := make([]byte, 18)
	_, _ = rand.Read(buf)
	return prefix + hex.EncodeToString(buf)
}

// parseAuthSecret extracts password/token from Authorization: Basic ... or Bearer ... header.
func parseAuthSecret(authHeader string) string {
	authHeader = strings.TrimSpace(authHeader)
	if strings.HasPrefix(authHeader, "Bearer ") {
		return strings.TrimSpace(authHeader[7:])
	}
	if strings.HasPrefix(authHeader, "Basic ") {
		raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(authHeader[6:]))
		if err != nil {
			return ""
		}
		dec := string(raw)
		if idx := strings.IndexByte(dec, ':'); idx >= 0 {
			return strings.TrimSpace(dec[idx+1:])
		}
		return strings.TrimSpace(dec)
	}
	return ""
}

// resolvePrincipal authenticates requests from:
// 1) Owner Engine Token (X-Engine-Token / kf_token cookie / ?token=) -> owner
// 2) Team Member Access Key (X-Access-Key / kf_access_key cookie / Authorization Basic|Bearer) -> owner | qc | clipper
// 3) Agent Key (X-Agent-Key: master .agent-key OR team member personal AgentKey) -> agent (attributed to that member!)
func (s *Server) resolvePrincipal(r *http.Request) (model.Principal, bool) {
	if s.EngineToken == "" {
		return model.Principal{ID: "owner", Name: "Owner", Role: "owner", Via: "open"}, true
	}

	// 1. Owner EngineToken (trusted Worker admin path or local owner)
	if s.isValidToken(r.Header.Get("X-Engine-Token")) {
		return model.Principal{ID: "owner", Name: "Owner", Role: "owner", Via: "token"}, true
	}
	if ck, err := r.Cookie("kf_token"); err == nil && s.isValidToken(ck.Value) {
		return model.Principal{ID: "owner", Name: "Owner", Role: "owner", Via: "token"}, true
	}
	if q := r.URL.Query().Get("token"); q != "" && s.isValidToken(q) {
		return model.Principal{ID: "owner", Name: "Owner", Role: "owner", Via: "token"}, true
	}

	// 2. Personal Team Member Access Key (forwarded by Cloudflare Worker as X-Access-Key, or direct VPS Basic/Bearer/Cookie)
	accessCandidates := []string{
		strings.TrimSpace(r.Header.Get("X-Access-Key")),
	}
	if ck, err := r.Cookie("kf_access_key"); err == nil && strings.TrimSpace(ck.Value) != "" {
		accessCandidates = append(accessCandidates, strings.TrimSpace(ck.Value))
	}
	if authSec := parseAuthSecret(r.Header.Get("Authorization")); authSec != "" {
		// Also support direct VPS owner access if they pass EngineToken via Bearer/Basic
		if s.isValidToken(authSec) {
			return model.Principal{ID: "owner", Name: "Owner", Role: "owner", Via: "web"}, true
		}
		accessCandidates = append(accessCandidates, authSec)
	}
	for _, ak := range accessCandidates {
		if ak == "" {
			continue
		}
		if m, ok := s.Store.FindTeamByAccessKey(ak); ok {
			return model.Principal{
				ID:        m.ID,
				Name:      m.Name,
				Role:      m.Role,
				Via:       "web",
				MaxActive: m.MaxActive,
			}, true
		}
	}

	// 3. Scoped Agent Key (Owner's Muse OR Team Member's personal Muse!)
	if agk := strings.TrimSpace(r.Header.Get("X-Agent-Key")); agk != "" {
		if s.isValidAgentKey(agk) {
			return model.Principal{
				ID:   "owner-agent",
				Name: "Owner",
				Role: "agent",
				Via:  "agent",
			}, true
		}
		if m, ok := s.Store.FindTeamByAgentKey(agk); ok {
			return model.Principal{
				ID:        m.ID,
				Name:      m.Name,
				Role:      "agent",
				Via:       "agent",
				MaxActive: m.MaxActive,
			}, true
		}
	}

	return model.Principal{}, false
}

// isRoleAllowed enforces least-privilege RBAC across owner, qc, clipper, and agent (Muse).
func isRoleAllowed(p model.Principal, method, path string) bool {
	if p.Role == "owner" || p.Role == "admin" {
		return true
	}
	// Team management & secrets are strictly Owner-only (except non-secret /api/team/metrics & /api/settings/cookies/status!)
	if (strings.HasPrefix(path, "/api/team") && path != "/api/team/metrics") ||
		path == "/api/settings/cookies" ||
		path == "/api/settings/cookies/refresh" ||
		strings.HasPrefix(path, "/api/publish/accounts") {
		return false
	}
	if p.Role == "agent" {
		return isAgentAllowed(method, path)
	}

	// Authenticated human team members: "qc" and "clipper"
	if method == http.MethodGet || method == http.MethodHead {
		return true
	}

	if method == http.MethodPost {
		allowedClipperExact := map[string]bool{
			"/api/projects/analyze":      true,
			"/api/jobs/render":           true,
			"/api/jobs/batch":            true,
			"/api/jobs/compile":          true,
			"/api/jobs/edit-script":      true,
			"/api/audio/presets":         true,
			"/api/audio/trending/import": true,
			"/api/board/move":            true,
			"/api/ledger/views":          true,
			"/api/mcp":                   true,
		}
		if allowedClipperExact[path] {
			return true
		}
		if strings.HasPrefix(path, "/api/audio/upload/") {
			return true
		}
		if strings.HasPrefix(path, "/api/projects/") {
			if strings.HasSuffix(path, "/reanalyze") ||
				strings.HasSuffix(path, "/scan-audio") ||
				strings.HasSuffix(path, "/scan-livechat") ||
				strings.HasSuffix(path, "/storyboard") ||
				strings.HasSuffix(path, "/download-full") ||
				strings.HasSuffix(path, "/verify-frames") ||
				strings.HasSuffix(path, "/grab-frame") ||
				strings.HasSuffix(path, "/detect-scenes") ||
				strings.HasSuffix(path, "/detect-facecam") ||
				strings.HasSuffix(path, "/diarize") ||
				(strings.Contains(path, "/candidates/") && strings.HasSuffix(path, "/verdict")) {
				return true
			}
		}
		if strings.HasPrefix(path, "/api/jobs/") {
			if strings.HasSuffix(path, "/post-edit") ||
				strings.HasSuffix(path, "/retry") ||
				strings.HasSuffix(path, "/cancel") ||
				strings.HasSuffix(path, "/publish-kit") ||
				strings.HasSuffix(path, "/publish") ||
				strings.HasSuffix(path, "/draft") ||
				strings.HasSuffix(path, "/analytics/sync") {
				return true
			}
			// Only QC (and Owner) can Approve / Reject clips in Review or Mark Clips Posted Manually!
			if p.Role == "qc" && (strings.HasSuffix(path, "/review") || path == "/api/jobs/review/batch" || strings.HasSuffix(path, "/mark-posted")) {
				return true
			}
		}
		return false
	}

	// Clippers & QC can delete jobs (ownership & unapproved guard enforced inside handleDeleteJob!) or close MCP session
	if method == http.MethodDelete && (strings.HasPrefix(path, "/api/jobs/") || path == "/api/mcp") {
		return true
	}

	return false
}

// checkMemberQueueLimit protects shared PC/VPS FFmpeg workers from 1 clipper flooding all slots.
func (s *Server) checkMemberQueueLimit(p model.Principal) error {
	if p.Role == "owner" || p.Role == "admin" || p.ID == "owner" || p.ID == "owner-agent" {
		return nil
	}
	limit := p.MaxActive
	if limit <= 0 {
		limit = 3
	}
	active := s.Store.CountActiveJobsByMember(p.ID)
	if active >= limit {
		return fmt.Errorf("batas antrean aktif tercapai (%d/%d job aktif untuk %s). Tunggu 1 job selesai dulu biar anggota tim lain kebagian render", active, limit, p.Name)
	}
	return nil
}

// tagJobWithPrincipal attributes a newly queued render job to the calling member/agent atomically.
func (s *Server) tagJobWithPrincipal(job *model.RenderJob, p model.Principal) {
	if job == nil || p.ID == "" {
		return
	}
	job.CreatedByID = p.ID
	job.CreatedBy = p.Name
	job.CreatedVia = p.Via
	if updated, ok := s.Store.UpdateJob(job.ID, func(j *model.RenderJob) {
		j.CreatedByID = p.ID
		j.CreatedBy = p.Name
		j.CreatedVia = p.Via
	}); ok {
		s.Broker.Publish("job_update", updated)
	}
}

func (s *Server) bridgeSecret() string {
	if v := strings.TrimSpace(os.Getenv("KLIPFORGE_BRIDGE_SECRET")); v != "" {
		return v
	}
	p := filepath.Join(s.Store.BaseDir, ".bridge-secret")
	if b, err := os.ReadFile(p); err == nil {
		return strings.TrimSpace(string(b))
	}
	return ""
}

func (s *Server) edgeBaseURL() string {
	if v := strings.TrimSpace(os.Getenv("KLIPFORGE_EDGE_URL")); v != "" {
		return strings.TrimRight(v, "/")
	}
	return "https://klip.breakthematrix.id"
}

type edgeTeamEntry struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Role string `json:"role"`
}

type edgeTeamPayload struct {
	Access map[string]edgeTeamEntry `json:"access"`
	Agent  map[string]edgeTeamEntry `json:"agent"`
}

// syncTeamToEdge pushes active team member AccessKey & AgentKey lookups to Cloudflare Worker KV
// so klip.breakthematrix.id gates static UI & /api/* per member with zero proxy latency!
func (s *Server) syncTeamToEdge() error {
	secret := s.bridgeSecret()
	if secret == "" {
		return fmt.Errorf("bridge secret tidak dikonfigurasi (mode lokal/direct VPS)")
	}
	payload := edgeTeamPayload{
		Access: make(map[string]edgeTeamEntry),
		Agent:  make(map[string]edgeTeamEntry),
	}
	for _, m := range s.Store.ListTeam() {
		if !m.Enabled {
			continue
		}
		ent := edgeTeamEntry{ID: m.ID, Name: m.Name, Role: m.Role}
		if strings.TrimSpace(m.AccessKey) != "" {
			payload.Access[strings.TrimSpace(m.AccessKey)] = ent
		}
		if strings.TrimSpace(m.AgentKey) != "" {
			payload.Agent[strings.TrimSpace(m.AgentKey)] = ent
		}
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	url := s.edgeBaseURL() + "/edge-sync-team"
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Bridge-Secret", secret)
	client := &http.Client{Timeout: 8 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("edge sync HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	return nil
}

func (s *Server) syncTeamToEdgeAsync() {
	go func() {
		if err := s.syncTeamToEdge(); err != nil {
			log.Printf("[Team] Edge sync skipped/failed: %v", err)
		}
	}()
}

// --- Team Management REST Handlers (Owner Only) ---

type memberStats struct {
	TotalJobs    int `json:"totalJobs"`
	DoneJobs     int `json:"doneJobs"`
	ApprovedJobs int `json:"approvedJobs"`
	PendingJobs  int `json:"pendingJobs"`
	ActiveJobs   int `json:"activeJobs"`
	AgentJobs    int `json:"agentJobs"`
}

func (s *Server) computeTeamStats() map[string]memberStats {
	stats := make(map[string]memberStats)
	for _, j := range s.Store.ListJobs() {
		id := j.CreatedByID
		if id == "" {
			id = "owner"
		}
		st := stats[id]
		st.TotalJobs++
		if j.Status == "done" {
			st.DoneJobs++
		}
		if j.ReviewStatus == "approved" {
			st.ApprovedJobs++
		} else if j.ReviewStatus == "pending_review" || (j.ReviewStatus == "" && j.Status == "done") {
			st.PendingJobs++
		}
		if j.Status == "queued" || j.Status == "downloading" || j.Status == "rendering" {
			st.ActiveJobs++
		}
		if j.CreatedVia == "agent" {
			st.AgentJobs++
		}
		stats[id] = st
	}
	return stats
}

func (s *Server) handleListTeam(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"members":            s.Store.ListTeam(),
		"stats":              s.computeTeamStats(),
		"edgeSyncConfigured": s.bridgeSecret() != "",
		"edgeUrl":            s.edgeBaseURL(),
	})
}

func (s *Server) handleCreateTeamMember(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name      string `json:"name"`
		Role      string `json:"role"`
		MaxActive int    `json:"maxActive"`
		Notes     string `json:"notes"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "Format JSON anggota tidak valid")
		return
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		writeErr(w, http.StatusBadRequest, "Nama anggota wajib diisi")
		return
	}
	role := strings.ToLower(strings.TrimSpace(req.Role))
	if role != "owner" && role != "qc" && role != "clipper" {
		role = "clipper"
	}
	maxActive := req.MaxActive
	if maxActive <= 0 {
		maxActive = 3
	}
	if maxActive > 10 {
		maxActive = 10
	}

	m := &model.TeamMember{
		ID:        fmt.Sprintf("mem-%d", time.Now().UnixNano()),
		Name:      name,
		Role:      role,
		AccessKey: randomPrefixedKey("kf_usr_"),
		AgentKey:  randomPrefixedKey("kf_agt_"),
		Enabled:   true,
		MaxActive: maxActive,
		Notes:     strings.TrimSpace(req.Notes),
		CreatedAt: time.Now(),
	}
	if err := s.Store.SaveTeamMember(m); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	edgeErr := ""
	if err := s.syncTeamToEdge(); err != nil {
		edgeErr = err.Error()
	}
	s.Broker.Log("team", "success", fmt.Sprintf("Anggota tim baru ditambahkan: %s (%s)", m.Name, m.Role))
	writeJSON(w, http.StatusCreated, map[string]interface{}{
		"member":     m,
		"edgeSynced": edgeErr == "",
		"edgeError":  edgeErr,
	})
}

func (s *Server) handleUpdateTeamMember(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	ex, ok := s.Store.GetTeamMember(id)
	if !ok {
		writeErr(w, http.StatusNotFound, "Anggota tim tidak ditemukan")
		return
	}
	var req struct {
		Name      *string `json:"name"`
		Role      *string `json:"role"`
		Enabled   *bool   `json:"enabled"`
		MaxActive *int    `json:"maxActive"`
		Notes     *string `json:"notes"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "Format JSON tidak valid")
		return
	}
	if req.Name != nil && strings.TrimSpace(*req.Name) != "" {
		ex.Name = strings.TrimSpace(*req.Name)
	}
	if req.Role != nil {
		role := strings.ToLower(strings.TrimSpace(*req.Role))
		if role == "owner" || role == "qc" || role == "clipper" {
			ex.Role = role
		}
	}
	if req.Enabled != nil {
		ex.Enabled = *req.Enabled
	}
	if req.MaxActive != nil && *req.MaxActive >= 1 && *req.MaxActive <= 10 {
		ex.MaxActive = *req.MaxActive
	}
	if req.Notes != nil {
		ex.Notes = strings.TrimSpace(*req.Notes)
	}
	if err := s.Store.SaveTeamMember(ex); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	edgeErr := ""
	if err := s.syncTeamToEdge(); err != nil {
		edgeErr = err.Error()
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"member":     ex,
		"edgeSynced": edgeErr == "",
		"edgeError":  edgeErr,
	})
}

func (s *Server) handleRotateTeamKeys(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	ex, ok := s.Store.GetTeamMember(id)
	if !ok {
		writeErr(w, http.StatusNotFound, "Anggota tim tidak ditemukan")
		return
	}
	var req struct {
		Target string `json:"target"` // "all", "access", "agent"
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	target := strings.ToLower(strings.TrimSpace(req.Target))
	if target == "" || target == "all" || target == "access" {
		ex.AccessKey = randomPrefixedKey("kf_usr_")
	}
	if target == "" || target == "all" || target == "agent" {
		ex.AgentKey = randomPrefixedKey("kf_agt_")
	}
	if err := s.Store.SaveTeamMember(ex); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	edgeErr := ""
	if err := s.syncTeamToEdge(); err != nil {
		edgeErr = err.Error()
	}
	s.Broker.Log("team", "info", fmt.Sprintf("Key diputar ulang untuk %s (%s)", ex.Name, target))
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"member":     ex,
		"edgeSynced": edgeErr == "",
		"edgeError":  edgeErr,
	})
}

func (s *Server) handleDeleteTeamMember(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.Store.DeleteTeamMember(id); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	edgeErr := ""
	if err := s.syncTeamToEdge(); err != nil {
		edgeErr = err.Error()
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"deleted":    true,
		"edgeSynced": edgeErr == "",
		"edgeError":  edgeErr,
	})
}

func (s *Server) handleSyncTeamEdge(w http.ResponseWriter, r *http.Request) {
	if err := s.syncTeamToEdge(); err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"ok":      true,
		"members": len(s.Store.ListTeam()),
		"edgeUrl": s.edgeBaseURL(),
	})
}

// convertJSONCookiesToNetscape converts a Cookie-Editor JSON array export into yt-dlp Netscape cookies.txt format.
func convertJSONCookiesToNetscape(raw []byte) ([]byte, bool) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || trimmed[0] != '[' {
		return nil, false
	}
	var items []struct {
		Domain         string  `json:"domain"`
		ExpirationDate float64 `json:"expirationDate"`
		Path           string  `json:"path"`
		Secure         bool    `json:"secure"`
		Name           string  `json:"name"`
		Value          string  `json:"value"`
	}
	if err := json.Unmarshal(trimmed, &items); err != nil || len(items) == 0 {
		return nil, false
	}
	var sb strings.Builder
	sb.WriteString("# Netscape HTTP Cookie File\n")
	for _, c := range items {
		dom := strings.TrimSpace(c.Domain)
		if dom == "" || c.Name == "" {
			continue
		}
		inc := "FALSE"
		if strings.HasPrefix(dom, ".") {
			inc = "TRUE"
		}
		p := c.Path
		if p == "" {
			p = "/"
		}
		sec := "FALSE"
		if c.Secure {
			sec = "TRUE"
		}
		exp := int64(c.ExpirationDate)
		if exp < 0 {
			exp = 0
		}
		sb.WriteString(fmt.Sprintf("%s\t%s\t%s\t%s\t%d\t%s\t%s\n", dom, inc, p, sec, exp, c.Name, c.Value))
	}
	return []byte(sb.String()), true
}

// handleUploadCookiesFile lets Owner upload YouTube cookies.txt (Netscape OR Cookie-Editor JSON!) directly from the browser.
func (s *Server) handleUploadCookiesFile(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseMultipartForm(4 << 20); err != nil {
		writeErr(w, http.StatusBadRequest, "Gagal membaca upload cookies.txt: "+err.Error())
		return
	}
	file, _, err := r.FormFile("file")
	if err != nil {
		writeErr(w, http.StatusBadRequest, "Field 'file' tidak ditemukan")
		return
	}
	defer file.Close()

	data, err := io.ReadAll(io.LimitReader(file, 4<<20))
	if err != nil || len(bytes.TrimSpace(data)) == 0 {
		writeErr(w, http.StatusBadRequest, "File cookies.txt kosong atau tidak terbaca")
		return
	}
	convertedFromJSON := false
	if netscape, ok := convertJSONCookiesToNetscape(data); ok {
		data = netscape
		convertedFromJSON = true
	}
	dest := filepath.Join(s.Store.DataDir, "cookies.txt")
	if err := os.WriteFile(dest, data, 0o600); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	cfg := s.Store.GetSettings()
	cfg.CookiesFile = dest
	if err := s.Store.SaveSettings(cfg); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if s.Cookies != nil {
		s.Cookies.OnCookiesUploaded()
	}
	s.Broker.Log("system", "success", "YouTube cookies.txt berhasil di-upload & diaktifkan (auto keep-alive server aktif!).")
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"ok":                true,
		"cookiesFile":       dest,
		"sizeBytes":         len(data),
		"convertedFromJson": convertedFromJSON,
		"cookieHealth":      s.Cookies.Status(),
	})
}

// handleCookieHealthStatus returns strictly non-secret server-side YouTube cookie keep-alive status.
func (s *Server) handleCookieHealthStatus(w http.ResponseWriter, r *http.Request) {
	if s.Cookies == nil {
		writeJSON(w, http.StatusOK, map[string]interface{}{"state": "unknown"})
		return
	}
	writeJSON(w, http.StatusOK, s.Cookies.Status())
}

// handleRefreshCookiesNow lets Owner manually trigger an immediate server-side yt-dlp cookie keep-alive check & refresh.
func (s *Server) handleRefreshCookiesNow(w http.ResponseWriter, r *http.Request) {
	if s.Cookies == nil {
		writeErr(w, http.StatusServiceUnavailable, "CookieKeeper tidak aktif")
		return
	}
	st, err := s.Cookies.RefreshNow(r.Context())
	if err != nil {
		errMsg := strings.TrimSpace(st.Message)
		if st.LastError != "" {
			if errMsg != "" {
				errMsg += " — " + st.LastError
			} else {
				errMsg = st.LastError
			}
		}
		if errMsg == "" {
			errMsg = err.Error()
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"ok":           false,
			"error":        errMsg,
			"cookieHealth": st,
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"ok":           true,
		"cookieHealth": st,
	})
}
