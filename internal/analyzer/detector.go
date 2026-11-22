package analyzer

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"unicode"

	"klipforge/internal/model"
	"klipforge/internal/youtube"
)

// AnalysisInput bundles all raw signals available for a YouTube video/VOD.
type AnalysisInput struct {
	DurationSec float64
	Heatmap     []youtube.HeatmapPoint
	Chats       []youtube.ChatMessage
	AudioEnergy []float64 // 1-second buckets (0.0 - 1.0)
	Transcript  []model.TranscriptCue
	Chapters    []model.Chapter
	Settings    model.DetectionSettings
}

// AnalysisResult contains the downsampled chart timeline, ranked viral candidates, and signal availability.
type AnalysisResult struct {
	Timeline   []model.TimelinePoint
	Candidates []model.ClipCandidate
	Signals    model.SignalAvailability
}

// Indonesian & global gaming streamer hype keywords (tailored for Windah Basudara & ID/EN gaming streams!).
var hypeKeywords = []string{
	"wkwk", "kwkw", "haha", "hehe", "ngakak", "kocak", "lucu",
	"anjir", "njir", "bjir", "anjay", "gila", "parah", "buset", "waduh", "astaga", "ya ampun", "kaget",
	"gg", "ez", "clutch", "epic", "peak", "gacor", "mantap", "keren", "sakit", "op ", "pro",
	"omg", "wtf", "lmao", "lol", "bro", "bruh", "rip", "no way", "crazy", "insane", "goat",
	"windah", "bocil", "kematian", "brando", "gta", "absen", "tamat", "panik", "kabur", "meledak",
	"😂", "🤣", "😭", "🔥", "💀", "😱", "🤯", "👏", "❤️",
}

// DetectHype runs multi-signal fusion (Heatmap + Live Chat + Audio Energy) and extracts ranked clip candidates.
func DetectHype(in AnalysisInput) AnalysisResult {
	dur := int(math.Ceil(in.DurationSec))
	if dur <= 0 {
		// Infer duration from available signals
		for _, h := range in.Heatmap {
			if int(math.Ceil(h.EndTime)) > dur {
				dur = int(math.Ceil(h.EndTime))
			}
		}
		for _, c := range in.Chats {
			if int(math.Ceil(c.OffsetSec)) > dur {
				dur = int(math.Ceil(c.OffsetSec))
			}
		}
		if len(in.AudioEnergy) > dur {
			dur = len(in.AudioEnergy)
		}
	}
	if dur <= 0 {
		return AnalysisResult{}
	}

	cfg := in.Settings
	if cfg.MaxClips <= 0 {
		cfg = model.DefaultDetectionSettings()
	}

	// Build 1-second resolution grids
	heatGrid := make([]float64, dur)
	chatGrid := make([]float64, dur)
	chatCounts := make([]int, dur)
	audioGrid := make([]float64, dur)
	chatBucketSamples := make([][]string, dur)

	hasHeatmap := len(in.Heatmap) > 0
	hasChat := len(in.Chats) > 20
	hasAudio := len(in.AudioEnergy) > 5

	// 1. Populate Heatmap grid
	if hasHeatmap {
		maxVal := 0.0
		for _, pt := range in.Heatmap {
			if pt.Value > maxVal {
				maxVal = pt.Value
			}
		}
		if maxVal <= 0 {
			maxVal = 1.0
		}
		for _, pt := range in.Heatmap {
			s := clampInt(int(math.Floor(pt.StartTime)), 0, dur-1)
			e := clampInt(int(math.Ceil(pt.EndTime)), s+1, dur)
			norm := pt.Value / maxVal
			for t := s; t < e; t++ {
				if norm > heatGrid[t] {
					heatGrid[t] = norm
				}
			}
		}
	}

	// 2. Populate Live Chat Hype grid (velocity + Indonesian/EN hype keywords + caps + SuperChat)
	if hasChat {
		rawHype := make([]float64, dur)
		for _, msg := range in.Chats {
			sec := int(math.Floor(msg.OffsetSec))
			if sec < 0 || sec >= dur {
				continue
			}
			chatCounts[sec]++
			weight := scoreChatMessage(msg)
			rawHype[sec] += weight
			if weight >= 1.8 && len(chatBucketSamples[sec]) < 4 && len(msg.Text) <= 65 {
				chatBucketSamples[sec] = append(chatBucketSamples[sec], msg.Text)
			}
		}

		// Smooth chat with a 7-second rolling window (captures sustained chat explosions)
		smoothed := make([]float64, dur)
		win := 3
		for i := 0; i < dur; i++ {
			var sum float64
			var cnt float64
			for j := i - win; j <= i+win; j++ {
				if j >= 0 && j < dur {
					sum += rawHype[j]
					cnt++
				}
			}
			if cnt > 0 {
				smoothed[i] = sum / cnt
			}
		}

		// Normalize using robust 98th percentile so a single spam bot spike doesn't crush the whole VOD
		p98 := percentile(smoothed, 0.985)
		if p98 <= 0 {
			p98 = 1.0
		}
		for i := 0; i < dur; i++ {
			v := smoothed[i] / p98
			if v > 1.0 {
				v = 1.0
			}
			chatGrid[i] = v
		}
	}

	// 3. Populate Audio Energy grid
	if hasAudio {
		for i := 0; i < dur && i < len(in.AudioEnergy); i++ {
			audioGrid[i] = in.AudioEnergy[i]
		}
	}

	// 4. Dynamic Multi-Signal Weight Redistribution
	// If a VOD is brand new and has no heatmap yet, Chat + Audio automatically take 100% weight!
	wH, wC, wA := 0.0, 0.0, 0.0
	if hasHeatmap {
		wH = cfg.HeatmapWeight
	}
	if hasChat {
		wC = cfg.ChatWeight
	}
	if hasAudio {
		wA = cfg.AudioWeight
	}
	wTotal := wH + wC + wA
	if wTotal <= 0 {
		// No signals available — still build timeline evenly
		wTotal = 1.0
	} else {
		wH /= wTotal
		wC /= wTotal
		wA /= wTotal
	}

	combined := make([]float64, dur)
	skipIntro := int(cfg.SkipIntroSec)
	for i := 0; i < dur; i++ {
		score := wH*heatGrid[i] + wC*chatGrid[i] + wA*audioGrid[i]

		// Synergy bonus: when 2+ independent signals spike together, boost confidence!
		activeHigh := 0
		if hasHeatmap && heatGrid[i] >= 0.55 {
			activeHigh++
		}
		if hasChat && chatGrid[i] >= 0.55 {
			activeHigh++
		}
		if hasAudio && audioGrid[i] >= 0.60 {
			activeHigh++
		}
		if activeHigh >= 2 {
			score = math.Min(1.0, score*1.16)
		}

		// Fix classic YouTube Heatmap 0:00 intro bias: damp first N seconds unless chat/audio also explodes
		if i < skipIntro && (!hasChat || chatGrid[i] < 0.65) && (!hasAudio || audioGrid[i] < 0.70) {
			score *= 0.30
		}

		combined[i] = score
	}

	// 5. Peak Detection + Non-Maximum Suppression (NMS)
	candidates := extractCandidates(dur, combined, heatGrid, chatGrid, audioGrid, chatBucketSamples, in.Transcript, in.Chapters, cfg, hasHeatmap, hasChat, hasAudio)

	// 6. Downsample timeline to max ~360 chart points for buttery-smooth Canvas rendering
	step := 1
	if dur > 360 {
		step = int(math.Ceil(float64(dur) / 360.0))
	}
	timeline := make([]model.TimelinePoint, 0, dur/step+1)
	for i := 0; i < dur; i += step {
		end := i + step
		if end > dur {
			end = dur
		}
		var maxH, maxC, maxA, maxComb float64
		var sumChat int
		for j := i; j < end; j++ {
			if heatGrid[j] > maxH {
				maxH = heatGrid[j]
			}
			if chatGrid[j] > maxC {
				maxC = chatGrid[j]
			}
			if audioGrid[j] > maxA {
				maxA = audioGrid[j]
			}
			if combined[j] > maxComb {
				maxComb = combined[j]
			}
			sumChat += chatCounts[j]
		}
		timeline = append(timeline, model.TimelinePoint{
			TimeSec:   float64(i),
			Heatmap:   round3(maxH),
			ChatHype:  round3(maxC),
			AudioPeak: round3(maxA),
			Combined:  round3(maxComb),
			ChatCount: sumChat,
		})
	}

	return AnalysisResult{
		Timeline:   timeline,
		Candidates: candidates,
		Signals: model.SignalAvailability{
			HasHeatmap:    hasHeatmap,
			HasLiveChat:   hasChat,
			HasAudio:      hasAudio,
			HasSubtitles:  len(in.Transcript) > 0,
			ChatMsgCount:  len(in.Chats),
			SubCueCount:   len(in.Transcript),
			HeatmapPoints: len(in.Heatmap),
		},
	}
}

func extractCandidates(
	dur int,
	combined, heat, chat, audio []float64,
	chatSamples [][]string,
	transcript []model.TranscriptCue,
	chapters []model.Chapter,
	cfg model.DetectionSettings,
	hasHeatmap, hasChat, hasAudio bool,
) []model.ClipCandidate {
	type peak struct {
		sec   int
		score float64
	}

	// Smooth combined slightly (5s) for peak localization
	smooth := make([]float64, dur)
	for i := 0; i < dur; i++ {
		var s, c float64
		for j := i - 2; j <= i+2; j++ {
			if j >= 0 && j < dur {
				s += combined[j]
				c++
			}
		}
		smooth[i] = s / c
	}

	minThreshold := cfg.MinScore / 100.0
	if minThreshold <= 0 {
		minThreshold = 0.25
	}

	var peaks []peak
	minPeakDist := int(math.Max(15, cfg.MinDurationSec*0.8))

	// Find all local maxima above threshold
	for i := 0; i < dur; i++ {
		val := smooth[i]
		if val < minThreshold {
			continue
		}
		isLocalMax := true
		for j := i - 5; j <= i+5; j++ {
			if j >= 0 && j < dur && smooth[j] > val {
				isLocalMax = false
				break
			}
		}
		if isLocalMax {
			peaks = append(peaks, peak{sec: i, score: combined[i]})
		}
	}

	// If very strict threshold yielded too few clips on a flat video, relax gracefully
	if len(peaks) == 0 && dur > 30 {
		bestSec := 0
		bestVal := -1.0
		for i := int(cfg.SkipIntroSec); i < dur; i++ {
			if smooth[i] > bestVal {
				bestVal = smooth[i]
				bestSec = i
			}
		}
		if bestVal > 0 {
			peaks = append(peaks, peak{sec: bestSec, score: bestVal})
		}
	}

	// Sort peaks descending by score
	sort.Slice(peaks, func(i, j int) bool {
		return peaks[i].score > peaks[j].score
	})

	// Non-Maximum Suppression & smart clip boundary expansion
	var selected []model.ClipCandidate
	for _, pk := range peaks {
		if len(selected) >= cfg.MaxClips {
			break
		}

		// Expand around peak while signal stays elevated, respecting pre/post padding & min/max duration
		left := pk.sec - int(cfg.PrePaddingSec)
		right := pk.sec + int(cfg.PostPaddingSec)

		// Extend slightly if adjacent seconds are still very hot (> 65% of peak)
		hotCut := smooth[pk.sec] * 0.65
		for left > 0 && smooth[left] >= hotCut && float64(right-left) < cfg.MaxDurationSec {
			left--
		}
		for right < dur-1 && smooth[right] >= hotCut && float64(right-left) < cfg.MaxDurationSec {
			right++
		}

		// Enforce min and max duration
		if left < 0 {
			left = 0
		}
		if right > dur {
			right = dur
		}
		segDur := float64(right - left)
		if segDur > cfg.MaxDurationSec {
			// Center around peak with 35% lead-in, 65% aftermath (ideal for streamer reactions!)
			lead := int(cfg.MaxDurationSec * 0.35)
			left = pk.sec - lead
			right = left + int(cfg.MaxDurationSec)
			if left < 0 {
				left = 0
				right = clampInt(int(cfg.MaxDurationSec), 1, dur)
			}
			if right > dur {
				right = dur
				left = clampInt(dur-int(cfg.MaxDurationSec), 0, dur-1)
			}
		} else if segDur < cfg.MinDurationSec && float64(dur) >= cfg.MinDurationSec {
			need := int(cfg.MinDurationSec) - (right - left)
			left = clampInt(left-need/2, 0, dur-1)
			right = clampInt(left+int(cfg.MinDurationSec), 1, dur)
		}

		// Check overlap against already selected clips
		overlaps := false
		for _, ex := range selected {
			interStart := math.Max(float64(left), ex.StartSec)
			interEnd := math.Min(float64(right), ex.EndSec)
			if interEnd-interStart > float64(minPeakDist)*0.45 {
				overlaps = true
				break
			}
			if math.Abs(float64(pk.sec)-ex.PeakSec) < float64(minPeakDist) {
				overlaps = true
				break
			}
		}
		if overlaps {
			continue
		}

		// Gather window peak stats
		maxH := windowMax(heat, left, right)
		maxC := windowMax(chat, left, right)
		maxA := windowMax(audio, left, right)
		score100 := math.Round(math.Min(100, pk.score*100))

		reasons := buildReasons(maxH, maxC, maxA, hasHeatmap, hasChat, hasAudio)
		samples := collectChatSamples(chatSamples, pk.sec-5, pk.sec+12, 5)
		speech := collectTranscriptSnippet(transcript, float64(left), float64(right), 180)
		chapTitle := findChapterTitle(chapters, float64(pk.sec))

		rank := len(selected) + 1
		title := fmt.Sprintf("Viral Moment #%d (%s)", rank, FormatMMSS(float64(pk.sec)))
		if chapTitle != "" {
			title = fmt.Sprintf("#%d %s (%s)", rank, chapTitle, FormatMMSS(float64(pk.sec)))
		}

		hookText := suggestHookText(reasons, samples, chapTitle)

		selected = append(selected, model.ClipCandidate{
			ID:           fmt.Sprintf("clip-%d-%d", left, right),
			Rank:         rank,
			Title:        title,
			StartSec:     float64(left),
			EndSec:       float64(right),
			PeakSec:      float64(pk.sec),
			DurationSec:  float64(right - left),
			Score:        score100,
			HeatmapScore: math.Round(maxH * 100),
			ChatScore:    math.Round(maxC * 100),
			AudioScore:   math.Round(maxA * 100),
			Reasons:      reasons,
			SampleChats:  samples,
			Transcript:   speech,
			HookText:     hookText,
			Selected:     rank <= 5, // Pre-select Top 5 hottest moments!
		})
	}

	return selected
}

func scoreChatMessage(msg youtube.ChatMessage) float64 {
	w := 1.0
	if msg.IsPaid {
		w += 2.5
	}
	lower := strings.ToLower(msg.Text)
	for _, kw := range hypeKeywords {
		if strings.Contains(lower, kw) {
			w += 1.0
			break
		}
	}
	// Check ALL-CAPS yelling or exclamation spam
	upper, letters, excl := 0, 0, 0
	for _, r := range msg.Text {
		if unicode.IsLetter(r) {
			letters++
			if unicode.IsUpper(r) {
				upper++
			}
		} else if r == '!' || r == '?' {
			excl++
		}
	}
	if letters >= 5 && float64(upper)/float64(letters) >= 0.75 {
		w += 0.6
	}
	if excl >= 2 {
		w += 0.4
	}
	// Repeated laughter chars (wkwkwk, aaaaa, hhhhh)
	if strings.Contains(lower, "wkwkw") || strings.Contains(lower, "hahah") || strings.Contains(lower, "aaaaa") {
		w += 0.7
	}
	return w
}

func buildReasons(maxH, maxC, maxA float64, hasH, hasC, hasA bool) []string {
	var reasons []string
	highCount := 0
	if hasH && maxH >= 0.60 {
		reasons = append(reasons, "Most Replayed")
		highCount++
	}
	if hasC && maxC >= 0.55 {
		reasons = append(reasons, "Chat Meledak")
		highCount++
	}
	if hasA && maxA >= 0.60 {
		reasons = append(reasons, "Reaksi Keras")
		highCount++
	}
	if highCount >= 2 {
		reasons = append([]string{"Multi-Signal Viral"}, reasons...)
	}
	if len(reasons) == 0 {
		reasons = append(reasons, "High Retention Peak")
	}
	return reasons
}

func suggestHookText(reasons, sampleChats []string, chapterTitle string) string {
	if chapterTitle != "" && len(chapterTitle) <= 40 {
		return strings.ToUpper(chapterTitle)
	}
	for _, c := range sampleChats {
		low := strings.ToLower(c)
		if strings.Contains(low, "wkwk") || strings.Contains(low, "ngakak") || strings.Contains(low, "kocak") {
			return "MOMEN NGAKAK PARAH! 😭🔥"
		}
		if strings.Contains(low, "kaget") || strings.Contains(low, "panik") {
			return "DETIK-DETIK PANIK KAGET! 😱"
		}
	}
	for _, r := range reasons {
		if r == "Chat Meledak" {
			return "CHAT LANGSUNG MELEDAK! 🔥"
		}
		if r == "Reaksi Keras" {
			return "REAKSI EPIC MOMENT! 🤯"
		}
	}
	return "MOMEN PALING RAMAI DI STREAM! 🔥"
}

func collectChatSamples(buckets [][]string, start, end, limit int) []string {
	seen := make(map[string]struct{})
	var out []string
	for i := clampInt(start, 0, len(buckets)-1); i < clampInt(end, 0, len(buckets)); i++ {
		for _, txt := range buckets[i] {
			key := strings.ToLower(txt)
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
			out = append(out, txt)
			if len(out) >= limit {
				return out
			}
		}
	}
	return out
}

func collectTranscriptSnippet(cues []model.TranscriptCue, start, end float64, maxChars int) string {
	if len(cues) == 0 {
		return ""
	}
	var parts []string
	total := 0
	for _, c := range cues {
		if c.End < start {
			continue
		}
		if c.Start > end {
			break
		}
		parts = append(parts, c.Text)
		total += len(c.Text) + 1
		if total >= maxChars {
			break
		}
	}
	res := strings.TrimSpace(strings.Join(parts, " "))
	if len(res) > maxChars {
		res = res[:maxChars] + "..."
	}
	return res
}

func findChapterTitle(chapters []model.Chapter, sec float64) string {
	for _, ch := range chapters {
		if sec >= ch.StartTime && sec <= ch.EndTime {
			return ch.Title
		}
	}
	return ""
}

func windowMax(arr []float64, l, r int) float64 {
	if len(arr) == 0 {
		return 0
	}
	l = clampInt(l, 0, len(arr)-1)
	r = clampInt(r, l+1, len(arr))
	m := 0.0
	for i := l; i < r; i++ {
		if arr[i] > m {
			m = arr[i]
		}
	}
	return m
}

func percentile(vals []float64, p float64) float64 {
	if len(vals) == 0 {
		return 0
	}
	cp := make([]float64, len(vals))
	copy(cp, vals)
	sort.Float64s(cp)
	idx := int(math.Floor(p * float64(len(cp)-1)))
	if idx < 0 {
		idx = 0
	}
	if idx >= len(cp) {
		idx = len(cp) - 1
	}
	return cp[idx]
}

func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func round3(v float64) float64 {
	return math.Round(v*1000) / 1000
}

// FormatMMSS formats seconds as MM:SS or H:MM:SS.
func FormatMMSS(sec float64) string {
	total := int(math.Round(sec))
	if total < 0 {
		total = 0
	}
	h := total / 3600
	m := (total % 3600) / 60
	s := total % 60
	if h > 0 {
		return fmt.Sprintf("%d:%02d:%02d", h, m, s)
	}
	return fmt.Sprintf("%02d:%02d", m, s)
}
