package analyzer

import (
	"bufio"
	"bytes"
	"context"
	"encoding/csv"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// SceneCutResult holds detected shot boundary timestamps (seconds from start of media).
type SceneCutResult struct {
	Cuts   []float64 `json:"cuts"`
	Engine string    `json:"engine"` // "pyscenedetect" | "ffmpeg_scene"
}

// DetectSceneCuts finds real shot boundaries using the PySceneDetect sidecar (`scenedetect list-scenes`)
// when installed, falling back cleanly to FFmpeg's built-in scene change detector (`select='gt(scene,0.32)'`)
// so scene-cut snapping works 100% offline on every machine.
func DetectSceneCuts(ctx context.Context, sceneDetectBin, ffmpegPath, mediaPath string) (SceneCutResult, error) {
	if mediaPath == "" {
		return SceneCutResult{}, fmt.Errorf("mediaPath kosong")
	}
	if _, err := os.Stat(mediaPath); err != nil {
		return SceneCutResult{}, fmt.Errorf("file media tidak ditemukan: %w", err)
	}

	// 1. Try PySceneDetect sidecar first (Breakthrough/PySceneDetect BSD-3 CLI)
	sdBin := resolveSceneDetectBin(sceneDetectBin)
	if sdBin != "" {
		if cuts, err := runPySceneDetect(ctx, sdBin, mediaPath); err == nil {
			return SceneCutResult{Cuts: cuts, Engine: "pyscenedetect"}, nil
		}
	}

	// 2. Built-in offline FFmpeg scene change detector fallback (zero external deps!)
	if ffmpegPath == "" {
		if p, err := exec.LookPath("ffmpeg"); err == nil {
			ffmpegPath = p
		}
	}
	if ffmpegPath == "" {
		return SceneCutResult{}, fmt.Errorf("baik scenedetect maupun ffmpeg tidak ditemukan")
	}
	cuts, err := runFFmpegSceneDetect(ctx, ffmpegPath, mediaPath, 0.32)
	if err != nil {
		return SceneCutResult{}, err
	}
	return SceneCutResult{Cuts: cuts, Engine: "ffmpeg_scene"}, nil
}

func resolveSceneDetectBin(configured string) string {
	if c := strings.TrimSpace(configured); c != "" {
		if p, err := exec.LookPath(c); err == nil {
			return p
		}
		if _, err := os.Stat(c); err == nil {
			return c
		}
	}
	for _, cand := range []string{"scenedetect", "/data/workspace/klipforge/.venv/bin/scenedetect"} {
		if p, err := exec.LookPath(cand); err == nil {
			return p
		}
	}
	return ""
}

func runPySceneDetect(ctx context.Context, bin, mediaPath string) ([]float64, error) {
	tmpDir, err := os.MkdirTemp("", "kf-scenedetect-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmpDir)

	cctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(cctx, bin,
		"-i", mediaPath,
		"-o", tmpDir,
		"detect-content", "-t", "27",
		"list-scenes", "-n",
	)
	if err := cmd.Run(); err != nil {
		return nil, err
	}

	matches, _ := filepath.Glob(filepath.Join(tmpDir, "*-Scenes.csv"))
	if len(matches) == 0 {
		return nil, nil
	}
	f, err := os.Open(matches[0])
	if err != nil {
		return nil, err
	}
	defer f.Close()

	r := csv.NewReader(f)
	r.FieldsPerRecord = -1
	rows, err := r.ReadAll()
	if err != nil {
		return nil, err
	}
	var cuts []float64
	for _, row := range rows {
		if len(row) < 4 {
			continue
		}
		// Standard PySceneDetect CSV columns: Scene Number, Start Frame, Start Timecode, Start Time (seconds), ...
		if sec, err := strconv.ParseFloat(strings.TrimSpace(row[3]), 64); err == nil && sec > 0.2 {
			cuts = append(cuts, math.Round(sec*100)/100)
		}
	}
	sort.Float64s(cuts)
	return dedupCuts(cuts, 0.4), nil
}

var ffPtsTimeRe = regexp.MustCompile(`pts_time:([0-9]+(?:\.[0-9]+)?)`)

func runFFmpegSceneDetect(ctx context.Context, ffmpegPath, mediaPath string, threshold float64) ([]float64, error) {
	if threshold <= 0.05 || threshold >= 0.95 {
		threshold = 0.32
	}
	cctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	filter := fmt.Sprintf("scale=320:-2,select='gt(scene,%.2f)',showinfo", threshold)
	cmd := exec.CommandContext(cctx, ffmpegPath,
		"-hide_banner", "-nostats",
		"-i", mediaPath,
		"-an", "-vf", filter,
		"-f", "null", "-",
	)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	_ = cmd.Run()

	var cuts []float64
	sc := bufio.NewScanner(&stderr)
	for sc.Scan() {
		if m := ffPtsTimeRe.FindStringSubmatch(sc.Text()); len(m) == 2 {
			if sec, err := strconv.ParseFloat(m[1], 64); err == nil && sec > 0.2 {
				cuts = append(cuts, math.Round(sec*100)/100)
			}
		}
	}
	sort.Float64s(cuts)
	return dedupCuts(cuts, 0.4), nil
}

func dedupCuts(cuts []float64, minGap float64) []float64 {
	var out []float64
	for _, c := range cuts {
		if len(out) == 0 || c-out[len(out)-1] >= minGap {
			out = append(out, c)
		}
	}
	return out
}

// SnapBoundariesToScenes snaps [startSec, endSec] to the nearest real shot cuts within maxSnapSec
// while ALWAYS keeping peakSec safely inside the clip and never collapsing duration below minDurSec.
func SnapBoundariesToScenes(startSec, endSec, peakSec float64, cuts []float64, maxSnapSec, minDurSec float64) (newStart, newEnd float64, snapped bool) {
	newStart, newEnd = startSec, endSec
	if len(cuts) == 0 || endSec <= startSec {
		return newStart, newEnd, false
	}
	if maxSnapSec <= 0 {
		maxSnapSec = 3.0
	}
	if minDurSec <= 0 {
		minDurSec = 8.0
	}

	nearestCut := func(target float64) (float64, bool) {
		best := target
		bestDist := maxSnapSec + 0.001
		found := false
		for _, c := range cuts {
			d := math.Abs(c - target)
			if d <= maxSnapSec && d < bestDist {
				bestDist = d
				best = c
				found = true
			}
		}
		return best, found
	}

	if cs, ok := nearestCut(startSec); ok {
		// Never snap start past peakSec - 2s
		if peakSec <= 0 || cs <= peakSec-2.0 {
			if endSec-cs >= minDurSec {
				newStart = cs
				snapped = true
			}
		}
	}
	if ce, ok := nearestCut(endSec); ok {
		// Never snap end before peakSec + 2s
		if peakSec <= 0 || ce >= peakSec+2.0 {
			if ce-newStart >= minDurSec {
				newEnd = ce
				snapped = true
			}
		}
	}
	return math.Round(newStart*100) / 100, math.Round(newEnd*100) / 100, snapped
}

// ShotBoundsAround returns the shot interval [shotStart, shotEnd] containing targetSec from sorted scene cuts.
func ShotBoundsAround(targetSec, mediaDur float64, cuts []float64) (shotStart, shotEnd float64) {
	shotStart = 0
	shotEnd = mediaDur
	for _, c := range cuts {
		if c <= targetSec {
			shotStart = c
		} else {
			shotEnd = c
			break
		}
	}
	if shotEnd <= shotStart {
		shotEnd = math.Max(mediaDur, shotStart+1)
	}
	return shotStart, shotEnd
}
