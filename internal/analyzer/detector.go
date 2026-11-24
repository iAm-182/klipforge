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
	DurationSec    float64
	Heatmap        []youtube.HeatmapPoint
	Chats          []youtube.ChatMessage
	AudioEnergy    []float64 // 1-second buckets (0.0 - 1.0)
	Transcript     []model.TranscriptCue
	Chapters       []model.Chapter
	Settings       model.DetectionSettings
	ForbiddenWords []string  // Campaign-level words never allowed in auto-generated hooks
	SceneCuts      []float64 // Optional shot boundary timestamps (PySceneDetect / FFmpeg) for boundary snapping
	PrimarySpeaker string    // Optional diarized primary speaker (e.g. "SPEAKER_0" = Windah)
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

// Speech reaction & panic keywords spoken by Indonesian/English gaming streamers (instant transcript hype!).
var speechHypeKeywords = []string{
	"anjir", "njir", "bjir", "anjay", "gila", "parah", "kaget", "panik", "tolong", "mati",
	"aduh", "waduh", "astaga", "buset", "kabur", "polisi", "meledak", "tamat", "hancur",
	"ngakak", "lucu", "kocak", "gokil", "mantap", "keren", "serius", "kacau", "sialan",
	"bangsat", "woy", "woi", "wkwk", "haha", "gg", "epic", "clutch", "omg", "wtf", "oh my god",
	"no way", "crazy", "what the", "tertawa", "tepuk tangan", "wah", "eh eh", "jangan", "lari",
	"nyetir", "nabrak", "jago", "wasted", "bocor", "pipa", "goblok", "anjing", "babi", "sumpah", "bocil",
}

// Indonesian streamer presence tokens vs English cutscene dialogue tokens (prevents quiet English cutscenes from topping the list!).
var indoStreamerTokens = []string{
	"gua ", "gue ", "guys", "cuy", "bang ", "bocil", "anjir", "kocak", "parah", "bener",
	"kalau", "enggak", "gak ", "nih ", "sih ", "dong", "tuh ", "kok ", "anjay", "gila",
	"aduh", "waduh", "tolong", "kabur", "nyetir", "nabrak", "polisi", "jago", "lihat", "liat",
}

var englishCutsceneTokens = []string{
	"what's the", "going on", "you sure", "all right", "gonna ", "don't ", "nothing",
	"because ", "should ", "would ", "could ", "something", "anything", "listen ",
	"sorry", "we'll ", "that's ", "they're ", "i'm ", "you're ", "let's see", "how's it",
}

// DetectHype runs multi-signal fusion (Heatmap + Live Chat + Audio Energy + Instant Transcript Hype) and extracts ranked clip candidates.
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
		for _, s := range in.Transcript {
			if int(math.Ceil(s.End)) > dur {
				dur = int(math.Ceil(s.End))
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
	transcriptGrid := make([]float64, dur)
	chatBucketSamples := make([][]string, dur)

	hasHeatmap := len(in.Heatmap) > 0
	hasChat := len(in.Chats) > 20
	hasAudio := len(in.AudioEnergy) > 5
	hasTranscript := len(in.Transcript) > 0
	viralityGrid := buildViralityGrid(dur, in.Transcript)
	hasVirality := false
	for _, v := range viralityGrid {
		if v > 0.25 {
			hasVirality = true
			break
		}
	}

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

		smoothed := make([]float64, dur)
		win := 3
		for i := 0; i < dur; i++ {
			var sum, cnt float64
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

	// 4. Populate Instant Transcript Hype grid (speech burst velocity + panic/funny reaction keywords!)
	if hasTranscript {
		rawSpeech := make([]float64, dur)
		for _, cue := range in.Transcript {
			s := clampInt(int(math.Floor(cue.Start)), 0, dur-1)
			e := clampInt(int(math.Ceil(cue.End)), s+1, dur)
			score := scoreTranscriptCue(cue)
			span := float64(e - s)
			if span < 1 {
				span = 1
			}
			perSec := score / span
			for t := s; t < e; t++ {
				rawSpeech[t] += perSec
			}
		}
		smoothed := make([]float64, dur)
		win := 6 // 13-second window catches comedic exchanges & panic bursts
		for i := 0; i < dur; i++ {
			var sum, cnt float64
			for j := i - win; j <= i+win; j++ {
				if j >= 0 && j < dur {
					sum += rawSpeech[j]
					cnt++
				}
			}
			if cnt > 0 {
				smoothed[i] = sum / cnt
			}
		}
		p98 := percentile(smoothed, 0.985)
		if p98 <= 0 {
			p98 = 1.0
		}
		for i := 0; i < dur; i++ {
			v := smoothed[i] / p98
			if v > 1.0 {
				v = 1.0
			}
			transcriptGrid[i] = v
		}
	}

	// 5. Dynamic Multi-Signal Weight Redistribution (Audio is ONE signal among four, never dominating alone!)
	wH, wC, wA, wT, wV := 0.0, 0.0, 0.0, 0.0, 0.0
	if hasHeatmap {
		wH = cfg.HeatmapWeight
	}
	if hasChat {
		wC = cfg.ChatWeight
	}
	if hasAudio {
		wA = cfg.AudioWeight
	}
	if hasTranscript {
		if !hasHeatmap && !hasChat && !hasAudio {
			wT = 0.45
			wV = 0.55
		} else if !hasHeatmap && !hasChat && hasAudio {
			// Balance audio energy with transcript hype & rule-based virality rubric so quiet crashes/boasts rank properly!
			wA = 0.35
			wT = 0.25
			wV = 0.40
		} else {
			wT = 0.18
			if hasVirality {
				wV = 0.28
			}
		}
	}
	if !hasVirality {
		wV = 0
	}
	wTotal := wH + wC + wA + wT + wV
	if wTotal <= 0 {
		wTotal = 1.0
	} else {
		wH /= wTotal
		wC /= wTotal
		wA /= wTotal
		wT /= wTotal
		wV /= wTotal
	}

	combined := make([]float64, dur)
	skipIntro := int(cfg.SkipIntroSec)
	for i := 0; i < dur; i++ {
		score := wH*heatGrid[i] + wC*chatGrid[i] + wA*audioGrid[i] + wT*transcriptGrid[i] + wV*viralityGrid[i]

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
		if hasTranscript && transcriptGrid[i] >= 0.65 {
			activeHigh++
		}
		if hasVirality && viralityGrid[i] >= 0.55 {
			activeHigh++
		}
		if activeHigh >= 2 {
			score = math.Min(1.0, score*1.16)
		}

		if i < skipIntro && (!hasChat || chatGrid[i] < 0.65) && (!hasAudio || audioGrid[i] < 0.70) {
			score *= 0.30
		}

		combined[i] = score
	}

	// Extract arc-aware candidates with setup->payoff expansion, 15s peak merging, scene snapping & speaker turns
	candidates := extractCandidates(dur, combined, heatGrid, chatGrid, audioGrid, transcriptGrid, viralityGrid, chatBucketSamples, in.Transcript, in.Chapters, cfg, hasHeatmap, hasChat, hasAudio, hasTranscript, in.ForbiddenWords, in.SceneCuts)

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
		var maxH, maxC, maxA, maxT, maxComb float64
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
			if transcriptGrid[j] > maxT {
				maxT = transcriptGrid[j]
			}
			if combined[j] > maxComb {
				maxComb = combined[j]
			}
			sumChat += chatCounts[j]
		}
		timeline = append(timeline, model.TimelinePoint{
			TimeSec:        float64(i),
			Heatmap:        round3(maxH),
			ChatHype:       round3(maxC),
			AudioPeak:      round3(maxA),
			TranscriptHype: round3(maxT),
			Combined:       round3(maxComb),
			ChatCount:      sumChat,
		})
	}

	return AnalysisResult{
		Timeline:   timeline,
		Candidates: candidates,
		Signals: model.SignalAvailability{
			HasHeatmap:        hasHeatmap,
			HasLiveChat:       hasChat,
			HasAudio:          hasAudio,
			HasSubtitles:      len(in.Transcript) > 0,
			HasTranscriptHype: hasTranscript,
			ChatMsgCount:      len(in.Chats),
			SubCueCount:       len(in.Transcript),
			HeatmapPoints:     len(in.Heatmap),
		},
	}
}

func extractCandidates(
	dur int,
	combined, heat, chat, audio, transcriptGrid, viralityGrid []float64,
	chatSamples [][]string,
	transcript []model.TranscriptCue,
	chapters []model.Chapter,
	cfg model.DetectionSettings,
	hasHeatmap, hasChat, hasAudio, hasTranscript bool,
	forbiddenWords []string,
	sceneCuts []float64,
) []model.ClipCandidate {
	type peak struct {
		sec               int
		earliestSetupHint int
		score             float64
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

	var rawPeaks []peak
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
			rawPeaks = append(rawPeaks, peak{sec: i, earliestSetupHint: i, score: combined[i]})
		}
	}

	// If very strict threshold yielded too few clips on a flat video, relax gracefully
	if len(rawPeaks) == 0 && dur > 30 {
		bestSec := 0
		bestVal := -1.0
		for i := int(cfg.SkipIntroSec); i < dur; i++ {
			if smooth[i] > bestVal {
				bestVal = smooth[i]
				bestSec = i
			}
		}
		if bestVal > 0 {
			rawPeaks = append(rawPeaks, peak{sec: bestSec, earliestSetupHint: bestSec, score: bestVal})
		}
	}

	// Merge peaks closer than 15 seconds apart into ONE unified arc:
	// keep the strongest peak as arc peak, and preserve the earliest setup start!
	sort.Slice(rawPeaks, func(i, j int) bool {
		return rawPeaks[i].sec < rawPeaks[j].sec
	})
	var peaks []peak
	for _, rp := range rawPeaks {
		if len(peaks) > 0 && rp.sec-peaks[len(peaks)-1].sec < 15 {
			prev := &peaks[len(peaks)-1]
			if rp.earliestSetupHint < prev.earliestSetupHint {
				prev.earliestSetupHint = rp.earliestSetupHint
			}
			if rp.score > prev.score {
				prev.sec = rp.sec
				prev.score = rp.score
			}
		} else {
			peaks = append(peaks, rp)
		}
	}

	// Sort merged peaks descending by score
	sort.Slice(peaks, func(i, j int) bool {
		return peaks[i].score > peaks[j].score
	})

	// Non-Maximum Suppression & Arc-Aware Setup->Payoff Boundary Expansion
	var selected []model.ClipCandidate
	usedHooks := make(map[string]bool)
	for _, pk := range peaks {
		if len(selected) >= cfg.MaxClips {
			break
		}

		// Expand into setup->payoff arc clamped to 20-45s!
		left, right, arcReason := expandSetupPayoffArc(pk.sec, pk.earliestSetupHint, dur, transcript, chat, audio, smooth)

		// Respect user-configured MaxDurationSec if explicitly tighter/wider than 45s while preserving arc bounds
		if cfg.MaxDurationSec >= 20 && cfg.MaxDurationSec <= 45 && float64(right-left) > cfg.MaxDurationSec {
			right = clampInt(left+int(cfg.MaxDurationSec), pk.sec+6, dur)
		}

		// Optional scene-cut boundary snapping (B3.2): snap arcStart/arcEnd to nearest shot cut while protecting peakSec!
		sceneSnapped := false
		if len(sceneCuts) > 0 {
			ns, ne, snapped := SnapBoundariesToScenes(float64(left), float64(right), float64(pk.sec), sceneCuts, 3.0, 15.0)
			if snapped {
				left = clampInt(int(math.Round(ns)), 0, dur-1)
				right = clampInt(int(math.Round(ne)), left+1, dur)
				sceneSnapped = true
			}
		}

		// Check overlap against already selected arcs
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

		// Gather window stats + rule-based Virality Rubric (Hook, Emotion, Conflict, Quotable)
		maxH := windowMax(heat, left, right)
		maxC := windowMax(chat, left, right)
		maxA := windowMax(audio, left, right)
		maxT := windowMax(transcriptGrid, left, right)
		vScore, vBreakdown := ScoreViralityWindow(transcript, float64(left), float64(right))

		// Blend peak score with arc virality so strong boast->crash->reaction arcs outrank empty loud noise!
		blended := pk.score * 100
		if vScore > 0 {
			blended = 0.65*(pk.score*100) + 0.35*vScore
		}
		score100 := math.Round(math.Min(100, blended))

		reasons := buildReasons(maxH, maxC, maxA, maxT, hasHeatmap, hasChat, hasAudio, hasTranscript)
		if vScore >= 55 {
			reasons = append(reasons, fmt.Sprintf("Virality %.0f", vScore))
		}
		samples := collectChatSamples(chatSamples, pk.sec-5, pk.sec+12, 5)
		speech := collectTranscriptSnippet(transcript, float64(left), float64(right), 220)
		chapTitle := findChapterTitle(chapters, float64(pk.sec))

		rank := len(selected) + 1
		title := fmt.Sprintf("Viral Moment #%d (%s)", rank, FormatMMSS(float64(pk.sec)))
		if chapTitle != "" {
			title = fmt.Sprintf("#%d %s (%s)", rank, chapTitle, FormatMMSS(float64(pk.sec)))
		}

		hookText := suggestHookText(rank, reasons, samples, transcript, float64(pk.sec), speech, chapTitle, forbiddenWords, usedHooks)
		usedHooks[strings.ToUpper(hookText)] = true

		vbCopy := vBreakdown
		momentType := ClassifyMomentType(speech, arcReason, samples)
		spkSum := SummarizeSpeakersInWindow(transcript, float64(left), float64(right))
		selected = append(selected, model.ClipCandidate{
			ID:                fmt.Sprintf("clip-%d-%d", left, right),
			Rank:              rank,
			Title:             title,
			StartSec:          float64(left),
			EndSec:            float64(right),
			PeakSec:           float64(pk.sec),
			ArcStartSec:       float64(left),
			ArcEndSec:         float64(right),
			ArcReason:         arcReason,
			MomentType:        momentType,
			SceneSnapped:      sceneSnapped,
			SpeakerSummary:    spkSum,
			DurationSec:       float64(right - left),
			Score:             score100,
			HeatmapScore:      math.Round(maxH * 100),
			ChatScore:         math.Round(maxC * 100),
			AudioScore:        math.Round(maxA * 100),
			ViralityScore:     vScore,
			ViralityBreakdown: &vbCopy,
			Reasons:           reasons,
			SampleChats:       samples,
			Transcript:        speech,
			HookText:          hookText,
			Selected:          rank <= 5,
		})
	}

	// Re-rank descending by final blended score
	sort.SliceStable(selected, func(i, j int) bool {
		return selected[i].Score > selected[j].Score
	})
	for i := range selected {
		selected[i].Rank = i + 1
		selected[i].Selected = i < 5
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

func scoreTranscriptCue(cue model.TranscriptCue) float64 {
	txt := strings.TrimSpace(cue.Text)
	if txt == "" {
		return 0
	}
	words := strings.Fields(txt)
	dur := math.Max(0.4, cue.End-cue.Start)
	wps := float64(len(words)) / dur
	// Base weight from rapid speech intensity
	score := math.Min(2.5, wps*0.45)

	lower := " " + strings.ToLower(txt) + " "
	kwHits := 0
	for _, kw := range speechHypeKeywords {
		if strings.Contains(lower, kw) {
			kwHits++
		}
	}
	score += float64(kwHits) * 1.8

	// Use real diarized Speaker turns (B3.4 sherpa-onnx) when available; fall back to keyword heuristic otherwise.
	if cue.Speaker != "" {
		if cue.Speaker == "SPEAKER_0" {
			score = score*1.35 + 1.2
		} else if kwHits == 0 {
			score *= 0.30
		}
	} else {
		// Boost genuine Indonesian streamer commentary vs English NPC/cutscene dialogue!
		indoHits := 0
		for _, tk := range indoStreamerTokens {
			if strings.Contains(lower, tk) {
				indoHits++
			}
		}
		engHits := 0
		for _, tk := range englishCutsceneTokens {
			if strings.Contains(lower, tk) {
				engHits++
			}
		}
		if indoHits > 0 {
			score += float64(indoHits) * 0.9
		} else if engHits > 0 && kwHits == 0 {
			// Dampen pure English NPC cutscenes so real Windah moments float to #1!
			score *= 0.25
		}
	}

	if strings.Contains(txt, "!") || strings.Contains(txt, "?") {
		score += 0.8
	}
	return score
}

// SearchTranscript performs an instant (<5ms) keyword search across a VOD's transcript cues
// and returns grouped context hits ready to preview or add to a Top-N compilation!
func SearchTranscript(cues []model.TranscriptCue, query string, limit int) []model.TranscriptHit {
	q := strings.TrimSpace(strings.ToLower(query))
	if q == "" || len(cues) == 0 {
		return nil
	}
	if limit <= 0 {
		limit = 40
	}
	terms := strings.Fields(q)
	var hits []model.TranscriptHit
	lastAddedSec := -999.0

	for i, c := range cues {
		low := strings.ToLower(c.Text)
		matched := true
		for _, t := range terms {
			if !strings.Contains(low, t) {
				matched = false
				break
			}
		}
		if !matched {
			continue
		}
		// Deduplicate hits within 6 seconds of each other
		if c.Start-lastAddedSec < 6.0 {
			continue
		}
		lastAddedSec = c.Start

		// Build 1-cue before + 2-cues after context snippet so user instantly sees the whole joke/moment!
		var ctxParts []string
		from := clampInt(i-1, 0, len(cues)-1)
		to := clampInt(i+2, i, len(cues)-1)
		for k := from; k <= to; k++ {
			ctxParts = append(ctxParts, cues[k].Text)
		}
		ctxStr := strings.Join(ctxParts, " ")
		if len(ctxStr) > 190 {
			ctxStr = ctxStr[:190] + "..."
		}

		hits = append(hits, model.TranscriptHit{
			StartSec: c.Start,
			EndSec:   c.End,
			TimeText: FormatMMSS(c.Start),
			Text:     c.Text,
			Context:  ctxStr,
		})
		if len(hits) >= limit {
			break
		}
	}
	return hits
}

func buildReasons(maxH, maxC, maxA, maxT float64, hasH, hasC, hasA, hasT bool) []string {
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
	if hasT && maxT >= 0.65 {
		reasons = append(reasons, "Ucapan & Reaksi Hype")
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

var defaultBannedHookWords = []string{
	"anjing", "babi", "bangsat", "kontol", "memek", "ngentot", "jancok", "cok",
	"goblok", "tolol", "bajingan", "brengsek", "fuck", "shit", "bitch", "nigger", "nigga",
}

// ContainsForbiddenWord returns true if text matches any built-in profanity or campaign forbidden word.
func ContainsForbiddenWord(text string, forbidden []string) bool {
	return containsForbiddenWord(text, forbidden)
}

func containsForbiddenWord(text string, forbidden []string) bool {
	low := " " + strings.ToLower(text) + " "
	for _, bw := range defaultBannedHookWords {
		if strings.Contains(low, bw) {
			return true
		}
	}
	for _, fw := range forbidden {
		w := strings.TrimSpace(strings.ToLower(fw))
		if w != "" && strings.Contains(low, w) {
			return true
		}
	}
	return false
}

func cleanHookPhrase(raw string) string {
	// Keep letters, digits, spaces, and punchy punctuation
	var b strings.Builder
	for _, r := range raw {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == ' ' || r == '!' || r == '?' || r == '-' {
			b.WriteRune(r)
		} else {
			b.WriteRune(' ')
		}
	}
	words := strings.Fields(b.String())
	if len(words) == 0 {
		return ""
	}
	// Strip leading filler words so the hook hits immediately
	fillers := map[string]bool{
		"dan": true, "yang": true, "di": true, "ke": true, "itu": true, "ini": true,
		"nah": true, "ya": true, "eh": true, "uh": true, "um": true, "musik": true,
		"tertawa": true, "tepuk": true, "tangan": true, "tapi": true, "jadi": true,
	}
	for len(words) > 3 && fillers[strings.ToLower(words[0])] {
		words = words[1:]
	}
	if len(words) > 6 {
		words = words[:6]
	}
	out := strings.ToUpper(strings.Join(words, " "))
	if len(out) > 40 {
		out = strings.TrimSpace(out[:40])
	}
	return out
}

func suggestHookText(
	rank int,
	reasons, sampleChats []string,
	transcript []model.TranscriptCue,
	peakSec float64,
	speech, chapterTitle string,
	forbiddenWords []string,
	usedHooks map[string]bool,
) string {
	tryCandidate := func(s string) (string, bool) {
		c := strings.TrimSpace(s)
		if len(c) < 6 {
			return "", false
		}
		if containsForbiddenWord(c, forbiddenWords) {
			return "", false
		}
		key := strings.ToUpper(c)
		if usedHooks[key] {
			return "", false
		}
		return key, true
	}

	if chapterTitle != "" && len(chapterTitle) <= 36 {
		if h, ok := tryCandidate(chapterTitle); ok {
			return h
		}
	}

	// 1. Extract the punchiest, highest-scoring Indonesian transcript phrase right near peakSec!
	if len(transcript) > 0 {
		type scoredCue struct {
			text  string
			score float64
		}
		var nearby []scoredCue
		for _, cue := range transcript {
			if cue.End < peakSec-8 || cue.Start > peakSec+10 {
				continue
			}
			phrase := cleanHookPhrase(cue.Text)
			if len(strings.Fields(phrase)) < 3 || containsForbiddenWord(phrase, forbiddenWords) {
				continue
			}
			sc := scoreTranscriptCue(cue)
			// Prefer cues closest to the actual peak second
			dist := math.Abs((cue.Start+cue.End)/2.0 - peakSec)
			sc += math.Max(0, 3.0-dist*0.25)
			nearby = append(nearby, scoredCue{text: phrase, score: sc})
		}
		sort.Slice(nearby, func(i, j int) bool {
			return nearby[i].score > nearby[j].score
		})
		for _, nc := range nearby {
			hook := nc.text
			if !strings.HasSuffix(hook, "!") && !strings.HasSuffix(hook, "?") {
				hook += "!"
			}
			if h, ok := tryCandidate(hook); ok {
				return h
			}
		}
	}

	// 2. Contextual keyword hooks (varied, never all identical)
	combined := strings.ToLower(strings.Join(sampleChats, " ") + " " + speech)
	topicTemplates := []struct {
		keys []string
		hook string
	}{
		{[]string{"polisi", "kabur", "kejar"}, "PANIK DIKEJAR POLISI!"},
		{[]string{"nabrak", "tabrak", "nyetir", "mobil", "motor"}, "DETIK-DETIK NYETIR AUTO NABRAK!"},
		{[]string{"kaget", "panik", "tolong", "ampun"}, "DETIK-DETIK PANIK PARAH!"},
		{[]string{"wkwk", "ngakak", "kocak", "lucu"}, "NGAKAK GAK KETAHAN DI STREAM!"},
		{[]string{"meledak", "mati", "hancur", "wasted"}, "AUTO KACAU MELEDAK PARAH!"},
		{[]string{"bos", "misi", "tembak", "senjata"}, "AKSI TEMBAK-TEMBAKAN PALING TEGANG!"},
		{[]string{"npc", "orang", "cewek", "cowok"}, "INTERAKSI NPC PALING KOCAK!"},
		{[]string{"bocil", "absen", "windah", "brando"}, "MOMEN EPIC BARENG BOCIL!"},
	}
	for _, tt := range topicTemplates {
		for _, k := range tt.keys {
			if strings.Contains(combined, k) {
				if h, ok := tryCandidate(tt.hook); ok {
					return h
				}
				break
			}
		}
	}

	// 3. Diverse rank-indexed fallbacks so NO two cards ever look copy-pasted!
	fallbacks := []string{
		"MOMEN PALING PECAH DI STREAM!",
		"REAKSI SPONTAN GAK KETEBAK!",
		"PLOT TWIST TAK TERDUGA!",
		"DETIK-DETIK PALING RAMAI!",
		"CLIP WAJIB NONTON SAMPAI HABIS!",
		"KEKACAUAN PALING KOCAK!",
		"REAKSI TERIAK PALING EPIC!",
		"HIGHLIGHT TERPANAS STREAM INI!",
	}
	for i := 0; i < len(fallbacks); i++ {
		idx := (rank - 1 + i) % len(fallbacks)
		if h, ok := tryCandidate(fallbacks[idx]); ok {
			return h
		}
	}
	return fmt.Sprintf("MOMEN VIRAL #%d (%s)!", rank, FormatMMSS(peakSec))
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
