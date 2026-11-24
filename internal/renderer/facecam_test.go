package renderer

import (
	"testing"

	"klipforge/internal/model"
)

func TestComputeFacecamBoxFromDetections_PrefersCornerWebcam(t *testing.T) {
	// Streamer webcam sits cleanly at bottom-right ~(0.84, 0.78), plus one noisy center face
	dets := []FaceDetection{
		{TimeSec: 10, CenterX: 0.84, CenterY: 0.78, Width: 0.11, Height: 0.14, Confidence: 0.92},
		{TimeSec: 20, CenterX: 0.85, CenterY: 0.79, Width: 0.10, Height: 0.13, Confidence: 0.94},
		{TimeSec: 30, CenterX: 0.83, CenterY: 0.77, Width: 0.12, Height: 0.15, Confidence: 0.91},
		{TimeSec: 40, CenterX: 0.50, CenterY: 0.50, Width: 0.08, Height: 0.10, Confidence: 0.75}, // NPC center ignored when edge faces exist
	}

	box, corner, ok := ComputeFacecamBoxFromDetections(dets)
	if !ok {
		t.Fatalf("expected facecam detection success")
	}
	if corner != "bottom-right" {
		t.Fatalf("expected bottom-right corner, got %q", corner)
	}
	if box.X < 0.55 || box.Y < 0.45 {
		t.Fatalf("expected crop box in bottom-right quadrant, got %+v", box)
	}
	if box.W < 0.20 || box.W > 0.42 || box.H < 0.24 || box.H > 0.48 {
		t.Fatalf("expected clamped webcam dimensions, got %+v", box)
	}
	if box.X+box.W > 1.0001 || box.Y+box.H > 1.0001 {
		t.Fatalf("expected crop box inside normalized frame [0,1], got %+v", box)
	}
}

func TestApplyProjectFacecamDefault(t *testing.T) {
	proj := &model.Project{
		FacecamBox:    &model.CropBox{X: 0.02, Y: 0.04, W: 0.28, H: 0.34},
		FacecamCorner: "top-left",
	}
	layout := model.LayoutConfig{CropMode: "facecam_top_neon"}
	applyProjectFacecamDefault(&layout, proj)
	if layout.FacecamBox.X != 0.02 || layout.FacecamBox.W != 0.28 {
		t.Fatalf("expected project facecamBox applied to split layout, got %+v", layout.FacecamBox)
	}

	// Non-split layout should stay untouched
	ft := model.LayoutConfig{CropMode: "face_track"}
	applyProjectFacecamDefault(&ft, proj)
	if ft.FacecamBox.W != 0 {
		t.Fatalf("expected face_track layout untouched, got %+v", ft.FacecamBox)
	}
}
