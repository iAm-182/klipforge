package renderer

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"klipforge/internal/model"
)

func TestReal30sKaraokeAndFaceTrackRender(t *testing.T) {
	ffmpegBin, err := exec.LookPath("ffmpeg")
	if err != nil {
		// Fallback to workspace bundled binary if present
		cand := `E:\klipforge\bin\ffmpeg.exe`
		if _, stErr := os.Stat(cand); stErr == nil {
			ffmpegBin = cand
		} else {
			t.Skip("ffmpeg not available")
		}
	}
	ffprobeBin, err := exec.LookPath("ffprobe")
	if err != nil {
		cand := `E:\klipforge\bin\ffprobe.exe`
		if _, stErr := os.Stat(cand); stErr == nil {
			ffprobeBin = cand
		} else {
			t.Skip("ffprobe not available")
		}
	}

	tmp := t.TempDir()
	srcMP4 := filepath.Join(tmp, "src30.mp4")
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// Generate a real 30-second 16:9 1280x720 video + audio track
	genCmd := exec.CommandContext(ctx, ffmpegBin,
		"-y", "-hide_banner", "-loglevel", "error",
		"-f", "lavfi", "-i", "testsrc2=size=1280x720:rate=30:duration=30",
		"-f", "lavfi", "-i", "sine=frequency=440:duration=30",
		"-c:v", "libx264", "-preset", "ultrafast", "-pix_fmt", "yuv420p",
		"-c:a", "aac", "-b:a", "96k",
		srcMP4,
	)
	if out, err := genCmd.CombinedOutput(); err != nil {
		t.Fatalf("generate 30s source failed: %v (%s)", err, string(out))
	}

	// Build 30s karaoke word-by-word ASS file
	var cues []model.TranscriptCue
	words := []string{"lihat", "nih", "gua", "jago", "nyetir", "anjir", "nabrak", "tiang", "wkwkwk", "ngakak"}
	for i := 0; i < 10; i++ {
		base := float64(i) * 3.0
		w1, w2, w3 := words[i%len(words)], words[(i+1)%len(words)], words[(i+2)%len(words)]
		cues = append(cues, model.TranscriptCue{
			Start: base,
			End:   base + 2.7,
			Text:  w1 + " " + w2 + " " + w3,
			Words: []model.WordTiming{
				{Start: base, End: base + 0.85, Text: w1},
				{Start: base + 0.88, End: base + 1.75, Text: w2},
				{Start: base + 1.78, End: base + 2.65, Text: w3},
			},
		})
	}
	assPath := filepath.Join(tmp, "karaoke30.ass")
	n, err := GenerateASS(cues, 0, 30, 1.0, 720, 1280, model.SubtitleStyle{
		Enabled:        true,
		Preset:         "karaoke",
		FontName:       "Arial",
		FontSize:       66,
		Position:       "bottom",
		MaxWordsPerRow: 3,
		Uppercase:      true,
	}, "GUA JAGO NYETIR", "face_track", assPath)
	if err != nil || n < 30 {
		t.Fatalf("GenerateASS 30s failed: n=%d err=%v", n, err)
	}

	// Verify real FFmpeg dynamic face_track crop filter + karaoke ASS burn-in on 30s clip!
	syntheticTrack := SmoothFaceTrack([]FaceDetection{
		{TimeSec: 0, CenterX: 0.25, Width: 0.15, Height: 0.2},
		{TimeSec: 15, CenterX: 0.75, Width: 0.15, Height: 0.2},
		{TimeSec: 29, CenterX: 0.50, Width: 0.15, Height: 0.2},
	}, 30, 4.0)
	ftFilter := BuildFaceTrackFilter(syntheticTrack, 1280, 720, 720, 1280, "[0:v]", "[v0]")
	outMP4 := filepath.Join(tmp, "out30_karaoke_facetrack.mp4")

	filterComplex := ftFilter + ";[v0]ass='" + EscapeFFmpegFilterPath(assPath) + "',format=yuv420p[vout]"
	encCmd := exec.CommandContext(ctx, ffmpegBin,
		"-y", "-hide_banner", "-loglevel", "error",
		"-i", srcMP4,
		"-filter_complex", filterComplex,
		"-map", "[vout]", "-map", "0:a",
		"-c:v", "libx264", "-preset", "ultrafast", "-crf", "28",
		"-c:a", "aac", "-b:a", "96k",
		outMP4,
	)
	if out, err := encCmd.CombinedOutput(); err != nil {
		t.Fatalf("real 30s karaoke + face_track render failed: %v (%s)", err, string(out))
	}

	info, err := ProbeMedia(ctx, ffprobeBin, outMP4)
	if err != nil {
		t.Fatalf("probe 30s output failed: %v", err)
	}
	if info.Width != 720 || info.Height != 1280 || info.Duration < 29.0 {
		t.Fatalf("unexpected 30s 9:16 output: %+v", info)
	}
}
