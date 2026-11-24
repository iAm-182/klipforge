package analyzer

import (
	"context"
	"math"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestSnapBoundariesToScenes_SnapsAndProtectsPeak(t *testing.T) {
	cuts := []float64{10.5, 22.0, 41.2, 58.6}
	ns, ne, snapped := SnapBoundariesToScenes(12.0, 43.0, 30.0, cuts, 3.0, 10.0)
	if !snapped || ns != 10.5 || ne != 41.2 {
		t.Fatalf("expected snap to [10.5, 41.2], got [%.2f, %.2f] snapped=%v", ns, ne, snapped)
	}

	// Must never snap past peakSec!
	ns2, ne2, _ := SnapBoundariesToScenes(20.0, 24.0, 21.5, []float64{21.8, 22.0}, 3.0, 8.0)
	if ns2 > 20.0 || ne2 < 24.0 {
		t.Fatalf("expected peak-safe non-collapsing snap, got [%.2f, %.2f]", ns2, ne2)
	}

	s0, s1 := ShotBoundsAround(30.0, 60.0, cuts)
	if s0 != 22.0 || s1 != 41.2 {
		t.Fatalf("expected shot [22.0, 41.2], got [%.1f, %.1f]", s0, s1)
	}
}

func TestDetectSceneCuts_FFmpegHardCut(t *testing.T) {
	ff, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg not installed")
	}
	tmp := t.TempDir()
	vid := filepath.Join(tmp, "twoscene.mp4")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Build a 6s clip with a clean hard cut at t=3.0s (red screen -> testsrc2 pattern)
	if out, err := exec.CommandContext(ctx, ff,
		"-y", "-hide_banner", "-loglevel", "error",
		"-f", "lavfi", "-i", "color=c=red:s=320x240:r=25:d=3",
		"-f", "lavfi", "-i", "testsrc2=s=320x240:r=25:d=3",
		"-filter_complex", "[0:v][1:v]concat=n=2:v=1:a=0[v]",
		"-map", "[v]", "-c:v", "libx264", "-preset", "ultrafast", "-pix_fmt", "yuv420p", vid,
	).CombinedOutput(); err != nil {
		t.Fatalf("generate test clip failed: %v %s", err, string(out))
	}

	res, err := DetectSceneCuts(ctx, "", ff, vid)
	if err != nil {
		t.Fatalf("DetectSceneCuts failed: %v", err)
	}
	foundNear3 := false
	for _, c := range res.Cuts {
		if math.Abs(c-3.0) <= 0.35 {
			foundNear3 = true
			break
		}
	}
	if !foundNear3 {
		t.Fatalf("expected scene cut near 3.0s, got %v (engine=%s)", res.Cuts, res.Engine)
	}
}
