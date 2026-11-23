package renderer

import (
	"context"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"klipforge/internal/model"
)

func TestSegmentEditControls_ClampAndRender(t *testing.T) {
	// 1. SpeedRamp clamping & time mapping
	ramp := ClampSpeedRamp(model.SpeedRamp{AtSec: 8.0, FromSpeed: 1.0, ToSpeed: 0.5}, 10.0)
	if ramp.AtSec != 8.0 || ramp.ToSpeed != 0.5 {
		t.Fatalf("unexpected clamped ramp: %+v", ramp)
	}
	// First 8s at 1.0x = 8.0s; remaining 2s at 0.5x = 4.0s -> total 12.0s!
	mapped := MapTimeWithRamp(10.0, 1.0, &ramp)
	if math.Abs(mapped-12.0) > 0.01 {
		t.Fatalf("expected mapped time 12.0s, got %.3f", mapped)
	}

	// 2. ZoomPunch clamping on a short segment (graceful degradation, never errors)
	cz, notes := ClampZoomPunch(model.ZoomPunch{Scale: 2.5, AtSec: 9.5, DurationSec: 3.0}, 5.0)
	if cz.Scale != 1.60 || cz.AtSec+cz.DurationSec > 5.01 || len(notes) == 0 {
		t.Fatalf("expected zoom safely clamped to 5.0s segment, got %+v notes=%v", cz, notes)
	}

	// 3. Real FFmpeg end-to-end compile test with speedRamp (1.0 -> 0.5 slow-mo), zoom punch, textOverlay, filter, transition, karaoke!
	ffmpegBin, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg not available")
	}
	ffprobeBin, err := exec.LookPath("ffprobe")
	if err != nil {
		t.Skip("ffprobe not available")
	}

	tmp := t.TempDir()
	srcMP4 := filepath.Join(tmp, "src8.mp4")
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	if out, err := exec.CommandContext(ctx, ffmpegBin,
		"-y", "-hide_banner", "-loglevel", "error",
		"-f", "lavfi", "-i", "testsrc2=size=1280x720:rate=30:duration=8",
		"-f", "lavfi", "-i", "sine=frequency=440:duration=8",
		"-c:v", "libx264", "-preset", "ultrafast", "-pix_fmt", "yuv420p",
		"-c:a", "aac", "-b:a", "96k", srcMP4,
	).CombinedOutput(); err != nil {
		t.Fatalf("gen src failed: %v %s", err, string(out))
	}

	outMP4 := filepath.Join(tmp, "edit_script_out.mp4")
	info, err := RenderCompilation(ctx, CompileInput{
		FFmpegPath:  ffmpegBin,
		FFprobePath: ffprobeBin,
		OutputPath:  outMP4,
		WorkDir:     filepath.Join(tmp, "work"),
		Title:       "Test CapCut Edit Script",
		Mode:        "edit_script",
		Layout:      model.LayoutConfig{AspectRatio: "9:16", Resolution: "720p", CropMode: "center"},
		Subtitles:   model.SubtitleStyle{Enabled: true, Preset: "karaoke", FontName: "Arial", FontSize: 64, Position: "bottom", Uppercase: true},
		AudioNorm:   true,
		Encoder:     "libx264",
		Preset:      "ultrafast",
		CRF:         28,
		Segments: []ResolvedCompileSegment{
			{
				SourcePath:   srcMP4,
				SeekStart:    0,
				ClipDuration: 6.0,
				RankNumber:   2,
				Label:        "baku tembak",
				OrigStart:    0,
				OrigEnd:      6.0,
				Cues: []model.TranscriptCue{
					{Start: 0.5, End: 2.5, Text: "dikit lagi mati wkwkwk"},
				},
				Speed:       1.0,
				SpeedRamp:   &model.SpeedRamp{AtSec: 4.0, FromSpeed: 1.0, ToSpeed: 0.5},
				Zoom:        &model.ZoomPunch{Scale: 1.25, AtSec: 4.2, DurationSec: 1.5},
				TextOverlay: &model.SegmentTextOverlay{Text: "DIKIT LAGI MATI", Position: "top", Style: "bold_yellow"},
				Filter:      "vivid_gaming",
				Transition:  "fade",
			},
			{
				SourcePath:   srcMP4,
				SeekStart:    0,
				ClipDuration: 3.0,
				RankNumber:   1,
				Label:        "nabrak",
				OrigStart:    0,
				OrigEnd:      3.0,
				Speed:        1.15,
				Transition:   "zoom",
			},
		},
	})
	if err != nil {
		t.Fatalf("RenderCompilation with per-segment controls failed: %v", err)
	}
	// Seg 1: 4s@1.0x + 2s@0.5x = 8.0s; Seg 2: 3s@1.15x = ~2.6s -> total ~10.6s
	if info.Width != 720 || info.Height != 1280 || info.Duration < 9.5 {
		t.Fatalf("unexpected edit-script render output: %+v", info)
	}
	// Verify seg_0.ass got both SegOverlay text & Karaoke word highlight!
	assBytes, err := os.ReadFile(filepath.Join(tmp, "work", "seg_0.ass"))
	if err != nil {
		t.Fatalf("read seg_0.ass: %v", err)
	}
	if !strings.Contains(string(assBytes), "DIKIT LAGI MATI") || !strings.Contains(string(assBytes), `\fscx132\fscy132`) {
		t.Fatalf("expected TextOverlay + karaoke punch-zoom in seg_0.ass")
	}
}
