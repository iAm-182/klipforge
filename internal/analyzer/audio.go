package analyzer

import (
	"bufio"
	"context"
	"fmt"
	"math"
	"os/exec"
	"strconv"
	"strings"
)

// AnalyzeAudioEnergy runs FFmpeg astats on a local video/audio file to extract a 1-second RMS energy curve (0.0 - 1.0).
// High spikes correspond to loud laughter, screaming, jumpscares, or explosive gameplay moments!
func AnalyzeAudioEnergy(ctx context.Context, ffmpegPath, mediaPath string, durationSec float64) ([]float64, error) {
	if ffmpegPath == "" || mediaPath == "" {
		return nil, fmt.Errorf("ffmpeg atau file media tidak tersedia")
	}

	// Fast decode: audio only (-vn), downsample to mono 16kHz for blazing speed, 1s RMS window
	cmd := exec.CommandContext(ctx, ffmpegPath,
		"-hide_banner", "-nostats",
		"-i", mediaPath,
		"-vn", "-ac", "1", "-ar", "16000",
		"-af", "asetnsamples=16000,astats=metadata=1:reset=1,ametadata=print:key=lavfi.astats.Overall.RMS_level:file=-",
		"-f", "null", "-",
	)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}

	var dbValues []float64
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 32*1024), 512*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if strings.HasPrefix(line, "lavfi.astats.Overall.RMS_level=") {
			valStr := strings.TrimPrefix(line, "lavfi.astats.Overall.RMS_level=")
			if valStr == "-inf" || valStr == "inf" {
				dbValues = append(dbValues, -90.0)
				continue
			}
			v, err := strconv.ParseFloat(valStr, 64)
			if err != nil || math.IsNaN(v) || math.IsInf(v, 0) {
				dbValues = append(dbValues, -90.0)
			} else {
				dbValues = append(dbValues, v)
			}
		}
	}
	if err := cmd.Wait(); err != nil && len(dbValues) == 0 {
		return nil, err
	}
	if len(dbValues) == 0 {
		return nil, nil
	}

	// Convert dBFS to linear energy relative to the video's own median & 98th percentile
	// so quiet streams and loud streams normalize equally well.
	linear := make([]float64, len(dbValues))
	for i, db := range dbValues {
		if db < -70 {
			db = -70
		}
		if db > 0 {
			db = 0
		}
		linear[i] = math.Pow(10, db/20.0)
	}

	return normalizeEnergyCurve(linear), nil
}

func normalizeEnergyCurve(vals []float64) []float64 {
	n := len(vals)
	if n == 0 {
		return nil
	}
	// Compute mean and stddev for Z-score based excitement boost
	var sum float64
	for _, v := range vals {
		sum += v
	}
	mean := sum / float64(n)

	var variance float64
	for _, v := range vals {
		d := v - mean
		variance += d * d
	}
	std := math.Sqrt(variance / float64(n))
	if std < 1e-6 {
		std = 1e-6
	}

	out := make([]float64, n)
	for i, v := range vals {
		// Smooth with a tiny 3-second local window so single 0.1s mic pops don't dominate
		local := v
		cnt := 1.0
		if i > 0 {
			local += vals[i-1] * 0.5
			cnt += 0.5
		}
		if i+1 < n {
			local += vals[i+1] * 0.5
			cnt += 0.5
		}
		local /= cnt

		z := (local - mean) / std
		// Map z in [-0.5, 3.0] -> [0.0, 1.0]
		norm := (z + 0.5) / 3.5
		if norm < 0 {
			norm = 0
		}
		if norm > 1 {
			norm = 1
		}
		out[i] = norm
	}
	return out
}
