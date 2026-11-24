package analyzer

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"strings"
	"time"

	"klipforge/internal/model"
)

// SpeakerSegment represents one diarized speaker turn [Start, End] in seconds.
type SpeakerSegment struct {
	Start   float64 `json:"start"`
	End     float64 `json:"end"`
	Speaker string  `json:"speaker"` // e.g. "SPEAKER_0" (primary streamer), "SPEAKER_1" (NPC/guest)
}

// VADSegment represents one active voice activity interval detected by sherpa-onnx VAD.
type VADSegment struct {
	Start float64 `json:"start"`
	End   float64 `json:"end"`
}

// DiarizeResult holds diarization + VAD output from sherpa-onnx-server (or offline fallback).
type DiarizeResult struct {
	Engine         string             `json:"engine"` // "sherpa-onnx" | "offline_linguistic_fallback"
	Fallback       bool               `json:"fallback"`
	PrimarySpeaker string             `json:"primarySpeaker"`
	Segments       []SpeakerSegment   `json:"segments"`
	VADSegments    []VADSegment       `json:"vadSegments,omitempty"`
	SpeakerTotals  map[string]float64 `json:"speakerTotals"`
	Note           string             `json:"note,omitempty"`
}

// SherpaTTSVoiceInfo documents the verified Indonesian voice in k2-fsa/sherpa-onnx model zoo.
const (
	VerifiedIndonesianTTSModel = "vits-piper-id_ID-news_tts-medium"
	VerifiedIndonesianTTSURL   = "https://github.com/k2-fsa/sherpa-onnx/releases/download/tts-models/vits-piper-id_ID-news_tts-medium.tar.bz2"
)

// DiarizeMedia invokes the offline sherpa-onnx-server HTTP sidecar (k2-fsa/sherpa-onnx, Apache-2.0,
// zero account/token — never uses HF-gated pyannote!) when available, and gracefully falls back to
// deterministic linguistic/turn-based speaker segmentation over transcript cues when the sidecar is offline.
func DiarizeMedia(ctx context.Context, sherpaURL, audioPath string, cues []model.TranscriptCue) DiarizeResult {
	base := strings.TrimRight(strings.TrimSpace(sherpaURL), "/")
	if base == "" {
		base = strings.TrimRight(strings.TrimSpace(os.Getenv("KLIPFORGE_SHERPA_URL")), "/")
	}
	if base == "" {
		base = "http://127.0.0.1:8790"
	}

	if audioPath != "" {
		if _, err := os.Stat(audioPath); err == nil {
			if res, err := callSherpaOnnxServer(ctx, base, audioPath); err == nil && len(res.Segments) > 0 {
				return res
			}
		}
	}

	// Deterministic offline fallback when sherpa-onnx-server sidecar is not running
	return diarizeFromTranscriptFallback(cues)
}

func callSherpaOnnxServer(ctx context.Context, baseURL, audioPath string) (DiarizeResult, error) {
	payload, _ := json.Marshal(map[string]string{
		"audioPath": audioPath,
		"task":      "diarize_vad",
	})
	cctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(cctx, http.MethodPost, baseURL+"/diarize", bytes.NewReader(payload))
	if err != nil {
		return DiarizeResult{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: 45 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return DiarizeResult{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return DiarizeResult{}, fmt.Errorf("sherpa-onnx HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	var parsed struct {
		Segments    []SpeakerSegment `json:"segments"`
		VADSegments []VADSegment     `json:"vadSegments"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return DiarizeResult{}, err
	}
	totals := computeSpeakerTotals(parsed.Segments)
	primary := pickPrimarySpeaker(totals)
	return DiarizeResult{
		Engine:         "sherpa-onnx",
		Fallback:       false,
		PrimarySpeaker: primary,
		Segments:       parsed.Segments,
		VADSegments:    parsed.VADSegments,
		SpeakerTotals:  totals,
		Note:           "Diarization + VAD via sherpa-onnx-server",
	}, nil
}

// diarizeFromTranscriptFallback produces SPEAKER_0 (primary Indonesian streamer) vs SPEAKER_1 (English NPC/cutscene)
// speaker turns when sherpa-onnx-server isn't reachable, enabling speaker-aware DetectHype & karaoke offline.
func diarizeFromTranscriptFallback(cues []model.TranscriptCue) DiarizeResult {
	var segs []SpeakerSegment
	var vad []VADSegment
	for _, c := range cues {
		if c.End <= c.Start || strings.TrimSpace(c.Text) == "" {
			continue
		}
		spk := classifyCueSpeakerHeuristic(c.Text)
		if len(segs) > 0 && segs[len(segs)-1].Speaker == spk && c.Start-segs[len(segs)-1].End <= 1.5 {
			segs[len(segs)-1].End = c.End
		} else {
			segs = append(segs, SpeakerSegment{Start: c.Start, End: c.End, Speaker: spk})
		}
		if len(vad) > 0 && c.Start-vad[len(vad)-1].End <= 0.8 {
			vad[len(vad)-1].End = c.End
		} else {
			vad = append(vad, VADSegment{Start: c.Start, End: c.End})
		}
	}
	totals := computeSpeakerTotals(segs)
	primary := "SPEAKER_0"
	if len(totals) > 0 {
		if _, has0 := totals["SPEAKER_0"]; !has0 {
			primary = pickPrimarySpeaker(totals)
		}
	}
	return DiarizeResult{
		Engine:         "offline_linguistic_fallback",
		Fallback:       true,
		PrimarySpeaker: primary,
		Segments:       segs,
		VADSegments:    vad,
		SpeakerTotals:  totals,
		Note:           "sherpa-onnx-server offline — menggunakan speaker-turn fallback (SPEAKER_0=Streamer ID, SPEAKER_1=NPC/Cutscene EN)",
	}
}

var extraEnglishNPCTokens = []string{
	"put your", "hands up", "right now", "please", "get out", "stop the",
	"officer", "freeze", "you ", "your ", "the ", "this is ", "we need ",
	"come on", "don't move", "hey ", "wait a ", "let me ", "tell me ",
}

func classifyCueSpeakerHeuristic(text string) string {
	low := " " + strings.ToLower(strings.TrimSpace(text)) + " "
	indoScore, engScore := 0, 0
	for _, tk := range indoStreamerTokens {
		if strings.Contains(low, tk) {
			indoScore++
		}
	}
	for _, kw := range speechHypeKeywords {
		if strings.Contains(low, kw) {
			indoScore++
		}
	}
	for _, tk := range englishCutsceneTokens {
		if strings.Contains(low, tk) {
			engScore += 2
		}
	}
	for _, tk := range extraEnglishNPCTokens {
		if strings.Contains(low, tk) {
			engScore++
		}
	}
	if engScore > indoScore && engScore >= 2 {
		return "SPEAKER_1"
	}
	return "SPEAKER_0"
}

func computeSpeakerTotals(segs []SpeakerSegment) map[string]float64 {
	totals := make(map[string]float64)
	for _, s := range segs {
		if s.End > s.Start && s.Speaker != "" {
			totals[s.Speaker] += s.End - s.Start
		}
	}
	for k, v := range totals {
		totals[k] = math.Round(v*10) / 10
	}
	return totals
}

func pickPrimarySpeaker(totals map[string]float64) string {
	bestSpk := "SPEAKER_0"
	bestDur := -1.0
	for spk, dur := range totals {
		if dur > bestDur {
			bestDur = dur
			bestSpk = spk
		}
	}
	return bestSpk
}

// AssignSpeakersToCues attaches the dominant overlapping speaker label to each TranscriptCue.
func AssignSpeakersToCues(cues []model.TranscriptCue, segments []SpeakerSegment) []model.TranscriptCue {
	if len(cues) == 0 || len(segments) == 0 {
		return cues
	}
	out := make([]model.TranscriptCue, len(cues))
	copy(out, cues)
	for i := range out {
		c := &out[i]
		bestSpk := ""
		bestOv := 0.0
		for _, s := range segments {
			ovStart := math.Max(c.Start, s.Start)
			ovEnd := math.Min(c.End, s.End)
			if ovEnd > ovStart && ovEnd-ovStart > bestOv {
				bestOv = ovEnd - ovStart
				bestSpk = s.Speaker
			}
		}
		if bestSpk != "" {
			c.Speaker = bestSpk
		}
	}
	return out
}

// SummarizeSpeakersInWindow computes total speaking seconds per speaker inside [startSec, endSec].
func SummarizeSpeakersInWindow(cues []model.TranscriptCue, startSec, endSec float64) map[string]float64 {
	if len(cues) == 0 || endSec <= startSec {
		return nil
	}
	res := make(map[string]float64)
	for _, c := range cues {
		if c.Speaker == "" || c.End <= startSec || c.Start >= endSec {
			continue
		}
		ov := math.Min(c.End, endSec) - math.Max(c.Start, startSec)
		if ov > 0 {
			res[c.Speaker] += ov
		}
	}
	if len(res) == 0 {
		return nil
	}
	for k, v := range res {
		res[k] = math.Round(v*10) / 10
	}
	return res
}
