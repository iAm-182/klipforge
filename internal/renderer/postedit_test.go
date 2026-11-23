package renderer

import (
	"context"
	"os/exec"
	"path/filepath"
	"testing"

	"klipforge/internal/model"
)

func TestRenderPostEdit_StreamCopyAndCapCutProFeatures(t *testing.T) {
	ffPath, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg tidak ditemukan di PATH")
	}
	ffprobePath, _ := exec.LookPath("ffprobe")
	tmp := t.TempDir()

	// Create synthetic 10s 360x640 source clip, 2s outro, 0.4s SFX WAV, and 4s BGM WAV
	srcMP4 := filepath.Join(tmp, "src.mp4")
	outroMP4 := filepath.Join(tmp, "outro.mp4")
	sfxWAV := filepath.Join(tmp, "sfx.wav")
	bgmWAV := filepath.Join(tmp, "bgm.wav")

	if out, err := exec.Command(ffPath, "-y", "-f", "lavfi", "-i", "color=c=navy:s=360x640:r=30:d=10", "-f", "lavfi", "-i", "sine=frequency=440:duration=10", "-c:v", "libx264", "-preset", "ultrafast", "-c:a", "aac", srcMP4).CombinedOutput(); err != nil {
		t.Fatalf("failed create src: %v %s", err, string(out))
	}
	if out, err := exec.Command(ffPath, "-y", "-f", "lavfi", "-i", "color=c=black:s=360x640:r=30:d=2", "-f", "lavfi", "-i", "sine=frequency=220:duration=2", "-c:v", "libx264", "-preset", "ultrafast", "-c:a", "aac", outroMP4).CombinedOutput(); err != nil {
		t.Fatalf("failed create outro: %v %s", err, string(out))
	}
	if out, err := exec.Command(ffPath, "-y", "-f", "lavfi", "-i", "sine=frequency=880:duration=0.4", sfxWAV).CombinedOutput(); err != nil {
		t.Fatalf("failed create sfx: %v %s", err, string(out))
	}
	if out, err := exec.Command(ffPath, "-y", "-f", "lavfi", "-i", "sine=frequency=260:duration=4", bgmWAV).CombinedOutput(); err != nil {
		t.Fatalf("failed create bgm: %v %s", err, string(out))
	}

	ctx := context.Background()

	// Test 1: Pure SFX + BGM custom fade (-c:v copy instant path!)
	out1 := filepath.Join(tmp, "edited_sfx_bgm_only.mp4")
	info1, err := RenderPostEdit(ctx, PostEditInput{
		FFmpegPath:     ffPath,
		FFprobePath:    ffprobePath,
		SourceMP4:      srcMP4,
		OutputPath:     out1,
		WorkDir:        filepath.Join(tmp, "w1"),
		PreserveOutro:  true,
		SourceHadOutro: true,
		Outro:          &model.ClipAssetVideo{Enabled: true, FilePath: outroMP4, Duration: 2},
		BGMPath:        bgmWAV,
		BGMVolume:      0.16,
		BGMFadeInSec:   0.5,
		BGMFadeOutSec:  1.0,
		BGMAutoDuck:    true,
		SFXCues:        []ResolvedSFX{{FilePath: sfxWAV, AtSec: 1.5, Volume: 1.0}},
	})
	if err != nil {
		t.Fatalf("RenderPostEdit stream-copy failed: %v", err)
	}
	if info1.Duration < 9.4 || info1.Duration > 10.6 {
		t.Fatalf("unexpected stream-copy duration: %.2f", info1.Duration)
	}

	// Test 2: Full CapCut Pro Edit — Trim [0.5..7.5] (7s) - CutMiddle [2.5..4.0] (-1.5s) + Freeze 1.0s at 5.0s (+1.0s) + Meme Text + SFX + Clean Outro 2s = ~8.5s!
	out2 := filepath.Join(tmp, "edited_capcut_pro.mp4")
	info2, err := RenderPostEdit(ctx, PostEditInput{
		FFmpegPath:     ffPath,
		FFprobePath:    ffprobePath,
		SourceMP4:      srcMP4,
		OutputPath:     out2,
		WorkDir:        filepath.Join(tmp, "w2"),
		TrimStartSec:   0.5,
		TrimEndSec:     7.5,
		PreserveOutro:  true,
		SourceHadOutro: true,
		Outro:          &model.ClipAssetVideo{Enabled: true, FilePath: outroMP4, Duration: 2},
		CutRanges:      []model.TimeRange{{StartSec: 2.5, EndSec: 4.0}},
		FreezeAtSec:    5.0,
		FreezeDurSec:   1.0,
		FreezeGray:     true,
		TextOverlays: []model.TextOverlay{
			{Text: "EL ALASAN!", StartSec: 4.8, EndSec: 6.2, X: 0.5, Y: 0.25, Color: "#FFE600", FontSize: 54},
		},
		SFXCues: []ResolvedSFX{{FilePath: sfxWAV, AtSec: 5.0, Volume: 1.1}},
		Encoder: "libx264",
		Preset:  "ultrafast",
		CRF:     22,
	})
	if err != nil {
		t.Fatalf("RenderPostEdit CapCut Pro full pipeline failed: %v", err)
	}
	if info2.Duration < 7.5 || info2.Duration > 9.6 {
		t.Fatalf("unexpected CapCut Pro edited duration: %.2f (want ~8.5s)", info2.Duration)
	}
}
