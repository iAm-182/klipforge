package analyzer

import (
	"fmt"
	"math"
)

// SilenceTrimConfig tunes the ported WyattBlue/auto-editor threshold + margin dead-air trimmer.
// Guardrail: NEVER trims inside [peakSec - PeakProtectBefore, peakSec + PeakProtectAfter]
// so meaningful comedic pauses right before or during a punchline stay 100% intact!
type SilenceTrimConfig struct {
	Threshold         float64 // Normalized 0..1 RMS energy floor below which a 1s bucket is silence (default 0.18)
	MarginSec         float64 // Margin kept before/after active speech (default 1.0s)
	PeakProtectBefore float64 // Seconds before peakSec strictly protected from trimming (default 4.0s)
	PeakProtectAfter  float64 // Seconds after peakSec strictly protected from trimming (default 3.0s)
	MinKeepDurSec     float64 // Minimum clip length preserved after trimming (default 8.0s)
}

func DefaultSilenceTrimConfig() SilenceTrimConfig {
	return SilenceTrimConfig{
		Threshold:         0.18,
		MarginSec:         1.0,
		PeakProtectBefore: 4.0,
		PeakProtectAfter:  3.0,
		MinKeepDurSec:     8.0,
	}
}

// TrimSilenceFromEnergy trims leading & trailing dead air from [startSec, endSec] using the
// existing 1-second RMS energy curve (internal/analyzer/audio.go) while preserving margins
// and STRICTLY protecting the peakSec comic-pause window.
func TrimSilenceFromEnergy(energy []float64, startSec, endSec, peakSec float64, cfg SilenceTrimConfig) (newStart, newEnd float64, trimmed bool, reason string) {
	newStart, newEnd = startSec, endSec
	if len(energy) == 0 || endSec <= startSec {
		return newStart, newEnd, false, "no audio energy curve"
	}
	if cfg.Threshold <= 0 {
		cfg.Threshold = 0.18
	}
	if cfg.MarginSec < 0 {
		cfg.MarginSec = 1.0
	}
	if cfg.PeakProtectBefore <= 0 {
		cfg.PeakProtectBefore = 4.0
	}
	if cfg.PeakProtectAfter <= 0 {
		cfg.PeakProtectAfter = 3.0
	}
	if cfg.MinKeepDurSec <= 0 {
		cfg.MinKeepDurSec = 8.0
	}

	sIdx := int(math.Floor(startSec))
	eIdx := int(math.Ceil(endSec))
	if sIdx < 0 {
		sIdx = 0
	}
	if eIdx > len(energy) {
		eIdx = len(energy)
	}
	if eIdx-sIdx <= int(cfg.MinKeepDurSec) {
		return newStart, newEnd, false, "clip already compact"
	}

	// Protected peak window — NEVER trim inside [protStart, protEnd]!
	protStart := peakSec - cfg.PeakProtectBefore
	protEnd := peakSec + cfg.PeakProtectAfter
	if peakSec <= 0 {
		mid := (startSec + endSec) / 2
		protStart = mid - cfg.PeakProtectBefore
		protEnd = mid + cfg.PeakProtectAfter
	}

	// 1. Scan forward from startSec to find first active sound bucket above threshold
	firstActive := sIdx
	for i := sIdx; i < eIdx; i++ {
		if float64(i) >= protStart {
			firstActive = i
			break
		}
		if energy[i] >= cfg.Threshold {
			firstActive = i
			break
		}
	}
	candStart := math.Max(startSec, float64(firstActive)-cfg.MarginSec)
	if candStart > protStart {
		candStart = math.Max(startSec, protStart)
	}

	// 2. Scan backward from endSec to trim trailing dead air after payoff
	lastActive := eIdx - 1
	for i := eIdx - 1; i >= sIdx; i-- {
		if float64(i+1) <= protEnd {
			lastActive = i
			break
		}
		if energy[i] >= cfg.Threshold {
			lastActive = i
			break
		}
	}
	candEnd := math.Min(endSec, float64(lastActive+1)+cfg.MarginSec)
	if candEnd < protEnd {
		candEnd = math.Min(endSec, protEnd)
	}

	if candEnd-candStart < cfg.MinKeepDurSec {
		return newStart, newEnd, false, "skipped to preserve minimum duration"
	}

	leadTrim := candStart - startSec
	tailTrim := endSec - candEnd
	if leadTrim >= 1.0 || tailTrim >= 1.0 {
		newStart = math.Round(candStart*100) / 100
		newEnd = math.Round(candEnd*100) / 100
		return newStart, newEnd, true, fmt.Sprintf("trimmed %.1fs dead-air lead / %.1fs tail (peak %.1fs protected)", leadTrim, tailTrim, peakSec)
	}
	return newStart, newEnd, false, "no leading/trailing dead air above 1s"
}
