package analyzer

import (
	"fmt"
	"math"
	"strings"

	"klipforge/internal/model"
)

// Rule-based offline virality & arc lexicons (zero LLM / zero API key required,
// modeled on clipsmith AI_PROVIDER=none + AI-Youtube-Shorts-Generator virality rubric).
var (
	setupBoastKeywords = []string{
		"jago", "nyetir", "latihan", "lihat nih", "liat nih", "gampang", "tenang",
		"gua bisa", "gue bisa", "paling jago", "pro player", "santai", "coba lihat",
		"tunggu", "watch this", "ez banget", "percaya sama gua", "skill", "drift",
		"gaspol", "ngebut", "berani", "tantang", "bukti",
	}

	conflictKeywords = []string{
		"nabrak", "tabrak", "polisi", "dikejar", "kejar", "tembak", "nembak", "dor",
		"mati", "tewas", "meledak", "ledak", "jatuh", "kecebur", "kebalik", "nyangkut",
		"hancur", "rusak", "kalah", "gagal", "bintang", "wasted", "busted", "perang",
		"serang", "musuh", "bunuh", "kabur", "kepung", "tangkap", "crash", "boom",
		"kecelakaan", "terbang", "mental", "kelindes", "ketabrak",
	}

	reactionPayoffKeywords = []string{
		"wkwk", "kwkw", "haha", "ngakak", "kocak", "lucu", "anjir", "njir", "bjir",
		"anjay", "gila", "parah", "astaga", "ya ampun", "waduh", "aduh", "buset",
		"lah", "kok bisa", "tolong", "panik", "kaget", "sumpah", "bego", "bodoh",
		"kacau", "tamat", "riwayat", "sialan", "what the", "no way", "omg", "bruh",
		"tertawa", "alasan", "bisa-bisanya",
	}

	hookOpeningTriggers = []string{
		"lihat", "liat", "gua jago", "gue jago", "coba", "jangan", "hati-hati",
		"serius", "tunggu", "eh", "guys", "cuy", "latihan", "akhirnya", "ternyata",
		"gila", "parah", "anjir", "kenapa", "gimana", "bisa", "paling", "rahasia",
	}
)

// ScoreViralityWindow computes an offline 4-dimension virality rubric (0-100 each)
// for a transcript window [startSec, endSec]: Hook, Emotion, Conflict, Quotable.
func ScoreViralityWindow(cues []model.TranscriptCue, startSec, endSec float64) (float64, model.ViralityBreakdown) {
	if len(cues) == 0 || endSec <= startSec {
		return 0, model.ViralityBreakdown{}
	}

	var windowCues []model.TranscriptCue
	for _, c := range cues {
		if c.End >= startSec && c.Start <= endSec {
			windowCues = append(windowCues, c)
		}
	}
	if len(windowCues) == 0 {
		return 0, model.ViralityBreakdown{}
	}

	var hookRaw, emotionRaw, conflictRaw, quotableRaw float64
	openingCutoff := startSec + math.Min(10.0, (endSec-startSec)*0.35)

	for idx, c := range windowCues {
		txt := strings.TrimSpace(c.Text)
		if txt == "" {
			continue
		}
		low := " " + strings.ToLower(txt) + " "
		words := strings.Fields(txt)
		wc := len(words)

		// 1. Hook strength (opening cues + boast/question/challenge lines)
		isOpening := idx < 2 || c.Start <= openingCutoff
		if isOpening {
			for _, tk := range hookOpeningTriggers {
				if strings.Contains(low, tk) {
					hookRaw += 22
				}
			}
			for _, tk := range setupBoastKeywords {
				if strings.Contains(low, tk) {
					hookRaw += 28
				}
			}
			if strings.ContainsAny(txt, "!?") {
				hookRaw += 18
			}
			if wc >= 3 && wc <= 12 {
				hookRaw += 12
			}
		}

		// 2. Emotion (laugh / panic / surprise — reuses speechHypeKeywords + reactionPayoffKeywords)
		for _, kw := range speechHypeKeywords {
			if strings.Contains(low, kw) {
				emotionRaw += 16
			}
		}
		for _, kw := range reactionPayoffKeywords {
			if strings.Contains(low, kw) {
				emotionRaw += 14
			}
		}
		if strings.Contains(txt, "!") {
			emotionRaw += 8
		}

		// 3. Conflict / Action / Fail (crash, police, death, gunfight, chase)
		for _, kw := range conflictKeywords {
			if strings.Contains(low, kw) {
				conflictRaw += 28
			}
		}

		// 4. Quotable punchiness (short punchy Indonesian lines, boast->payoff contrast)
		if wc >= 3 && wc <= 11 {
			hasPunch := strings.ContainsAny(txt, "!?")
			if !hasPunch {
				for _, kw := range setupBoastKeywords {
					if strings.Contains(low, kw) {
						hasPunch = true
						break
					}
				}
			}
			if !hasPunch {
				for _, kw := range reactionPayoffKeywords {
					if strings.Contains(low, kw) {
						hasPunch = true
						break
					}
				}
			}
			if hasPunch {
				quotableRaw += 24
			} else {
				quotableRaw += 6
			}
		}
	}

	// Setup -> Payoff contrast bonus (e.g. boast followed by conflict/reaction in the same window!)
	hasSetup, hasConflict, hasReaction := false, false, false
	for _, c := range windowCues {
		low := " " + strings.ToLower(c.Text) + " "
		if containsAnySub(low, setupBoastKeywords) {
			hasSetup = true
		}
		if containsAnySub(low, conflictKeywords) {
			hasConflict = true
		}
		if containsAnySub(low, reactionPayoffKeywords) {
			hasReaction = true
		}
	}
	if hasSetup && (hasConflict || hasReaction) {
		hookRaw += 25
		quotableRaw += 25
	}

	bd := model.ViralityBreakdown{
		Hook:     math.Round(clamp0100(hookRaw)),
		Emotion:  math.Round(clamp0100(emotionRaw)),
		Conflict: math.Round(clamp0100(conflictRaw)),
		Quotable: math.Round(clamp0100(quotableRaw)),
	}
	composite := math.Round(0.25*bd.Hook + 0.30*bd.Emotion + 0.30*bd.Conflict + 0.15*bd.Quotable)
	return composite, bd
}

// buildViralityGrid builds a 1-second normalized virality curve (0.0 - 1.0) across the VOD
// so rule-based virality (hook + emotion + conflict + quotable) fuses alongside audio, chat, and heatmap.
func buildViralityGrid(dur int, cues []model.TranscriptCue) []float64 {
	grid := make([]float64, dur)
	if dur <= 0 || len(cues) == 0 {
		return grid
	}
	// Evaluate overlapping 20s windows every 4 seconds for O(N) speed even on 5-hour VODs
	step := 4
	win := 20.0
	raw := make([]float64, dur)
	for sec := 0; sec < dur; sec += step {
		score, _ := ScoreViralityWindow(cues, float64(sec), float64(sec)+win)
		norm := score / 100.0
		end := sec + int(win)
		if end > dur {
			end = dur
		}
		for t := sec; t < end; t++ {
			if norm > raw[t] {
				raw[t] = norm
			}
		}
	}
	p98 := percentile(raw, 0.985)
	if p98 < 0.35 {
		p98 = 1.0
	}
	for i := 0; i < dur; i++ {
		v := raw[i] / p98
		if v > 1.0 {
			v = 1.0
		}
		grid[i] = v
	}
	return grid
}

// expandSetupPayoffArc widens a peak into a full comedic/action setup -> peak -> payoff arc,
// clamped to [20s, 45s], and synthesizes a crisp 1-line ArcReason!
func expandSetupPayoffArc(
	peakSec int,
	earliestSetupHint int,
	dur int,
	cues []model.TranscriptCue,
	chatGrid, audioGrid, smooth []float64,
) (arcStart int, arcEnd int, reason string) {
	const minArc = 20
	const maxArc = 45

	left := peakSec - 10
	if earliestSetupHint >= 0 && earliestSetupHint < left && peakSec-earliestSetupHint <= 30 {
		left = earliestSetupHint
	}
	right := peakSec + 14

	var setupPhrase, conflictPhrase, payoffPhrase string

	// 1. Walk BACKWARDS up to 28s before peak to capture boast/setup cues & rising chat build-up
	searchStart := peakSec - 28
	if searchStart < 0 {
		searchStart = 0
	}
	for i := len(cues) - 1; i >= 0; i-- {
		c := cues[i]
		if c.Start > float64(peakSec)+1 {
			continue
		}
		if c.End < float64(searchStart) {
			break
		}
		low := " " + strings.ToLower(c.Text) + " "
		if kw := firstMatchingSub(low, setupBoastKeywords); kw != "" {
			if int(math.Floor(c.Start))-1 < left {
				left = int(math.Floor(c.Start)) - 1
			}
			if setupPhrase == "" {
				setupPhrase = shortArcQuote(c.Text, kw)
			}
		}
		if kw := firstMatchingSub(low, conflictKeywords); kw != "" && conflictPhrase == "" && c.Start >= float64(peakSec-10) {
			conflictPhrase = kw
		}
	}
	// Also walk backwards while chat hype is rising steadily before peak
	for left > searchStart && left > 0 && peakSec-left < 24 {
		if chatGrid[left] >= 0.45 && (left == 0 || chatGrid[left] >= chatGrid[left-1]*0.85) {
			left--
		} else {
			break
		}
	}

	// 2. Walk FORWARDS up to 28s after peak until reaction/comment payoff lands & audio falls off
	searchEnd := peakSec + 28
	if searchEnd > dur {
		searchEnd = dur
	}
	lastReactionEnd := peakSec + 8
	for _, c := range cues {
		if c.End < float64(peakSec)-2 {
			continue
		}
		if c.Start > float64(searchEnd) {
			break
		}
		low := " " + strings.ToLower(c.Text) + " "
		if kw := firstMatchingSub(low, conflictKeywords); kw != "" && conflictPhrase == "" {
			conflictPhrase = kw
		}
		if kw := firstMatchingSub(low, reactionPayoffKeywords); kw != "" {
			ce := int(math.Ceil(c.End)) + 2
			if ce > lastReactionEnd {
				lastReactionEnd = ce
			}
			if payoffPhrase == "" {
				payoffPhrase = kw
			}
		}
	}
	if lastReactionEnd > right {
		right = lastReactionEnd
	}
	// Follow audio energy falloff right after peak
	for right < searchEnd && right < dur && right-left < maxArc {
		if audioGrid[right] >= 0.55 || smooth[right] >= smooth[peakSec]*0.60 {
			right++
		} else {
			break
		}
	}

	// 3. Clamp to [0, dur] and [minArc=20s, maxArc=45s] while always keeping peakSec inside!
	if left < 0 {
		left = 0
	}
	if right > dur {
		right = dur
	}
	if right-left > maxArc {
		// Prioritize preserving full setup before peak + payoff right after peak!
		maxLead := 26
		if peakSec-left > maxLead {
			left = peakSec - maxLead
		}
		right = left + maxArc
		if right < peakSec+10 {
			right = peakSec + 10
			left = right - maxArc
		}
		if left < 0 {
			left = 0
			right = clampInt(maxArc, 1, dur)
		}
		if right > dur {
			right = dur
			left = clampInt(dur-maxArc, 0, dur-1)
		}
	}
	if right-left < minArc && dur >= minArc {
		need := minArc - (right - left)
		left = clampInt(left-need/2, 0, dur-1)
		right = clampInt(left+minArc, 1, dur)
		if right-left < minArc {
			left = clampInt(right-minArc, 0, dur-1)
		}
	}

	// 4. Build concise 1-line ArcReason (e.g. "boast 'gua jago nyetir' → nabrak → ngakak")
	var parts []string
	if setupPhrase != "" {
		parts = append(parts, fmt.Sprintf("setup '%s'", setupPhrase))
	}
	if conflictPhrase != "" {
		parts = append(parts, conflictPhrase)
	} else {
		parts = append(parts, "peak")
	}
	if payoffPhrase != "" && payoffPhrase != conflictPhrase {
		parts = append(parts, fmt.Sprintf("reaksi (%s)", payoffPhrase))
	} else {
		parts = append(parts, "payoff")
	}
	reason = strings.Join(parts, " → ")
	return left, right, reason
}

func containsAnySub(haystack string, list []string) bool {
	return firstMatchingSub(haystack, list) != ""
}

func firstMatchingSub(haystack string, list []string) string {
	for _, kw := range list {
		if strings.Contains(haystack, kw) {
			return strings.TrimSpace(kw)
		}
	}
	return ""
}

func shortArcQuote(raw, fallback string) string {
	clean := strings.TrimSpace(strings.Join(strings.Fields(raw), " "))
	if clean == "" {
		return fallback
	}
	if len(clean) > 28 {
		// Center around matched keyword if possible
		low := strings.ToLower(clean)
		if idx := strings.Index(low, strings.ToLower(fallback)); idx >= 0 {
			start := idx - 6
			if start < 0 {
				start = 0
			}
			end := start + 26
			if end > len(clean) {
				end = len(clean)
			}
			return strings.TrimSpace(clean[start:end])
		}
		return strings.TrimSpace(clean[:28])
	}
	return clean
}

func clamp0100(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 100 {
		return 100
	}
	return v
}

// ClassifyMomentType performs fast offline rule-based classification of a candidate moment
// into one of: crash, fail, victory, twist, meme, or hype.
func ClassifyMomentType(transcript, arcReason string, sampleChats []string) string {
	var sb strings.Builder
	sb.WriteByte(' ')
	sb.WriteString(strings.ToLower(transcript))
	sb.WriteByte(' ')
	sb.WriteString(strings.ToLower(arcReason))
	for _, ch := range sampleChats {
		sb.WriteByte(' ')
		sb.WriteString(strings.ToLower(ch))
	}
	sb.WriteByte(' ')
	hay := sb.String()

	scoreOf := func(kws []string) int {
		n := 0
		for _, kw := range kws {
			if strings.Contains(hay, kw) {
				n++
			}
		}
		return n
	}

	crashKws := []string{"nabrak", "tabrak", "kecelakaan", "kelindes", "ketabrak", "kebalik", "meledak", "ledak", "nyangkut", "crash", "boom", "mental", "hancur"}
	failKws := []string{"mati", "tewas", "kalah", "gagal", "wasted", "busted", "jatuh", "kecebur", "tamat", "riwayat", "ketangkap", "tangkap", "kepung", "tembak", "baku tembak", "polisi"}
	victoryKws := []string{"menang", "clutch", "booyah", "juara", "berhasil", "lolos", "selamat", " gg ", "victory", "ez banget", "finish"}
	twistKws := []string{"ternyata", "plot twist", "kok bisa", "apaan tuh", "tiba-tiba", "gak nyangka", "ga nyangka", "kaget", "bisa-bisanya", "no way"}
	memeKws := []string{"wkwk", "kwkw", "ngakak", "kocak", "lucu", "badut", "alasan", "bruh", "lawak", "haha", "njir", "bjir"}

	scores := []struct {
		kind  string
		score int
	}{
		{"crash", scoreOf(crashKws) * 3},
		{"fail", scoreOf(failKws) * 3},
		{"victory", scoreOf(victoryKws) * 3},
		{"twist", scoreOf(twistKws) * 2},
		{"meme", scoreOf(memeKws) * 2},
	}

	bestKind := "hype"
	bestScore := 0
	for _, s := range scores {
		if s.score > bestScore {
			bestScore = s.score
			bestKind = s.kind
		}
	}
	return bestKind
}
