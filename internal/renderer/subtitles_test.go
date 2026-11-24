package renderer

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"klipforge/internal/model"
)

func parseASSTimeSec(s string) float64 {
	parts := strings.Split(s, ":")
	if len(parts) != 3 {
		return -1
	}
	h, _ := strconv.Atoi(parts[0])
	m, _ := strconv.Atoi(parts[1])
	sec, _ := strconv.ParseFloat(parts[2], 64)
	return float64(h*3600+m*60) + sec
}

func TestGenerateASS_KaraokeWordTimingsAndPunchZoom(t *testing.T) {
	tmp := t.TempDir()
	outASS := filepath.Join(tmp, "karaoke.ass")

	cues := []model.TranscriptCue{
		{
			Start: 10.0,
			End:   12.4,
			Text:  "gua jago nyetir",
			Words: []model.WordTiming{
				{Start: 10.00, End: 10.60, Text: "gua"},
				{Start: 10.62, End: 11.40, Text: "jago"},
				{Start: 11.42, End: 12.30, Text: "nyetir"},
			},
		},
		{
			Start: 12.6,
			End:   14.8,
			Text:  "anjir nabrak wkwkwk",
			Words: []model.WordTiming{
				{Start: 12.60, End: 13.25, Text: "anjir"},
				{Start: 13.28, End: 14.00, Text: "nabrak"},
				{Start: 14.05, End: 14.80, Text: "wkwkwk"},
			},
		},
	}

	style := model.SubtitleStyle{
		Enabled:        true,
		Preset:         "karaoke",
		FontName:       "Impact",
		FontSize:       66,
		Position:       "bottom",
		MaxWordsPerRow: 3,
		Uppercase:      true,
	}

	n, err := GenerateASS(cues, 10.0, 20.0, 1.0, 1080, 1920, style, "GUA JAGO NYETIR", "center", outASS)
	if err != nil {
		t.Fatalf("GenerateASS karaoke failed: %v", err)
	}
	if n < 7 {
		t.Fatalf("expected >=7 ASS dialogues (1 hook + 6 word highlights), got %d", n)
	}

	raw, err := os.ReadFile(outASS)
	if err != nil {
		t.Fatalf("read ass: %v", err)
	}
	txt := string(raw)

	// Verify punch-in zoom (\fscx132\fscy132) triggered on key viral words (JAGO, NYETIR, ANJIR, NABRAK, WKWKWK)!
	if !strings.Contains(txt, `\fscx132\fscy132`) {
		t.Fatalf("expected karaoke punch-in zoom tag on key viral words, got:\n%s", txt)
	}
	// Verify standard active word scale (\fscx122\fscy122) on non-key word ("GUA")
	if !strings.Contains(txt, `\fscx122\fscy122`) {
		t.Fatalf("expected karaoke active word highlight tag on standard words, got:\n%s", txt)
	}

	// Verify EVERY spoken word dialogue lands within <= 150ms of its spoken WordTiming!
	re := regexp.MustCompile(`Dialogue: 1,(\d+:\d{2}:\d{2}\.\d{2}),(\d+:\d{2}:\d{2}\.\d{2}),Viral,`)
	matches := re.FindAllStringSubmatch(txt, -1)
	expectedStarts := []float64{0.00, 0.62, 1.42, 2.60, 3.28, 4.05}
	if len(matches) != len(expectedStarts) {
		t.Fatalf("expected %d word dialogues, got %d", len(expectedStarts), len(matches))
	}
	for i, m := range matches {
		gotStart := parseASSTimeSec(m[1])
		diff := gotStart - expectedStarts[i]
		if diff < 0 {
			diff = -diff
		}
		if diff > 0.15 {
			t.Fatalf("word %d timing drifted by %.3fs (>150ms): got %.2fs, want %.2fs", i, diff, gotStart, expectedStarts[i])
		}
	}
}

func TestDistributeWordTimings_SyllableAndPunctuationWeighted(t *testing.T) {
	wt := distributeWordTimings([]string{"eh", "astaghfirullah,", "nabrak!"}, 10.0, 14.0)
	if len(wt) != 3 {
		t.Fatalf("expected 3 word timings, got %d", len(wt))
	}
	if wt[0].Start != 10.0 || wt[2].End != 14.0 {
		t.Fatalf("expected span [10.0, 14.0], got [%.3f, %.3f]", wt[0].Start, wt[2].End)
	}
	d0 := wt[0].End - wt[0].Start
	d1 := wt[1].End - wt[1].Start
	d2 := wt[2].End - wt[2].Start
	if !(d1 > d0*1.8) {
		t.Fatalf("expected long word 'astaghfirullah,' (%.3fs) to take meaningfully longer than short 'eh' (%.3fs)", d1, d0)
	}
	if !(d2 > d0) {
		t.Fatalf("expected punctuated 'nabrak!' (%.3fs) > 'eh' (%.3fs)", d2, d0)
	}
	for i := 1; i < len(wt); i++ {
		if wt[i].Start < wt[i-1].End-1e-9 || wt[i].End <= wt[i].Start {
			t.Fatalf("non-monotonic word timings: %+v", wt)
		}
	}
}
