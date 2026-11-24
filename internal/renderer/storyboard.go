package renderer

import (
	"bytes"
	"context"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"klipforge/internal/model"
)

// StoryboardFrame describes one tile inside a generated contact-sheet JPEG.
type StoryboardFrame struct {
	Index    int     `json:"index"`
	Sec      float64 `json:"sec"`
	TimeText string  `json:"timeText"`
}

// StoryboardResult describes the stitched contact-sheet JPEG produced for vision models & human visual scan.
type StoryboardResult struct {
	FilePath string            `json:"filePath"`
	FileName string            `json:"fileName"`
	Width    int               `json:"width"`
	Height   int               `json:"height"`
	Cols     int               `json:"cols"`
	Rows     int               `json:"rows"`
	Frames   []StoryboardFrame `json:"frames"`
}

// PickStoryboardTimestamps chooses representative frame timestamps either inside a requested [startSec, endSec]
// range or across an entire VOD (blending top Chat/Audio/Combined hype peaks with uniform coverage).
func PickStoryboardTimestamps(durationSec, startSec, endSec float64, count int, timeline []model.TimelinePoint, sceneCuts []float64) []float64 {
	if count <= 0 {
		count = 9
	}
	if count > 16 {
		count = 16
	}
	if durationSec <= 0 {
		for _, pt := range timeline {
			if pt.TimeSec > durationSec {
				durationSec = pt.TimeSec
			}
		}
		if endSec > durationSec {
			durationSec = endSec
		}
	}
	if durationSec <= 0 {
		durationSec = 60
	}

	// Range mode: evenly sample across [startSec, endSec] so vision models inspect a candidate/segment cleanly.
	if endSec > startSec+0.5 {
		if startSec < 0 {
			startSec = 0
		}
		if endSec > durationSec {
			endSec = durationSec
		}
		span := endSec - startSec
		if count == 1 || span < 1.0 {
			return []float64{math.Round((startSec+endSec)*5) / 10}
		}
		margin := math.Min(0.4, span*0.04)
		lo := startSec + margin
		hi := endSec - margin
		if hi <= lo {
			lo, hi = startSec, endSec
		}
		out := make([]float64, 0, count)
		for i := 0; i < count; i++ {
			frac := float64(i) / float64(count-1)
			t := lo + frac*(hi-lo)
			out = append(out, math.Round(t*10)/10)
		}
		return out
	}

	// Full-VOD mode: prioritize diverse top hype peaks (chat/audio/combined), then fill uniformly.
	minGap := math.Max(15.0, durationSec/(float64(count)*2.4))
	if durationSec < 90 {
		minGap = math.Max(2.0, durationSec/(float64(count)*1.8))
	}
	skipLead := math.Min(15.0, durationSec*0.03)
	maxSafe := math.Max(0.5, durationSec-1.0)

	var picks []float64
	isTooClose := func(t, gap float64) bool {
		for _, p := range picks {
			if math.Abs(p-t) < gap {
				return true
			}
		}
		return false
	}

	if len(timeline) > 0 {
		type scoredPt struct {
			sec   float64
			score float64
		}
		pts := make([]scoredPt, 0, len(timeline))
		for _, tp := range timeline {
			if tp.TimeSec < skipLead || tp.TimeSec > maxSafe {
				continue
			}
			sc := tp.Combined
			if tp.ChatHype*1.15 > sc {
				sc = tp.ChatHype * 1.15
			}
			if tp.AudioPeak*0.92 > sc {
				sc = tp.AudioPeak * 0.92
			}
			if sc > 0.15 {
				pts = append(pts, scoredPt{sec: tp.TimeSec, score: sc})
			}
		}
		sort.Slice(pts, func(i, j int) bool {
			return pts[i].score > pts[j].score
		})
		peakQuota := count
		if count >= 4 {
			peakQuota = count - 2 // reserve 2 slots for uniform VOD coverage
		}
		for _, pt := range pts {
			if len(picks) >= peakQuota {
				break
			}
			if !isTooClose(pt.sec, minGap) {
				picks = append(picks, math.Round(pt.sec*10)/10)
			}
		}
	}

	// Optionally include strong scene-cut anchor points if timeline was empty
	if len(picks) < count && len(sceneCuts) > 0 {
		step := maxInt(1, len(sceneCuts)/count)
		for i := 0; i < len(sceneCuts) && len(picks) < count; i += step {
			t := sceneCuts[i] + 0.5
			if t >= skipLead && t <= maxSafe && !isTooClose(t, minGap*0.75) {
				picks = append(picks, math.Round(t*10)/10)
			}
		}
	}

	// Fill any remaining slots evenly across the VOD
	if len(picks) < count {
		lo := math.Max(skipLead, durationSec*0.05)
		hi := math.Min(maxSafe, durationSec*0.95)
		if hi <= lo {
			lo, hi = 0, maxSafe
		}
		for i := 0; i < count*2 && len(picks) < count; i++ {
			frac := float64(i+1) / float64(count*2+1)
			t := lo + frac*(hi-lo)
			if !isTooClose(t, minGap*0.45) {
				picks = append(picks, math.Round(t*10)/10)
			}
		}
	}

	// Ultimate fallback if duration was tiny
	for len(picks) < count {
		frac := float64(len(picks)+1) / float64(count+1)
		t := math.Round(frac*maxSafe*10) / 10
		picks = append(picks, t)
	}

	sort.Float64s(picks)
	return picks
}

// GenerateStoryboardContactSheet extracts frames at the given timestamps (works on local MP4 OR remote YouTube stream URL)
// and stitches them into a single labeled contact-sheet JPEG for vision-LLM moment picking & fast human visual QC.
func GenerateStoryboardContactSheet(
	ctx context.Context,
	ffmpegPath string,
	mediaSource string,
	timestamps []float64,
	cols int,
	thumbW int,
	destJPG string,
) (*StoryboardResult, error) {
	if strings.TrimSpace(mediaSource) == "" {
		return nil, fmt.Errorf("mediaSource kosong untuk storyboard")
	}
	if len(timestamps) == 0 {
		return nil, fmt.Errorf("timestamps kosong untuk storyboard")
	}
	if len(timestamps) > 16 {
		timestamps = timestamps[:16]
	}
	if ffmpegPath == "" {
		if p, err := exec.LookPath("ffmpeg"); err == nil {
			ffmpegPath = p
		}
	}
	if ffmpegPath == "" {
		return nil, fmt.Errorf("ffmpeg tidak ditemukan untuk membuat storyboard")
	}

	if cols <= 0 {
		cols = 3
	}
	if cols > 4 {
		cols = 4
	}
	if thumbW < 160 || thumbW > 640 {
		thumbW = 320
	}
	thumbW = makeEven(thumbW)
	thumbH := makeEven(int(math.Round(float64(thumbW) * 9.0 / 16.0)))

	tmpDir, err := os.MkdirTemp("", "kf-storyboard-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmpDir)

	fontPath := findBoldFont()
	type extracted struct {
		sec   float64
		label string
		path  string
		ok    bool
	}
	results := make([]extracted, len(timestamps))

	// Extract up to 4 frames concurrently so remote stream URLs finish in a few seconds.
	sem := make(chan struct{}, 4)
	var wg sync.WaitGroup
	for i, sec := range timestamps {
		if sec < 0 {
			sec = 0
		}
		wg.Add(1)
		go func(idx int, t float64) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			outPath := filepath.Join(tmpDir, fmt.Sprintf("raw_%02d.jpg", idx))
			label := formatStoryboardTime(t)
			baseVF := fmt.Sprintf("scale=%d:%d:force_original_aspect_ratio=decrease,pad=%d:%d:(ow-iw)/2:(oh-ih)/2:black",
				thumbW, thumbH, thumbW, thumbH)

			vf := baseVF
			if fontPath != "" {
				escLabel := strings.ReplaceAll(label, ":", `\:`)
				boxW := 74
				if len(label) > 5 {
					boxW = 94
				}
				vf = fmt.Sprintf("%s,drawbox=x=6:y=6:w=%d:h=24:color=black@0.78:t=fill,drawtext=fontfile='%s':text='%s':fontcolor=#FFE600:fontsize=15:x=12:y=10",
					baseVF, boxW, EscapeFFmpegFilterPath(fontPath), escLabel)
			}

			runGrab := func(filter string) bool {
				cctx, cancel := context.WithTimeout(ctx, 25*time.Second)
				defer cancel()
				cmd := exec.CommandContext(cctx, ffmpegPath,
					"-y", "-hide_banner", "-loglevel", "error",
					"-ss", fmt.Sprintf("%.2f", t),
					"-i", mediaSource,
					"-frames:v", "1",
					"-vf", filter,
					"-q:v", "4",
					outPath,
				)
				if err := cmd.Run(); err != nil {
					return false
				}
				fi, err := os.Stat(outPath)
				return err == nil && fi.Size() > 200
			}

			ok := runGrab(vf)
			if !ok && vf != baseVF {
				// Graceful fallback if ffmpeg lacks drawtext/libfreetype
				ok = runGrab(baseVF)
			}
			results[idx] = extracted{sec: t, label: label, path: outPath, ok: ok}
		}(i, sec)
	}
	wg.Wait()

	var frames []StoryboardFrame
	seqIdx := 0
	for _, r := range results {
		if !r.ok {
			continue
		}
		seqPath := filepath.Join(tmpDir, fmt.Sprintf("tile_%02d.jpg", seqIdx))
		if err := os.Rename(r.path, seqPath); err != nil {
			continue
		}
		frames = append(frames, StoryboardFrame{
			Index:    seqIdx,
			Sec:      r.sec,
			TimeText: r.label,
		})
		seqIdx++
	}
	if len(frames) == 0 {
		return nil, fmt.Errorf("gagal mengekstrak frame dari sumber video untuk storyboard")
	}

	if len(frames) < cols {
		cols = len(frames)
	}
	rows := (len(frames) + cols - 1) / cols

	if err := os.MkdirAll(filepath.Dir(destJPG), 0o755); err != nil {
		return nil, err
	}
	tmpOut := destJPG + ".tmp.jpg"
	_ = os.Remove(tmpOut)

	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	const margin = 4
	tileFilter := fmt.Sprintf("tile=%dx%d:margin=%d:padding=%d:color=0x0b0f1a", cols, rows, margin, margin)
	cmd := exec.CommandContext(cctx, ffmpegPath,
		"-y", "-hide_banner", "-loglevel", "error",
		"-start_number", "0",
		"-i", filepath.Join(tmpDir, "tile_%02d.jpg"),
		"-vf", tileFilter,
		"-frames:v", "1",
		"-q:v", "3",
		tmpOut,
	)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		_ = os.Remove(tmpOut)
		return nil, fmt.Errorf("gagal menggabungkan tile storyboard: %w (%s)", err, strings.TrimSpace(stderr.String()))
	}
	if err := os.Rename(tmpOut, destJPG); err != nil {
		_ = os.Remove(tmpOut)
		return nil, fmt.Errorf("gagal menyimpan file storyboard: %w", err)
	}

	outW := cols*thumbW + (cols+1)*margin
	outH := rows*thumbH + (rows+1)*margin
	return &StoryboardResult{
		FilePath: destJPG,
		FileName: filepath.Base(destJPG),
		Width:    outW,
		Height:   outH,
		Cols:     cols,
		Rows:     rows,
		Frames:   frames,
	}, nil
}

func formatStoryboardTime(sec float64) string {
	s := int(math.Round(sec))
	if s < 0 {
		s = 0
	}
	h := s / 3600
	m := (s % 3600) / 60
	r := s % 60
	if h > 0 {
		return fmt.Sprintf("%d:%02d:%02d", h, m, r)
	}
	return fmt.Sprintf("%02d:%02d", m, r)
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
