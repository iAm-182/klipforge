package analyzer

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"klipforge/internal/model"
)

// WhisperResult holds optional word-level ASR output from local whisper-cli / faster-whisper,
// falling back gracefully to VTT cues when no local Whisper sidecar is installed.
type WhisperResult struct {
	Engine   string                `json:"engine"` // "whisper_cpp" | "faster_whisper" | "fallback_vtt"
	Language string                `json:"language,omitempty"`
	Cues     []model.TranscriptCue `json:"cues,omitempty"`
	Note     string                `json:"note,omitempty"`
}

// TranscribeWordTimings runs local word-level transcription when whisper-cli or faster-whisper is installed.
// Zero hard dependencies: when absent, returns Engine="fallback_vtt" and err==nil so callers use VTT + weighted karaoke cleanly.
func TranscribeWordTimings(ctx context.Context, mediaPath string, startSec, endSec float64, lang string) (*WhisperResult, error) {
	if strings.TrimSpace(lang) == "" {
		lang = "id"
	}
	if strings.TrimSpace(mediaPath) == "" {
		return &WhisperResult{Engine: "fallback_vtt", Language: lang, Note: "mediaPath kosong"}, nil
	}

	// Tier 1: Try faster-whisper inside KlipForge venv (Linux/Windows) or system Python
	pyCandidates := []string{"/data/workspace/klipforge/.venv/bin/python", "/app/.venv/bin/python"}
	for _, rel := range []string{".venv/bin/python", ".venv/Scripts/python.exe"} {
		if abs, err := filepath.Abs(rel); err == nil {
			if fi, serr := os.Stat(abs); serr == nil && !fi.IsDir() {
				pyCandidates = append(pyCandidates, abs)
			}
		}
	}
	pyCandidates = append(pyCandidates, "python3", "python")
	for _, py := range pyCandidates {
		if _, err := os.Stat(py); err == nil {
			if res, ok := tryFasterWhisperPython(ctx, py, mediaPath, startSec, endSec, lang); ok {
				return res, nil
			}
			continue
		}
		if p, err := exec.LookPath(py); err == nil {
			if res, ok := tryFasterWhisperPython(ctx, p, mediaPath, startSec, endSec, lang); ok {
				return res, nil
			}
		}
	}

	// Tier 2: Try whisper-cli / whisper-cpp with an optional local model file
	for _, bin := range []string{"whisper-cli", "whisper-cpp", "main"} {
		if b, err := exec.LookPath(bin); err == nil {
			if modelPath := findWhisperModel(); modelPath != "" {
				if res, ok := tryWhisperCPP(ctx, b, modelPath, mediaPath, startSec, lang); ok {
					return res, nil
				}
			}
		}
	}

	return &WhisperResult{
		Engine:   "fallback_vtt",
		Language: lang,
		Note:     "Sidecar faster-whisper / whisper-cli tidak terpasang; memakai subtitle VTT + distribusi timing per suku kata",
	}, nil
}

func findWhisperModel() string {
	for _, cand := range []string{
		os.Getenv("KLIPFORGE_WHISPER_MODEL"),
		"/data/workspace/klipforge/data/models/ggml-small.bin",
		"/data/workspace/klipforge/data/models/ggml-base.bin",
		"data/models/ggml-small.bin",
		"data/models/ggml-base.bin",
	} {
		if cand != "" {
			if _, err := os.Stat(cand); err == nil {
				return cand
			}
		}
	}
	return ""
}

const inlineFasterWhisperScript = `
import sys, json
try:
    from faster_whisper import WhisperModel
except Exception:
    sys.exit(42)

media = sys.argv[1]
start_sec = float(sys.argv[2])
lang = sys.argv[3] or "id"
model_name = sys.argv[4] if len(sys.argv) > 4 and sys.argv[4] else "small"

m = WhisperModel(model_name, device="cpu", compute_type="int8")
segs, _ = m.transcribe(media, language=lang, word_timestamps=True, vad_filter=True)
out = []
for s in segs:
    words = []
    for w in (s.words or []):
        words.append({"start": round(float(w.start) + start_sec, 3), "end": round(float(w.end) + start_sec, 3), "word": w.word.strip()})
    out.append({"start": round(float(s.start) + start_sec, 3), "end": round(float(s.end) + start_sec, 3), "text": s.text.strip(), "words": words})
print(json.dumps({"segments": out}))
`

func tryFasterWhisperPython(ctx context.Context, pyBin, mediaPath string, startSec, endSec float64, lang string) (*WhisperResult, bool) {
	cctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	cmd := exec.CommandContext(cctx, pyBin, "-c", inlineFasterWhisperScript, mediaPath, fmt.Sprintf("%.3f", startSec), lang, "small")
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		return nil, false
	}
	cues := ParseWhisperWordJSON(out.Bytes(), 0)
	if len(cues) == 0 {
		return nil, false
	}
	return &WhisperResult{Engine: "faster_whisper", Language: lang, Cues: cues}, true
}

func tryWhisperCPP(ctx context.Context, bin, modelPath, mediaPath string, offsetSec float64, lang string) (*WhisperResult, bool) {
	tmpDir, err := os.MkdirTemp("", "kf-wcpp-*")
	if err != nil {
		return nil, false
	}
	defer os.RemoveAll(tmpDir)
	outPrefix := filepath.Join(tmpDir, "out")

	cctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	cmd := exec.CommandContext(cctx, bin, "-m", modelPath, "-f", mediaPath, "-l", lang, "-ojf", "-of", outPrefix)
	if err := cmd.Run(); err != nil {
		return nil, false
	}
	raw, err := os.ReadFile(outPrefix + ".json")
	if err != nil {
		return nil, false
	}
	cues := ParseWhisperWordJSON(raw, offsetSec)
	if len(cues) == 0 {
		return nil, false
	}
	return &WhisperResult{Engine: "whisper_cpp", Language: lang, Cues: cues}, true
}

// ParseWhisperWordJSON normalizes both faster-whisper JSON (`segments[].words[]`) and whisper.cpp `-ojf`
// (`transcription[].tokens[]` with ms offsets) into KlipForge TranscriptCue slices with word timings.
func ParseWhisperWordJSON(raw []byte, offsetSec float64) []model.TranscriptCue {
	var payload struct {
		Segments []struct {
			Start float64 `json:"start"`
			End   float64 `json:"end"`
			Text  string  `json:"text"`
			Words []struct {
				Start float64 `json:"start"`
				End   float64 `json:"end"`
				Word  string  `json:"word"`
				Text  string  `json:"text"`
			} `json:"words"`
		} `json:"segments"`
		Transcription []struct {
			Offsets struct {
				From int64 `json:"from"`
				To   int64 `json:"to"`
			} `json:"offsets"`
			Timestamps struct {
				From string `json:"from"`
				To   string `json:"to"`
			} `json:"timestamps"`
			Text   string `json:"text"`
			Tokens []struct {
				Text    string `json:"text"`
				Offsets struct {
					From int64 `json:"from"`
					To   int64 `json:"to"`
				} `json:"offsets"`
			} `json:"tokens"`
		} `json:"transcription"`
	}
	if err := json.Unmarshal(bytes.TrimSpace(raw), &payload); err != nil {
		return nil
	}

	var cues []model.TranscriptCue
	for _, seg := range payload.Segments {
		txt := strings.TrimSpace(seg.Text)
		if txt == "" {
			continue
		}
		var words []model.WordTiming
		for _, w := range seg.Words {
			wt := strings.TrimSpace(w.Word)
			if wt == "" {
				wt = strings.TrimSpace(w.Text)
			}
			if wt == "" {
				continue
			}
			words = append(words, model.WordTiming{
				Start: w.Start + offsetSec,
				End:   w.End + offsetSec,
				Text:  wt,
			})
		}
		cues = append(cues, model.TranscriptCue{
			Start: seg.Start + offsetSec,
			End:   seg.End + offsetSec,
			Text:  txt,
			Words: words,
		})
	}
	if len(cues) > 0 {
		return cues
	}

	for _, tr := range payload.Transcription {
		txt := strings.TrimSpace(tr.Text)
		if txt == "" {
			continue
		}
		cs := float64(tr.Offsets.From)/1000.0 + offsetSec
		ce := float64(tr.Offsets.To)/1000.0 + offsetSec
		if tr.Offsets.To == 0 && tr.Timestamps.To != "" {
			cs = parseWhisperClock(tr.Timestamps.From) + offsetSec
			ce = parseWhisperClock(tr.Timestamps.To) + offsetSec
		}
		var words []model.WordTiming
		for _, tk := range tr.Tokens {
			rawTok := tk.Text
			if strings.HasPrefix(strings.TrimSpace(rawTok), "[_") {
				continue
			}
			ws := float64(tk.Offsets.From)/1000.0 + offsetSec
			we := float64(tk.Offsets.To)/1000.0 + offsetSec
			// whisper.cpp encodes leading spaces as word boundaries; attach subword tokens cleanly
			if !strings.HasPrefix(rawTok, " ") && len(words) > 0 && !isPunctToken(strings.TrimSpace(rawTok)) {
				last := &words[len(words)-1]
				last.Text += strings.TrimSpace(rawTok)
				if we > last.End {
					last.End = we
				}
				continue
			}
			clean := strings.TrimSpace(rawTok)
			if clean == "" || we <= ws {
				continue
			}
			words = append(words, model.WordTiming{Start: ws, End: we, Text: clean})
		}
		cues = append(cues, model.TranscriptCue{
			Start: cs,
			End:   ce,
			Text:  txt,
			Words: words,
		})
	}
	return cues
}

func isPunctToken(s string) bool {
	return s == "," || s == "." || s == "!" || s == "?" || s == ":" || s == ";"
}

func parseWhisperClock(s string) float64 {
	s = strings.ReplaceAll(strings.TrimSpace(s), ",", ".")
	parts := strings.Split(s, ":")
	if len(parts) != 3 {
		return 0
	}
	h, _ := strconv.Atoi(parts[0])
	m, _ := strconv.Atoi(parts[1])
	sec, _ := strconv.ParseFloat(parts[2], 64)
	return float64(h*3600+m*60) + sec
}
