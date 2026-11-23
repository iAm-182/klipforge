package renderer

import (
	"bytes"
	"context"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"klipforge/internal/model"
)

// PostEditInput bundles everything needed for CapCut-Pro-grade local editing on a rendered clip:
// trim, middle jump-cuts (silence removal), freeze-frame meme, partial slow-mo, pop-up meme text,
// manual drag-and-drop watermark, custom BGM fades + Auto-Ducking, and unlimited SFX cues!
type PostEditInput struct {
	FFmpegPath     string
	FFprobePath    string
	SourceMP4      string
	OutputPath     string
	WorkDir        string
	TrimStartSec   float64
	TrimEndSec     float64
	PreserveOutro  bool
	SourceHadOutro bool
	Outro          *model.ClipAssetVideo
	CutRanges      []model.TimeRange      // Middle spans to remove (Jump-Cut / Dead-Air Cut)
	TextOverlays   []model.TextOverlay    // Timed pop-up meme text overlays
	FreezeAtSec    float64                // Optional Freeze-Frame Meme timestamp (source timeline)
	FreezeDurSec   float64                // Freeze hold duration (0.4 - 4.0s)
	FreezeGray     bool                   // Dramatic B&W + vignette during freeze
	SlowMoStart    float64                // Optional partial Slow-Mo start
	SlowMoEnd      float64                // Optional partial Slow-Mo end
	SlowMoFactor   float64                // e.g. 0.5 (half speed)
	SFXCues        []ResolvedSFX          // Timestamps are on the SOURCE MP4 timeline (auto-mapped!)
	BGMPath        string
	BGMVolume      float64
	BGMFadeInSec   float64
	BGMFadeOutSec  float64
	BGMAutoDuck    bool
	Speed          float64
	Watermark      *model.WatermarkConfig // Optional manual drag-and-drop watermark (ONLY on main clip, NEVER Outro!)
	Encoder        string
	Preset         string
	CRF            int
	OnProgress     func(pct float64, speed string)
}

var (
	reSilStart = regexp.MustCompile(`silence_start:\s*([0-9.]+)`)
	reSilEnd   = regexp.MustCompile(`silence_end:\s*([0-9.]+)`)
)

// DetectSilence runs FFmpeg silencedetect to find dead-air spans >= minDur seconds (ReaCut-style silence removal).
func DetectSilence(ctx context.Context, ffmpegPath, videoPath string, minDur float64) ([]model.TimeRange, error) {
	if ffmpegPath == "" || !fileExists(videoPath) {
		return nil, fmt.Errorf("ffmpeg atau file video tidak ditemukan")
	}
	if minDur < 0.5 {
		minDur = 1.2
	}
	cmd := exec.CommandContext(ctx, ffmpegPath,
		"-hide_banner", "-i", videoPath,
		"-af", fmt.Sprintf("silencedetect=noise=-32dB:d=%.2f", minDur),
		"-f", "null", "-",
	)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	_ = cmd.Run()

	var ranges []model.TimeRange
	curStart := -1.0
	for _, line := range strings.Split(stderr.String(), "\n") {
		if m := reSilStart.FindStringSubmatch(line); m != nil {
			if v, err := strconv.ParseFloat(m[1], 64); err == nil {
				curStart = v
			}
		} else if m := reSilEnd.FindStringSubmatch(line); m != nil && curStart >= 0 {
			if v, err := strconv.ParseFloat(m[1], 64); err == nil && v-curStart >= minDur {
				// Leave 0.15s breathing room on both edges so speech is never clipped harshly
				s := curStart + 0.15
				e := v - 0.15
				if e-s >= 0.4 {
					ranges = append(ranges, model.TimeRange{
						StartSec: math.Round(s*10) / 10,
						EndSec:   math.Round(e*10) / 10,
					})
				}
			}
			curStart = -1
		}
	}
	return ranges, nil
}

type timelinePiece struct {
	start    float64
	end      float64
	speedMul float64 // 1.0 normal, 0.5 slow-mo
	isFreeze bool
	freezeD  float64
}

// buildKeptPieces computes the ordered kept pieces inside [trimStart, trimEnd] after subtracting CutRanges,
// splitting for SlowMoRange, and inserting an optional FreezeFrame hold.
func buildKeptPieces(trimStart, trimEnd float64, cuts []model.TimeRange, slowStart, slowEnd, slowFactor, freezeAt, freezeDur float64) []timelinePiece {
	// 1. Normalize & merge cut ranges inside [trimStart, trimEnd]
	var cleanCuts []model.TimeRange
	for _, c := range cuts {
		s := math.Max(trimStart, c.StartSec)
		e := math.Min(trimEnd, c.EndSec)
		if e-s >= 0.25 {
			cleanCuts = append(cleanCuts, model.TimeRange{StartSec: s, EndSec: e})
		}
	}
	sort.Slice(cleanCuts, func(i, j int) bool { return cleanCuts[i].StartSec < cleanCuts[j].StartSec })
	var merged []model.TimeRange
	for _, c := range cleanCuts {
		if len(merged) == 0 || c.StartSec > merged[len(merged)-1].EndSec {
			merged = append(merged, c)
		} else if c.EndSec > merged[len(merged)-1].EndSec {
			merged[len(merged)-1].EndSec = c.EndSec
		}
	}

	// 2. Complement cuts to get kept spans
	var base []timelinePiece
	cur := trimStart
	for _, c := range merged {
		if c.StartSec-cur >= 0.15 {
			base = append(base, timelinePiece{start: cur, end: c.StartSec, speedMul: 1.0})
		}
		cur = math.Max(cur, c.EndSec)
	}
	if trimEnd-cur >= 0.15 {
		base = append(base, timelinePiece{start: cur, end: trimEnd, speedMul: 1.0})
	}
	if len(base) == 0 {
		base = []timelinePiece{{start: trimStart, end: trimEnd, speedMul: 1.0}}
	}

	// 3. Apply optional partial Slow-Mo split
	if slowFactor >= 0.35 && slowFactor <= 0.85 && slowEnd > slowStart+0.3 {
		var split []timelinePiece
		for _, p := range base {
			if slowEnd <= p.start || slowStart >= p.end {
				split = append(split, p)
				continue
			}
			if slowStart > p.start+0.1 {
				split = append(split, timelinePiece{start: p.start, end: slowStart, speedMul: 1.0})
			}
			sA := math.Max(p.start, slowStart)
			sB := math.Min(p.end, slowEnd)
			if sB > sA+0.1 {
				split = append(split, timelinePiece{start: sA, end: sB, speedMul: slowFactor})
			}
			if p.end > slowEnd+0.1 {
				split = append(split, timelinePiece{start: slowEnd, end: p.end, speedMul: 1.0})
			}
		}
		base = split
	}

	// 4. Insert optional Freeze-Frame hold at freezeAt
	if freezeDur >= 0.3 && freezeDur <= 5.0 && freezeAt > trimStart+0.15 && freezeAt < trimEnd-0.15 {
		var withFreeze []timelinePiece
		inserted := false
		for _, p := range base {
			if !inserted && freezeAt > p.start+0.05 && freezeAt < p.end-0.05 {
				withFreeze = append(withFreeze, timelinePiece{start: p.start, end: freezeAt, speedMul: p.speedMul})
				withFreeze = append(withFreeze, timelinePiece{start: freezeAt, end: freezeAt, speedMul: 1.0, isFreeze: true, freezeD: freezeDur})
				withFreeze = append(withFreeze, timelinePiece{start: freezeAt, end: p.end, speedMul: p.speedMul})
				inserted = true
			} else {
				withFreeze = append(withFreeze, p)
			}
		}
		if inserted {
			base = withFreeze
		}
	}
	return base
}

// mapSourceTimeToOutput maps a source timestamp onto the edited output timeline (accounting for trim, cuts, slow-mo, freeze, and global speed).
func mapSourceTimeToOutput(srcT float64, pieces []timelinePiece, globalSpeed float64) (float64, bool) {
	outPos := 0.0
	for _, p := range pieces {
		if p.isFreeze {
			if math.Abs(srcT-p.start) <= 0.25 {
				return outPos / globalSpeed, true
			}
			outPos += p.freezeD
			continue
		}
		if srcT >= p.start-0.2 && srcT <= p.end+0.05 {
			local := math.Max(0, srcT-p.start)
			return (outPos + local/p.speedMul) / globalSpeed, true
		}
		outPos += (p.end - p.start) / p.speedMul
	}
	return 0, false
}

// RenderPostEdit performs fast local CapCut-Pro editing on a finished clip.
// When no trim/cuts/text/freeze/slowmo/watermark/speed change is requested, it uses `-c:v copy` (~1-2s render!)
// while keeping any campaign Outro strictly clean and untouched!
func RenderPostEdit(ctx context.Context, in PostEditInput) (MediaInfo, error) {
	if in.FFmpegPath == "" {
		return MediaInfo{}, fmt.Errorf("ffmpeg tidak ditemukan")
	}
	if !fileExists(in.SourceMP4) {
		return MediaInfo{}, fmt.Errorf("file video sumber tidak ditemukan: %s", in.SourceMP4)
	}
	_ = os.MkdirAll(filepath.Dir(in.OutputPath), 0o755)
	_ = os.MkdirAll(in.WorkDir, 0o755)

	srcInfo, err := ProbeMedia(ctx, in.FFprobePath, in.SourceMP4)
	if err != nil || srcInfo.Duration <= 0 {
		return MediaInfo{}, fmt.Errorf("gagal membaca durasi video sumber: %v", err)
	}
	outW, outH := srcInfo.Width, srcInfo.Height
	if outW <= 0 || outH <= 0 {
		outW, outH = 1080, 1920
	}

	speed := in.Speed
	if speed < 0.75 || speed > 1.50 {
		speed = 1.0
	}

	// Determine whether source already has a clean campaign Outro at the tail
	var outroInfo MediaInfo
	hasOutroFile := in.PreserveOutro && in.Outro != nil && in.Outro.Enabled && in.Outro.FilePath != "" && fileExists(in.Outro.FilePath)
	outroDur := 0.0
	if hasOutroFile {
		if oi, probeErr := ProbeMedia(ctx, in.FFprobePath, in.Outro.FilePath); probeErr == nil && oi.Duration > 0.2 {
			outroInfo = oi
			outroDur = oi.Duration
		} else {
			hasOutroFile = false
		}
	}

	// Main body boundary (excluding clean MotionKlip Outro tail if source already had it)
	bodyLimit := srcInfo.Duration
	sourceHasProtectedTail := hasOutroFile && in.SourceHadOutro && srcInfo.Duration > outroDur+1.5
	if sourceHasProtectedTail {
		bodyLimit = srcInfo.Duration - outroDur
	}

	trimStart := math.Max(0, in.TrimStartSec)
	trimEnd := in.TrimEndSec
	if trimEnd <= 0 || trimEnd > srcInfo.Duration {
		if sourceHasProtectedTail {
			trimEnd = bodyLimit
		} else {
			trimEnd = srcInfo.Duration
		}
	}
	if sourceHasProtectedTail && trimEnd > bodyLimit {
		trimEnd = bodyLimit
	}
	if trimEnd <= trimStart+0.5 {
		return MediaInfo{}, fmt.Errorf("durasi potong tidak valid: End (%.1fs) harus lebih besar dari Start (%.1fs)", trimEnd, trimStart)
	}

	var validSFX []ResolvedSFX
	for _, s := range in.SFXCues {
		if s.FilePath != "" && fileExists(s.FilePath) {
			validSFX = append(validSFX, s)
		}
	}
	hasBGM := in.BGMPath != "" && fileExists(in.BGMPath)
	hasWM := in.Watermark != nil && in.Watermark.Enabled && in.Watermark.FilePath != "" && fileExists(in.Watermark.FilePath)

	// Build timeline pieces (handling cuts, partial slow-mo, and freeze-frame!)
	pieces := buildKeptPieces(trimStart, trimEnd, in.CutRanges, in.SlowMoStart, in.SlowMoEnd, in.SlowMoFactor, in.FreezeAtSec, in.FreezeDurSec)
	hasTimelineMods := len(pieces) > 1 || (len(pieces) == 1 && (pieces[0].isFreeze || math.Abs(pieces[0].speedMul-1.0) > 0.01))
	hasMemeText := len(in.TextOverlays) > 0

	// Check if we can take the ULTRA-FAST `-c:v copy` path (pure audio SFX/BGM edit with zero video mods!)
	noTrim := trimStart <= 0.05 && ((sourceHasProtectedTail && trimEnd >= bodyLimit-0.15) || (!sourceHasProtectedTail && trimEnd >= srcInfo.Duration-0.15))
	noSpeed := math.Abs(speed-1.0) <= 0.005

	if noTrim && noSpeed && !hasWM && !hasTimelineMods && !hasMemeText && !(!in.SourceHadOutro && hasOutroFile) {
		return renderPostEditStreamCopy(ctx, in, srcInfo, bodyLimit, validSFX, hasBGM)
	}

	// Calculate total expected main duration after cuts / slow-mo / freeze / global speed
	rawMainDur := 0.0
	for _, p := range pieces {
		if p.isFreeze {
			rawMainDur += p.freezeD
		} else {
			rawMainDur += (p.end - p.start) / p.speedMul
		}
	}
	if rawMainDur < 0.4 {
		return MediaInfo{}, fmt.Errorf("hasil potongan terlalu pendek (< 0.4 detik)")
	}
	effectiveMainDur := rawMainDur / speed
	reattachOutro := hasOutroFile
	totalExpected := effectiveMainDur
	if reattachOutro {
		totalExpected += outroDur
	}

	// Generate optional ASS file for Timed Meme Pop-Up Text Overlays (mapped onto edited output timeline!)
	memeASSPath := ""
	if hasMemeText {
		p := filepath.Join(in.WorkDir, "meme_overlays.ass")
		if cnt, err := generateMemeTextOverlaysASS(in.TextOverlays, pieces, speed, effectiveMainDur, outW, outH, p); err == nil && cnt > 0 {
			memeASSPath = p
		}
	}

	var args []string
	args = append(args, "-y", "-hide_banner", "-loglevel", "error", "-progress", "pipe:1", "-nostats")
	// Input 0: Local source MP4 (when no complex middle pieces, fast seek; with multiple pieces, read whole file for accurate trim filters)
	if !hasTimelineMods {
		args = append(args, "-ss", fmt.Sprintf("%.3f", trimStart), "-t", fmt.Sprintf("%.3f", trimEnd-trimStart), "-i", in.SourceMP4)
	} else {
		args = append(args, "-i", in.SourceMP4)
	}

	nextIdx := 1
	wmIdx := -1
	outroIdx := -1
	bgmIdx := -1
	sfxIndices := make([]int, len(validSFX))

	if hasWM {
		wmIdx = nextIdx
		nextIdx++
		if strings.EqualFold(filepath.Ext(in.Watermark.FilePath), ".gif") {
			args = append(args, "-ignore_loop", "0")
		}
		args = append(args, "-i", in.Watermark.FilePath)
	}
	if reattachOutro {
		outroIdx = nextIdx
		nextIdx++
		args = append(args, "-i", in.Outro.FilePath)
	}
	if hasBGM {
		bgmIdx = nextIdx
		nextIdx++
		args = append(args, "-stream_loop", "-1", "-i", in.BGMPath)
	}
	for i, sfx := range validSFX {
		sfxIndices[i] = nextIdx
		nextIdx++
		args = append(args, "-i", sfx.FilePath)
	}

	var filters []string

	// Build video + voice base streams ([main_v0] and [voice_raw])
	if !hasTimelineMods {
		if !noSpeed {
			filters = append(filters, fmt.Sprintf("[0:v]setpts=(PTS-STARTPTS)/%.4f,fps=30,setsar=1,format=yuv420p[main_v0]", speed))
		} else {
			filters = append(filters, "[0:v]setpts=PTS-STARTPTS,fps=30,setsar=1,format=yuv420p[main_v0]")
		}
		if srcInfo.HasAudio {
			voiceChain := "[0:a]aformat=sample_fmts=fltp:sample_rates=48000:channel_layouts=stereo"
			if !noSpeed {
				voiceChain += fmt.Sprintf(",atempo=%.4f", speed)
			}
			voiceChain += fmt.Sprintf(",atrim=0:%.3f,asetpts=PTS-STARTPTS[voice_raw]", effectiveMainDur)
			filters = append(filters, voiceChain)
		} else {
			filters = append(filters, fmt.Sprintf("anullsrc=r=48000:cl=stereo:d=%.3f[voice_raw]", effectiveMainDur))
		}
	} else {
		// Multi-piece CapCut timeline assembly: Cut-Middle + Partial Slow-Mo + Freeze-Frame Meme!
		var pieceCats []string
		for idx, pc := range pieces {
			vLbl := fmt.Sprintf("[pv_%d]", idx)
			aLbl := fmt.Sprintf("[pa_%d]", idx)
			if pc.isFreeze {
				// Grab 1 frame at freeze timestamp, hold for freezeD seconds (optional dramatic grayscale + slight zoom!)
				fx := ""
				if in.FreezeGray {
					fx = ",hue=s=0,eq=contrast=1.18,vignette=PI/4"
				}
				filters = append(filters, fmt.Sprintf(
					"[0:v]trim=start=%.3f:duration=0.08,setpts=PTS-STARTPTS,tpad=stop_mode=clone:stop_duration=%.3f,trim=0:%.3f,fps=30,setsar=1,format=yuv420p%s,setpts=PTS-STARTPTS%s",
					pc.start, pc.freezeD, pc.freezeD, fx, vLbl,
				))
				filters = append(filters, fmt.Sprintf("anullsrc=r=48000:cl=stereo:d=%.3f%s", pc.freezeD, aLbl))
			} else {
				vChain := fmt.Sprintf("[0:v]trim=start=%.3f:end=%.3f,setpts=(PTS-STARTPTS)/%.4f,fps=30,setsar=1,format=yuv420p%s",
					pc.start, pc.end, pc.speedMul, vLbl)
				filters = append(filters, vChain)
				if srcInfo.HasAudio {
					aChain := fmt.Sprintf("[0:a]atrim=start=%.3f:end=%.3f,asetpts=PTS-STARTPTS,aformat=sample_fmts=fltp:sample_rates=48000:channel_layouts=stereo", pc.start, pc.end)
					if math.Abs(pc.speedMul-1.0) > 0.01 {
						aChain += fmt.Sprintf(",atempo=%.4f", math.Max(0.5, pc.speedMul))
					}
					aChain += aLbl
					filters = append(filters, aChain)
				} else {
					filters = append(filters, fmt.Sprintf("anullsrc=r=48000:cl=stereo:d=%.3f%s", (pc.end-pc.start)/pc.speedMul, aLbl))
				}
			}
			pieceCats = append(pieceCats, vLbl+aLbl)
		}
		filters = append(filters, fmt.Sprintf("%sconcat=n=%d:v=1:a=1[pc_v][pc_a]", strings.Join(pieceCats, ""), len(pieces)))
		if !noSpeed {
			filters = append(filters, fmt.Sprintf("[pc_v]setpts=(PTS-STARTPTS)/%.4f,fps=30[main_v0]", speed))
			filters = append(filters, fmt.Sprintf("[pc_a]atempo=%.4f,atrim=0:%.3f,asetpts=PTS-STARTPTS[voice_raw]", speed, effectiveMainDur))
		} else {
			filters = append(filters, "[pc_v]copy[main_v0]")
			filters = append(filters, fmt.Sprintf("[pc_a]atrim=0:%.3f,asetpts=PTS-STARTPTS[voice_raw]", effectiveMainDur))
		}
	}

	curV := "[main_v0]"
	if memeASSPath != "" {
		filters = append(filters, fmt.Sprintf("%sass='%s'[main_vtxt]", curV, EscapeFFmpegFilterPath(memeASSPath)))
		curV = "[main_vtxt]"
	}
	if hasWM {
		wmFilter, _ := buildWatermarkFilter(curV, wmIdx, outW, outH, *in.Watermark, "[main_v]")
		filters = append(filters, wmFilter)
	} else {
		filters = append(filters, fmt.Sprintf("%scopy[main_v]", curV))
	}

	// Map SFX timestamps through our pieces map so SFX stay perfectly aligned even after Cut-Middle / Freeze / Slow-Mo!
	mappedSFX := make([]ResolvedSFX, 0, len(validSFX))
	mappedIndices := make([]int, 0, len(validSFX))
	for i, s := range validSFX {
		if tOut, ok := mapSourceTimeToOutput(s.AtSec, pieces, speed); ok {
			cp := s
			cp.AtSec = tOut
			mappedSFX = append(mappedSFX, cp)
			mappedIndices = append(mappedIndices, sfxIndices[i])
		}
	}

	// Mix optional BGM (with custom fade-in/out & Auto-Ducking) + mapped SFX strictly inside main body!
	filters = append(filters, buildPostAudioMix(
		srcInfo.HasAudio, effectiveMainDur, 0, 1.0,
		hasBGM, bgmIdx, in.BGMVolume, in.BGMFadeInSec, in.BGMFadeOutSec, in.BGMAutoDuck,
		mappedSFX, mappedIndices,
	)...)

	finalV := "[main_v]"
	finalA := "[main_a]"
	if reattachOutro {
		filters = append(filters, fmt.Sprintf(
			"[%d:v]scale=%d:%d:force_original_aspect_ratio=decrease,pad=%d:%d:(ow-iw)/2:(oh-ih)/2:black,fps=30,setsar=1,format=yuv420p,setpts=PTS-STARTPTS[outro_v]",
			outroIdx, outW, outH, outW, outH,
		))
		if outroInfo.HasAudio {
			filters = append(filters, fmt.Sprintf("[%d:a]aformat=sample_fmts=fltp:sample_rates=48000:channel_layouts=stereo,asetpts=PTS-STARTPTS[outro_a]", outroIdx))
		} else {
			filters = append(filters, fmt.Sprintf("anullsrc=r=48000:cl=stereo:d=%.3f[outro_a]", math.Max(0.5, outroDur)))
		}
		filters = append(filters, "[main_v][main_a][outro_v][outro_a]concat=n=2:v=1:a=1[cat_v][cat_a]")
		finalV = "[cat_v]"
		finalA = "[cat_a]"
	}

	encoder := in.Encoder
	if encoder == "" || encoder == "auto" {
		encoder = "libx264"
	}
	preset := in.Preset
	if preset == "" {
		preset = "veryfast"
	}
	crf := in.CRF
	if crf <= 0 {
		crf = 19
	}

	filterGraph := strings.Join(filters, ";")
	err = runFFmpegEncode(ctx, in.FFmpegPath, args, filterGraph, finalV, finalA, encoder, preset, crf, in.OutputPath, totalExpected, in.OnProgress)
	if err != nil && encoder != "libx264" {
		err = runFFmpegEncode(ctx, in.FFmpegPath, args, filterGraph, finalV, finalA, "libx264", preset, crf, in.OutputPath, totalExpected, in.OnProgress)
	}
	if err != nil {
		return MediaInfo{}, err
	}
	return ProbeMedia(ctx, in.FFprobePath, in.OutputPath)
}

// renderPostEditStreamCopy copies the video stream bit-for-bit (`-c:v copy`) and ONLY mixes additional SFX/BGM
// strictly before `bodyLimit` so the clean MotionKlip Outro audio remains 100% untouched (~1-2s total render!).
func renderPostEditStreamCopy(ctx context.Context, in PostEditInput, srcInfo MediaInfo, bodyLimit float64, validSFX []ResolvedSFX, hasBGM bool) (MediaInfo, error) {
	var args []string
	args = append(args, "-y", "-hide_banner", "-loglevel", "error", "-progress", "pipe:1", "-nostats")
	args = append(args, "-i", in.SourceMP4)

	nextIdx := 1
	bgmIdx := -1
	sfxIndices := make([]int, len(validSFX))
	if hasBGM {
		bgmIdx = nextIdx
		nextIdx++
		args = append(args, "-stream_loop", "-1", "-i", in.BGMPath)
	}
	for i, sfx := range validSFX {
		sfxIndices[i] = nextIdx
		nextIdx++
		args = append(args, "-i", sfx.FilePath)
	}

	var filters []string
	if srcInfo.HasAudio {
		filters = append(filters, fmt.Sprintf("[0:a]aformat=sample_fmts=fltp:sample_rates=48000:channel_layouts=stereo,atrim=0:%.3f,asetpts=PTS-STARTPTS[voice_raw]", srcInfo.Duration))
	} else {
		filters = append(filters, fmt.Sprintf("anullsrc=r=48000:cl=stereo:d=%.3f[voice_raw]", srcInfo.Duration))
	}

	// Clamp SFX & BGM strictly inside [0, bodyLimit] so ZERO SFX/BGM ever bleeds into Outro tail!
	filters = append(filters, buildPostAudioMix(
		srcInfo.HasAudio, bodyLimit, 0, 1.0,
		hasBGM, bgmIdx, in.BGMVolume, in.BGMFadeInSec, in.BGMFadeOutSec, in.BGMAutoDuck,
		validSFX, sfxIndices,
	)...)

	filterGraph := strings.Join(filters, ";")
	fullArgs := append(args,
		"-filter_complex", filterGraph,
		"-map", "0:v:0",
		"-map", "[main_a]",
		"-c:v", "copy",
		"-c:a", "aac", "-b:a", "192k", "-ar", "48000", "-ac", "2",
		"-movflags", "+faststart",
		in.OutputPath,
	)

	if err := runRawFFmpegWithProgress(ctx, in.FFmpegPath, fullArgs, srcInfo.Duration, in.OnProgress); err != nil {
		return MediaInfo{}, err
	}
	return ProbeMedia(ctx, in.FFprobePath, in.OutputPath)
}

func buildPostAudioMix(
	hasVoice bool,
	activeDur, trimStart, speed float64,
	hasBGM bool, bgmIdx int, bgmVol, fadeInSec, fadeOutSec float64, autoDuck bool,
	validSFX []ResolvedSFX, sfxIndices []int,
) []string {
	var filters []string
	var activeSFX []struct {
		idx   int
		atSec float64
		vol   float64
	}
	for i, sfx := range validSFX {
		shifted := (sfx.AtSec - trimStart) / speed
		if shifted < -0.25 || shifted >= activeDur-0.05 {
			continue
		}
		if shifted < 0 {
			shifted = 0
		}
		v := sfx.Volume
		if v <= 0.05 || v > 2.0 {
			v = 0.90
		}
		activeSFX = append(activeSFX, struct {
			idx   int
			atSec float64
			vol   float64
		}{idx: sfxIndices[i], atSec: shifted, vol: v})
	}

	if !hasBGM && len(activeSFX) == 0 {
		return []string{"[voice_raw]acopy[main_a]"}
	}

	var mixInputs []string
	if hasBGM {
		if bgmVol <= 0.01 || bgmVol > 1.0 {
			bgmVol = 0.16
		}
		fi := fadeInSec
		if fi <= 0 {
			fi = 0.35
		}
		fi = math.Min(fi, activeDur*0.3)
		fo := fadeOutSec
		if fo <= 0 {
			fo = 0.85
		}
		fo = math.Min(fo, activeDur*0.35)
		fadeOutStart := math.Max(0, activeDur-fo)

		filters = append(filters, fmt.Sprintf(
			"[%d:a]aformat=sample_fmts=fltp:sample_rates=48000:channel_layouts=stereo,atrim=0:%.3f,asetpts=PTS-STARTPTS,volume=%.3f,afade=t=in:st=0:d=%.3f,afade=t=out:st=%.3f:d=%.3f[bgm_bed]",
			bgmIdx, activeDur, bgmVol, fi, fadeOutStart, fo,
		))
		if autoDuck && hasVoice {
			filters = append(filters, "[voice_raw]asplit=2[voice_mix][voice_sc]")
			filters = append(filters, "[bgm_bed][voice_sc]sidechaincompress=threshold=0.03:ratio=6:attack=120:release=350:makeup=1[bgm_final]")
			mixInputs = append(mixInputs, "[voice_mix]", "[bgm_final]")
		} else {
			mixInputs = append(mixInputs, "[voice_raw]", "[bgm_bed]")
		}
	} else {
		mixInputs = append(mixInputs, "[voice_raw]")
	}

	for i, s := range activeSFX {
		delayMs := int(math.Round(s.atSec * 1000))
		lbl := fmt.Sprintf("[psfx_%d]", i)
		filters = append(filters, fmt.Sprintf(
			"[%d:a]aformat=sample_fmts=fltp:sample_rates=48000:channel_layouts=stereo,volume=%.3f,adelay=%d|%d,apad=whole_dur=%.3f,atrim=0:%.3f,asetpts=PTS-STARTPTS%s",
			s.idx, s.vol, delayMs, delayMs, activeDur, activeDur, lbl,
		))
		mixInputs = append(mixInputs, lbl)
	}

	filters = append(filters, fmt.Sprintf(
		"%samix=inputs=%d:duration=first:dropout_transition=0:normalize=0,alimiter=limit=0.95:attack=5:release=50,asetpts=PTS-STARTPTS[main_a]",
		strings.Join(mixInputs, ""), len(mixInputs),
	))
	return filters
}

func hexToASSColor(hex string) string {
	h := strings.TrimPrefix(strings.TrimSpace(hex), "#")
	if len(h) != 6 {
		return "&H0000E6FF" // Viral Yellow default (ASS is BBGGRR!)
	}
	return fmt.Sprintf("&H00%s%s%s", strings.ToUpper(h[4:6]), strings.ToUpper(h[2:4]), strings.ToUpper(h[0:2]))
}

func generateMemeTextOverlaysASS(overlays []model.TextOverlay, pieces []timelinePiece, speed, maxDur float64, outW, outH int, destASS string) (int, error) {
	var b strings.Builder
	b.WriteString("[Script Info]\nScriptType: v4.00+\n")
	b.WriteString(fmt.Sprintf("PlayResX: %d\nPlayResY: %d\nScaledBorderAndShadow: yes\n\n", outW, outH))
	b.WriteString("[V4+ Styles]\nFormat: Name, Fontname, Fontsize, PrimaryColour, SecondaryColour, OutlineColour, BackColour, Bold, Italic, Underline, StrikeOut, ScaleX, ScaleY, Spacing, Angle, BorderStyle, Outline, Shadow, Alignment, MarginL, MarginR, MarginV, Encoding\n")
	b.WriteString("Style: Meme,Impact,68,&H0000E6FF,&H00FFFFFF,&H00000000,&H80000000,1,0,0,0,100,100,1,0,1,6,2,5,30,30,30,1\n\n")
	b.WriteString("[Events]\nFormat: Layer, Start, End, Style, Name, MarginL, MarginR, MarginV, Effect, Text\n")

	count := 0
	for _, ov := range overlays {
		txt := StripUnsupportedEmojis(strings.TrimSpace(ov.Text))
		if txt == "" {
			continue
		}
		st, ok1 := mapSourceTimeToOutput(ov.StartSec, pieces, speed)
		en, ok2 := mapSourceTimeToOutput(ov.EndSec, pieces, speed)
		if !ok1 && !ok2 {
			continue
		}
		if !ok1 {
			st = 0
		}
		if !ok2 || en <= st+0.2 {
			en = math.Min(maxDur, st+2.5)
		}
		if en > maxDur {
			en = maxDur
		}
		if en <= st+0.15 {
			continue
		}
		x := ov.X
		if x <= 0 || x >= 1 {
			x = 0.5
		}
		y := ov.Y
		if y <= 0 || y >= 1 {
			y = 0.25
		}
		fs := ov.FontSize
		if fs < 28 || fs > 140 {
			fs = 68
		}
		px := int(math.Round(x * float64(outW)))
		py := int(math.Round(y * float64(outH)))
		col := hexToASSColor(ov.Color)
		safeTxt := strings.ReplaceAll(sanitizeASSText(strings.ToUpper(txt)), "\n", "\\N")
		// Crisp CapCut pop-in animation right at exact (px, py)
		b.WriteString(fmt.Sprintf(
			"Dialogue: 2,%s,%s,Meme,,0,0,0,,{\\an5\\pos(%d,%d)\\fs%d\\c%s\\fscx78\\fscy78\\t(0,130,\\fscx106\\fscy106)\\t(130,230,\\fscx100\\fscy100)}%s\n",
			formatASSTime(st), formatASSTime(en), px, py, fs, col, safeTxt,
		))
		count++
	}
	if count == 0 {
		return 0, nil
	}
	if err := os.WriteFile(destASS, []byte(b.String()), 0o644); err != nil {
		return 0, err
	}
	return count, nil
}
