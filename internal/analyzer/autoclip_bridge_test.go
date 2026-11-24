package analyzer

import (
	"fmt"
	"strings"
	"testing"

	"klipforge/internal/model"
	"klipforge/internal/youtube"
)

func TestLiveChatTrailingLagAntiSpamAndVisualGag(t *testing.T) {
	// Scenario (600s silent gameplay VOD, flat audio 0.15, no speech):
	// - At t=120s: 1 single spammer sends 45x "WKWKWK" at t=128..134s -> MUST be heavily dampened by unique-chatter anti-spam!
	// - At t=300s: Visual gag on screen! 30 UNIQUE viewers react 6..11s LATER (t=306..311s) -> MUST rank #1 near t=300s (NOT 309s!) and tag "Visual Gag"!
	audio := make([]float64, 600)
	for i := range audio {
		audio[i] = 0.15
	}
	var chats []youtube.ChatMessage
	// Baseline trickle from unique viewers
	for s := 0; s < 600; s += 20 {
		chats = append(chats, youtube.ChatMessage{
			OffsetSec: float64(s),
			AuthorID:  fmt.Sprintf("UC_base_%d", s),
			Author:    "NormalViewer",
			Text:      "halo bang",
		})
	}
	// Single-user spam flood at t=128..134s
	for i := 0; i < 45; i++ {
		chats = append(chats, youtube.ChatMessage{
			OffsetSec: 128.0 + float64(i%7),
			AuthorID:  "UC_SPAMMER_ONE",
			Author:    "SpammerBot",
			Text:      "WKWKWK CLIP ANJIR!!!",
		})
	}
	// Genuine visual gag at t=300s -> 30 UNIQUE chatters laugh 6..11s later!
	for i := 0; i < 30; i++ {
		chats = append(chats, youtube.ChatMessage{
			OffsetSec: 306.0 + float64(i%6),
			AuthorID:  fmt.Sprintf("UC_real_%d", i),
			Author:    fmt.Sprintf("Viewer%d", i),
			Text:      "WKWKWK KOCAK BANGET CLIP 😂",
		})
	}

	res := DetectHype(AnalysisInput{
		DurationSec: 600,
		Chats:       chats,
		AudioEnergy: audio,
		Settings:    model.DefaultDetectionSettings(),
	})

	if !res.Signals.HasLiveChat || len(res.Candidates) == 0 {
		t.Fatalf("expected live chat candidates, got %+v", res.Signals)
	}
	top := res.Candidates[0]
	// Trailing lag window (+4..+15s) should pull the peak back to ~295..303s (BEFORE the 306..311s chat wave!)
	if top.PeakSec < 292 || top.PeakSec > 304 {
		t.Fatalf("expected trailing-lag #1 peak near 300s (and beating 1-user spam at 120s), got peak=%.1fs score=%.1f", top.PeakSec, top.Score)
	}
	hasVisualTag := false
	for _, r := range top.Reasons {
		if strings.Contains(r, "Visual Gag") {
			hasVisualTag = true
		}
	}
	if !hasVisualTag {
		t.Fatalf("expected Visual Gag reason tag for silent high-chat moment, got reasons=%+v", top.Reasons)
	}
}

func TestBuildCompactTranscriptWindows_OnePassAndSilentChatSpike(t *testing.T) {
	cues := []model.TranscriptCue{
		{Start: 10, End: 14, Text: "halo guys hari ini main gta", Speaker: "SPEAKER_0"},
		{Start: 900, End: 905, Text: "anjir nabrak tiang listrik!", Speaker: "SPEAKER_0"},
		{Start: 2000, End: 2006, Text: "polisi ngejar bintang lima!", Speaker: "SPEAKER_0"},
	}
	tl := []model.TimelinePoint{
		{TimeSec: 450, ChatHype: 0.88, ChatCount: 140, AudioPeak: 0.2}, // Silent visual gag!
		{TimeSec: 900, ChatHype: 0.72, ChatCount: 95, AudioPeak: 0.85},
	}

	out := BuildCompactTranscriptWindows(CompactTranscriptInput{
		DurationSec:   2400,
		Cues:          cues,
		Timeline:      tl,
		WindowMinutes: 20,
		OverlapSec:    60,
		WindowIndex:   0,
	})
	if out.TotalWindows < 2 {
		t.Fatalf("expected >=2 overlapping windows for 40-min VOD, got %d", out.TotalWindows)
	}
	if !strings.Contains(out.CompactText, "|00:10|") {
		t.Fatalf("expected AutoClip id|MM:SS|text format, got:\n%s", out.CompactText)
	}
	if !strings.Contains(out.CompactText, "[VISUAL_CHAT_SPIKE") {
		t.Fatalf("expected synthetic silent visual chat spike injected at 07:30, got:\n%s", out.CompactText)
	}
	if !strings.Contains(out.CompactText, "CHAT:72") || !strings.Contains(out.CompactText, "AUD:85") {
		t.Fatalf("expected inline CHAT/AUD tags on cue 900s, got:\n%s", out.CompactText)
	}
}
