package youtube

import (
	"os"
	"path/filepath"
	"testing"
)

func TestExtractVideoID(t *testing.T) {
	cases := map[string]string{
		"https://www.youtube.com/watch?v=dQw4w9WgXcQ":            "dQw4w9WgXcQ",
		"https://youtu.be/dQw4w9WgXcQ?t=42":                      "dQw4w9WgXcQ",
		"https://www.youtube.com/live/AbCdEfGhIjK?si=xyz":        "AbCdEfGhIjK",
		"https://www.youtube.com/shorts/12345678901":             "12345678901",
		"https://m.youtube.com/watch?v=ZyXwVuTsRqP&feature=push": "ZyXwVuTsRqP",
	}
	for in, want := range cases {
		if got := ExtractVideoID(in); got != want {
			t.Errorf("ExtractVideoID(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestParseLiveChatAndSubtitleFile(t *testing.T) {
	dir := t.TempDir()

	// Test live_chat.json NDJSON parsing
	chatFile := filepath.Join(dir, "test.live_chat.json")
	chatData := `{"replayChatItemAction":{"videoOffsetTimeMsec":"125500","actions":[{"addChatItemAction":{"item":{"liveChatTextMessageRenderer":{"authorName":{"simpleText":"Bocil1"},"message":{"runs":[{"text":"WKWKWK KAGET BANGET"}]}}}}}]}}
{"replayChatItemAction":{"videoOffsetTimeMsec":"126800","actions":[{"addChatItemAction":{"item":{"liveChatPaidMessageRenderer":{"authorName":{"simpleText":"Sultan"},"message":{"runs":[{"text":"GG BANG WINDAH!"}]}}}}}]}}
`
	if err := os.WriteFile(chatFile, []byte(chatData), 0o644); err != nil {
		t.Fatal(err)
	}
	msgs, err := ParseLiveChatFile(chatFile)
	if err != nil {
		t.Fatalf("ParseLiveChatFile error: %v", err)
	}
	if len(msgs) != 2 {
		t.Fatalf("expected 2 chat messages, got %d", len(msgs))
	}
	if msgs[0].OffsetSec != 125.5 || msgs[0].Text != "WKWKWK KAGET BANGET" {
		t.Fatalf("unexpected msg[0]: %+v", msgs[0])
	}
	if !msgs[1].IsPaid || msgs[1].Text != "GG BANG WINDAH!" {
		t.Fatalf("unexpected msg[1]: %+v", msgs[1])
	}

	// Test VTT parsing & YouTube rolling deduplication
	vttFile := filepath.Join(dir, "subs.id.vtt")
	vttData := `WEBVTT

00:00:01.000 --> 00:00:03.000
Halo guys selamat datang

00:00:03.000 --> 00:00:05.500
Halo guys selamat datang di GTA VI!
`
	if err := os.WriteFile(vttFile, []byte(vttData), 0o644); err != nil {
		t.Fatal(err)
	}
	cues, err := ParseSubtitleFile(vttFile)
	if err != nil {
		t.Fatalf("ParseSubtitleFile error: %v", err)
	}
	if len(cues) != 2 {
		t.Fatalf("expected 2 cues, got %d: %+v", len(cues), cues)
	}
	if cues[1].Text != "di GTA VI!" {
		t.Fatalf("expected rolling prefix stripped to 'di GTA VI!', got %q", cues[1].Text)
	}
}
