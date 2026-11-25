package audiofx

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"klipforge/internal/model"
)

// TrendingSound represents a scraped/discovered TikTok viral sound or meme SFX ready for 1-click import.
type TrendingSound struct {
	ID            string  `json:"id"`
	Title         string  `json:"title"`
	Creator       string  `json:"creator"`
	Category      string  `json:"category"`
	SourceURL     string  `json:"sourceUrl"`
	Thumbnail     string  `json:"thumbnail,omitempty"`
	Duration      float64 `json:"duration"`
	ViewCount     int64   `json:"viewCount"`
	SuggestedKind string  `json:"suggestedKind"` // "bgm" or "sfx"
	Source        string  `json:"source"`
}

type trendCacheEntry struct {
	FetchedAt time.Time       `json:"fetchedAt"`
	Items     []TrendingSound `json:"items"`
}

var (
	trendCacheMu sync.RWMutex
	trendCache   = make(map[string]trendCacheEntry)
)

// NormalizeRadarURL canonicalizes YouTube/TikTok URLs so minor query/variant differences match reliably.
func NormalizeRadarURL(rawURL string) string {
	u := strings.TrimSpace(rawURL)
	if u == "" {
		return ""
	}
	parsed, err := url.Parse(u)
	if err != nil || parsed.Hostname() == "" {
		return strings.ToLower(u)
	}
	host := strings.ToLower(parsed.Hostname())
	host = strings.TrimPrefix(host, "www.")
	host = strings.TrimPrefix(host, "m.")
	if host == "youtu.be" {
		id := strings.Trim(parsed.Path, "/")
		if id != "" {
			return "https://www.youtube.com/watch?v=" + id
		}
	}
	if host == "youtube.com" {
		if v := strings.TrimSpace(parsed.Query().Get("v")); v != "" {
			return "https://www.youtube.com/watch?v=" + v
		}
		if strings.HasPrefix(parsed.Path, "/shorts/") {
			id := strings.Trim(strings.TrimPrefix(parsed.Path, "/shorts/"), "/")
			if idx := strings.IndexByte(id, '/'); idx >= 0 {
				id = id[:idx]
			}
			if id != "" {
				return "https://www.youtube.com/watch?v=" + id
			}
		}
	}
	scheme := strings.ToLower(parsed.Scheme)
	if scheme == "" {
		scheme = "https"
	}
	cleanPath := strings.TrimRight(parsed.Path, "/")
	if parsed.RawQuery != "" {
		return fmt.Sprintf("%s://%s%s?%s", scheme, host, cleanPath, parsed.RawQuery)
	}
	return fmt.Sprintf("%s://%s%s", scheme, host, cleanPath)
}

func (l *Library) radarURLsPath() string {
	return filepath.Join(l.AudioDir, "radar_urls.json")
}

func (l *Library) loadRadarURLs() {
	raw, err := os.ReadFile(l.radarURLsPath())
	if err != nil {
		return
	}
	var loaded map[string]time.Time
	if json.Unmarshal(raw, &loaded) != nil {
		return
	}
	cutoff := time.Now().Add(-7 * 24 * time.Hour)
	l.mu.Lock()
	for k, ts := range loaded {
		if ts.After(cutoff) {
			l.radarURLs[k] = ts
		}
	}
	l.mu.Unlock()
}

func (l *Library) saveRadarURLsLocked() {
	cutoff := time.Now().Add(-7 * 24 * time.Hour)
	for k, ts := range l.radarURLs {
		if ts.Before(cutoff) {
			delete(l.radarURLs, k)
		}
	}
	// Cap to 500 most recent entries so file never grows unbounded
	if len(l.radarURLs) > 500 {
		type kv struct {
			k string
			t time.Time
		}
		all := make([]kv, 0, len(l.radarURLs))
		for k, t := range l.radarURLs {
			all = append(all, kv{k: k, t: t})
		}
		sort.Slice(all, func(i, j int) bool { return all[i].t.After(all[j].t) })
		trimmed := make(map[string]time.Time, 500)
		for _, item := range all[:500] {
			trimmed[item.k] = item.t
		}
		l.radarURLs = trimmed
	}
	if b, err := json.MarshalIndent(l.radarURLs, "", "  "); err == nil {
		_ = os.WriteFile(l.radarURLsPath(), b, 0o644)
	}
}

// RecordRadarURL registers a URL discovered by the trending radar so scoped agent imports are permitted for it.
func (l *Library) RecordRadarURL(rawURL string) {
	norm := NormalizeRadarURL(rawURL)
	if norm == "" {
		return
	}
	l.mu.Lock()
	if l.radarURLs == nil {
		l.radarURLs = make(map[string]time.Time)
	}
	l.radarURLs[norm] = time.Now()
	l.saveRadarURLsLocked()
	l.mu.Unlock()
}

// RecordRadarItems batch-registers all URLs returned by the trending radar.
func (l *Library) RecordRadarItems(items []TrendingSound) {
	if len(items) == 0 {
		return
	}
	now := time.Now()
	l.mu.Lock()
	if l.radarURLs == nil {
		l.radarURLs = make(map[string]time.Time)
	}
	for _, it := range items {
		if norm := NormalizeRadarURL(it.SourceURL); norm != "" {
			l.radarURLs[norm] = now
		}
	}
	l.saveRadarURLsLocked()
	l.mu.Unlock()
}

// IsKnownRadarURL returns true ONLY if rawURL was previously returned by GET /api/audio/trending (within 7 days).
func (l *Library) IsKnownRadarURL(rawURL string) bool {
	norm := NormalizeRadarURL(rawURL)
	if norm == "" {
		return false
	}
	cutoff := time.Now().Add(-7 * 24 * time.Hour)
	l.mu.RLock()
	ts, ok := l.radarURLs[norm]
	l.mu.RUnlock()
	return ok && ts.After(cutoff)
}

// TrendingCategorySpec maps curated TikTok trend categories to clean search queries & duration caps
// (automatically filtering out 1-hour compilation mixes so only real clippable sounds appear!).
type trendingQuerySpec struct {
	Query         string
	SuggestedKind string
	MinDurSec     float64
	MaxDurSec     float64
	Label         string
}

func resolveTrendingQuery(category, customQuery string) trendingQuerySpec {
	q := strings.TrimSpace(customQuery)
	if q != "" {
		kind := "bgm"
		low := strings.ToLower(q)
		if strings.Contains(low, "sfx") || strings.Contains(low, "meme") || strings.Contains(low, "sound effect") || strings.Contains(low, "roblox") || strings.Contains(low, "boom") {
			kind = "sfx"
		}
		return trendingQuerySpec{
			Query:         fmt.Sprintf("ytsearch15:%s tiktok sound", q),
			SuggestedKind: kind,
			MinDurSec:     0.3,
			MaxDurSec:     260,
			Label:         "Custom: " + q,
		}
	}
	switch strings.ToLower(strings.TrimSpace(category)) {
	case "meme_sfx", "sfx", "meme":
		return trendingQuerySpec{
			Query:         "ytsearch18:sound effect meme viral tiktok roblox ahh vine boom sfx",
			SuggestedKind: "sfx",
			MinDurSec:     0.3,
			MaxDurSec:     40,
			Label:         "Meme SFX Viral",
		}
	case "phonk_gaming", "phonk", "gaming":
		return trendingQuerySpec{
			Query:         "ytsearch18:tiktok viral phonk jedag jedug gaming sound short",
			SuggestedKind: "bgm",
			MinDurSec:     10,
			MaxDurSec:     240,
			Label:         "Phonk & Jedag-Jedug",
		}
	case "windah_viral", "windah":
		return trendingQuerySpec{
			Query:         "ytsearch16:sound meme windah basudara viral tiktok short",
			SuggestedKind: "sfx",
			MinDurSec:     0.5,
			MaxDurSec:     120,
			Label:         "Windah / Gaming Viral",
		}
	default: // "id_viral"
		return trendingQuerySpec{
			Query:         "ytsearch18:sound viral tiktok indonesia fyp terbaru short audio",
			SuggestedKind: "bgm",
			MinDurSec:     8,
			MaxDurSec:     240,
			Label:         "Viral TikTok ID",
		}
	}
}

// filterAndRankTrending filters out long compilation mixes and ranks trending sounds by view count.
func filterAndRankTrending(raw []TrendingSound, spec trendingQuerySpec, limit int) []TrendingSound {
	seen := make(map[string]bool)
	out := make([]TrendingSound, 0, len(raw))
	for _, item := range raw {
		if item.ID == "" || item.SourceURL == "" || seen[item.ID] {
			continue
		}
		// Reject 1-hour compilations / full albums!
		if item.Duration > 0 && (item.Duration < spec.MinDurSec || item.Duration > spec.MaxDurSec) {
			continue
		}
		low := strings.ToLower(item.Title)
		if strings.Contains(low, "full album") || strings.Contains(low, "1 hour") || strings.Contains(low, "nonstop 20") {
			continue
		}
		seen[item.ID] = true
		if item.Duration > 0 && item.Duration <= 15 {
			item.SuggestedKind = "sfx"
		} else if item.SuggestedKind == "" {
			item.SuggestedKind = spec.SuggestedKind
		}
		if item.Category == "" {
			item.Category = spec.Label
		}
		out = append(out, item)
	}
	sort.SliceStable(out, func(i, j int) bool {
		return out[i].ViewCount > out[j].ViewCount
	})
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out
}

// FetchTrendingSounds scrapes/discovers current viral TikTok Indonesia & meme sounds using yt-dlp
// (zero CAPTCHA bypass, zero paid API keys, 30-min in-memory cache).
func (l *Library) FetchTrendingSounds(ctx context.Context, category, customQuery string, forceRefresh bool, settings model.AppSettings) ([]TrendingSound, error) {
	catKey := strings.ToLower(strings.TrimSpace(category))
	if catKey == "" {
		catKey = "id_viral"
	}
	cacheKey := catKey + "::" + strings.ToLower(strings.TrimSpace(customQuery))

	if !forceRefresh {
		trendCacheMu.RLock()
		if ent, ok := trendCache[cacheKey]; ok && time.Since(ent.FetchedAt) < 30*time.Minute && len(ent.Items) > 0 {
			cp := append([]TrendingSound(nil), ent.Items...)
			trendCacheMu.RUnlock()
			l.RecordRadarItems(cp)
			return cp, nil
		}
		trendCacheMu.RUnlock()
	}

	ytBin := l.Deps.GetStatus().YtDlpPath
	if ytBin == "" {
		return nil, fmt.Errorf("yt-dlp tidak ditemukan")
	}

	spec := resolveTrendingQuery(catKey, customQuery)
	args := []string{"--flat-playlist", "--no-warnings", "-J", spec.Query}
	if _, err := exec.LookPath("node"); err == nil {
		args = append(args, "--js-runtimes", "node", "--remote-components", "ejs:github")
	}
	if settings.CookiesFile != "" {
		if tmpC, cleanC := cloneTempCookies(settings.CookiesFile); tmpC != "" {
			defer cleanC()
			args = append(args, "--cookies", tmpC)
		}
	}

	cctx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()
	cmd := exec.CommandContext(cctx, ytBin, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("gagal mengambil sound trend: %s", strings.TrimSpace(stderr.String()))
	}

	var parsed struct {
		Entries []struct {
			ID         string  `json:"id"`
			Title      string  `json:"title"`
			Channel    string  `json:"channel"`
			Uploader   string  `json:"uploader"`
			URL        string  `json:"url"`
			WebpageURL string  `json:"webpage_url"`
			Duration   float64 `json:"duration"`
			ViewCount  int64   `json:"view_count"`
			Thumbnails []struct {
				URL string `json:"url"`
			} `json:"thumbnails"`
		} `json:"entries"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &parsed); err != nil {
		return nil, fmt.Errorf("gagal parsing hasil trend: %w", err)
	}

	raw := make([]TrendingSound, 0, len(parsed.Entries))
	for _, e := range parsed.Entries {
		srcURL := strings.TrimSpace(e.WebpageURL)
		if srcURL == "" {
			srcURL = strings.TrimSpace(e.URL)
		}
		if srcURL != "" && !strings.HasPrefix(srcURL, "http") {
			srcURL = "https://www.youtube.com/watch?v=" + e.ID
		}
		creator := strings.TrimSpace(e.Channel)
		if creator == "" {
			creator = strings.TrimSpace(e.Uploader)
		}
		thumb := ""
		if len(e.Thumbnails) > 0 {
			thumb = e.Thumbnails[0].URL
		}
		raw = append(raw, TrendingSound{
			ID:            e.ID,
			Title:         strings.TrimSpace(e.Title),
			Creator:       creator,
			Category:      spec.Label,
			SourceURL:     srcURL,
			Thumbnail:     thumb,
			Duration:      e.Duration,
			ViewCount:     e.ViewCount,
			SuggestedKind: spec.SuggestedKind,
			Source:        "tiktok_trend",
		})
	}

	items := filterAndRankTrending(raw, spec, 12)
	trendCacheMu.Lock()
	trendCache[cacheKey] = trendCacheEntry{FetchedAt: time.Now(), Items: items}
	trendCacheMu.Unlock()
	l.RecordRadarItems(items)
	return items, nil
}

// ValidateImportURL ensures only safe http/https URLs are passed to yt-dlp for sound extraction.
func ValidateImportURL(rawURL string) (string, error) {
	u := strings.TrimSpace(rawURL)
	if u == "" {
		return "", fmt.Errorf("URL sound wajib diisi")
	}
	parsed, err := url.Parse(u)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Hostname() == "" {
		return "", fmt.Errorf("URL tidak valid (harus diawali https://)")
	}
	return u, nil
}

// ImportSoundFromURL downloads ONLY the audio track from a TikTok / YouTube URL via yt-dlp,
// normalizes & trims it via FFmpeg (max 45s for BGM, max 12s for SFX), and saves it into the Audio Library!
func (l *Library) ImportSoundFromURL(ctx context.Context, rawURL, name, kind, category string, maxDurSec float64, settings model.AppSettings) (*model.AudioAsset, error) {
	cleanURL, err := ValidateImportURL(rawURL)
	if err != nil {
		return nil, err
	}
	kind = strings.ToLower(strings.TrimSpace(kind))
	if kind != "bgm" && kind != "sfx" {
		kind = "bgm"
	}
	if maxDurSec <= 0 {
		if kind == "sfx" {
			maxDurSec = 12.0
		} else {
			maxDurSec = 45.0
		}
	}
	if maxDurSec > 90 {
		maxDurSec = 90
	}

	st := l.Deps.GetStatus()
	if st.YtDlpPath == "" || st.FFmpegPath == "" {
		return nil, fmt.Errorf("yt-dlp dan ffmpeg diperlukan untuk import sound")
	}

	tmpDir, err := os.MkdirTemp("", "kf-trend-imp-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmpDir)

	rawTpl := filepath.Join(tmpDir, "raw.%(ext)s")
	ytArgs := []string{
		"--no-playlist",
		"--no-warnings",
		"-f", "bestaudio/best",
		"-o", rawTpl,
	}
	if _, err := exec.LookPath("node"); err == nil {
		ytArgs = append(ytArgs, "--js-runtimes", "node", "--remote-components", "ejs:github")
	}
	if dir := l.Deps.FFmpegDir(); dir != "" {
		ytArgs = append(ytArgs, "--ffmpeg-location", dir)
	}
	if settings.CookiesFile != "" {
		if tmpC, cleanC := cloneTempCookies(settings.CookiesFile); tmpC != "" {
			defer cleanC()
			ytArgs = append(ytArgs, "--cookies", tmpC)
		}
	} else if settings.CookiesBrowser != "" {
		ytArgs = append(ytArgs, "--cookies-from-browser", settings.CookiesBrowser)
	}
	ytArgs = append(ytArgs, cleanURL)

	cctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(cctx, st.YtDlpPath, ytArgs...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("gagal mengunduh audio dari link: %s", strings.TrimSpace(stderr.String()))
	}

	matches, _ := filepath.Glob(filepath.Join(tmpDir, "raw.*"))
	if len(matches) == 0 {
		return nil, fmt.Errorf("file audio hasil download tidak ditemukan")
	}
	rawPath := matches[0]

	ext := ".mp3"
	afChain := "loudnorm=I=-14:TP=-1.5:LRA=11"
	if kind == "sfx" {
		ext = ".wav"
		// Sub-second SFX (Vine Boom, Metal Pipe) distort under 3s EBU R128 loudnorm; use peak-safe limiter instead!
		afChain = "alimiter=limit=0.92:attack=3:release=40:level=disabled"
	}
	normPath := filepath.Join(tmpDir, "normalized"+ext)
	ffArgs := []string{
		"-y", "-hide_banner", "-loglevel", "error",
		"-i", rawPath,
		"-vn",
		"-t", fmt.Sprintf("%.2f", maxDurSec),
		"-ar", "48000", "-ac", "2",
		"-af", afChain,
	}
	if kind == "bgm" {
		ffArgs = append(ffArgs, "-c:a", "libmp3lame", "-b:a", "192k")
	}
	ffArgs = append(ffArgs, normPath)

	var ffErr bytes.Buffer
	ffCmd := exec.CommandContext(cctx, st.FFmpegPath, ffArgs...)
	ffCmd.Stderr = &ffErr
	if err := ffCmd.Run(); err != nil {
		return nil, fmt.Errorf("ffmpeg konversi audio gagal: %s", strings.TrimSpace(ffErr.String()))
	}

	actualDur := probeAudioDurationSec(cctx, st.FFprobePath, normPath, kind, maxDurSec)

	f, err := os.Open(normPath)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	cleanName := strings.TrimSpace(name)
	if cleanName == "" {
		cleanName = "TikTok Trend " + time.Now().Format("15:04")
	}
	if len(cleanName) > 60 {
		cleanName = strings.TrimSpace(cleanName[:60])
	}
	cleanCat := strings.TrimSpace(category)
	if cleanCat == "" {
		cleanCat = "TikTok Trend"
	}

	return l.AddUpload(kind, cleanName, cleanCat, "tiktok_trend"+ext, f, actualDur)
}

func cloneTempCookies(cookiesPath string) (string, func()) {
	noop := func() {}
	p := strings.TrimSpace(cookiesPath)
	if p == "" {
		return "", noop
	}
	data, err := os.ReadFile(p)
	if err != nil || len(bytes.TrimSpace(data)) == 0 {
		if bak, berr := os.ReadFile(p + ".good"); berr == nil && len(bytes.TrimSpace(bak)) > 0 {
			data = bak
		} else {
			return "", noop
		}
	}
	tmp, err := os.CreateTemp("", "kf-audio-cookie-*.txt")
	if err != nil {
		return p, noop
	}
	tp := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tp)
		return p, noop
	}
	_ = tmp.Close()
	_ = os.Chmod(tp, 0o600)
	return tp, func() { _ = os.Remove(tp) }
}

func probeAudioDurationSec(ctx context.Context, ffprobePath, mediaPath, kind string, fallback float64) float64 {
	if ffprobePath != "" {
		out, err := exec.CommandContext(ctx, ffprobePath,
			"-v", "error",
			"-show_entries", "format=duration",
			"-of", "default=noprint_wrappers=1:nokey=1",
			mediaPath,
		).Output()
		if err == nil {
			if d, perr := strconv.ParseFloat(strings.TrimSpace(string(out)), 64); perr == nil && d > 0.05 {
				return d
			}
		}
	}
	// Exact PCM WAV duration fallback (48kHz stereo 16-bit = 192,000 bytes/sec)
	if kind == "sfx" {
		if fi, err := os.Stat(mediaPath); err == nil && fi.Size() > 44 {
			return float64(fi.Size()-44) / 192000.0
		}
	}
	return fallback
}
