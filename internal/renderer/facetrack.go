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
)

// FaceDetection is one detected primary speaker face at timestamp TimeSec (normalized 0.0..1.0).
type FaceDetection struct {
	TimeSec    float64 `json:"t"`
	CenterX    float64 `json:"cx"`
	CenterY    float64 `json:"cy"`
	Width      float64 `json:"w"`
	Height     float64 `json:"h"`
	Confidence float64 `json:"conf"`
}

// TrackPoint is a smoothed horizontal crop center (0.0..1.0) at TimeSec.
type TrackPoint struct {
	TimeSec float64
	CenterX float64
}

// SmoothFaceTrack implements "heavy tripod" camera smoothing (inspired by reelsaw):
// - Samples onto a steady 4 Hz grid over [0, durationSec]
// - Deadzone (±3.5% frame width) absorbs small head micro-jitter so the crop stays locked
// - Exponential smoothing + velocity clamp (max 0.28 frame-width/sec) glides smoothly across moves/cuts
// - Missing-face stretches (> 2.5s without a face) smoothly drift back to center (0.50).
func SmoothFaceTrack(detections []FaceDetection, durationSec float64, sampleFPS float64) []TrackPoint {
	if durationSec <= 0 {
		durationSec = 1.0
	}
	if sampleFPS <= 0 {
		sampleFPS = 4.0
	}
	dt := 1.0 / sampleFPS
	nSteps := int(math.Ceil(durationSec*sampleFPS)) + 1
	if nSteps < 2 {
		nSteps = 2
	}

	sorted := append([]FaceDetection(nil), detections...)
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i].TimeSec < sorted[j].TimeSec
	})

	// Initial anchor: first valid detection within 2s, else center 0.50
	curX := 0.50
	for _, d := range sorted {
		if d.TimeSec <= 2.0 && d.CenterX > 0 && d.CenterX < 1 {
			curX = clamp01(d.CenterX)
			break
		}
	}

	const (
		deadzone       = 0.035 // Ignore micro-jitter < 3.5% of frame width
		emaAlpha       = 0.22  // Heavy tripod inertia
		maxVelPerSec   = 0.28  // Smooth pan speed cap (frame widths / sec)
		missingTimeout = 2.5   // Return smoothly toward center after 2.5s without face
	)
	maxStep := maxVelPerSec * dt

	out := make([]TrackPoint, 0, nSteps)
	lastSeenTime := -999.0
	lastRawX := curX
	detIdx := 0

	for i := 0; i < nSteps; i++ {
		t := float64(i) * dt
		if t > durationSec {
			t = durationSec
		}

		// Pick the largest/most confident face detection inside [t-dt/2, t+dt/2]
		var best *FaceDetection
		for detIdx < len(sorted) && sorted[detIdx].TimeSec < t-dt*0.6 {
			detIdx++
		}
		for k := detIdx; k < len(sorted) && sorted[k].TimeSec <= t+dt*0.6; k++ {
			cand := sorted[k]
			if cand.CenterX <= 0 || cand.CenterX >= 1 {
				continue
			}
			if best == nil || (cand.Width*cand.Height) > (best.Width*best.Height) {
				cp := cand
				best = &cp
			}
		}

		targetX := curX
		if best != nil {
			lastSeenTime = t
			lastRawX = clamp01(best.CenterX)
			if math.Abs(lastRawX-curX) > deadzone {
				targetX = lastRawX
			}
		} else if t-lastSeenTime > missingTimeout {
			// Smoothly return toward center when speaker is absent
			targetX = 0.50
		} else if lastSeenTime >= 0 && math.Abs(lastRawX-curX) > deadzone {
			targetX = lastRawX
		}

		// Heavy-tripod EMA + velocity clamp
		desired := curX + emaAlpha*(targetX-curX)
		delta := desired - curX
		if delta > maxStep {
			delta = maxStep
		} else if delta < -maxStep {
			delta = -maxStep
		}
		curX = clamp01(curX + delta)
		out = append(out, TrackPoint{TimeSec: t, CenterX: curX})
	}

	return out
}

// SimplifyTrackPoints reduces dense 4Hz points to ~0.75s keyframes while preserving smooth pans,
// keeping FFmpeg filter expressions compact and fast to evaluate.
func SimplifyTrackPoints(pts []TrackPoint, minIntervalSec float64) []TrackPoint {
	if len(pts) <= 2 {
		return pts
	}
	if minIntervalSec <= 0 {
		minIntervalSec = 0.75
	}
	out := []TrackPoint{pts[0]}
	last := pts[0]
	for i := 1; i < len(pts)-1; i++ {
		if pts[i].TimeSec-last.TimeSec >= minIntervalSec || math.Abs(pts[i].CenterX-last.CenterX) >= 0.04 {
			out = append(out, pts[i])
			last = pts[i]
		}
	}
	out = append(out, pts[len(pts)-1])
	// Cap total keyframes to 45 so FFmpeg expression never overflows parser stack on long clips
	if len(out) > 45 {
		step := float64(len(out)-1) / 44.0
		capped := make([]TrackPoint, 0, 45)
		for i := 0; i < 45; i++ {
			idx := int(math.Round(float64(i) * step))
			if idx >= len(out) {
				idx = len(out) - 1
			}
			capped = append(capped, out[idx])
		}
		return capped
	}
	return out
}

// BuildFaceTrackFilter builds a 9:16 FFmpeg dynamic crop filter following smoothed TrackPoints.
// Falls back cleanly to standard centered 9:16 crop when no valid track is available.
func BuildFaceTrackFilter(pts []TrackPoint, srcW, srcH, outW, outH int, inLabel, outLabel string) string {
	if srcW <= 0 || srcH <= 0 || outW <= 0 || outH <= 0 {
		return fmt.Sprintf("%sscale=%d:%d:force_original_aspect_ratio=increase,crop=%d:%d%s",
			inLabel, outW, outH, outW, outH, outLabel)
	}
	targetAR := float64(outW) / float64(outH)
	cropH := makeEven(srcH)
	cropW := makeEven(int(math.Round(float64(cropH) * targetAR)))
	if cropW > srcW {
		cropW = makeEven(srcW)
		cropH = makeEven(int(math.Round(float64(cropW) / targetAR)))
	}
	maxX := srcW - cropW
	if maxX <= 4 || len(pts) == 0 {
		return fmt.Sprintf("%sscale=%d:%d:force_original_aspect_ratio=increase,crop=%d:%d%s",
			inLabel, outW, outH, outW, outH, outLabel)
	}

	kf := SimplifyTrackPoints(pts, 0.75)
	xAt := func(cx float64) float64 {
		x := cx*float64(srcW) - float64(cropW)/2.0
		if x < 0 {
			x = 0
		}
		if x > float64(maxX) {
			x = float64(maxX)
		}
		return math.Round(x)
	}

	if len(kf) == 1 {
		return fmt.Sprintf("%scrop=%d:%d:%.0f:0,scale=%d:%d%s",
			inLabel, cropW, cropH, xAt(kf[0].CenterX), outW, outH, outLabel)
	}

	// Build clean piecewise linear x(t) expression inside FFmpeg's crop filter
	var expr strings.Builder
	expr.WriteString(fmt.Sprintf("if(lte(t,%.2f),%.0f", kf[0].TimeSec, xAt(kf[0].CenterX)))
	for i := 0; i < len(kf)-1; i++ {
		t0, t1 := kf[i].TimeSec, kf[i+1].TimeSec
		x0, x1 := xAt(kf[i].CenterX), xAt(kf[i+1].CenterX)
		span := t1 - t0
		if span < 0.05 {
			span = 0.05
		}
		if math.Abs(x1-x0) < 1.0 {
			expr.WriteString(fmt.Sprintf(",if(lte(t,%.2f),%.0f", t1, x0))
		} else {
			expr.WriteString(fmt.Sprintf(",if(lte(t,%.2f),%.0f+(%.0f)*((t-%.2f)/%.2f)", t1, x0, x1-x0, t0, span))
		}
	}
	expr.WriteString(fmt.Sprintf(",%.0f", xAt(kf[len(kf)-1].CenterX)))
	for i := 0; i < len(kf); i++ {
		expr.WriteByte(')')
	}

	return fmt.Sprintf("%scrop=%d:%d:'%s':0,scale=%d:%d%s",
		inLabel, cropW, cropH, expr.String(), outW, outH, outLabel)
}

// ShotReframeSegment represents one scene shot inside a clip with AutoClip-style per-shot framing choice:
// - "crop": YuNet/OpenCV face is present in this shot -> dynamic portrait follow crop
// - "fit":  No face / landscape gameplay/menu/cutscene -> full 16:9 uncropped over blurred vertical backdrop
type ShotReframeSegment struct {
	StartSec float64      `json:"startSec"`
	EndSec   float64      `json:"endSec"`
	Mode     string       `json:"mode"` // "crop" | "fit"
	Track    []TrackPoint `json:"-"`
}

// PlanShotAwareReframe partitions [0, durSec] along detected scene cuts (merging tiny <1.2s micro-shots,
// capping at 10 shots for compact FFmpeg graphs) and chooses "crop" vs "fit" per shot based on face presence.
func PlanShotAwareReframe(dets []FaceDetection, cuts []float64, durSec float64) []ShotReframeSegment {
	if durSec <= 0 {
		durSec = 1.0
	}
	// 1. Clean & filter cut boundaries strictly inside (1.2, durSec-1.2)
	sortedCuts := make([]float64, 0, len(cuts))
	for _, c := range cuts {
		if c > 1.2 && c < durSec-1.2 {
			sortedCuts = append(sortedCuts, c)
		}
	}
	sort.Float64s(sortedCuts)

	// Merge cuts closer than 1.2s and cap to at most 9 internal cuts (10 shots)
	var boundaries []float64
	boundaries = append(boundaries, 0)
	for _, c := range sortedCuts {
		if c-boundaries[len(boundaries)-1] >= 1.2 {
			boundaries = append(boundaries, c)
		}
	}
	if len(boundaries) > 10 {
		// Downsample internal cuts evenly while keeping 0
		keep := make([]float64, 0, 10)
		keep = append(keep, 0)
		step := float64(len(boundaries)-1) / 9.0
		for i := 1; i < 10; i++ {
			idx := int(math.Round(float64(i) * step))
			if idx >= len(boundaries) {
				idx = len(boundaries) - 1
			}
			if boundaries[idx] > keep[len(keep)-1]+0.5 {
				keep = append(keep, boundaries[idx])
			}
		}
		boundaries = keep
	}
	boundaries = append(boundaries, durSec)

	// 2. Classify each shot using 4 Hz face presence ratio (>= 35% buckets with valid face -> "crop", else "fit")
	const sampleFPS = 4.0
	rawShots := make([]ShotReframeSegment, 0, len(boundaries)-1)
	for i := 0; i < len(boundaries)-1; i++ {
		s0, s1 := boundaries[i], boundaries[i+1]
		span := s1 - s0
		if span <= 0.1 {
			continue
		}
		var shotDets []FaceDetection
		bucketSeen := make(map[int]bool)
		for _, d := range dets {
			if d.TimeSec >= s0-0.05 && d.TimeSec <= s1+0.05 && d.CenterX > 0 && d.CenterX < 1 {
				relT := d.TimeSec - s0
				if relT < 0 {
					relT = 0
				}
				if relT > span {
					relT = span
				}
				cp := d
				cp.TimeSec = relT
				shotDets = append(shotDets, cp)
				bucketSeen[int(math.Floor(relT*sampleFPS))] = true
			}
		}
		totalBuckets := int(math.Ceil(span * sampleFPS))
		if totalBuckets < 1 {
			totalBuckets = 1
		}
		presenceRatio := float64(len(bucketSeen)) / float64(totalBuckets)

		mode := "fit"
		var track []TrackPoint
		if presenceRatio >= 0.35 && len(shotDets) > 0 {
			mode = "crop"
			track = SmoothFaceTrack(shotDets, span, sampleFPS)
		}
		rawShots = append(rawShots, ShotReframeSegment{
			StartSec: s0,
			EndSec:   s1,
			Mode:     mode,
			Track:    track,
		})
	}

	if len(rawShots) == 0 {
		return []ShotReframeSegment{{StartSec: 0, EndSec: durSec, Mode: "fit"}}
	}

	// 3. Coalesce consecutive "fit" shots so FFmpeg doesn't split/concat identical blurred-fit stretches unnecessarily.
	merged := make([]ShotReframeSegment, 0, len(rawShots))
	for _, sh := range rawShots {
		if len(merged) > 0 && sh.Mode == "fit" && merged[len(merged)-1].Mode == "fit" {
			merged[len(merged)-1].EndSec = sh.EndSec
			continue
		}
		merged = append(merged, sh)
	}
	return merged
}

func buildBlurFitBranch(inLabel string, outW, outH int, outLabel, prefix string) string {
	return fmt.Sprintf(
		"%ssplit=2[%s_bgs][%s_fgs];"+
			"[%s_bgs]scale=%d:%d:force_original_aspect_ratio=increase,crop=%d:%d,boxblur=26:6[%s_bg];"+
			"[%s_fgs]scale=%d:-2:force_original_aspect_ratio=decrease[%s_fg];"+
			"[%s_bg][%s_fg]overlay=(W-w)/2:(H-h)/2,setsar=1%s",
		inLabel, prefix, prefix,
		prefix, outW, outH, outW, outH, prefix,
		prefix, outW, prefix,
		prefix, prefix, outLabel,
	)
}

// BuildShotAwareReframeFilter builds an FFmpeg filtergraph that switches cleanly per shot between
// YuNet dynamic speaker crop ("crop") and full 16:9 blurred-backdrop portrait ("fit").
func BuildShotAwareReframeFilter(shots []ShotReframeSegment, srcW, srcH, outW, outH int, inLabel, outLabel string) string {
	if len(shots) == 0 {
		return buildBlurFitBranch(inLabel, outW, outH, outLabel, "sfit0")
	}
	if len(shots) == 1 {
		if shots[0].Mode == "crop" && len(shots[0].Track) > 0 {
			return BuildFaceTrackFilter(shots[0].Track, srcW, srcH, outW, outH, inLabel, outLabel)
		}
		return buildBlurFitBranch(inLabel, outW, outH, outLabel, "sfit0")
	}

	var parts []string
	var splitOuts strings.Builder
	for i := range shots {
		splitOuts.WriteString(fmt.Sprintf("[sh_in_%d]", i))
	}
	parts = append(parts, fmt.Sprintf("%ssplit=%d%s", inLabel, len(shots), splitOuts.String()))

	var concatIns strings.Builder
	for i, sh := range shots {
		trLabel := fmt.Sprintf("[sh_tr_%d]", i)
		outSegLabel := fmt.Sprintf("[sh_out_%d]", i)
		parts = append(parts, fmt.Sprintf("[sh_in_%d]trim=start=%.3f:end=%.3f,setpts=PTS-STARTPTS%s",
			i, sh.StartSec, sh.EndSec, trLabel))

		if sh.Mode == "crop" && len(sh.Track) > 0 {
			cropRaw := fmt.Sprintf("[sh_crop_%d]", i)
			parts = append(parts, BuildFaceTrackFilter(sh.Track, srcW, srcH, outW, outH, trLabel, cropRaw))
			parts = append(parts, fmt.Sprintf("%ssetsar=1%s", cropRaw, outSegLabel))
		} else {
			parts = append(parts, buildBlurFitBranch(trLabel, outW, outH, outSegLabel, fmt.Sprintf("sf%d", i)))
		}
		concatIns.WriteString(outSegLabel)
	}
	parts = append(parts, fmt.Sprintf("%sconcat=n=%d:v=1:a=0%s", concatIns.String(), len(shots), outLabel))
	return strings.Join(parts, ";")
}

// inlineOpenCVFaceScript samples the video segment at ~4 fps and detects the largest frontal face
// using YuNet ONNX (230 KB CPU model, per reelsaw design) when present, falling back cleanly to
// OpenCV's bundled Haar Cascade (zero external downloads required!).
const inlineOpenCVFaceScript = `
import sys, os, json
try:
    import cv2
except Exception:
    print("[]")
    sys.exit(0)

video_path = sys.argv[1]
seek_sec = float(sys.argv[2])
dur_sec = float(sys.argv[3])
sample_fps = 4.0

cap = cv2.VideoCapture(video_path)
if not cap.isOpened():
    print("[]")
    sys.exit(0)

yunet = None
for cand in ["data/models/face_detection_yunet.onnx", "/data/workspace/klipforge/data/models/face_detection_yunet.onnx"]:
    if os.path.exists(cand) and hasattr(cv2, "FaceDetectorYN"):
        try:
            yunet = cv2.FaceDetectorYN.create(cand, "", (320, 320), 0.65, 0.3, 50)
            break
        except Exception:
            yunet = None

cascade = None
if yunet is None:
    cascade = cv2.CascadeClassifier(cv2.data.haarcascades + "haarcascade_frontalface_default.xml")

results = []
t = 0.0
step = 1.0 / sample_fps
while t <= dur_sec:
    cap.set(cv2.CAP_PROP_POS_MSEC, (seek_sec + t) * 1000.0)
    ok, frame = cap.read()
    if not ok or frame is None:
        break
    h, w = frame.shape[:2]
    scale = 480.0 / max(1, w)
    small = cv2.resize(frame, (max(1, int(w * scale)), max(1, int(h * scale))))
    sh, sw = small.shape[:2]
    best = None
    if yunet is not None:
        yunet.setInputSize((sw, sh))
        _, dets = yunet.detect(small)
        if dets is not None and len(dets) > 0:
            top = max(dets, key=lambda d: float(d[2]) * float(d[3]))
            fx, fy, fw, fh, conf = float(top[0]), float(top[1]), float(top[2]), float(top[3]), float(top[-1])
            best = (fx, fy, fw, fh, conf)
    elif cascade is not None:
        gray = cv2.cvtColor(small, cv2.COLOR_BGR2GRAY)
        faces = cascade.detectMultiScale(gray, scaleFactor=1.15, minNeighbors=5, minSize=(28, 28))
        if len(faces) > 0:
            fx, fy, fw, fh = max(faces, key=lambda b: b[2] * b[3])
            best = (float(fx), float(fy), float(fw), float(fh), 0.9)
    if best is not None:
        fx, fy, fw, fh, conf = best
        results.append({
            "t": round(t, 3),
            "cx": round((fx + fw / 2.0) / max(1, sw), 4),
            "cy": round((fy + fh / 2.0) / max(1, sh), 4),
            "w": round(fw / max(1, sw), 4),
            "h": round(fh / max(1, sh), 4),
            "conf": round(conf, 3)
        })
    t += step
cap.release()
print(json.dumps(results))
`

// DetectFacesInVideo optionally invokes a lightweight Python+OpenCV (YuNet ONNX / Haar) 4fps face sampler.
// If Python or cv2 is unavailable, returns nil cleanly so caller gracefully falls back to centered 9:16 crop.
func DetectFacesInVideo(ctx context.Context, videoPath string, seekStart, durationSec float64) []FaceDetection {
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
	if pyBin == "" || durationSec <= 0 {
		return nil
	}
	cctx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()
	cmd := exec.CommandContext(cctx, pyBin, "-c", inlineOpenCVFaceScript,
		videoPath, fmt.Sprintf("%.3f", seekStart), fmt.Sprintf("%.3f", durationSec))
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		return nil
	}
	var dets []FaceDetection
	if err := json.Unmarshal(bytes.TrimSpace(out.Bytes()), &dets); err != nil {
		return nil
	}
	return dets
}

func clamp01(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}
