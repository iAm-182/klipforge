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
	if settings.CookiesFile != "" {
		if _, err := os.Stat(settings.CookiesFile); err == nil {
			args = append(args, "--cookies", settings.CookiesFile)
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
	if settings.CookiesFile != "" {
		if _, err := os.Stat(settings.CookiesFile); err == nil {
			ytArgs = append(ytArgs, "--cookies", settings.CookiesFile)
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
	if kind == "sfx" {
		ext = ".wav"
	}
	normPath := filepath.Join(tmpDir, "normalized"+ext)
	ffArgs := []string{
		"-y", "-hide_banner", "-loglevel", "error",
		"-i", rawPath,
		"-vn",
		"-t", fmt.Sprintf("%.2f", maxDurSec),
		"-ar", "48000", "-ac", "2",
		"-af", "loudnorm=I=-14:TP=-1.5:LRA=11",
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

	return l.AddUpload(kind, cleanName, cleanCat, "tiktok_trend"+ext, f, maxDurSec)
}
