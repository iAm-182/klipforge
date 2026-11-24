package renderer

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"klipforge/internal/model"
)

func TestPickStoryboardTimestamps_RangeAndFullVOD(t *testing.T) {
	// 1. Range mode [100, 140], 5 frames
	rng := PickStoryboardTimestamps(3600, 100, 140, 5, nil, nil)
	if len(rng) != 5 {
		t.Fatalf("expected 5 timestamps in range mode, got %d (%v)", len(rng), rng)
	}
	if rng[0] < 100 || rng[len(rng)-1] > 140 {
		t.Fatalf("range timestamps out of bounds: %v", rng)
	}
	for i := 1; i < len(rng); i++ {
		if rng[i] <= rng[i-1] {
			t.Fatalf("expected strictly increasing timestamps, got %v", rng)
		}
	}

	// 2. Full VOD mode with chat/audio peaks
	tl := []model.TimelinePoint{
		{TimeSec: 120, Combined: 0.25, ChatHype: 0.2},
		{TimeSec: 900, Combined: 0.90, ChatHype: 0.95},
		{TimeSec: 2400, Combined: 0.85, AudioPeak: 0.92},
		{TimeSec: 5100, Combined: 0.88, ChatHype: 0.85},
	}
	vod := PickStoryboardTimestamps(7200, 0, 0, 9, tl, nil)
	if len(vod) != 9 {
		t.Fatalf("expected 9 timestamps in VOD mode, got %d (%v)", len(vod), vod)
	}
	has900 := false
	has2400 := false
	for _, ts := range vod {
		if ts == 900 {
			has900 = true
		}
		if ts == 2400 {
			has2400 = true
		}
	}
	if !has900 || !has2400 {
		t.Fatalf("expected top timeline peaks 900 & 2400 to be selected, got %v", vod)
	}
}

func TestGenerateStoryboardContactSheet_RealFFmpeg(t *testing.T) {
	ffmpegPath, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg not found in PATH")
	}

	tmpDir := t.TempDir()
	srcMP4 := filepath.Join(tmpDir, "sample.mp4")
	cmd := exec.Command(ffmpegPath,
		"-y", "-hide_banner", "-loglevel", "error",
		"-f", "lavfi", "-i", "testsrc2=size=640x360:rate=15:duration=4",
		"-c:v", "libx264", "-pix_fmt", "yuv420p",
		srcMP4,
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("failed generating test MP4: %v (%s)", err, string(out))
	}

	destJPG := filepath.Join(tmpDir, "sheet.jpg")
	res, err := GenerateStoryboardContactSheet(context.Background(), ffmpegPath, srcMP4, []float64{0.3, 1.3, 2.3, 3.3}, 2, 240, destJPG)
	if err != nil {
		t.Fatalf("GenerateStoryboardContactSheet failed: %v", err)
	}
	if res.Cols != 2 || res.Rows != 2 || len(res.Frames) != 4 {
		t.Fatalf("unexpected grid result: %+v", res)
	}
	raw, err := os.ReadFile(destJPG)
	if err != nil || len(raw) < 1000 {
		t.Fatalf("expected non-empty storyboard JPEG, err=%v size=%d", err, len(raw))
	}
	if raw[0] != 0xFF || raw[1] != 0xD8 {
		t.Fatalf("expected JPEG magic bytes FF D8, got %x %x", raw[0], raw[1])
	}
}
