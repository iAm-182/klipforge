package analyzer

import (
	"testing"

	"klipforge/internal/model"
	"klipforge/internal/youtube"
)

func TestDetectHypeMultiSignalAndIntroDampening(t *testing.T) {
	// Simulate a 10-minute (600s) Windah stream:
	// - Classic YouTube 0:00 intro heatmap bias (1.0 at t=0..10s, no chat hype) -> should be dampened!
	// - Massive viral moment at t=180..205s (Heatmap 0.95 + Chat explosion "WKWKWK GILA" + Audio peak 0.95)
	// - Second hype moment at t=420..440s (Chat explosion even without high heatmap)
	var heat []youtube.HeatmapPoint
	for s := 0; s < 600; s += 10 {
		val := 0.15
		if s == 0 {
			val = 1.0 // Intro replay artifact
		} else if s >= 180 && s <= 200 {
			val = 0.95
		} else if s >= 420 && s <= 430 {
			val = 0.45
		}
		heat = append(heat, youtube.HeatmapPoint{
			StartTime: float64(s),
			EndTime:   float64(s + 10),
			Value:     val,
		})
	}

	var chats []youtube.ChatMessage
	// Baseline trickle
	for s := 0; s < 600; s += 15 {
		chats = append(chats, youtube.ChatMessage{OffsetSec: float64(s), Text: "halo bang"})
	}
	// Huge explosion at t=185..195
	for i := 0; i < 60; i++ {
		chats = append(chats, youtube.ChatMessage{
			OffsetSec: 185.0 + float64(i%10),
			Text:      "WKWKWK GILA KAGET NJIR 😭🔥",
		})
	}
	// Second explosion at t=425..432
	for i := 0; i < 40; i++ {
		chats = append(chats, youtube.ChatMessage{
			OffsetSec: 425.0 + float64(i%8),
			Text:      "GG EPIC PEAK GTA VI!!!",
		})
	}

	audio := make([]float64, 600)
	for i := range audio {
		audio[i] = 0.2
	}
	for i := 184; i <= 196; i++ {
		audio[i] = 0.96
	}

	res := DetectHype(AnalysisInput{
		DurationSec: 600,
		Heatmap:     heat,
		Chats:       chats,
		AudioEnergy: audio,
		Settings:    model.DefaultDetectionSettings(),
	})

	if !res.Signals.HasHeatmap || !res.Signals.HasLiveChat || !res.Signals.HasAudio {
		t.Fatalf("expected all 3 signals active, got %+v", res.Signals)
	}
	if len(res.Candidates) < 2 {
		t.Fatalf("expected at least 2 candidates, got %d", len(res.Candidates))
	}

	top := res.Candidates[0]
	if top.PeakSec < 170 || top.PeakSec > 210 {
		t.Fatalf("expected #1 peak near 185-195s (not intro 0s), got peak=%.1fs (score=%.1f)", top.PeakSec, top.Score)
	}
	if top.Score < 80 {
		t.Fatalf("expected high viral score >= 80 for multi-signal peak, got %.1f", top.Score)
	}
	if len(top.SampleChats) == 0 {
		t.Fatalf("expected sample hype chats populated")
	}
}

func TestUniqueHooksAndForbiddenWords(t *testing.T) {
	cues := []model.TranscriptCue{
		{Start: 60, End: 64, Text: "anjir mobil gua kebalik polisi kejar woi"},
		{Start: 64, End: 68, Text: "aduh nabrak tiang listrik parah banget"},
		{Start: 200, End: 204, Text: "kata terlarang judol gacor banget guys"},
		{Start: 204, End: 208, Text: "wkwkwk kocak banget mukanya ngakak"},
	}
	audio := make([]float64, 300)
	for i := range audio {
		audio[i] = 0.2
	}
	for i := 60; i <= 68; i++ {
		audio[i] = 0.95
	}
	for i := 200; i <= 208; i++ {
		audio[i] = 0.92
	}

	res := DetectHype(AnalysisInput{
		DurationSec:    300,
		AudioEnergy:    audio,
		Transcript:     cues,
		Settings:       model.DefaultDetectionSettings(),
		ForbiddenWords: []string{"judol", "terlarang"},
	})
	if len(res.Candidates) < 2 {
		t.Fatalf("expected >= 2 candidates, got %d", len(res.Candidates))
	}
	h1 := res.Candidates[0].HookText
	h2 := res.Candidates[1].HookText
	if h1 == "" || h2 == "" {
		t.Fatalf("expected non-empty hooks, got %q and %q", h1, h2)
	}
	if h1 == h2 {
		t.Fatalf("expected unique hooks per moment, got duplicate %q", h1)
	}
	for _, c := range res.Candidates {
		low := c.HookText
		if containsAnyFold(low, []string{"judol", "terlarang"}) {
			t.Fatalf("hook violated forbidden words: %q", c.HookText)
		}
	}
}

func TestArcAwareWindahRealCuesAndVirality(t *testing.T) {
	// Real Windah Basudara GTA VI VOD transcript timeline:
	// 1. Gunfight at 2290-2318
	// 2. Boast -> crash -> reaction arc: boasts at 2928/2937/2949, crash 2952-2955, comment 2965-2980
	// 3. "latihan dikejar polisi" at 9616
	cues := []model.TranscriptCue{
		{Start: 2288, End: 2293, Text: "hati-hati guys musuh di depan siap perang!"},
		{Start: 2295, End: 2302, Text: "tembak tembak anjir kepung kita kabur!"},
		{Start: 2305, End: 2312, Text: "mati satu meledak parah banget gila!"},
		{Start: 2314, End: 2319, Text: "wkwkwk panik banget sumpah hampir tewas"},

		{Start: 2928, End: 2932, Text: "lihat nih guys gua jago nyetir banget"},
		{Start: 2937, End: 2941, Text: "tenang gua jago nyetir pro player gta"},
		{Start: 2949, End: 2952, Text: "gampang banget nyetir begini mah santai"},
		{Start: 2953, End: 2957, Text: "anjir nabrak tiang mobil kebalik meledak!"},
		{Start: 2960, End: 2966, Text: "wkwkwk ngakak astaga kok bisa nabrak sih"},
		{Start: 2968, End: 2975, Text: "aduh parah banget el alasan nyetirnya kocak"},

		{Start: 9614, End: 9619, Text: "oke kita latihan dikejar polisi bintang tiga!"},
		{Start: 9621, End: 9627, Text: "kabur woy polisi ngejar banyak banget anjir!"},
		{Start: 9629, End: 9636, Text: "waduh nabrak mobil polisi busted tamat wkwk"},
	}

	audio := make([]float64, 10000)
	for i := range audio {
		audio[i] = 0.15
	}
	// Put a loud EMPTY noise spike at 5000 (no transcript/conflict) to prove audio alone doesn't beat real arcs!
	for i := 5000; i <= 5005; i++ {
		audio[i] = 0.90
	}
	for i := 2295; i <= 2315; i++ {
		audio[i] = 0.78
	}
	for i := 2952; i <= 2964; i++ {
		audio[i] = 0.88
	}
	for i := 9618; i <= 9632; i++ {
		audio[i] = 0.82
	}

	res := DetectHype(AnalysisInput{
		DurationSec: 10000,
		AudioEnergy: audio,
		Transcript:  cues,
		Settings:    model.DefaultDetectionSettings(),
	})

	if len(res.Candidates) < 3 {
		t.Fatalf("expected >= 3 arc candidates, got %d", len(res.Candidates))
	}

	findNear := func(targetSec float64) *model.ClipCandidate {
		for i := range res.Candidates {
			c := &res.Candidates[i]
			if c.ArcStartSec <= targetSec && c.ArcEndSec >= targetSec {
				return c
			}
		}
		return nil
	}

	// 1. Boast->Crash->Reaction arc around 2928..2975 MUST be captured as a 20-45s arc with ArcReason & ViralityScore!
	boastArc := findNear(2953)
	if boastArc == nil {
		t.Fatalf("expected boast->crash arc covering 2953s, candidates=%+v", res.Candidates)
	}
	arcLen := boastArc.ArcEndSec - boastArc.ArcStartSec
	if arcLen < 20 || arcLen > 45 {
		t.Fatalf("expected boast arc duration within 20..45s, got %.1fs (%.1f-%.1f)", arcLen, boastArc.ArcStartSec, boastArc.ArcEndSec)
	}
	if boastArc.ArcStartSec > 2942 {
		t.Fatalf("expected arc to walk backwards into boast setup (<=2942s), got ArcStartSec=%.1f", boastArc.ArcStartSec)
	}
	if boastArc.ArcReason == "" || boastArc.ViralityScore < 50 || boastArc.ViralityBreakdown == nil {
		t.Fatalf("expected populated ArcReason & ViralityScore>=50, got reason=%q virality=%.1f", boastArc.ArcReason, boastArc.ViralityScore)
	}

	// 2. "latihan dikejar polisi" at 9616
	if policeArc := findNear(9620); policeArc == nil {
		t.Fatalf("expected police chase arc covering 9620s")
	}

	// 3. Gunfight at 2290-2318
	if gunArc := findNear(2305); gunArc == nil {
		t.Fatalf("expected gunfight arc covering 2305s")
	}
}

func TestWordBoundaryAndAcousticJumpscareBoost(t *testing.T) {
	// 1. Word-boundary check: "enggak tunggu" must NOT trigger "gg",
	//    and "Ucok cocok dorong masalah boleh proses" must NOT trigger "cok"/"dor"/"lah"/"eh"/"pro".
	if containsWordOrPhrase("enggak tunggu dulu", "gg") {
		t.Fatalf("false positive: 'gg' matched inside 'enggak tunggu'")
	}
	if !containsWordOrPhrase("mantap gg banget!", "gg") {
		t.Fatalf("expected standalone 'gg' to match")
	}
	if ContainsForbiddenWord("Ucok cocok dorong masalah", []string{"cok", "dor", "lah"}) {
		t.Fatalf("false positive: short forbidden word matched substring")
	}
	if !ContainsForbiddenWord("dasar cok parah", []string{"cok"}) {
		t.Fatalf("expected standalone forbidden word 'cok' to match")
	}

	// 2. Acoustic jumpscare/crash contrast boost: quiet buildup (<0.28) -> sudden peak (>=0.78)
	audio := make([]float64, 180)
	for i := range audio {
		audio[i] = 0.14
	}
	// Sudden crash/scream at t=90..92 preceded by quiet 86..89
	audio[90] = 0.92
	audio[91] = 0.88
	audio[92] = 0.84
	res := DetectHype(AnalysisInput{
		DurationSec: 180,
		AudioEnergy: audio,
		Settings:    model.DefaultDetectionSettings(),
	})
	if len(res.Candidates) == 0 {
		t.Fatalf("expected acoustic jumpscare candidate")
	}
	if res.Candidates[0].PeakSec < 88 || res.Candidates[0].PeakSec > 95 {
		t.Fatalf("expected peak near 90s jumpscare, got %.1fs", res.Candidates[0].PeakSec)
	}
}

func containsAnyFold(s string, list []string) bool {
	for _, w := range list {
		if len(w) > 0 && containsFold(s, w) {
			return true
		}
	}
	return false
}

func containsFold(s, sub string) bool {
	ls := []rune(s)
	lsub := []rune(sub)
	if len(lsub) > len(ls) {
		return false
	}
	for i := 0; i <= len(ls)-len(lsub); i++ {
		ok := true
		for j := 0; j < len(lsub); j++ {
			a, b := ls[i+j], lsub[j]
			if a >= 'A' && a <= 'Z' {
				a += 32
			}
			if b >= 'A' && b <= 'Z' {
				b += 32
			}
			if a != b {
				ok = false
				break
			}
		}
		if ok {
			return true
		}
	}
	return false
}
