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
