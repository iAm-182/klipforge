package renderer

import (
	"context"
	"os/exec"
	"path/filepath"
	"testing"

	"klipforge/internal/model"
)

func TestRenderPostEdit_StreamCopyAndTrimWithOutro(t *testing.T) {
	ffPath, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg tidak ditemukan di PATH")
	}
	ffprobePath, _ := exec.LookPath("ffprobe")
	tmp := t.TempDir()

	// Create synthetic 8s 720x1280 source clip, 2s outro, and 0.5s SFX WAV
	srcMP4 := filepath.Join(tmp, "src.mp4")
	outroMP4 := filepath.Join(tmp, "outro.mp4")
	sfxWAV := filepath.Join(tmp, "sfx.wav")

	if out, err := exec.Command(ffPath, "-y", "-f", "lavfi", "-i", "color=c=navy:s=360x640:r=30:d=8", "-f", "lavfi", "-i", "sine=frequency=440:duration=8", "-c:v", "libx264", "-preset", "ultrafast", "-c:a", "aac", srcMP4).CombinedOutput(); err != nil {
		t.Fatalf("failed create src: %v %s", err, string(out))
	}
	if out, err := exec.Command(ffPath, "-y", "-f", "lavfi", "-i", "color=c=black:s=360x640:r=30:d=2", "-f", "lavfi", "-i", "sine=frequency=220:duration=2", "-c:v", "libx264", "-preset", "ultrafast", "-c:a", "aac", outroMP4).CombinedOutput(); err != nil {
		t.Fatalf("failed create outro: %v %s", err, string(out))
	}
	if out, err := exec.Command(ffPath, "-y", "-f", "lavfi", "-i", "sine=frequency=880:duration=0.4", sfxWAV).CombinedOutput(); err != nil {
		t.Fatalf("failed create sfx: %v %s", err, string(out))
	}

	ctx := context.Background()

	// Test 1: Pure SFX overlay (-c:v copy instant path!)
	out1 := filepath.Join(tmp, "edited_sfx_only.mp4")
	info1, err := RenderPostEdit(ctx, PostEditInput{
		FFmpegPath:     ffPath,
		FFprobePath:    ffprobePath,
		SourceMP4:      srcMP4,
		OutputPath:     out1,
		WorkDir:        filepath.Join(tmp, "w1"),
		TrimStartSec:   0,
		TrimEndSec:     0,
		PreserveOutro:  true,
		SourceHadOutro: true,
		Outro:          &model.ClipAssetVideo{Enabled: true, FilePath: outroMP4, Duration: 2},
		SFXCues:        []ResolvedSFX{{FilePath: sfxWAV, AtSec: 1.5, Volume: 1.0}},
	})
	if err != nil {
		t.Fatalf("RenderPostEdit stream-copy failed: %v", err)
	}
	if info1.Duration < 7.5 || info1.Duration > 8.6 {
		t.Fatalf("unexpected stream-copy duration: %.2f", info1.Duration)
	}

	// Test 2: Trim [1.0s .. 5.0s] + SFX at 2.5s + Preserve 2s clean Outro -> expected ~6.0s total!
	out2 := filepath.Join(tmp, "edited_trimmed.mp4")
	info2, err := RenderPostEdit(ctx, PostEditInput{
		FFmpegPath:     ffPath,
		FFprobePath:    ffprobePath,
		SourceMP4:      srcMP4,
		OutputPath:     out2,
		WorkDir:        filepath.Join(tmp, "w2"),
		TrimStartSec:   1.0,
		TrimEndSec:     5.0,
		PreserveOutro:  true,
		SourceHadOutro: true,
		Outro:          &model.ClipAssetVideo{Enabled: true, FilePath: outroMP4, Duration: 2},
		SFXCues:        []ResolvedSFX{{FilePath: sfxWAV, AtSec: 2.5, Volume: 1.1}},
		Encoder:        "libx264",
		Preset:         "ultrafast",
		CRF:            22,
	})
	if err != nil {
		t.Fatalf("RenderPostEdit trim+outro failed: %v", err)
	}
	if info2.Duration < 5.5 || info2.Duration > 6.6 {
		t.Fatalf("unexpected trimmed+outro duration: %.2f (want ~6.0s)", info2.Duration)
	}
}
