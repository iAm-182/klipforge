package renderer

import (
	"math"
	"strings"
	"testing"
)

func TestSmoothFaceTrack_HeavyTripodAndFallback(t *testing.T) {
	// 1. Stationary jitter inside deadzone (+/- 1.5%) must remain rock-steady!
	var jitter []FaceDetection
	for i := 0; i < 20; i++ {
		cx := 0.30
		if i%2 == 0 {
			cx = 0.312
		} else {
			cx = 0.288
		}
		jitter = append(jitter, FaceDetection{TimeSec: float64(i) * 0.25, CenterX: cx, Width: 0.12, Height: 0.18, Confidence: 0.9})
	}
	pts := SmoothFaceTrack(jitter, 5.0, 4.0)
	if len(pts) < 10 {
		t.Fatalf("expected >=10 smoothed points, got %d", len(pts))
	}
	for _, p := range pts {
		if math.Abs(p.CenterX-0.312) > 0.03 {
			t.Fatalf("heavy tripod jittered too much at t=%.2f: got %.4f", p.TimeSec, p.CenterX)
		}
	}

	// 2. Hard camera cut from 0.20 -> 0.80 must glide smoothly (maxVelPerSec=0.28 -> max 0.07 per 0.25s step!)
	var cut []FaceDetection
	for i := 0; i < 40; i++ {
		cx := 0.20
		if i >= 12 {
			cx = 0.80
		}
		cut = append(cut, FaceDetection{TimeSec: float64(i) * 0.25, CenterX: cx, Width: 0.14, Height: 0.20, Confidence: 0.95})
	}
	cutPts := SmoothFaceTrack(cut, 10.0, 4.0)
	for i := 1; i < len(cutPts); i++ {
		step := math.Abs(cutPts[i].CenterX - cutPts[i-1].CenterX)
		if step > 0.075 {
			t.Fatalf("velocity clamp exceeded at t=%.2f: step=%.4f", cutPts[i].TimeSec, step)
		}
	}
	if cutPts[len(cutPts)-1].CenterX < 0.72 {
		t.Fatalf("expected smoothed track to settle near 0.80, got %.3f", cutPts[len(cutPts)-1].CenterX)
	}

	// 3. Missing faces for > 2.5s must drift smoothly back toward center (0.50)
	missing := []FaceDetection{
		{TimeSec: 0.0, CenterX: 0.22, Width: 0.15, Height: 0.20, Confidence: 0.9},
		{TimeSec: 0.5, CenterX: 0.22, Width: 0.15, Height: 0.20, Confidence: 0.9},
	}
	missPts := SmoothFaceTrack(missing, 10.0, 4.0)
	endX := missPts[len(missPts)-1].CenterX
	if math.Abs(endX-0.50) > 0.05 {
		t.Fatalf("expected fallback drift back to center 0.50, got %.3f", endX)
	}

	// 4. FFmpeg dynamic crop expression builds cleanly for 1920x1080 -> 1080x1920
	flt := BuildFaceTrackFilter(cutPts, 1920, 1080, 1080, 1920, "[0:v]", "[main_v0]")
	if !strings.Contains(flt, "crop=608:1080:") || !strings.Contains(flt, "scale=1080:1920[main_v0]") {
		t.Fatalf("unexpected face_track FFmpeg filter: %s", flt)
	}
}
