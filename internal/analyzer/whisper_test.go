package analyzer

import (
	"context"
	"testing"
)

func TestParseWhisperWordJSON_FasterWhisperAndWhisperCPP(t *testing.T) {
	fwJSON := []byte(`{
		"segments": [
			{
				"start": 1.2,
				"end": 3.5,
				"text": "anjir nabrak polisi",
				"words": [
					{"start": 1.20, "end": 1.80, "word": "anjir"},
					{"start": 1.82, "end": 2.60, "word": "nabrak"},
					{"start": 2.65, "end": 3.50, "word": "polisi"}
				]
			}
		]
	}`)
	cues := ParseWhisperWordJSON(fwJSON, 100.0)
	if len(cues) != 1 || len(cues[0].Words) != 3 {
		t.Fatalf("expected 1 cue with 3 words, got %+v", cues)
	}
	if cues[0].Words[1].Text != "nabrak" || cues[0].Words[1].Start < 101.8 || cues[0].Words[1].End > 102.7 {
		t.Fatalf("unexpected word timing: %+v", cues[0].Words[1])
	}

	cppJSON := []byte(`{
		"transcription": [
			{
				"offsets": {"from": 500, "to": 2400},
				"text": " gua jago",
				"tokens": [
					{"text": "[_BEG_]", "offsets": {"from": 0, "to": 100}},
					{"text": " gua", "offsets": {"from": 500, "to": 1100}},
					{"text": " ja", "offsets": {"from": 1120, "to": 1700}},
					{"text": "go", "offsets": {"from": 1700, "to": 2350}}
				]
			}
		]
	}`)
	cppCues := ParseWhisperWordJSON(cppJSON, 10.0)
	if len(cppCues) != 1 || len(cppCues[0].Words) != 2 {
		t.Fatalf("expected subwords merged into 2 words ('gua', 'jago'), got %+v", cppCues)
	}
	if cppCues[0].Words[1].Text != "jago" || cppCues[0].Words[1].End != 12.35 {
		t.Fatalf("unexpected merged subword timing: %+v", cppCues[0].Words[1])
	}
}

func TestTranscribeWordTimings_GracefulFallbackWhenEmptyMedia(t *testing.T) {
	res, err := TranscribeWordTimings(context.Background(), "", 0, 10, "id")
	if err != nil {
		t.Fatalf("expected graceful fallback without error, got %v", err)
	}
	if res == nil || res.Engine != "fallback_vtt" {
		t.Fatalf("expected fallback_vtt engine, got %+v", res)
	}
}
