package youtube

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"klipforge/internal/deps"
	"klipforge/internal/model"
)

// VideoMeta holds rich metadata returned by yt-dlp -J.
type VideoMeta struct {
	ID            string
	Title         string
	Channel       string
	ChannelURL    string
	Thumbnail     string
	Duration      float64
	ViewCount     int64
	UploadDate    string
	WasLive       bool
	Heatmap       []HeatmapPoint
	Chapters      []model.Chapter
	HasLiveChat   bool
	SubLang       string // Best subtitle language found ("id", "en", etc.)
	IsAutoSub     bool
	StreamURL     string // Direct stream URL for grabbing a single HD preview frame
}

// Client wraps yt-dlp and ffmpeg helpers for YouTube ingestion.
type Client struct {
	Deps *deps.Manager
}

func NewClient(d *deps.Manager) *Client {
	return &Client{Deps: d}
}

func (c *Client) baseArgs(settings model.AppSettings) []string {
	args := []string{
		"--no-playlist",
		"--no-warnings",
	}
	if _, err := exec.LookPath("node"); err == nil {
		args = append(args, "--js-runtimes", "node")
	}
	if dir := c.Deps.FFmpegDir(); dir != "" {
		args = append(args, "--ffmpeg-location", dir)
	}
	if settings.CookiesFile != "" {
		if _, err := os.Stat(settings.CookiesFile); err == nil {
			args = append(args, "--cookies", settings.CookiesFile)
		}
	} else if settings.CookiesBrowser != "" {
		args = append(args, "--cookies-from-browser", settings.CookiesBrowser)
	}
	return args
}

// FetchChannelVideos quickly lists the latest N videos/streams from a YouTube channel URL using --flat-playlist (~3s).
func (c *Client) FetchChannelVideos(ctx context.Context, channelURL string, limit int, settings model.AppSettings) ([]model.ChannelVideo, error) {
	ytBin := c.Deps.GetStatus().YtDlpPath
	if ytBin == "" {
		return nil, fmt.Errorf("yt-dlp tidak ditemukan")
	}
	if limit <= 0 {
		limit = 10
	}
	args := []string{
		"--flat-playlist",
		"--playlist-end", strconv.Itoa(limit),
		"--no-warnings",
		"-J",
		channelURL,
	}
	if _, err := exec.LookPath("node"); err == nil {
		args = append(args, "--js-runtimes", "node")
	}
	cmd := exec.CommandContext(ctx, ytBin, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("gagal scan channel: %s", strings.TrimSpace(stderr.String()))
	}
	var parsed struct {
		Entries []struct {
			ID         string  `json:"id"`
			Title      string  `json:"title"`
			URL        string  `json:"url"`
			Duration   float64 `json:"duration"`
			LiveStatus string  `json:"live_status"`
		} `json:"entries"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &parsed); err != nil {
		return nil, err
	}
	var out []model.ChannelVideo
	for _, e := range parsed.Entries {
		if e.ID == "" {
			continue
		}
		u := e.URL
		if u == "" || !strings.HasPrefix(u, "http") {
			u = "https://www.youtube.com/watch?v=" + e.ID
		}
		out = append(out, model.ChannelVideo{
			ID:         e.ID,
			Title:      e.Title,
			URL:        u,
			Duration:   e.Duration,
			LiveStatus: e.LiveStatus,
		})
	}
	return out, nil
}

// FetchMetadata extracts video metadata, Most Replayed heatmap, chapters, and available chat/subtitles.
func (c *Client) FetchMetadata(ctx context.Context, videoURL string, settings model.AppSettings) (*VideoMeta, error) {
	ytBin := c.Deps.GetStatus().YtDlpPath
	if ytBin == "" {
		return nil, fmt.Errorf("yt-dlp tidak ditemukan")
	}

	args := append(c.baseArgs(settings),
		"-J",
		"--skip-download",
		videoURL,
	)

	cmd := exec.CommandContext(ctx, ytBin, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return nil, fmt.Errorf("yt-dlp gagal ambil metadata: %s", msg)
	}

	var raw struct {
		ID                 string                       `json:"id"`
		Title              string                       `json:"title"`
		Uploader           string                       `json:"uploader"`
		Channel            string                       `json:"channel"`
		ChannelURL         string                       `json:"channel_url"`
		UploaderURL        string                       `json:"uploader_url"`
		Thumbnail          string                       `json:"thumbnail"`
		Duration           float64                      `json:"duration"`
		ViewCount          int64                        `json:"view_count"`
		UploadDate         string                       `json:"upload_date"`
		WasLive            bool                         `json:"was_live"`
		LiveStatus         string                       `json:"live_status"`
		Heatmap            []HeatmapPoint               `json:"heatmap"`
		Chapters           []struct {
			Title     string  `json:"title"`
			StartTime float64 `json:"start_time"`
			EndTime   float64 `json:"end_time"`
		} `json:"chapters"`
		Subtitles          map[string]json.RawMessage   `json:"subtitles"`
		AutomaticCaptions  map[string]json.RawMessage   `json:"automatic_captions"`
		URL                string                       `json:"url"`
		Formats            []struct {
			URL    string  `json:"url"`
			VCodec string  `json:"vcodec"`
			Ext    string  `json:"ext"`
			Height int     `json:"height"`
		} `json:"formats"`
	}

	if err := json.Unmarshal(stdout.Bytes(), &raw); err != nil {
		return nil, fmt.Errorf("gagal parse JSON metadata yt-dlp: %w", err)
	}

	meta := &VideoMeta{
		ID:         raw.ID,
		Title:      raw.Title,
		Channel:    raw.Channel,
		ChannelURL: raw.ChannelURL,
		Thumbnail:  raw.Thumbnail,
		Duration:   raw.Duration,
		ViewCount:  raw.ViewCount,
		UploadDate: raw.UploadDate,
		WasLive:    raw.WasLive || raw.LiveStatus == "was_live",
		Heatmap:    raw.Heatmap,
	}
	if meta.Channel == "" {
		meta.Channel = raw.Uploader
	}
	if meta.ChannelURL == "" {
		meta.ChannelURL = raw.UploaderURL
	}
	for _, ch := range raw.Chapters {
		meta.Chapters = append(meta.Chapters, model.Chapter{
			Title:     ch.Title,
			StartTime: ch.StartTime,
			EndTime:   ch.EndTime,
		})
	}

	// Detect live_chat availability
	if _, ok := raw.Subtitles["live_chat"]; ok {
		meta.HasLiveChat = true
	}

	// Pick best caption language (prefer Indonesian for Windah/ID creators, fallback English)
	preferredLangs := []string{"id", "id-orig", "en", "en-orig"}
	for _, lang := range preferredLangs {
		if _, ok := raw.Subtitles[lang]; ok {
			meta.SubLang = lang
			meta.IsAutoSub = false
			break
		}
	}
	if meta.SubLang == "" {
		for _, lang := range preferredLangs {
			if _, ok := raw.AutomaticCaptions[lang]; ok {
				meta.SubLang = lang
				meta.IsAutoSub = true
				break
			}
		}
	}

	// Pick a clean direct video stream URL (720p or 1080p) for grabbing 1 preview frame
	for _, f := range raw.Formats {
		if f.VCodec != "" && f.VCodec != "none" && f.URL != "" && f.Height >= 480 && f.Height <= 1080 && (f.Ext == "mp4" || f.Ext == "webm") {
			meta.StreamURL = f.URL
			if f.Height == 720 {
				break
			}
		}
	}

	// Fallback: if yt-dlp didn't return heatmap, try direct watch page HTML scrape
	if len(meta.Heatmap) == 0 && meta.ID != "" {
		if fallback, err := FetchHeatmapHTMLFallback(meta.ID); err == nil && len(fallback) > 0 {
			meta.Heatmap = fallback
		}
	}

	return meta, nil
}

// DownloadLiveChat downloads the YouTube live chat replay JSON into destDir with proper Windows process-tree timeout.
func (c *Client) DownloadLiveChat(ctx context.Context, videoURL, destDir string, settings model.AppSettings) (string, error) {
	outTmpl := filepath.Join(destDir, "source.%(ext)s")
	args := append(c.baseArgs(settings),
		"--skip-download",
		"--write-subs",
		"--sub-langs", "live_chat",
		"-o", outTmpl,
		videoURL,
	)

	cmd := exec.Command(c.Deps.GetStatus().YtDlpPath, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return "", err
	}

	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	select {
	case err := <-done:
		if err != nil {
			return "", fmt.Errorf("download live chat gagal: %s", strings.TrimSpace(stderr.String()))
		}
	case <-ctx.Done():
		// Kill entire process tree on Windows so PyInstaller yt-dlp child doesn't linger!
		if cmd.Process != nil {
			_ = exec.Command("taskkill", "/T", "/F", "/PID", strconv.Itoa(cmd.Process.Pid)).Run()
		}
		<-done
		return "", fmt.Errorf("download live chat melebihi batas waktu (VOD terlalu besar)")
	}

	matches, _ := filepath.Glob(filepath.Join(destDir, "*.live_chat.json"))
	if len(matches) > 0 {
		return matches[0], nil
	}
	return "", nil
}

// DownloadSubtitles downloads Indonesian/English VTT captions into destDir.
func (c *Client) DownloadSubtitles(ctx context.Context, videoURL, destDir, lang string, isAuto bool, settings model.AppSettings) (string, error) {
	if lang == "" {
		lang = "id,en"
	}
	outTmpl := filepath.Join(destDir, "subs.%(ext)s")
	args := append(c.baseArgs(settings),
		"--skip-download",
		"--sub-langs", lang,
		"--sub-format", "vtt/srt/best",
		"--convert-subs", "vtt",
		"-o", outTmpl,
	)
	if isAuto {
		args = append(args, "--write-auto-subs")
	} else {
		args = append(args, "--write-subs", "--write-auto-subs")
	}
	args = append(args, videoURL)

	cmd := exec.CommandContext(ctx, c.Deps.GetStatus().YtDlpPath, args...)
	_ = cmd.Run()

	matches, _ := filepath.Glob(filepath.Join(destDir, "subs*.vtt"))
	if len(matches) > 0 {
		return matches[0], nil
	}
	matchesSrt, _ := filepath.Glob(filepath.Join(destDir, "subs*.srt"))
	if len(matchesSrt) > 0 {
		return matchesSrt[0], nil
	}
	return "", nil
}

// GrabPreviewFrame grabs a single HD frame from the video stream (or falls back to thumbnail)
// so the user can visually position the Facecam & Gameplay crop boxes before downloading the full VOD!
func (c *Client) GrabPreviewFrame(ctx context.Context, streamURL, thumbnailURL string, seekSec float64, destJPG string) error {
	if streamURL != "" && c.Deps.GetStatus().FFmpegPath != "" {
		fCtx, cancel := context.WithTimeout(ctx, 18*time.Second)
		defer cancel()
		cmd := exec.CommandContext(fCtx, c.Deps.GetStatus().FFmpegPath,
			"-y", "-hide_banner", "-loglevel", "error",
			"-ss", fmt.Sprintf("%.2f", seekSec),
			"-i", streamURL,
			"-frames:v", "1",
			"-q:v", "3",
			destJPG,
		)
		if err := cmd.Run(); err == nil {
			if fi, err := os.Stat(destJPG); err == nil && fi.Size() > 1024 {
				return nil
			}
		}
	}

	// Fallback: download YouTube thumbnail directly
	if thumbnailURL != "" {
		client := &http.Client{Timeout: 10 * time.Second}
		resp, err := client.Get(thumbnailURL)
		if err == nil {
			defer resp.Body.Close()
			if resp.StatusCode == 200 {
				f, err := os.Create(destJPG)
				if err == nil {
					_, _ = io.Copy(f, resp.Body)
					f.Close()
					return nil
				}
			}
		}
	}
	return fmt.Errorf("gagal mengambil preview frame")
}

// GrabFrameFromLocalFile extracts a frame from an already-downloaded local video.
func (c *Client) GrabFrameFromLocalFile(ctx context.Context, localVideo string, seekSec float64, destJPG string) error {
	if c.Deps.GetStatus().FFmpegPath == "" {
		return fmt.Errorf("ffmpeg tidak ditemukan")
	}
	cmd := exec.CommandContext(ctx, c.Deps.GetStatus().FFmpegPath,
		"-y", "-hide_banner", "-loglevel", "error",
		"-ss", fmt.Sprintf("%.2f", seekSec),
		"-i", localVideo,
		"-frames:v", "1",
		"-q:v", "2",
		destJPG,
	)
	return cmd.Run()
}

var ytDlpPctRe = regexp.MustCompile(`(\d{1,3}(?:\.\d+)?)%`)

func formatSelector(quality string) string {
	switch quality {
	case "720p":
		return "bv*[height<=720][ext=mp4]+ba[ext=m4a]/b[height<=720][ext=mp4]/bv*[height<=720]+ba/best[height<=720]/best"
	case "best":
		return "bv*[ext=mp4]+ba[ext=m4a]/bv*+ba/best"
	default: // 1080p
		return "bv*[height<=1080][ext=mp4]+ba[ext=m4a]/b[height<=1080][ext=mp4]/bv*[height<=1080]+ba/best[height<=1080]/best"
	}
}

// DownloadSection downloads only the requested [startSec, endSec] range using yt-dlp --download-sections.
func (c *Client) DownloadSection(ctx context.Context, videoURL string, startSec, endSec float64, destMP4 string, settings model.AppSettings, onProgress func(pct float64, line string)) error {
	if startSec < 0 {
		startSec = 0
	}
	section := fmt.Sprintf("*%.2f-%.2f", startSec, endSec)
	args := append(c.baseArgs(settings),
		"-f", formatSelector(settings.DownloadQuality),
		"--merge-output-format", "mp4",
		"--download-sections", section,
		"--force-keyframes-at-cuts",
		"--newline",
		"-o", destMP4,
		videoURL,
	)
	return c.runDownloadCmd(ctx, args, onProgress)
}

// DownloadFullVideo downloads the full source MP4 video locally for instant scrubbing, audio analysis & fast re-renders.
func (c *Client) DownloadFullVideo(ctx context.Context, videoURL, destMP4 string, settings model.AppSettings, onProgress func(pct float64, line string)) error {
	args := append(c.baseArgs(settings),
		"-f", formatSelector(settings.DownloadQuality),
		"--merge-output-format", "mp4",
		"--newline",
		"-o", destMP4,
		videoURL,
	)
	return c.runDownloadCmd(ctx, args, onProgress)
}

// DownloadAudioOnly downloads the lightest fast audio track (<=64kbps opus/m4a) for ultra-fast audio scream peak analysis!
func (c *Client) DownloadAudioOnly(ctx context.Context, videoURL, destPath string, settings model.AppSettings) (string, error) {
	outTmpl := strings.TrimSuffix(destPath, filepath.Ext(destPath)) + ".%(ext)s"
	args := append(c.baseArgs(settings),
		"-f", "ba[abr<=64]/worstaudio/ba[ext=m4a]/bestaudio",
		"-o", outTmpl,
		videoURL,
	)
	cmd := exec.CommandContext(ctx, c.Deps.GetStatus().YtDlpPath, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("download audio gagal: %s", strings.TrimSpace(stderr.String()))
	}
	base := strings.TrimSuffix(destPath, filepath.Ext(destPath))
	matches, _ := filepath.Glob(base + ".*")
	if len(matches) > 0 {
		return matches[0], nil
	}
	return "", fmt.Errorf("file audio tidak ditemukan")
}

func (c *Client) runDownloadCmd(ctx context.Context, args []string, onProgress func(pct float64, line string)) error {
	cmd := exec.CommandContext(ctx, c.Deps.GetStatus().YtDlpPath, args...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Start(); err != nil {
		return err
	}

	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 32*1024), 512*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		if onProgress != nil {
			if m := ytDlpPctRe.FindStringSubmatch(line); m != nil {
				if p, err := strconv.ParseFloat(m[1], 64); err == nil {
					onProgress(p, line)
					continue
				}
			}
			onProgress(-1, line)
		}
	}

	if err := cmd.Wait(); err != nil {
		errMsg := strings.TrimSpace(stderr.String())
		if errMsg == "" {
			errMsg = err.Error()
		}
		return fmt.Errorf("yt-dlp download gagal: %s", errMsg)
	}
	return nil
}
