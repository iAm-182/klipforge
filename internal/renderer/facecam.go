package renderer

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os/exec"
	"sort"
	"strings"
	"time"

	"klipforge/internal/model"
)

// FacecamDetectResult summarizes detected webcam coordinates for split-screen layouts.
type FacecamDetectResult struct {
	Found       bool          `json:"found"`
	Corner      string        `json:"corner"` // top-left, top-right, bottom-left, bottom-right
	FacecamBox  model.CropBox `json:"facecamBox"`
	GameplayBox model.CropBox `json:"gameplayBox"`
	SampleCount int           `json:"sampleCount"`
	Engine      string        `json:"engine"` // yunet_video, yunet_preview, haar_video, haar_preview, fallback_default
}

func isSplitFacecamLayout(mode string) bool {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "facecam_auto", "facecam_top_neon", "facecam_top_float", "facecam_top_ribbon", "facecam_top_clean", "gaming_cinema", "split_custom":
		return true
	default:
		return false
	}
}

func applyProjectFacecamDefault(layout *model.LayoutConfig, proj *model.Project) {
	if layout == nil || proj == nil || proj.FacecamBox == nil {
		return
	}
	if !isSplitFacecamLayout(layout.CropMode) {
		return
	}
	if layout.FacecamBox.W <= 0.05 || layout.FacecamBox.H <= 0.05 {
		layout.FacecamBox = *proj.FacecamBox
	}
}

// ResolveAutoLayout upgrades `cropMode == "facecam_auto"` cleanly:
// uses cached project FacecamBox (or runs live YuNet detection) -> `facecam_top_neon`,
// falling back gracefully to `face_track` when no webcam corner face exists!
func ResolveAutoLayout(ctx context.Context, layout *model.LayoutConfig, proj *model.Project, sourcePath string, clipDur float64) {
	if layout == nil {
		return
	}
	if strings.EqualFold(strings.TrimSpace(layout.CropMode), "facecam_auto") {
		if proj != nil && proj.FacecamBox != nil && proj.FacecamBox.W > 0.05 {
			layout.FacecamBox = *proj.FacecamBox
			layout.CropMode = "facecam_top_neon"
			return
		}
		preview := ""
		if proj != nil {
			preview = proj.PreviewFrame
		}
		if res, err := DetectProjectFacecam(ctx, sourcePath, preview, clipDur); err == nil && res != nil && res.Found {
			layout.FacecamBox = res.FacecamBox
			layout.CropMode = "facecam_top_neon"
			if proj != nil {
				cp := res.FacecamBox
				proj.FacecamBox = &cp
				proj.FacecamCorner = res.Corner
			}
			return
		}
		layout.CropMode = "face_track"
		return
	}
	applyProjectFacecamDefault(layout, proj)
}

// ComputeFacecamBoxFromDetections estimates a clean 16:10 webcam crop box from multi-frame face detections.
// Prefers detections in edge/corner webcam overlay zones (cx < 0.38 || cx > 0.62 || cy < 0.40 || cy > 0.60).
func ComputeFacecamBoxFromDetections(dets []FaceDetection) (box model.CropBox, corner string, ok bool) {
	var valid []FaceDetection
	var edge []FaceDetection
	for _, d := range dets {
		if d.CenterX <= 0 || d.CenterX >= 1 || d.CenterY <= 0 || d.CenterY >= 1 || d.Width <= 0 || d.Height <= 0 {
			continue
		}
		valid = append(valid, d)
		if d.CenterX < 0.38 || d.CenterX > 0.62 || d.CenterY < 0.40 || d.CenterY > 0.60 {
			edge = append(edge, d)
		}
	}
	chosen := edge
	if len(chosen) == 0 {
		chosen = valid
	}
	if len(chosen) == 0 {
		return model.CropBox{X: 0.70, Y: 0.60, W: 0.30, H: 0.40}, "bottom-right", false
	}

	medX := medianFloat(chosen, func(d FaceDetection) float64 { return d.CenterX })
	medY := medianFloat(chosen, func(d FaceDetection) float64 { return d.CenterY })
	medW := medianFloat(chosen, func(d FaceDetection) float64 { return d.Width })
	medH := medianFloat(chosen, func(d FaceDetection) float64 { return d.Height })

	boxW := clampRange(medW*2.6, 0.20, 0.42)
	boxH := clampRange(medH*2.8, 0.24, 0.48)
	// Bias toward a crisp ~16:10 webcam framing ratio while staying within bounds
	targetH := clampRange(boxW*1.12, 0.24, 0.48)
	if targetH > boxH {
		boxH = targetH
	}

	bx := medX - boxW/2.0
	by := medY - boxH/2.0
	if bx < 0 {
		bx = 0
	}
	if bx+boxW > 1 {
		bx = 1 - boxW
	}
	if by < 0 {
		by = 0
	}
	if by+boxH > 1 {
		by = 1 - boxH
	}

	corner = classifyCorner(medX, medY)
	return model.CropBox{
		X: round4(bx),
		Y: round4(by),
		W: round4(boxW),
		H: round4(boxH),
	}, corner, true
}

func classifyCorner(cx, cy float64) string {
	horiz := "right"
	if cx < 0.5 {
		horiz = "left"
	}
	vert := "bottom"
	if cy < 0.5 {
		vert = "top"
	}
	return vert + "-" + horiz
}

func medianFloat(items []FaceDetection, fn func(FaceDetection) float64) float64 {
	vals := make([]float64, len(items))
	for i, it := range items {
		vals[i] = fn(it)
	}
	sort.Float64s(vals)
	mid := len(vals) / 2
	if len(vals)%2 == 1 {
		return vals[mid]
	}
	return (vals[mid-1] + vals[mid]) / 2.0
}

func clampRange(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func round4(v float64) float64 {
	return math.Round(v*10000) / 10000
}

const inlineFacecamSampleScript = `
import sys, os, json
try:
    import cv2
except Exception:
    print(json.dumps({"engine":"unavailable","faces":[]}))
    sys.exit(0)

mode = sys.argv[1] # "video" or "image"
path = sys.argv[2]

yunet = None
for cand in ["data/models/face_detection_yunet.onnx", "/data/workspace/klipforge/data/models/face_detection_yunet.onnx"]:
    if os.path.exists(cand) and hasattr(cv2, "FaceDetectorYN"):
        try:
            yunet = cv2.FaceDetectorYN.create(cand, "", (320, 320), 0.62, 0.3, 50)
            break
        except Exception:
            yunet = None

cascade = None
if yunet is None:
    cascade = cv2.CascadeClassifier(cv2.data.haarcascades + "haarcascade_frontalface_default.xml")

def detect_frame(frame):
    h, w = frame.shape[:2]
    scale = 640.0 / max(1, w)
    small = cv2.resize(frame, (max(1, int(w * scale)), max(1, int(h * scale))))
    sh, sw = small.shape[:2]
    out = []
    if yunet is not None:
        yunet.setInputSize((sw, sh))
        _, dets = yunet.detect(small)
        if dets is not None:
            for d in dets:
                fx, fy, fw, fh, conf = float(d[0]), float(d[1]), float(d[2]), float(d[3]), float(d[-1])
                out.append((fx, fy, fw, fh, conf, sw, sh))
    elif cascade is not None:
        gray = cv2.cvtColor(small, cv2.COLOR_BGR2GRAY)
        faces = cascade.detectMultiScale(gray, scaleFactor=1.15, minNeighbors=5, minSize=(24, 24))
        for (fx, fy, fw, fh) in faces:
            out.append((float(fx), float(fy), float(fw), float(fh), 0.88, sw, sh))
    return out

faces = []
if mode == "image":
    img = cv2.imread(path)
    if img is not None:
        for (fx, fy, fw, fh, conf, sw, sh) in detect_frame(img):
            faces.append({
                "t": 0.0,
                "cx": round((fx + fw / 2.0) / max(1, sw), 4),
                "cy": round((fy + fh / 2.0) / max(1, sh), 4),
                "w": round(fw / max(1, sw), 4),
                "h": round(fh / max(1, sh), 4),
                "conf": round(conf, 3),
            })
else:
    dur = float(sys.argv[3]) if len(sys.argv) > 3 else 0.0
    cap = cv2.VideoCapture(path)
    if cap.isOpened():
        if dur <= 0:
            fps = cap.get(cv2.CAP_PROP_FPS) or 30.0
            cnt = cap.get(cv2.CAP_PROP_FRAME_COUNT) or 0.0
            dur = cnt / max(1.0, fps)
        if dur <= 1.0:
            dur = 60.0
        for frac in [0.15, 0.30, 0.50, 0.70, 0.85]:
            t = dur * frac
            cap.set(cv2.CAP_PROP_POS_MSEC, t * 1000.0)
            ok, frame = cap.read()
            if not ok or frame is None:
                continue
            for (fx, fy, fw, fh, conf, sw, sh) in detect_frame(frame):
                faces.append({
                    "t": round(t, 2),
                    "cx": round((fx + fw / 2.0) / max(1, sw), 4),
                    "cy": round((fy + fh / 2.0) / max(1, sh), 4),
                    "w": round(fw / max(1, sw), 4),
                    "h": round(fh / max(1, sh), 4),
                    "conf": round(conf, 3),
                })
        cap.release()

eng = ("yunet_" if yunet is not None else "haar_") + ("video" if mode == "video" else "preview")
print(json.dumps({"engine": eng, "faces": faces}))
`

// DetectProjectFacecam detects the streamer webcam crop box using local video (5 spaced frames) or
// preview thumbnail JPEG (Audio-First mode) via YuNet ONNX / OpenCV Haar fallback.
func DetectProjectFacecam(ctx context.Context, videoPath, previewImagePath string, durationSec float64) (*FacecamDetectResult, error) {
	defRes := &FacecamDetectResult{
		Found:       false,
		Corner:      "bottom-right",
		FacecamBox:  model.CropBox{X: 0.70, Y: 0.60, W: 0.30, H: 0.40},
		GameplayBox: model.CropBox{X: 0.14, Y: 0.0, W: 0.72, H: 1.0},
		Engine:      "fallback_default",
	}

	pyBin := ""
	for _, cand := range []string{
		"/data/workspace/klipforge/.venv/bin/python",
		"python3",
		"python",
	} {
		if p, err := exec.LookPath(cand); err == nil {
			pyBin = p
			break
		}
	}
	if pyBin == "" {
		return defRes, nil
	}

	mode := ""
	target := ""
	if strings.TrimSpace(videoPath) != "" {
		mode = "video"
		target = videoPath
	} else if strings.TrimSpace(previewImagePath) != "" {
		mode = "image"
		target = previewImagePath
	} else {
		return defRes, nil
	}

	cctx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()
	args := []string{"-c", inlineFacecamSampleScript, mode, target}
	if mode == "video" {
		args = append(args, fmt.Sprintf("%.2f", durationSec))
	}
	cmd := exec.CommandContext(cctx, pyBin, args...)
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		return defRes, nil
	}

	var parsed struct {
		Engine string          `json:"engine"`
		Faces  []FaceDetection `json:"faces"`
	}
	if err := json.Unmarshal(bytes.TrimSpace(out.Bytes()), &parsed); err != nil {
		return defRes, nil
	}
	if parsed.Engine == "unavailable" || len(parsed.Faces) == 0 {
		if parsed.Engine != "" && parsed.Engine != "unavailable" {
			defRes.Engine = parsed.Engine
		}
		return defRes, nil
	}

	box, corner, ok := ComputeFacecamBoxFromDetections(parsed.Faces)
	if !ok {
		return defRes, nil
	}
	return &FacecamDetectResult{
		Found:       true,
		Corner:      corner,
		FacecamBox:  box,
		GameplayBox: model.CropBox{X: 0.14, Y: 0.0, W: 0.72, H: 1.0},
		SampleCount: len(parsed.Faces),
		Engine:      parsed.Engine,
	}, nil
}
