package renderer

import (
	"context"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"

	"klipforge/internal/model"
)

// PostEditInput bundles everything needed to trim and/or layer additional SFX & BGM
// directly onto an already-rendered local MP4 clip (zero YouTube re-download!).
type PostEditInput struct {
	FFmpegPath         string
	FFprobePath        string
	SourceMP4          string
	OutputPath         string
	WorkDir            string
	TrimStartSec       float64
	TrimEndSec         float64
	PreserveOutro      bool
	SourceHadOutro     bool
	Outro              *model.ClipAssetVideo
	SFXCues            []ResolvedSFX // Timestamps are on the SOURCE MP4 timeline (auto-shifted by TrimStartSec)
	BGMPath            string
	BGMVolume          float64
	BGMFadeInSec       float64
	BGMFadeOutSec      float64
	BGMAutoDuck        bool
	Speed              float64
	Watermark          *model.WatermarkConfig // Optional manual drag-and-drop watermark overlay (ONLY on main clip, NEVER Outro!)
	Encoder            string
	Preset             string
	CRF                int
	OnProgress         func(pct float64, speed string)
}

// RenderPostEdit performs fast local trimming and/or multi-SFX + BGM audio layering on a finished clip.
// When no trim/speed change is requested, it uses `-c:v copy` (bit-exact video, ~1-2 second render!)
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

	// Check if we can take the ULTRA-FAST `-c:v copy` path (no trim, no speed change, AND no new watermark!)
	noTrim := trimStart <= 0.05 && ((sourceHasProtectedTail && trimEnd >= bodyLimit-0.15) || (!sourceHasProtectedTail && trimEnd >= srcInfo.Duration-0.15))
	noSpeed := math.Abs(speed-1.0) <= 0.005

	if noTrim && noSpeed && !hasWM && !(!in.SourceHadOutro && hasOutroFile) {
		return renderPostEditStreamCopy(ctx, in, srcInfo, bodyLimit, validSFX, hasBGM)
	}

	// Full local trim + optional manual watermark + audio SFX/BGM mix + optional clean Outro re-attach
	mainDurRaw := trimEnd - trimStart
	effectiveMainDur := mainDurRaw / speed
	reattachOutro := hasOutroFile
	totalExpected := effectiveMainDur
	if reattachOutro {
		totalExpected += outroDur
	}

	var args []string
	args = append(args, "-y", "-hide_banner", "-loglevel", "error", "-progress", "pipe:1", "-nostats")
	// Input 0: Local source MP4 trimmed accurately
	args = append(args, "-ss", fmt.Sprintf("%.3f", trimStart), "-t", fmt.Sprintf("%.3f", mainDurRaw), "-i", in.SourceMP4)

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
	// Video stream of trimmed main clip
	if !noSpeed {
		filters = append(filters, fmt.Sprintf("[0:v]setpts=(PTS-STARTPTS)/%.4f,fps=30,setsar=1,format=yuv420p[main_v0]", speed))
	} else {
		filters = append(filters, "[0:v]setpts=PTS-STARTPTS,fps=30,setsar=1,format=yuv420p[main_v0]")
	}
	if hasWM {
		wmFilter, _ := buildWatermarkFilter("[main_v0]", wmIdx, outW, outH, *in.Watermark, "[main_v]")
		filters = append(filters, wmFilter)
	} else {
		filters = append(filters, "[main_v0]copy[main_v]")
	}

	// Audio stream of trimmed main clip
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

	// Mix optional BGM + additional SFX cues (shifted cleanly by trimStart & clamped strictly inside main body!)
	filters = append(filters, buildPostAudioMix(srcInfo.HasAudio, effectiveMainDur, trimStart, speed, hasBGM, bgmIdx, in.BGMVolume, in.BGMFadeInSec, in.BGMFadeOutSec, in.BGMAutoDuck, validSFX, sfxIndices)...)

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
	filters = append(filters, buildPostAudioMix(srcInfo.HasAudio, bodyLimit, 0, 1.0, hasBGM, bgmIdx, in.BGMVolume, in.BGMFadeInSec, in.BGMFadeOutSec, in.BGMAutoDuck, validSFX, sfxIndices)...)

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

func buildPostAudioMix(hasVoice bool, activeDur, trimStart, speed float64, hasBGM bool, bgmIdx int, bgmVol, bgmFadeIn, bgmFadeOut float64, autoDuck bool, validSFX []ResolvedSFX, sfxIndices []int) []string {
	var filters []string
	var activeSFX []struct {
		idx     int
		atSec   float64
		vol     float64
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
		if bgmFadeIn < 0 || bgmFadeIn > 5.0 {
			bgmFadeIn = 0.35
		}
		if bgmFadeOut <= 0 || bgmFadeOut > 5.0 {
			bgmFadeOut = 0.85
		}
		fadeInDur := math.Min(bgmFadeIn, activeDur*0.35)
		fadeOutDur := math.Min(bgmFadeOut, activeDur*0.35)
		fadeOutStart := math.Max(0, activeDur-fadeOutDur)
		filters = append(filters, fmt.Sprintf(
			"[%d:a]aformat=sample_fmts=fltp:sample_rates=48000:channel_layouts=stereo,atrim=0:%.3f,asetpts=PTS-STARTPTS,volume=%.3f,afade=t=in:st=0:d=%.3f,afade=t=out:st=%.3f:d=%.3f[bgm_bed]",
			bgmIdx, activeDur, bgmVol, fadeInDur, fadeOutStart, fadeOutDur,
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
