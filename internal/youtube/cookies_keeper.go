package youtube

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"klipforge/internal/deps"
	"klipforge/internal/model"
)

const (
	defaultKeepAliveInterval = 3 * time.Hour
	defaultProbeVideoURL     = "https://www.youtube.com/watch?v=jNQXAC9IVRw"
)

// CookieHealthStatus exposes strictly non-secret health metadata for YouTube cookies.txt.
type CookieHealthStatus struct {
	Configured           bool       `json:"configured"`
	State                string     `json:"state"` // "healthy" | "expired" | "missing" | "unknown"
	CookieCount          int        `json:"cookieCount"`
	HasAuthTokens        bool       `json:"hasAuthTokens"`
	KeepAliveEnabled     bool       `json:"keepAliveEnabled"`
	KeepAliveIntervalMin int        `json:"keepAliveIntervalMin"`
	LastCheckedAt        *time.Time `json:"lastCheckedAt,omitempty"`
	LastRefreshedAt      *time.Time `json:"lastRefreshedAt,omitempty"`
	NextCheckAt          *time.Time `json:"nextCheckAt,omitempty"`
	Message              string     `json:"message"`
	LastError            string     `json:"lastError,omitempty"`
}

// CookieKeeper runs inside the Go engine (e.g. InstaBox Cloud) while the engine is awake.
// It safely refreshes Set-Cookie rotation tokens into cookies.txt via yt-dlp without racing workers
// and tracks expired state without ever waking a sleeping machine from outside.
type CookieKeeper struct {
	Deps        *deps.Manager
	GetSettings func() model.AppSettings
	OnLog       func(level, message string)
	OnExpired   func(reason string)

	Interval time.Duration
	ProbeURL string
	// Optional test hook replacing the real yt-dlp command execution.
	probeFn func(ctx context.Context, tmpCookiePath string) (string, error)

	mu              sync.Mutex
	statusMu        sync.RWMutex
	status          CookieHealthStatus
	expiredNotified bool
}

func NewCookieKeeper(d *deps.Manager, getSettings func() model.AppSettings) *CookieKeeper {
	return &CookieKeeper{
		Deps:        d,
		GetSettings: getSettings,
		Interval:    defaultKeepAliveInterval,
		ProbeURL:    defaultProbeVideoURL,
		status: CookieHealthStatus{
			State:                "unknown",
			KeepAliveEnabled:     true,
			KeepAliveIntervalMin: int(defaultKeepAliveInterval / time.Minute),
			Message:              "Belum diperiksa",
		},
	}
}

// Start launches the in-process keep-alive loop (runs ONLY while the Go engine is already awake).
func (k *CookieKeeper) Start(ctx context.Context) {
	if k == nil {
		return
	}
	interval := k.Interval
	if interval <= 0 {
		interval = defaultKeepAliveInterval
	}
	k.refreshMetadataOnly()

	go func() {
		// Short settle delay on boot so HTTP server comes up immediately
		bootTimer := time.NewTimer(12 * time.Second)
		select {
		case <-ctx.Done():
			bootTimer.Stop()
			return
		case <-bootTimer.C:
		}
		_, _ = k.RefreshNow(ctx)

		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				_, _ = k.RefreshNow(ctx)
			}
		}
	}()
}

// Status returns the latest non-secret cookie health snapshot.
func (k *CookieKeeper) Status() CookieHealthStatus {
	if k == nil {
		return CookieHealthStatus{State: "unknown", Message: "Cookie keeper nonaktif"}
	}
	k.statusMu.RLock()
	st := k.status
	k.statusMu.RUnlock()
	return st
}

// OnCookiesUploaded resets expiry alert state, updates the safe backup, and verifies the newly uploaded cookies asynchronously.
func (k *CookieKeeper) OnCookiesUploaded() {
	if k == nil {
		return
	}
	k.mu.Lock()
	k.expiredNotified = false
	cookiesPath := k.resolveCookiesPath()
	if cookiesPath != "" {
		if data, err := os.ReadFile(cookiesPath); err == nil {
			cnt, hasAuth := ParseNetscapeYouTubeStats(data)
			if hasAuth {
				_ = writeFileAtomic(cookiesPath+".good", data, 0o600)
			}
			now := time.Now()
			k.statusMu.Lock()
			k.status.Configured = true
			k.status.CookieCount = cnt
			k.status.HasAuthTokens = hasAuth
			k.status.LastRefreshedAt = &now
			k.status.LastError = ""
			if hasAuth {
				k.status.State = "healthy"
				k.status.Message = fmt.Sprintf("Cookie baru di-upload (%d baris YouTube), memverifikasi...", cnt)
			} else {
				k.status.State = "expired"
				k.status.Message = "File cookies.txt tidak mengandung token login YouTube"
			}
			k.statusMu.Unlock()
		}
	}
	k.mu.Unlock()

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
		defer cancel()
		_, _ = k.RefreshNow(ctx)
	}()
}

func (k *CookieKeeper) resolveCookiesPath() string {
	if k.GetSettings == nil {
		return ""
	}
	return strings.TrimSpace(k.GetSettings().CookiesFile)
}

func (k *CookieKeeper) refreshMetadataOnly() {
	path := k.resolveCookiesPath()
	if path == "" {
		k.setStatus(CookieHealthStatus{
			Configured:           false,
			State:                "missing",
			KeepAliveEnabled:     true,
			KeepAliveIntervalMin: int(k.effectiveInterval() / time.Minute),
			Message:              "Belum ada cookies.txt (upload di Pengaturan Engine)",
		})
		return
	}
	data, err := os.ReadFile(path)
	if err != nil {
		k.setStatus(CookieHealthStatus{
			Configured:           false,
			State:                "missing",
			KeepAliveEnabled:     true,
			KeepAliveIntervalMin: int(k.effectiveInterval() / time.Minute),
			Message:              "File cookies.txt tidak ditemukan",
		})
		return
	}
	cnt, hasAuth := ParseNetscapeYouTubeStats(data)
	state := "unknown"
	msg := fmt.Sprintf("Cookie terpasang (%d baris YouTube), menunggu verifikasi", cnt)
	if !hasAuth {
		state = "expired"
		msg = "File cookies.txt tidak punya token auth YouTube"
	}
	k.setStatus(CookieHealthStatus{
		Configured:           true,
		State:                state,
		CookieCount:          cnt,
		HasAuthTokens:        hasAuth,
		KeepAliveEnabled:     true,
		KeepAliveIntervalMin: int(k.effectiveInterval() / time.Minute),
		Message:              msg,
	})
}

func (k *CookieKeeper) effectiveInterval() time.Duration {
	if k.Interval > 0 {
		return k.Interval
	}
	return defaultKeepAliveInterval
}

func (k *CookieKeeper) setStatus(st CookieHealthStatus) {
	k.statusMu.Lock()
	k.status = st
	k.statusMu.Unlock()
}

// RefreshNow safely copies cookies.txt to a temp file, runs a lightweight yt-dlp probe
// so YouTube Set-Cookie rotations are written cleanly, validates auth tokens, and atomically swaps back.
func (k *CookieKeeper) RefreshNow(ctx context.Context) (CookieHealthStatus, error) {
	if k == nil {
		return CookieHealthStatus{State: "unknown"}, nil
	}
	k.mu.Lock()
	defer k.mu.Unlock()

	now := time.Now()
	next := now.Add(k.effectiveInterval())
	cookiesPath := k.resolveCookiesPath()
	if cookiesPath == "" {
		st := CookieHealthStatus{
			Configured:           false,
			State:                "missing",
			KeepAliveEnabled:     true,
			KeepAliveIntervalMin: int(k.effectiveInterval() / time.Minute),
			LastCheckedAt:        &now,
			NextCheckAt:          &next,
			Message:              "cookies.txt belum dikonfigurasi",
		}
		k.setStatus(st)
		return st, nil
	}

	origData, err := os.ReadFile(cookiesPath)
	if err != nil || len(bytes.TrimSpace(origData)) == 0 {
		// Auto-recover from atomic backup if file was ever truncated by a racing process
		if bakData, bakErr := os.ReadFile(cookiesPath + ".good"); bakErr == nil && len(bytes.TrimSpace(bakData)) > 0 {
			if _, ok := ParseNetscapeYouTubeStats(bakData); ok {
				_ = writeFileAtomic(cookiesPath, bakData, 0o600)
				origData = bakData
				err = nil
				k.log("warn", "CookieKeeper memulihkan cookies.txt otomatis dari backup aman (.good).")
			}
		}
	}
	if err != nil || len(bytes.TrimSpace(origData)) == 0 {
		st := CookieHealthStatus{
			Configured:           false,
			State:                "missing",
			KeepAliveEnabled:     true,
			KeepAliveIntervalMin: int(k.effectiveInterval() / time.Minute),
			LastCheckedAt:        &now,
			NextCheckAt:          &next,
			Message:              "File cookies.txt hilang atau kosong",
		}
		k.setStatus(st)
		return st, fmt.Errorf("cookies file missing: %s", cookiesPath)
	}

	origCount, origHasAuth := ParseNetscapeYouTubeStats(origData)
	if !origHasAuth {
		// Try backup before declaring broken
		if bakData, bakErr := os.ReadFile(cookiesPath + ".good"); bakErr == nil {
			if bakCnt, bakAuth := ParseNetscapeYouTubeStats(bakData); bakAuth {
				_ = writeFileAtomic(cookiesPath, bakData, 0o600)
				origData = bakData
				origCount = bakCnt
				origHasAuth = true
				k.log("warn", "CookieKeeper memulihkan token auth cookies.txt dari backup .good.")
			}
		}
	}
	if !origHasAuth {
		st := CookieHealthStatus{
			Configured:           true,
			State:                "expired",
			CookieCount:          origCount,
			HasAuthTokens:        false,
			KeepAliveEnabled:     true,
			KeepAliveIntervalMin: int(k.effectiveInterval() / time.Minute),
			LastCheckedAt:        &now,
			NextCheckAt:          &next,
			Message:              "cookies.txt tidak memiliki token login YouTube — upload ulang di Pengaturan",
		}
		k.setStatus(st)
		k.notifyExpiredOnce(st.Message)
		return st, fmt.Errorf("%s", st.Message)
	}

	// Copy to isolated temp file so yt-dlp writes rotated Set-Cookie values safely without colliding with workers
	tmpDir := filepath.Dir(cookiesPath)
	tmpFile, err := os.CreateTemp(tmpDir, "kf-cookie-keep-*.txt")
	if err != nil {
		return k.Status(), err
	}
	tmpPath := tmpFile.Name()
	_, wErr := tmpFile.Write(origData)
	_ = tmpFile.Close()
	defer os.Remove(tmpPath)
	if wErr != nil {
		return k.Status(), wErr
	}

	probeOut, probeErr := k.runProbe(ctx, tmpPath)
	if probeErr != nil {
		lower := strings.ToLower(probeOut + " " + probeErr.Error())
		isExpired := strings.Contains(lower, "sign in to confirm") ||
			strings.Contains(lower, "not a bot") ||
			strings.Contains(lower, "cookies are no longer valid") ||
			strings.Contains(lower, "login required") ||
			strings.Contains(lower, "confirm your age")

		prev := k.Status()
		st := CookieHealthStatus{
			Configured:           true,
			State:                "unknown",
			CookieCount:          origCount,
			HasAuthTokens:        origHasAuth,
			KeepAliveEnabled:     true,
			KeepAliveIntervalMin: int(k.effectiveInterval() / time.Minute),
			LastCheckedAt:        &now,
			LastRefreshedAt:      prev.LastRefreshedAt,
			NextCheckAt:          &next,
			LastError:            compactSingleLine(probeOut, probeErr.Error(), 200),
		}
		if isExpired {
			st.State = "expired"
			st.Message = "Cookie YouTube kedaluwarsa — upload cookies.txt baru (Incognito) di Pengaturan"
			k.setStatus(st)
			k.log("warn", "Cookie YouTube terdeteksi kedaluwarsa di InstaBox. Upload ulang cookies.txt (Incognito) di Pengaturan Engine.")
			k.notifyExpiredOnce(st.Message)
		} else {
			// Transient network error: preserve previous healthy state if known, do not falsely alarm
			if prev.State == "healthy" {
				st.State = "healthy"
			}
			st.Message = "Cek keep-alive gagal sementara (jaringan/yt-dlp), cookie lama tetap dipertahankan"
			k.setStatus(st)
			k.log("info", "Cookie keep-alive tertunda sementara: "+st.LastError)
		}
		return st, probeErr
	}

	// Probe succeeded! Inspect refreshed temp cookie file before committing atomically
	finalData := origData
	finalCount := origCount
	if updatedData, rErr := os.ReadFile(tmpPath); rErr == nil && len(bytes.TrimSpace(updatedData)) > 0 {
		if updCount, updAuth := ParseNetscapeYouTubeStats(updatedData); updAuth && updCount >= 2 {
			finalData = updatedData
			finalCount = updCount
		}
	}

	// Atomically persist refreshed cookies + clean backup
	_ = writeFileAtomic(cookiesPath, finalData, 0o600)
	_ = writeFileAtomic(cookiesPath+".good", finalData, 0o600)

	k.expiredNotified = false
	st := CookieHealthStatus{
		Configured:           true,
		State:                "healthy",
		CookieCount:          finalCount,
		HasAuthTokens:        true,
		KeepAliveEnabled:     true,
		KeepAliveIntervalMin: int(k.effectiveInterval() / time.Minute),
		LastCheckedAt:        &now,
		LastRefreshedAt:      &now,
		NextCheckAt:          &next,
		Message:              fmt.Sprintf("Aktif di server (%d baris YouTube, auto keep-alive tiap %dj)", finalCount, int(k.effectiveInterval()/time.Hour)),
	}
	k.setStatus(st)
	k.log("info", fmt.Sprintf("Cookie YouTube aktif & disegarkan langsung di server (%d baris YouTube).", finalCount))
	return st, nil
}

func (k *CookieKeeper) runProbe(ctx context.Context, tmpCookiePath string) (string, error) {
	if k.probeFn != nil {
		return k.probeFn(ctx, tmpCookiePath)
	}
	if k.Deps == nil {
		return "", fmt.Errorf("deps manager unavailable")
	}
	st := k.Deps.GetStatus()
	if st.YtDlpPath == "" {
		return "", fmt.Errorf("yt-dlp tidak ditemukan")
	}
	probeURL := k.ProbeURL
	if probeURL == "" {
		probeURL = defaultProbeVideoURL
	}
	args := []string{
		"--cookies", tmpCookiePath,
		"--skip-download",
		"--no-playlist",
		"--no-warnings",
		"--quiet",
	}
	if _, err := exec.LookPath("node"); err == nil {
		args = append(args, "--js-runtimes", "node")
	}
	args = append(args, probeURL)

	cctx, cancel := context.WithTimeout(ctx, 35*time.Second)
	defer cancel()
	cmd := exec.CommandContext(cctx, st.YtDlpPath, args...)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	err := cmd.Run()
	return strings.TrimSpace(out.String()), err
}

func (k *CookieKeeper) log(level, msg string) {
	if k.OnLog != nil {
		k.OnLog(level, msg)
	}
}

func (k *CookieKeeper) notifyExpiredOnce(reason string) {
	if k.expiredNotified {
		return
	}
	k.expiredNotified = true
	if k.OnExpired != nil {
		k.OnExpired(reason)
	}
}

// ParseNetscapeYouTubeStats inspects Netscape cookie content without ever returning secret values.
func ParseNetscapeYouTubeStats(data []byte) (ytCount int, hasAuth bool) {
	authHits := 0
	lines := strings.Split(string(data), "\n")
	for _, raw := range lines {
		ln := strings.TrimSpace(raw)
		if ln == "" {
			continue
		}
		if strings.HasPrefix(ln, "#") && !strings.HasPrefix(ln, "#HttpOnly_") {
			continue
		}
		cols := strings.Split(ln, "\t")
		if len(cols) < 7 {
			continue
		}
		dom := strings.TrimPrefix(cols[0], "#HttpOnly_")
		if !strings.Contains(strings.ToLower(dom), "youtube.com") &&
			!strings.Contains(strings.ToLower(dom), "youtu.be") &&
			!strings.Contains(strings.ToLower(dom), "googlevideo.com") {
			continue
		}
		ytCount++
		name := strings.TrimSpace(cols[5])
		val := strings.TrimSpace(cols[6])
		if val == "" {
			continue
		}
		switch name {
		case "LOGIN_INFO", "SID", "__Secure-1PSID", "__Secure-3PSID", "SAPISID":
			authHits++
		}
	}
	return ytCount, authHits >= 2
}

func writeFileAtomic(dest string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(dest)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".kf-atomic-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}
	_ = os.Chmod(tmpPath, perm)
	if err := os.Rename(tmpPath, dest); err != nil {
		// Windows fallback when destination exists
		_ = os.Remove(dest)
		if err2 := os.Rename(tmpPath, dest); err2 != nil {
			_ = os.Remove(tmpPath)
			return err2
		}
	}
	return nil
}

func compactSingleLine(out, fallback string, maxLen int) string {
	s := strings.TrimSpace(out)
	if s == "" {
		s = strings.TrimSpace(fallback)
	}
	s = strings.ReplaceAll(s, "\r\n", " | ")
	s = strings.ReplaceAll(s, "\n", " | ")
	if len(s) > maxLen && maxLen > 3 {
		return s[:maxLen-3] + "..."
	}
	return s
}
