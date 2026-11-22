package renderer

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"klipforge/internal/audiofx"
	"klipforge/internal/deps"
	"klipforge/internal/model"
)

func TestRenderClipFullPipelineWithFXBGMSFXWatermarkAndCleanOutro(t *testing.T) {
	depMgr := deps.NewManager(`E:\klipforge`)
	st := depMgr.GetStatus()
	if st.FFmpegPath == "" || st.FFprobePath == "" {
		t.Skip("ffmpeg/ffprobe not found")
	}

	tmpDir := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	// 0. Generate built-in SFX + BGM starter pack into temp dir to verify synthesis works too!
	audioLib := audiofx.NewLibrary(tmpDir, depMgr)
	audioLib.EnsureBuiltIns()
	bgm, okBGM := audioLib.Get("bgm-gaming-pulse")
	sfxBoom, okBoom := audioLib.Get("sfx-boom")
	sfxWhoosh, okWhoosh := audioLib.Get("sfx-whoosh")
	if !okBGM || !okBoom || !okWhoosh {
		t.Fatalf("expected built-in BGM and SFX generated cleanly (bgm=%v boom=%v whoosh=%v)", okBGM, okBoom, okWhoosh)
	}

	// 1. Generate synthetic 16:9 1280x720 6s source clip with stereo sine audio
	srcVideo := filepath.Join(tmpDir, "source.mp4")
	runCmd(t, ctx, st.FFmpegPath,
		"-y", "-hide_banner", "-loglevel", "error",
		"-f", "lavfi", "-i", "testsrc2=size=1280x720:rate=30:duration=6",
		"-f", "lavfi", "-i", "sine=frequency=440:sample_rate=48000:duration=6",
		"-c:v", "libx264", "-preset", "ultrafast", "-pix_fmt", "yuv420p",
		"-c:a", "aac", srcVideo,
	)

	// 2. Generate synthetic 3s Outro template (NO audio track -> tests auto silent audio + zero FX bleed!)
	outroVideo := filepath.Join(tmpDir, "outro3s.mp4")
	runCmd(t, ctx, st.FFmpegPath,
		"-y", "-hide_banner", "-loglevel", "error",
		"-f", "lavfi", "-i", "color=c=navy:s=1080x1920:r=30:d=3",
		"-c:v", "libx264", "-preset", "ultrafast", "-pix_fmt", "yuv420p",
		outroVideo,
	)

	// 3. Generate synthetic PNG watermark
	wmPNG := filepath.Join(tmpDir, "watermark.png")
	runCmd(t, ctx, st.FFmpegPath,
		"-y", "-hide_banner", "-loglevel", "error",
		"-f", "lavfi", "-i", "color=c=orange:s=240x80:d=0.1",
		"-frames:v", "1", wmPNG,
	)

	// 4. Generate ASS subtitles
	assPath := filepath.Join(tmpDir, "captions.ass")
	cues := []model.TranscriptCue{
		{Start: 1.0, End: 3.5, Text: "Waduh gila banget GTA VI ini guys"},
		{Start: 3.6, End: 5.5, Text: "Kaget parah bang Windah!"},
	}
	subStyle := model.SubtitleStyle{
		Enabled:        true,
		Preset:         "viral-yellow",
		FontName:       "Impact",
		FontSize:       56,
		Position:       "split-seam",
		MaxWordsPerRow: 4,
		Uppercase:      true,
	}
	n, err := GenerateASS(cues, 1.0, 5.0, 1.05, 720, 1280, subStyle, "WINDAH KAGET LIAT GTA VI! 🔥", assPath)
	if err != nil || n == 0 {
		t.Fatalf("GenerateASS failed: n=%d err=%v", n, err)
	}

	// 5. Render 4s clip + Vivid Color + Peak Punch Zoom + Speed 1.05x + Fade-In + Auto-Ducked BGM + 2 SFX Cues + Hook + Subtitles + Watermark + Clean 3s Outro!
	outMP4 := filepath.Join(tmpDir, "final_clip.mp4")
	info, err := RenderClip(ctx, RenderInput{
		FFmpegPath:   st.FFmpegPath,
		FFprobePath:  st.FFprobePath,
		SourcePath:   srcVideo,
		SeekStart:    1.0,
		ClipDuration: 4.0,
		OutputPath:   outMP4,
		WorkDir:      tmpDir,
		Layout: model.LayoutConfig{
			AspectRatio: "9:16",
			Resolution:  "720p",
			CropMode:    "split_custom",
			SplitRatio:  0.62,
			GameplayBox: model.CropBox{X: 0.14, Y: 0.0, W: 0.72, H: 1.0},
			FacecamBox:  model.CropBox{X: 0.70, Y: 0.60, W: 0.30, H: 0.40},
		},
		Subtitles:    subStyle,
		SubtitleFile: assPath,
		HookText:     "WINDAH KAGET LIAT GTA VI! 🔥",
		AudioNorm:    true,
		VideoFX: model.VideoFXConfig{
			ColorPreset:   "vivid_gaming",
			MotionPreset:  "peak_punch",
			PeakOffsetSec: 1.6,
			FadeIn:        true,
			Speed:         1.05,
			Sharpen:       true,
		},
		BGMPath:     bgm.FilePath,
		BGMVolume:   0.20,
		BGMAutoDuck: true,
		SFXCues: []ResolvedSFX{
			{FilePath: sfxWhoosh.FilePath, AtSec: 0.1, Volume: 0.8},
			{FilePath: sfxBoom.FilePath, AtSec: 1.6, Volume: 1.0},
		},
		Watermark: &model.WatermarkConfig{
			Enabled:  true,
			FilePath: wmPNG,
			Position: "top-right",
			Scale:    0.22,
			Opacity:  0.90,
			MarginPx: 24,
			OnOutro:  false,
		},
		Outro: &model.ClipAssetVideo{
			Enabled:  true,
			FilePath: outroVideo,
			Duration: 3.0,
			KeepRaw:  true,
		},
		Encoder: "libx264",
		Preset:  "ultrafast",
		CRF:     23,
	})
	if err != nil {
		t.Fatalf("RenderClip with full FX+BGM+SFX failed: %v", err)
	}

	fi, err := os.Stat(outMP4)
	if err != nil || fi.Size() < 10_000 {
		t.Fatalf("unexpected output file size: err=%v size=%d", err, fi.Size())
	}
	if info.Width != 720 || info.Height != 1280 {
		t.Fatalf("expected 720x1280 vertical clip, got %dx%d", info.Width, info.Height)
	}
	if !info.HasAudio {
		t.Fatalf("expected mixed stereo audio track in output")
	}
	// Expected duration: ~(4.0/1.05 = 3.81s) main + 3.0s outro = ~6.81s
	if info.Duration < 6.0 || info.Duration > 7.6 {
		t.Fatalf("expected final duration ~6.8s (main + clean 3s outro), got %.2fs", info.Duration)
	}
}

func runCmd(t *testing.T, ctx context.Context, bin string, args ...string) {
	t.Helper()
	cmd := exec.CommandContext(ctx, bin, args...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("command failed: %v\n%s", err, string(out))
	}
}
