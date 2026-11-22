package youtube

import (
	"bufio"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"klipforge/internal/model"
)

// HeatmapPoint matches yt-dlp's heatmap JSON schema.
type HeatmapPoint struct {
	StartTime float64 `json:"start_time"`
	EndTime   float64 `json:"end_time"`
	Value     float64 `json:"value"`
}

// ChatMessage represents a single YouTube Live Chat replay message.
type ChatMessage struct {
	OffsetSec  float64
	Author     string
	Text       string
	IsPaid     bool // SuperChat / SuperSticker = strong hype signal
}

// ExtractVideoID extracts the 11-char YouTube video ID from watch, live, shorts, or youtu.be URLs.
func ExtractVideoID(rawURL string) string {
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" {
		return ""
	}
	if !strings.HasPrefix(rawURL, "http://") && !strings.HasPrefix(rawURL, "https://") {
		if len(rawURL) == 11 && !strings.ContainsAny(rawURL, "/?& ") {
			return rawURL
		}
		rawURL = "https://" + rawURL
	}

	u, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	host := strings.ToLower(strings.TrimPrefix(u.Hostname(), "www."))
	host = strings.TrimPrefix(host, "m.")

	if host == "youtu.be" {
		id := strings.Trim(u.Path, "/")
		if parts := strings.Split(id, "/"); len(parts) > 0 {
			return parts[0]
		}
		return ""
	}

	if host == "youtube.com" || host == "music.youtube.com" {
		if u.Path == "/watch" || u.Path == "/watch/" {
			return u.Query().Get("v")
		}
		parts := strings.Split(strings.Trim(u.Path, "/"), "/")
		if len(parts) >= 2 {
			switch parts[0] {
			case "shorts", "live", "embed", "v":
				return parts[1]
			}
		}
	}
	return ""
}

// FetchHeatmapHTMLFallback scrapes YouTube's watch page HTML directly for Most Replayed markers
// (inspired by 0xACAB666/yt-heatmap-clipper & naufaljct48/youtube-heatmap-clipper).
func FetchHeatmapHTMLFallback(videoID string) ([]HeatmapPoint, error) {
	if videoID == "" {
		return nil, fmt.Errorf("empty video id")
	}
	req, err := http.NewRequest("GET", "https://www.youtube.com/watch?v="+videoID, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36")
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20)) // max 8MB
	if err != nil {
		return nil, err
	}
	text := string(body)

	// Pattern 1: heatMarkerRenderer array inside markers
	re := regexp.MustCompile(`"heatMarkerRenderer"\s*:\s*\{[^}]*"timeRangeStartMillis"\s*:\s*"?(\d+)"?[^}]*"markerDurationMillis"\s*:\s*"?(\d+)"?[^}]*"heatMarkerIntensityScoreNormalized"\s*:\s*([0-9.eE+-]+)`)
	matches := re.FindAllStringSubmatch(text, -1)
	if len(matches) > 0 {
		pts := make([]HeatmapPoint, 0, len(matches))
		for _, m := range matches {
			startMs, err1 := strconv.ParseFloat(m[1], 64)
			durMs, err2 := strconv.ParseFloat(m[2], 64)
			val, err3 := strconv.ParseFloat(m[3], 64)
			if err1 == nil && err2 == nil && err3 == nil {
				pts = append(pts, HeatmapPoint{
					StartTime: startMs / 1000.0,
					EndTime:   (startMs + durMs) / 1000.0,
					Value:     val,
				})
			}
		}
		if len(pts) > 0 {
			return pts, nil
		}
	}

	// Pattern 2: intensityScoreNormalized (newer YouTube JSON shape)
	re2 := regexp.MustCompile(`"startMillis"\s*:\s*"?(\d+)"?[^}]*"durationMillis"\s*:\s*"?(\d+)"?[^}]*"intensityScoreNormalized"\s*:\s*([0-9.eE+-]+)`)
	matches2 := re2.FindAllStringSubmatch(text, -1)
	if len(matches2) > 0 {
		pts := make([]HeatmapPoint, 0, len(matches2))
		for _, m := range matches2 {
			startMs, err1 := strconv.ParseFloat(m[1], 64)
			durMs, err2 := strconv.ParseFloat(m[2], 64)
			val, err3 := strconv.ParseFloat(m[3], 64)
			if err1 == nil && err2 == nil && err3 == nil {
				pts = append(pts, HeatmapPoint{
					StartTime: startMs / 1000.0,
					EndTime:   (startMs + durMs) / 1000.0,
					Value:     val,
				})
			}
		}
		return pts, nil
	}

	return nil, nil
}

// ParseLiveChatFile streaming-parses a yt-dlp .live_chat.json NDJSON file quickly.
func ParseLiveChatFile(path string) ([]ChatMessage, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var msgs []ChatMessage
	scanner := bufio.NewScanner(f)
	// Support large lines up to 2MB
	scanner.Buffer(make([]byte, 64*1024), 2*1024*1024)

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || line[0] != '{' {
			continue
		}
		var item struct {
			ReplayChatItemAction struct {
				VideoOffsetTimeMsec string `json:"videoOffsetTimeMsec"`
				Actions             []struct {
					AddChatItemAction struct {
						Item struct {
							LiveChatTextMessageRenderer *struct {
								AuthorName struct {
									SimpleText string `json:"simpleText"`
								} `json:"authorName"`
								Message struct {
									Runs []struct {
										Text  string `json:"text"`
										Emoji *struct {
											Shortcuts []string `json:"shortcuts"`
										} `json:"emoji"`
									} `json:"runs"`
								} `json:"message"`
							} `json:"liveChatTextMessageRenderer"`
							LiveChatPaidMessageRenderer *struct {
								AuthorName struct {
									SimpleText string `json:"simpleText"`
								} `json:"authorName"`
								Message struct {
									Runs []struct {
										Text string `json:"text"`
									} `json:"runs"`
								} `json:"message"`
							} `json:"liveChatPaidMessageRenderer"`
						} `json:"item"`
					} `json:"addChatItemAction"`
				} `json:"actions"`
			} `json:"replayChatItemAction"`
		}

		if err := json.Unmarshal([]byte(line), &item); err != nil {
			continue
		}
		offsetMs, err := strconv.ParseFloat(item.ReplayChatItemAction.VideoOffsetTimeMsec, 64)
		if err != nil || offsetMs < 0 {
			continue
		}
		offsetSec := offsetMs / 1000.0

		for _, act := range item.ReplayChatItemAction.Actions {
			if r := act.AddChatItemAction.Item.LiveChatTextMessageRenderer; r != nil {
				var sb strings.Builder
				for _, run := range r.Message.Runs {
					if run.Text != "" {
						sb.WriteString(run.Text)
					} else if run.Emoji != nil && len(run.Emoji.Shortcuts) > 0 {
						sb.WriteString(" ")
						sb.WriteString(run.Emoji.Shortcuts[0])
					}
				}
				txt := strings.TrimSpace(sb.String())
				if txt != "" {
					msgs = append(msgs, ChatMessage{
						OffsetSec: offsetSec,
						Author:    r.AuthorName.SimpleText,
						Text:      txt,
					})
				}
			}
			if r := act.AddChatItemAction.Item.LiveChatPaidMessageRenderer; r != nil {
				var sb strings.Builder
				for _, run := range r.Message.Runs {
					sb.WriteString(run.Text)
				}
				msgs = append(msgs, ChatMessage{
					OffsetSec: offsetSec,
					Author:    r.AuthorName.SimpleText,
					Text:      strings.TrimSpace(sb.String()),
					IsPaid:    true,
				})
			}
		}
	}

	return msgs, scanner.Err()
}

var (
	vttTimeRe = regexp.MustCompile(`(\d{1,2}:)?(\d{2}):(\d{2})[.,](\d{3})\s*-->\s*(\d{1,2}:)?(\d{2}):(\d{2})[.,](\d{3})`)
	vttTagRe  = regexp.MustCompile(`<[^>]+>`)
)

// ParseSubtitleFile parses a WebVTT (.vtt) or SubRip (.srt) file into deduplicated transcript cues.
func ParseSubtitleFile(path string) ([]model.TranscriptCue, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	content := strings.ReplaceAll(string(b), "\r\n", "\n")
	lines := strings.Split(content, "\n")

	var raw []model.TranscriptCue
	for i := 0; i < len(lines); i++ {
		line := strings.TrimSpace(lines[i])
		m := vttTimeRe.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		start := parseSubTimestamp(m[1], m[2], m[3], m[4])
		end := parseSubTimestamp(m[5], m[6], m[7], m[8])
		if end <= start {
			end = start + 1.5
		}

		var textLines []string
		for i+1 < len(lines) {
			next := strings.TrimSpace(lines[i+1])
			if next == "" {
				i++
				break
			}
			if vttTimeRe.MatchString(next) {
				break
			}
			clean := vttTagRe.ReplaceAllString(next, "")
			clean = html.UnescapeString(clean)
			clean = strings.TrimSpace(clean)
			if clean != "" {
				textLines = append(textLines, clean)
			}
			i++
		}
		full := strings.TrimSpace(strings.Join(textLines, " "))
		if full == "" {
			continue
		}
		raw = append(raw, model.TranscriptCue{
			Start: start,
			End:   end,
			Text:  full,
		})
	}

	// YouTube auto-captions often repeat rolling lines; deduplicate cleanly
	var clean []model.TranscriptCue
	for _, c := range raw {
		// Skip tiny 10ms YouTube transition cues
		if c.End-c.Start < 0.08 && len(clean) > 0 {
			continue
		}
		if len(clean) > 0 {
			prev := &clean[len(clean)-1]
			if strings.EqualFold(prev.Text, c.Text) {
				if c.End > prev.End {
					prev.End = c.End
				}
				continue
			}
			// If new cue starts with previous cue's text (rolling VTT), strip prefix
			if strings.HasPrefix(c.Text, prev.Text) && len(c.Text) > len(prev.Text)+1 {
				trimmed := strings.TrimSpace(strings.TrimPrefix(c.Text, prev.Text))
				if trimmed != "" {
					c.Text = trimmed
				}
			}
			if c.Start < prev.End {
				prev.End = c.Start
			}
		}
		if c.End > c.Start && c.Text != "" {
			clean = append(clean, c)
		}
	}
	return clean, nil
}

func parseSubTimestamp(hStr, mStr, sStr, msStr string) float64 {
	var h int
	if hStr != "" {
		h, _ = strconv.Atoi(strings.TrimSuffix(hStr, ":"))
	}
	m, _ := strconv.Atoi(mStr)
	s, _ := strconv.Atoi(sStr)
	ms, _ := strconv.Atoi(msStr)
	return float64(h*3600+m*60+s) + float64(ms)/1000.0
}
