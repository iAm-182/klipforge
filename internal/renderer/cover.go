package renderer

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"os/exec"
	"time"
)

// inlineCoverPickerScript evaluates frames inside [shotStart, shotEnd] around peakSec,
// preferring face-present frames (reusing YuNet ONNX / Haar) with highest Laplacian sharpness!
const inlineCoverPickerScript = `
import sys, os, json
try:
    import cv2
except Exception:
    print("{}")
    sys.exit(0)

video_path = sys.argv[1]
shot_start = float(sys.argv[2])
shot_end = float(sys.argv[3])
peak_sec = float(sys.argv[4])
out_jpg = sys.argv[5]

cap = cv2.VideoCapture(video_path)
if not cap.isOpened():
    print("{}")
    sys.exit(0)

yunet = None
for cand in ["data/models/face_detection_yunet.onnx", "/data/workspace/klipforge/data/models/face_detection_yunet.onnx"]:
    if os.path.exists(cand) and hasattr(cv2, "FaceDetectorYN"):
        try:
            yunet = cv2.FaceDetectorYN.create(cand, "", (320, 320), 0.6, 0.3, 50)
            break
        except Exception:
            yunet = None

cascade = None
if yunet is None:
    cascade = cv2.CascadeClassifier(cv2.data.haarcascades + "haarcascade_frontalface_default.xml")

# Sample up to 9 timestamps centered on peak_sec strictly inside [shot_start, shot_end]
lo = max(shot_start, peak_sec - 2.0)
hi = min(shot_end, peak_sec + 2.0)
if hi <= lo:
    lo = max(0.0, peak_sec - 0.5)
    hi = peak_sec + 0.5
times = [lo + (hi - lo) * i / 8.0 for i in range(9)]

best_score = -1.0
best_frame = None
best_t = peak_sec
best_face = False

for t in times:
    cap.set(cv2.CAP_PROP_POS_MSEC, t * 1000.0)
    ok, frame = cap.read()
    if not ok or frame is None:
        continue
    gray = cv2.cvtColor(frame, cv2.COLOR_BGR2GRAY)
    sharp = float(cv2.Laplacian(gray, cv2.CV_64F).var())
    has_face = False
    h, w = frame.shape[:2]
    scale = 480.0 / max(1, w)
    small = cv2.resize(frame, (max(1, int(w * scale)), max(1, int(h * scale))))
    sh, sw = small.shape[:2]
    if yunet is not None:
        yunet.setInputSize((sw, sh))
        _, dets = yunet.detect(small)
        has_face = dets is not None and len(dets) > 0
    elif cascade is not None:
        g2 = cv2.cvtColor(small, cv2.COLOR_BGR2GRAY)
        faces = cascade.detectMultiScale(g2, scaleFactor=1.15, minNeighbors=4, minSize=(24, 24))
        has_face = len(faces) > 0
    score = sharp + (10000.0 if has_face else 0.0) - abs(t - peak_sec) * 20.0
    if score > best_score:
        best_score = score
        best_frame = frame
        best_t = t
        best_face = has_face

cap.release()
if best_frame is not None:
    cv2.imwrite(out_jpg, best_frame, [int(cv2.IMWRITE_JPEG_QUALITY), 88])
    print(json.dumps({"ok": True, "timeSec": round(best_t, 2), "facePresent": best_face, "score": round(best_score, 1)}))
else:
    print("{}")
`

type CoverPickResult struct {
	OK          bool    `json:"ok"`
	TimeSec     float64 `json:"timeSec"`
	FacePresent bool    `json:"facePresent"`
	Score       float64 `json:"score"`
}

// PickSharpestCoverFrame picks the sharpest face-present frame near peakSec strictly inside [shotStart, shotEnd]
// using existing OpenCV + YuNet (falling back to FFmpeg thumbnail grab at peakSec when Python/OpenCV is unavailable).
func PickSharpestCoverFrame(ctx context.Context, ffmpegPath, videoPath string, shotStart, shotEnd, peakSec float64, outJPG string) (CoverPickResult, error) {
	if videoPath == "" || !fileExists(videoPath) {
		return CoverPickResult{}, fmt.Errorf("video tidak ditemukan")
	}
	if peakSec < shotStart {
		peakSec = shotStart
	}
	if shotEnd > shotStart && peakSec > shotEnd {
		peakSec = shotEnd
	}

	pyBin := ""
	for _, cand := range []string{"/data/workspace/klipforge/.venv/bin/python", "python3", "python"} {
		if p, err := exec.LookPath(cand); err == nil {
			pyBin = p
			break
		}
	}
	if pyBin != "" {
		cctx, cancel := context.WithTimeout(ctx, 25*time.Second)
		defer cancel()
		cmd := exec.CommandContext(cctx, pyBin, "-c", inlineCoverPickerScript,
			videoPath,
			fmt.Sprintf("%.3f", math.Max(0, shotStart)),
			fmt.Sprintf("%.3f", math.Max(shotStart+0.5, shotEnd)),
			fmt.Sprintf("%.3f", math.Max(0, peakSec)),
			outJPG,
		)
		var out bytes.Buffer
		cmd.Stdout = &out
		if err := cmd.Run(); err == nil {
			var res CoverPickResult
			if json.Unmarshal(bytes.TrimSpace(out.Bytes()), &res) == nil && res.OK && fileExists(outJPG) {
				return res, nil
			}
		}
	}

	// Fallback: direct FFmpeg frame grab at peakSec
	if ffmpegPath == "" {
		if p, err := exec.LookPath("ffmpeg"); err == nil {
			ffmpegPath = p
		}
	}
	if ffmpegPath == "" {
		return CoverPickResult{}, fmt.Errorf("ffmpeg tidak ditemukan untuk cover picker")
	}
	cctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	if err := exec.CommandContext(cctx, ffmpegPath,
		"-y", "-hide_banner", "-loglevel", "error",
		"-ss", fmt.Sprintf("%.2f", math.Max(0, peakSec)),
		"-i", videoPath,
		"-frames:v", "1", "-q:v", "3", outJPG,
	).Run(); err != nil {
		return CoverPickResult{}, err
	}
	if _, err := os.Stat(outJPG); err != nil {
		return CoverPickResult{}, err
	}
	return CoverPickResult{OK: true, TimeSec: peakSec, FacePresent: false, Score: 1.0}, nil
}
