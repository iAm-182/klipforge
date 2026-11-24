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
		if cuts, err := runPySceneDetect(ctx, sdBin, mediaPath); err == nil && cuts != nil {
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
		if _, err := os.Stat(c); err == nil {
			if abs, aerr := filepath.Abs(c); aerr == nil {
				return abs
			}
			return c
		}
		if p, err := exec.LookPath(c); err == nil {
			return p
		}
	}
	for _, rel := range []string{
		"/data/workspace/klipforge/.venv/bin/scenedetect",
		"/app/.venv/bin/scenedetect",
		".venv/bin/scenedetect",
		".venv/Scripts/scenedetect.exe",
	} {
		if abs, err := filepath.Abs(rel); err == nil {
			if fi, serr := os.Stat(abs); serr == nil && !fi.IsDir() {
				return abs
			}
		}
	}
	if p, err := exec.LookPath("scenedetect"); err == nil {
		return p
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
	// Use `list-scenes -s -q` (--skip-cuts --quiet) so `*-Scenes.csv` IS written cleanly!
	// (Note: `-n` suppresses CSV output and would falsely return 0 cuts.)
	cmd := exec.CommandContext(cctx, bin,
		"-i", mediaPath,
		"-o", tmpDir,
		"detect-content", "-t", "27",
		"list-scenes", "-s", "-q",
	)
	if err := cmd.Run(); err != nil {
		return nil, err
	}

	matches, _ := filepath.Glob(filepath.Join(tmpDir, "*-Scenes.csv"))
	if len(matches) == 0 {
		return nil, fmt.Errorf("pyscenedetect did not produce Scenes.csv")
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
	cuts := make([]float64, 0)
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

// DetectSceneCutsWindow scans only a local time window [startSec-5, startSec+durSec+5] of a long VOD
// so single-clip rendering never wastes minutes scanning a 4-hour MP4, and maps cut timestamps back to VOD time.
func DetectSceneCutsWindow(ctx context.Context, sceneDetectBin, ffmpegPath, mediaPath string, startSec, durSec float64) (SceneCutResult, error) {
	if startSec <= 0 && durSec <= 0 {
		return DetectSceneCuts(ctx, sceneDetectBin, ffmpegPath, mediaPath)
	}
	if ffmpegPath == "" {
		if p, err := exec.LookPath("ffmpeg"); err == nil {
			ffmpegPath = p
		}
	}
	if ffmpegPath == "" {
		return DetectSceneCuts(ctx, sceneDetectBin, ffmpegPath, mediaPath)
	}
	winStart := startSec - 5.0
	if winStart < 0 {
		winStart = 0
	}
	winDur := durSec + 10.0
	if winDur < 12.0 {
		winDur = 12.0
	}
	cctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	filter := "scale=320:-2,select='gt(scene,0.32)',showinfo"
	cmd := exec.CommandContext(cctx, ffmpegPath,
		"-hide_banner", "-nostats",
		"-ss", fmt.Sprintf("%.2f", winStart),
		"-t", fmt.Sprintf("%.2f", winDur),
		"-i", mediaPath,
		"-an", "-vf", filter,
		"-f", "null", "-",
	)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	_ = cmd.Run()

	cuts := make([]float64, 0)
	sc := bufio.NewScanner(&stderr)
	for sc.Scan() {
		if m := ffPtsTimeRe.FindStringSubmatch(sc.Text()); len(m) == 2 {
			if sec, err := strconv.ParseFloat(m[1], 64); err == nil && sec > 0.1 {
				cuts = append(cuts, math.Round((winStart+sec)*100)/100)
			}
		}
	}
	sort.Float64s(cuts)
	return SceneCutResult{Cuts: dedupCuts(cuts, 0.4), Engine: "ffmpeg_scene_window"}, nil
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
