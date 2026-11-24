package analyzer

import (
	"fmt"
	"math"
	"strings"

	"klipforge/internal/model"
)

// CompactTranscriptInput configures AutoClip-inspired one-pass compact transcript window generation
// (`id|MM:SS|text` lines + inline chat/audio spike markers so LLMs/agents can scan 4-hour VODs in 1 pass!).
type CompactTranscriptInput struct {
	DurationSec   float64
	Cues          []model.TranscriptCue
	Timeline      []model.TimelinePoint
	WindowMinutes float64 // Default 25 min (local-LLM friendly; up to 60 min)
	OverlapSec    float64 // Default 90s overlap across window edges so arcs aren't split
	MaxChars      int     // Default 14000 chars per window
	WindowIndex   int     // Requested window index (0-based)
}

// TranscriptWindowMeta describes one time window slice of a VOD.
type TranscriptWindowMeta struct {
	Index      int     `json:"index"`
	StartSec   float64 `json:"startSec"`
	EndSec     float64 `json:"endSec"`
	StartLabel string  `json:"startLabel"`
	EndLabel   string  `json:"endLabel"`
	LineCount  int     `json:"lineCount"`
	CharCount  int     `json:"charCount"`
	ChatSpikes int     `json:"chatSpikes"`
	AudioPeaks int     `json:"audioPeaks"`
}

// CompactTranscriptResult returns one compact window payload plus metadata for all windows.
type CompactTranscriptResult struct {
	TotalWindows int                    `json:"totalWindows"`
	WindowIndex  int                    `json:"windowIndex"`
	Window       TranscriptWindowMeta   `json:"window"`
	AllWindows   []TranscriptWindowMeta `json:"allWindows"`
	Format       string                 `json:"format"`
	CompactText  string                 `json:"compactText"`
}

// BuildCompactTranscriptWindows converts timestamped cues + timeline spikes into AutoClip-style
// `id|time|text` lines sliced into overlapping windows so Carbon/Muse/Ollama can perform one-pass moment picking.
func BuildCompactTranscriptWindows(in CompactTranscriptInput) CompactTranscriptResult {
	winMin := in.WindowMinutes
	if winMin <= 0 {
		winMin = 25
	}
	if winMin > 60 {
		winMin = 60
	}
	winSec := winMin * 60.0
	overlap := in.OverlapSec
	if overlap < 0 {
		overlap = 0
	}
	if overlap == 0 {
		overlap = 90
	}
	if overlap >= winSec*0.5 {
		overlap = winSec * 0.25
	}
	maxChars := in.MaxChars
	if maxChars <= 0 {
		maxChars = 14000
	}
	if maxChars > 60000 {
		maxChars = 60000
	}

	dur := in.DurationSec
	if dur <= 0 {
		for _, c := range in.Cues {
			if c.End > dur {
				dur = c.End
			}
		}
		for _, pt := range in.Timeline {
			if pt.TimeSec > dur {
				dur = pt.TimeSec
			}
		}
	}
	if dur <= 0 {
		dur = 60
	}

	// Helper to find strongest chat/audio timeline signal near second t
	signalAt := func(sec float64) (chatPct int, audioPct int) {
		if len(in.Timeline) == 0 {
			return 0, 0
		}
		step := dur / float64(len(in.Timeline))
		if step < 10 {
			step = 10
		}
		if step > 45 {
			step = 45
		}
		var maxC, maxA float64
		for _, pt := range in.Timeline {
			if math.Abs(pt.TimeSec-sec) <= step {
				if pt.ChatHype > maxC {
					maxC = pt.ChatHype
				}
				if pt.AudioPeak > maxA {
					maxA = pt.AudioPeak
				}
			}
		}
		return int(math.Round(maxC * 100)), int(math.Round(maxA * 100))
	}

	// Build unified chronological lines: transcript cues + synthetic [VISUAL_CHAT_SPIKE] lines during quiet visual gags!
	type lineItem struct {
		sec        float64
		text       string
		isChatPeak bool
		isAudPeak  bool
	}
	var allLines []lineItem
	for _, c := range in.Cues {
		txt := strings.TrimSpace(c.Text)
		if txt == "" {
			continue
		}
		low := strings.ToLower(txt)
		if low == "[musik]" || low == "[music]" {
			continue
		}
		cPct, aPct := signalAt(c.Start)
		var tags []string
		if c.Speaker != "" {
			tags = append(tags, c.Speaker)
		}
		if cPct >= 55 {
			tags = append(tags, fmt.Sprintf("CHAT:%d", cPct))
		}
		if aPct >= 75 {
			tags = append(tags, fmt.Sprintf("AUD:%d", aPct))
		}
		prefix := ""
		if len(tags) > 0 {
			prefix = "[" + strings.Join(tags, " ") + "] "
		}
		allLines = append(allLines, lineItem{
			sec:        c.Start,
			text:       prefix + txt,
			isChatPeak: cPct >= 55,
			isAudPeak:  aPct >= 75,
		})
	}

	// Inject synthetic visual-gag chat spikes when live chat exploded >= 65% with no nearby transcript cue!
	for _, pt := range in.Timeline {
		if pt.ChatHype < 0.65 {
			continue
		}
		hasNearbyCue := false
		for _, c := range in.Cues {
			if math.Abs(c.Start-pt.TimeSec) <= 8.0 && strings.TrimSpace(c.Text) != "" {
				hasNearbyCue = true
				break
			}
		}
		if !hasNearbyCue {
			allLines = append(allLines, lineItem{
				sec:        pt.TimeSec,
				text:       fmt.Sprintf("[VISUAL_CHAT_SPIKE CHAT:%d msgs:%d] (chat meledak tanpa dialog — cek frame visual!)", int(math.Round(pt.ChatHype*100)), pt.ChatCount),
				isChatPeak: true,
			})
		}
	}

	// Sort chronologically
	for i := 1; i < len(allLines); i++ {
		for j := i; j > 0 && allLines[j].sec < allLines[j-1].sec; j-- {
			allLines[j], allLines[j-1] = allLines[j-1], allLines[j]
		}
	}

	// Slice into overlapping windows
	stride := winSec - overlap
	if stride < 60 {
		stride = 60
	}
	type builtWindow struct {
		meta TranscriptWindowMeta
		body string
	}
	var windows []builtWindow
	for start := 0.0; start < dur || len(windows) == 0; start += stride {
		end := math.Min(dur, start+winSec)
		var sb strings.Builder
		lineCnt := 0
		chatSpikes := 0
		audioPeaks := 0
		for id, ln := range allLines {
			if ln.sec < start || ln.sec > end {
				continue
			}
			formatted := fmt.Sprintf("%d|%s|%s\n", id+1, formatCompactTime(ln.sec), ln.text)
			if sb.Len()+len(formatted) > maxChars {
				break
			}
			sb.WriteString(formatted)
			lineCnt++
			if ln.isChatPeak {
				chatSpikes++
			}
			if ln.isAudPeak {
				audioPeaks++
			}
		}
		windows = append(windows, builtWindow{
			meta: TranscriptWindowMeta{
				Index:      len(windows),
				StartSec:   math.Round(start),
				EndSec:     math.Round(end),
				StartLabel: formatCompactTime(start),
				EndLabel:   formatCompactTime(end),
				LineCount:  lineCnt,
				CharCount:  sb.Len(),
				ChatSpikes: chatSpikes,
				AudioPeaks: audioPeaks,
			},
			body: sb.String(),
		})
		if end >= dur {
			break
		}
	}

	reqIdx := in.WindowIndex
	if reqIdx < 0 {
		reqIdx = 0
	}
	if reqIdx >= len(windows) {
		reqIdx = len(windows) - 1
	}

	allMeta := make([]TranscriptWindowMeta, len(windows))
	for i, w := range windows {
		allMeta[i] = w.meta
	}

	return CompactTranscriptResult{
		TotalWindows: len(windows),
		WindowIndex:  reqIdx,
		Window:       windows[reqIdx].meta,
		AllWindows:   allMeta,
		Format:       "id|time|text (AutoClip one-pass compact format with inline [CHAT:N]/[AUD:N]/[VISUAL_CHAT_SPIKE] tags)",
		CompactText:  windows[reqIdx].body,
	}
}

func formatCompactTime(sec float64) string {
	t := int(math.Max(0, math.Round(sec)))
	h := t / 3600
	m := (t % 3600) / 60
	s := t % 60
	if h > 0 {
		return fmt.Sprintf("%d:%02d:%02d", h, m, s)
	}
	return fmt.Sprintf("%02d:%02d", m, s)
}
