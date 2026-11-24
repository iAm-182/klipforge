package analyzer

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"klipforge/internal/model"
)

func TestDiarizeMedia_SherpaSidecarAndFallback(t *testing.T) {
	// 1. Mock sherpa-onnx-server HTTP sidecar
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"segments": []SpeakerSegment{
				{Start: 0, End: 5, Speaker: "SPEAKER_0"},
				{Start: 5, End: 9, Speaker: "SPEAKER_1"},
				{Start: 9, End: 16, Speaker: "SPEAKER_0"},
			},
			"vadSegments": []VADSegment{{Start: 0, End: 16}},
		})
	}))
	defer ts.Close()

	wav := filepath.Join(t.TempDir(), "audio.wav")
	_ = os.WriteFile(wav, []byte("RIFF"), 0o644)

	cues := []model.TranscriptCue{
		{Start: 0.5, End: 4.5, Text: "gua jago nyetir guys wkwk"},
		{Start: 5.5, End: 8.5, Text: "put your hands up right now please"},
		{Start: 9.5, End: 15.0, Text: "anjir nabrak polisi!"},
	}

	res := DiarizeMedia(context.Background(), ts.URL, wav, cues)
	if res.Engine != "sherpa-onnx" || res.Fallback || res.PrimarySpeaker != "SPEAKER_0" {
		t.Fatalf("expected sherpa-onnx sidecar result, got %+v", res)
	}
	assigned := AssignSpeakersToCues(cues, res.Segments)
	if assigned[0].Speaker != "SPEAKER_0" || assigned[1].Speaker != "SPEAKER_1" || assigned[2].Speaker != "SPEAKER_0" {
		t.Fatalf("unexpected speaker assignment: %+v", assigned)
	}
	sum := SummarizeSpeakersInWindow(assigned, 0, 16)
	if sum["SPEAKER_0"] <= sum["SPEAKER_1"] {
		t.Fatalf("expected SPEAKER_0 > SPEAKER_1 in window, got %+v", sum)
	}

	// 2. Offline fallback when sherpa-onnx-server is unreachable -> still distinguishes Windah (SPEAKER_0) vs NPC (SPEAKER_1)!
	fb := DiarizeMedia(context.Background(), "http://127.0.0.1:1", "", cues)
	if !fb.Fallback || len(fb.Segments) < 3 {
		t.Fatalf("expected fallback diarization with Windah/NPC turns, got %+v", fb)
	}
	fbCues := AssignSpeakersToCues(cues, fb.Segments)
	if fbCues[0].Speaker != "SPEAKER_0" || fbCues[1].Speaker != "SPEAKER_1" {
		t.Fatalf("expected SPEAKER_0 for Windah and SPEAKER_1 for NPC, got %+v", fbCues)
	}
}
