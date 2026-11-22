package renderer

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"klipforge/internal/model"
)

// MediaInfo holds probed metadata for a video/audio file.
type MediaInfo struct {
	Duration float64
	Width    int
	Height   int
	HasAudio bool
	HasVideo bool
}

// ResolvedSFX is an SFX cue resolved to an existing local audio file on disk.
type ResolvedSFX struct {
	FilePath string
	AtSec    float64
	Volume   float64
}

// ProbeMedia inspects a media file using ffprobe.
func ProbeMedia(ctx context.Context, ffprobePath, mediaPath string) (MediaInfo, error) {
	cmd := exec.CommandContext(ctx, ffprobePath,
		"-v", "quiet",
		"-print_format", "json",
		"-show_format",
		"-show_streams",
		mediaPath,
	)
	out, err := cmd.Output()
	if err != nil {
		return MediaInfo{}, fmt.Errorf("ffprobe gagal: %w", err)
	}

	var parsed struct {
		Format struct {
			Duration string `json:"duration"`
		} `json:"format"`
		Streams []struct {
			CodecType string `json:"codec_type"`
			Width     int    `json:"width"`
			Height    int    `json:"height"`
			Duration  string `json:"duration"`
		} `json:"streams"`
	}
	if err := json.Unmarshal(out, &parsed); err != nil {
		return MediaInfo{}, err
	}

	info := MediaInfo{}
	if d, err := strconv.ParseFloat(parsed.Format.Duration, 64); err == nil {
		info.Duration = d
	}
	for _, s := range parsed.Streams {
		switch s.CodecType {
		case "video":
			info.HasVideo = true
			if s.Width > 0 {
				info.Width = s.Width
				info.Height = s.Height
			}
			if info.Duration == 0 {
				if d, err := strconv.ParseFloat(s.Duration, 64); err == nil {
					info.Duration = d
				}
			}
		case "audio":
			info.HasAudio = true
		}
	}
	return info, nil
}

// RenderInput encapsulates everything needed to execute an FFmpeg clip render.
type RenderInput struct {
	FFmpegPath   string
	FFprobePath  string
	SourcePath   string  // Local MP4 (either full video or pre-downloaded section)
	SeekStart    float64 // > 0 when cutting directly from full local video; 0 when using section file
	ClipDuration float64 // Duration of the main clip segment
	OutputPath   string
	WorkDir      string // Temp directory for ASS / hook text files
	Layout       model.LayoutConfig
	Subtitles    model.SubtitleStyle
	SubtitleFile string // Generated .ass path (empty if none)
	HookText     string
	AudioNorm    bool
	VideoFX      model.VideoFXConfig
	BGMPath      string        // Optional BGM file path (looped & trimmed to main clip only)
	BGMVolume    float64       // 0.05 - 0.80
	BGMAutoDuck  bool          // ReaCut-style sidechaincompress ducking under voice
	SFXCues      []ResolvedSFX // Optional SFX triggers (mixed into main clip only, NEVER outro!)
	Watermark    *model.WatermarkConfig
	Intro        *model.ClipAssetVideo
	Outro        *model.ClipAssetVideo
	Encoder      string
	Preset       string
	CRF          int
	OnProgress   func(pct float64, speed string)
}

// ResolveOutputDimensions returns even width & height based on aspect ratio and resolution preset.
func ResolveOutputDimensions(layout model.LayoutConfig, srcW, srcH int) (int, int) {
	is720 := layout.Resolution == "720p"
	switch layout.AspectRatio {
	case "1:1":
		if is720 {
			return 720, 720
		}
		return 1080, 1080
	case "16:9":
		if is720 {
			return 1280, 720
		}
		return 1920, 1080
	case "original":
		if srcW > 0 && srcH > 0 {
			return makeEven(srcW), makeEven(srcH)
		}
		return 1920, 1080
	default: // 9:16
		if is720 {
			return 720, 1280
		}
		return 1080, 1920
	}
}

// EscapeFFmpegFilterPath escapes a file path for FFmpeg filter options (ass=, fontfile=, textfile=) on Windows/Unix.
func EscapeFFmpegFilterPath(p string) string {
	abs, err := filepath.Abs(p)
	if err != nil {
		abs = p
	}
	s := strings.ReplaceAll(abs, `\`, `/`)
	s = strings.ReplaceAll(s, `:`, `\:`)
	s = strings.ReplaceAll(s, `'`, `\'`)
	return s
}

// RenderClip executes the complete FFmpeg filtergraph:
// Layout/Crop -> Video FX (Color/Zoom/Jedag-Jedug/Speed/Fade) -> Hook Banner -> Viral ASS Subtitles -> Watermark
// + Audio Mix (Voice + Loudnorm + Auto-Ducked BGM + SFX Cues + Limiter)
// -> Clean Outro/Intro Concat (ZERO filters, ZERO BGM/SFX bleed on mandatory 3s outro!).
func RenderClip(ctx context.Context, in RenderInput) (MediaInfo, error) {
	srcInfo, err := ProbeMedia(ctx, in.FFprobePath, in.SourcePath)
	if err != nil {
		return MediaInfo{}, fmt.Errorf("gagal probe video sumber: %w", err)
	}
	outW, outH := ResolveOutputDimensions(in.Layout, srcInfo.Width, srcInfo.Height)

	speed := in.VideoFX.Speed
	if speed < 0.75 || speed > 1.50 {
		speed = 1.0
	}

	// Probe optional intro/outro so we know their audio tracks & durations
	var introInfo, outroInfo MediaInfo
	hasIntro := in.Intro != nil && in.Intro.Enabled && in.Intro.FilePath != "" && fileExists(in.Intro.FilePath)
	hasOutro := in.Outro != nil && in.Outro.Enabled && in.Outro.FilePath != "" && fileExists(in.Outro.FilePath)
	hasWM := in.Watermark != nil && in.Watermark.Enabled && in.Watermark.FilePath != "" && fileExists(in.Watermark.FilePath)
	hasBGM := in.BGMPath != "" && fileExists(in.BGMPath)

	var validSFX []ResolvedSFX
	for _, s := range in.SFXCues {
		if s.FilePath != "" && fileExists(s.FilePath) {
			validSFX = append(validSFX, s)
		}
	}

	if hasIntro {
		introInfo, err = ProbeMedia(ctx, in.FFprobePath, in.Intro.FilePath)
		if err != nil {
			return MediaInfo{}, fmt.Errorf("gagal probe intro video: %w", err)
		}
	}
	if hasOutro {
		outroInfo, err = ProbeMedia(ctx, in.FFprobePath, in.Outro.FilePath)
		if err != nil {
			return MediaInfo{}, fmt.Errorf("gagal probe outro video: %w", err)
		}
	}

	// Calculate effective main clip duration after speed adjustment
	rawMainDur := in.ClipDuration
	if rawMainDur <= 0 || (in.SeekStart == 0 && srcInfo.Duration > 0 && srcInfo.Duration < rawMainDur+2) {
		rawMainDur = srcInfo.Duration
	}
	if rawMainDur <= 0 {
		rawMainDur = 30
	}
	effectiveMainDur := rawMainDur / speed

	totalExpected := effectiveMainDur
	if hasIntro {
		totalExpected += introInfo.Duration
	}
	if hasOutro {
		totalExpected += outroInfo.Duration
	}

	// Build FFmpeg input list
	var args []string
	args = append(args, "-y", "-hide_banner", "-loglevel", "error", "-progress", "pipe:1", "-nostats")

	// Input 0: Main source clip
	if in.SeekStart > 0 {
		args = append(args, "-ss", fmt.Sprintf("%.3f", in.SeekStart))
	}
	if in.ClipDuration > 0 && in.SeekStart > 0 {
		args = append(args, "-t", fmt.Sprintf("%.3f", in.ClipDuration))
	}
	args = append(args, "-i", in.SourcePath)

	nextInputIdx := 1
	wmIdx := -1
	introIdx := -1
	outroIdx := -1
	bgmIdx := -1
	sfxIndices := make([]int, len(validSFX))

	if hasWM {
		wmIdx = nextInputIdx
		nextInputIdx++
		if strings.EqualFold(filepath.Ext(in.Watermark.FilePath), ".gif") {
			args = append(args, "-ignore_loop", "0")
		}
		args = append(args, "-i", in.Watermark.FilePath)
	}
	if hasIntro {
		introIdx = nextInputIdx
		nextInputIdx++
		args = append(args, "-i", in.Intro.FilePath)
	}
	if hasOutro {
		outroIdx = nextInputIdx
		nextInputIdx++
		args = append(args, "-i", in.Outro.FilePath)
	}
	if hasBGM {
		bgmIdx = nextInputIdx
		nextInputIdx++
		args = append(args, "-stream_loop", "-1", "-i", in.BGMPath)
	}
	for i, sfx := range validSFX {
		sfxIndices[i] = nextInputIdx
		nextInputIdx++
		args = append(args, "-i", sfx.FilePath)
	}

	// ================= VIDEO FILTERGRAPH (Main Clip Only) =================
	var filters []string

	// 1. Frame/Crop main video [0:v] -> [main_v0]
	filters = append(filters, buildLayoutFilter(in.Layout, outW, outH, "[0:v]", "[main_v0]"))
	curV := "[main_v0]"

	// 2. Speed adjustment + normalize timestamps BEFORE time-based FX
	if math.Abs(speed-1.0) > 0.005 {
		filters = append(filters, fmt.Sprintf("%ssetpts=(PTS-STARTPTS)/%.4f,fps=30[main_vspd]", curV, speed))
	} else {
		filters = append(filters, fmt.Sprintf("%ssetpts=PTS-STARTPTS,fps=30[main_vspd]", curV))
	}
	curV = "[main_vspd]"

	// 3. Video FX: Color Grade, Sharpen, Dynamic Motion (Slow Zoom / Peak Punch / Jedag-Jedug), Fade-In
	if fxFilter, outLabel := buildVideoFXFilter(curV, outW, outH, effectiveMainDur, speed, in.VideoFX); fxFilter != "" {
		filters = append(filters, fxFilter)
		curV = outLabel
	}

	// 4. Optional ASS Viral Subtitles + Animated Hook Banner (ONLY on main clip, zero emoji tofu boxes!)
	if in.SubtitleFile != "" && fileExists(in.SubtitleFile) {
		filters = append(filters, fmt.Sprintf(
			"%sass='%s'[main_vsub]",
			curV, EscapeFFmpegFilterPath(in.SubtitleFile),
		))
		curV = "[main_vsub]"
	} else if hookText := StripUnsupportedEmojis(strings.TrimSpace(in.HookText)); hookText != "" {
		// Fallback drawtext hook banner (first 4.8s only, emoji-free!)
		hookFile := filepath.Join(in.WorkDir, "hook.txt")
		wrappedHook := wrapText(strings.ToUpper(hookText), 22)
		if err := os.WriteFile(hookFile, []byte(wrappedHook), 0o644); err == nil {
			fontPath := findBoldFont()
			fontOpt := ""
			if fontPath != "" {
				fontOpt = fmt.Sprintf("fontfile='%s':", EscapeFFmpegFilterPath(fontPath))
			}
			fontSize := int(float64(outW) * 0.054)
			if fontSize < 28 {
				fontSize = 28
			}
			yPos := int(float64(outH) * 0.085)
			filters = append(filters, fmt.Sprintf(
				"%sdrawtext=%stextfile='%s':fontcolor=#FFE600:fontsize=%d:borderw=6:bordercolor=black:x=(w-text_w)/2:y=%d:line_spacing=8:enable='between(t,0,4.8)'[main_vhook]",
				curV, fontOpt, EscapeFFmpegFilterPath(hookFile), fontSize, yPos,
			))
			curV = "[main_vhook]"
		}
	}

	// 6. Watermark overlay on main clip (default: NOT on outro so 3s MotionKlip template stays pure!)
	applyWMOnMain := hasWM && !in.Watermark.OnOutro
	if applyWMOnMain {
		wmFilter, outLabel := buildWatermarkFilter(curV, wmIdx, outW, outH, *in.Watermark, "[main_vwm]")
		filters = append(filters, wmFilter)
		curV = outLabel
	}

	// Finalize main video stream for rock-solid concat
	filters = append(filters, fmt.Sprintf("%sfps=30,setsar=1,format=yuv420p,setpts=PTS-STARTPTS[main_v]", curV))

	// ================= AUDIO FILTERGRAPH (Voice + Auto-Ducked BGM + SFX) =================
	// Main voice track
	if srcInfo.HasAudio {
		voiceChain := "[0:a]aformat=sample_fmts=fltp:sample_rates=48000:channel_layouts=stereo"
		if math.Abs(speed-1.0) > 0.005 {
			voiceChain += fmt.Sprintf(",atempo=%.4f", speed)
		}
		if in.AudioNorm {
			voiceChain += ",loudnorm=I=-14:TP=-1.5:LRA=11"
		}
		voiceChain += fmt.Sprintf(",atrim=0:%.3f,asetpts=PTS-STARTPTS[voice_raw]", effectiveMainDur)
		filters = append(filters, voiceChain)
	} else {
		filters = append(filters, fmt.Sprintf("anullsrc=r=48000:cl=stereo:d=%.3f[voice_raw]", effectiveMainDur))
	}

	if !hasBGM && len(validSFX) == 0 {
		filters = append(filters, "[voice_raw]acopy[main_a]")
	} else {
		var mixInputs []string

		if hasBGM {
			bgmVol := in.BGMVolume
			if bgmVol <= 0.01 || bgmVol > 1.0 {
				bgmVol = 0.18
			}
			fadeOutStart := math.Max(0, effectiveMainDur-0.85)
			fadeOutDur := math.Min(0.85, effectiveMainDur*0.3)
			filters = append(filters, fmt.Sprintf(
				"[%d:a]aformat=sample_fmts=fltp:sample_rates=48000:channel_layouts=stereo,atrim=0:%.3f,asetpts=PTS-STARTPTS,volume=%.3f,afade=t=in:st=0:d=0.4,afade=t=out:st=%.3f:d=%.3f[bgm_bed]",
				bgmIdx, effectiveMainDur, bgmVol, fadeOutStart, fadeOutDur,
			))

			if in.BGMAutoDuck && srcInfo.HasAudio {
				// ReaCut-inspired automatic music ducking when streamer talks!
				filters = append(filters, "[voice_raw]asplit=2[voice_mix][voice_sc]")
				filters = append(filters, "[bgm_bed][voice_sc]sidechaincompress=threshold=0.03:ratio=6:attack=120:release=350:makeup=1[bgm_final]")
				mixInputs = append(mixInputs, "[voice_mix]", "[bgm_final]")
			} else {
				mixInputs = append(mixInputs, "[voice_raw]", "[bgm_bed]")
			}
		} else {
			mixInputs = append(mixInputs, "[voice_raw]")
		}

		// Mix each timed SFX cue (trimmed strictly to main clip duration so ZERO SFX leaks into outro!)
		for i, sfx := range validSFX {
			sfxVol := sfx.Volume
			if sfxVol <= 0.05 || sfxVol > 2.0 {
				sfxVol = 0.85
			}
			atSec := sfx.AtSec / speed
			if atSec < 0 {
				atSec = 0
			}
			if atSec > effectiveMainDur-0.2 {
				atSec = math.Max(0, effectiveMainDur-0.5)
			}
			delayMs := int(math.Round(atSec * 1000))
			lbl := fmt.Sprintf("[sfx_%d]", i)
			filters = append(filters, fmt.Sprintf(
				"[%d:a]aformat=sample_fmts=fltp:sample_rates=48000:channel_layouts=stereo,volume=%.3f,adelay=%d|%d,apad=whole_dur=%.3f,atrim=0:%.3f,asetpts=PTS-STARTPTS%s",
				sfxIndices[i], sfxVol, delayMs, delayMs, effectiveMainDur, effectiveMainDur, lbl,
			))
			mixInputs = append(mixInputs, lbl)
		}

		filters = append(filters, fmt.Sprintf(
			"%samix=inputs=%d:duration=first:dropout_transition=0:normalize=0,alimiter=limit=0.95:attack=5:release=50,asetpts=PTS-STARTPTS[main_a]",
			strings.Join(mixInputs, ""), len(mixInputs),
		))
	}

	// ================= INTRO / OUTRO CONCAT (100% PURE, ZERO FILTERS ON OUTRO!) =================
	var concatInputs []string
	if hasIntro {
		filters = append(filters, fmt.Sprintf(
			"[%d:v]scale=%d:%d:force_original_aspect_ratio=decrease,pad=%d:%d:(ow-iw)/2:(oh-ih)/2:black,fps=30,setsar=1,format=yuv420p,setpts=PTS-STARTPTS[intro_v]",
			introIdx, outW, outH, outW, outH,
		))
		if introInfo.HasAudio {
			filters = append(filters, fmt.Sprintf("[%d:a]aformat=sample_fmts=fltp:sample_rates=48000:channel_layouts=stereo,asetpts=PTS-STARTPTS[intro_a]", introIdx))
		} else {
			d := math.Max(0.5, introInfo.Duration)
			filters = append(filters, fmt.Sprintf("anullsrc=r=48000:cl=stereo:d=%.3f[intro_a]", d))
		}
		concatInputs = append(concatInputs, "[intro_v][intro_a]")
	}

	concatInputs = append(concatInputs, "[main_v][main_a]")

	if hasOutro {
		// Mandatory Outro Template: ZERO color filter, ZERO zoom, ZERO subtitle, ZERO BGM/SFX bleed!
		filters = append(filters, fmt.Sprintf(
			"[%d:v]scale=%d:%d:force_original_aspect_ratio=decrease,pad=%d:%d:(ow-iw)/2:(oh-ih)/2:black,fps=30,setsar=1,format=yuv420p,setpts=PTS-STARTPTS[outro_v]",
			outroIdx, outW, outH, outW, outH,
		))
		if outroInfo.HasAudio {
			filters = append(filters, fmt.Sprintf("[%d:a]aformat=sample_fmts=fltp:sample_rates=48000:channel_layouts=stereo,asetpts=PTS-STARTPTS[outro_a]", outroIdx))
		} else {
			d := math.Max(0.5, outroInfo.Duration)
			filters = append(filters, fmt.Sprintf("anullsrc=r=48000:cl=stereo:d=%.3f[outro_a]", d))
		}
		concatInputs = append(concatInputs, "[outro_v][outro_a]")
	}

	finalV := "[main_v]"
	finalA := "[main_a]"
	if len(concatInputs) > 1 {
		filters = append(filters, fmt.Sprintf(
			"%sconcat=n=%d:v=1:a=1[cat_v][cat_a]",
			strings.Join(concatInputs, ""), len(concatInputs),
		))
		finalV = "[cat_v]"
		finalA = "[cat_a]"
	}

	if hasWM && in.Watermark.OnOutro {
		wmFilter, outLabel := buildWatermarkFilter(finalV, wmIdx, outW, outH, *in.Watermark, "[final_vwm]")
		filters = append(filters, wmFilter)
		finalV = outLabel
	}

	filterGraph := strings.Join(filters, ";")

	encoder := in.Encoder
	if encoder == "" || encoder == "auto" {
		encoder = "libx264"
	}
	err = runFFmpegEncode(ctx, in.FFmpegPath, args, filterGraph, finalV, finalA, encoder, in.Preset, in.CRF, in.OutputPath, totalExpected, in.OnProgress)
	if err != nil && encoder != "libx264" {
		err = runFFmpegEncode(ctx, in.FFmpegPath, args, filterGraph, finalV, finalA, "libx264", in.Preset, in.CRF, in.OutputPath, totalExpected, in.OnProgress)
	}
	if err != nil {
		return MediaInfo{}, err
	}

	return ProbeMedia(ctx, in.FFprobePath, in.OutputPath)
}

func buildVideoFXFilter(inLabel string, outW, outH int, dur, speed float64, fx model.VideoFXConfig) (string, string) {
	var chain []string

	// 1. Color grading look (ReaCut / FilmCraft-inspired)
	switch fx.ColorPreset {
	case "vivid_gaming":
		chain = append(chain, "eq=contrast=1.08:saturation=1.24:brightness=0.02")
	case "cinematic":
		chain = append(chain, "eq=contrast=1.12:saturation=0.92:gamma=0.96", "vignette=PI/5")
	case "meme_deep":
		chain = append(chain, "eq=contrast=1.20:saturation=1.35:brightness=0.01")
	}

	if fx.Sharpen {
		chain = append(chain, "unsharp=5:5:0.65:5:5:0.0")
	}

	// 2. Dynamic viral motion FX
	peakT := fx.PeakOffsetSec / speed
	if peakT <= 0.2 || peakT >= dur-0.5 {
		peakT = dur * 0.38
	}

	switch fx.MotionPreset {
	case "slow_zoom":
		// Smooth continuous 1.0x -> 1.08x retention zoom (quoted expressions so commas stay inside filter)
		safeDur := math.Max(1.0, dur)
		chain = append(chain, fmt.Sprintf(
			"scale=w='trunc(%d*(1+0.08*min(t/%.2f,1))/2)*2':h='trunc(%d*(1+0.08*min(t/%.2f,1))/2)*2':eval=frame,"+
				"crop=w=%d:h=%d:x='(iw-%d)/2':y='(ih-%d)/2'",
			outW, safeDur, outH, safeDur, outW, outH, outW, outH,
		))
	case "peak_punch":
		// Viral punch zoom + micro camera shake right at the detected hype peak!
		pEnd := peakT + 0.50
		shakeEnd := peakT + 0.35
		chain = append(chain, fmt.Sprintf(
			"scale=w='trunc(%d*(1+0.13*between(t,%.2f,%.2f))/2)*2':h='trunc(%d*(1+0.13*between(t,%.2f,%.2f))/2)*2':eval=frame,"+
				"crop=w=%d:h=%d:x='(iw-%d)/2+10*sin(t*75)*between(t,%.2f,%.2f)':y='(ih-%d)/2+8*cos(t*65)*between(t,%.2f,%.2f)'",
			outW, peakT, pEnd, outH, peakT, pEnd,
			outW, outH, outW, peakT, shakeEnd, outH, peakT, shakeEnd,
		))
	case "jedag_jedug":
		// Rhythmic 128 BPM punch zoom pulse (great for meme/epic hype clips!)
		chain = append(chain, fmt.Sprintf(
			"scale=w='trunc(%d*(1+0.08*pow(max(0,1-mod(t,0.46875)*5),2))/2)*2':h='trunc(%d*(1+0.08*pow(max(0,1-mod(t,0.46875)*5),2))/2)*2':eval=frame,"+
				"crop=w=%d:h=%d:x='(iw-%d)/2':y='(ih-%d)/2'",
			outW, outH, outW, outH, outW, outH,
		))
	}

	// 3. Smooth entry fade-in
	if fx.FadeIn {
		chain = append(chain, "fade=t=in:st=0:d=0.25")
	}

	if len(chain) == 0 {
		return "", inLabel
	}
	return fmt.Sprintf("%s%s[main_vfx]", inLabel, strings.Join(chain, ",")), "[main_vfx]"
}

func runFFmpegEncode(
	ctx context.Context,
	ffmpegPath string,
	baseArgs []string,
	filterGraph, finalV, finalA string,
	encoder, preset string,
	crf int,
	outputPath string,
	expectedDur float64,
	onProgress func(pct float64, speed string),
) error {
	args := append([]string{}, baseArgs...)
	args = append(args,
		"-filter_complex", filterGraph,
		"-map", finalV,
		"-map", finalA,
	)

	if preset == "" {
		preset = "veryfast"
	}
	if crf <= 0 {
		crf = 20
	}

	switch encoder {
	case "h264_nvenc":
		args = append(args, "-c:v", "h264_nvenc", "-preset", "p4", "-cq", strconv.Itoa(crf))
	case "h264_qsv":
		args = append(args, "-c:v", "h264_qsv", "-global_quality", strconv.Itoa(crf))
	case "h264_amf":
		args = append(args, "-c:v", "h264_amf", "-quality", "balanced")
	default:
		args = append(args, "-c:v", "libx264", "-preset", preset, "-crf", strconv.Itoa(crf))
	}

	args = append(args,
		"-pix_fmt", "yuv420p",
		"-c:a", "aac", "-b:a", "192k", "-ar", "48000", "-ac", "2",
		"-movflags", "+faststart",
		outputPath,
	)

	cmd := exec.CommandContext(ctx, ffmpegPath, args...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Start(); err != nil {
		return err
	}

	scanner := bufio.NewScanner(stdout)
	var curSpeed string
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if strings.HasPrefix(line, "speed=") {
			curSpeed = strings.TrimPrefix(line, "speed=")
		} else if strings.HasPrefix(line, "out_time_us=") {
			usStr := strings.TrimPrefix(line, "out_time_us=")
			if us, err := strconv.ParseFloat(usStr, 64); err == nil && expectedDur > 0 {
				sec := us / 1_000_000.0
				pct := (sec / expectedDur) * 100.0
				if pct > 99 {
					pct = 99
				}
				if pct < 0 {
					pct = 0
				}
				if onProgress != nil {
					onProgress(pct, curSpeed)
				}
			}
		}
	}

	if err := cmd.Wait(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return fmt.Errorf("ffmpeg render gagal: %s", msg)
	}
	if onProgress != nil {
		onProgress(100, curSpeed)
	}
	return nil
}

func buildLayoutFilter(layout model.LayoutConfig, outW, outH int, inLabel, outLabel string) string {
	mode := layout.CropMode
	if layout.AspectRatio == "original" || mode == "original" {
		return fmt.Sprintf("%sscale=%d:%d:force_original_aspect_ratio=decrease,pad=%d:%d:(ow-iw)/2:(oh-ih)/2:black%s",
			inLabel, outW, outH, outW, outH, outLabel)
	}

	switch mode {
	case "gaming_cinema":
		// PRO GAMING CINEMA: Blurred full-bleed background + Full crisp uncropped 16:9 Gameplay
		// + Cleanly framed Streamer Facecam below! Zero left/right HUD cutoff & super clean!
		_, fcBox := resolveSplitBoxes(layout)
		gpW := outW
		gpH := makeEven(int(float64(outW) * 9.0 / 16.0))
		gpY := int(float64(outH) * 0.16)
		fcW := makeEven(int(float64(outW) * 0.56))
		fcH := makeEven(int(float64(outH) * 0.27))
		fcX := (outW - fcW) / 2
		fcY := gpY + gpH + int(float64(outH)*0.03)
		if fcY+fcH > outH-int(float64(outH)*0.16) {
			// Leave safe room at bottom for MotionKlip official watermark!
			fcY = outH - fcH - int(float64(outH)*0.16)
		}
		return fmt.Sprintf(
			"%ssplit=3[bg_src][gp_src][fc_src];"+
				"[bg_src]scale=%d:%d:force_original_aspect_ratio=increase,crop=%d:%d,boxblur=28:6,eq=brightness=-0.14:saturation=1.1[bg];"+
				"[gp_src]scale=%d:%d:force_original_aspect_ratio=decrease,pad=%d:%d:(ow-iw)/2:(oh-ih)/2:black[gp];"+
				"[fc_src]crop=iw*%.4f:ih*%.4f:iw*%.4f:ih*%.4f,scale=%d:%d:force_original_aspect_ratio=increase,crop=%d:%d,drawbox=x=0:y=0:w=iw:h=ih:color=white@0.65:t=5[fc];"+
				"[bg][gp]overlay=0:%d[tmp1];"+
				"[tmp1][fc]overlay=%d:%d%s",
			inLabel,
			outW, outH, outW, outH,
			gpW, gpH, gpW, gpH,
			fcBox.W, fcBox.H, fcBox.X, fcBox.Y, fcW, fcH, fcW, fcH,
			gpY, fcX, fcY, outLabel,
		)

	case "blur_bg":
		return fmt.Sprintf(
			"%ssplit=2[bg_src][fg_src];"+
				"[bg_src]scale=%d:%d:force_original_aspect_ratio=increase,crop=%d:%d,boxblur=26:6[bg];"+
				"[fg_src]scale=%d:-2:force_original_aspect_ratio=decrease[fg];"+
				"[bg][fg]overlay=(W-w)/2:(H-h)/2%s",
			inLabel, outW, outH, outW, outH, outW, outLabel,
		)

	case "split_custom", "split_right", "split_left", "split_top_right", "split_top_left":
		ratio := layout.SplitRatio
		if ratio < 0.35 || ratio > 0.80 {
			ratio = 0.64
		}
		topH := makeEven(int(float64(outH) * ratio))
		botH := outH - topH
		if botH < 100 {
			botH = 100
			topH = outH - botH
		}

		gpBox, fcBox := resolveSplitBoxes(layout)
		return fmt.Sprintf(
			"%ssplit=2[gp_in][fc_in];"+
				"[gp_in]crop=iw*%.4f:ih*%.4f:iw*%.4f:ih*%.4f,scale=%d:%d:force_original_aspect_ratio=increase,crop=%d:%d[gp_out];"+
				"[fc_in]crop=iw*%.4f:ih*%.4f:iw*%.4f:ih*%.4f,scale=%d:%d:force_original_aspect_ratio=increase,crop=%d:%d[fc_out];"+
				"[gp_out][fc_out]vstack=inputs=2,drawbox=x=0:y=%d:w=%d:h=5:color=#FFE600@0.80:t=fill%s",
			inLabel,
			gpBox.W, gpBox.H, gpBox.X, gpBox.Y, outW, topH, outW, topH,
			fcBox.W, fcBox.H, fcBox.X, fcBox.Y, outW, botH, outW, botH,
			topH-2, outW, outLabel,
		)

	default: // center crop
		return fmt.Sprintf(
			"%sscale=%d:%d:force_original_aspect_ratio=increase,crop=%d:%d%s",
			inLabel, outW, outH, outW, outH, outLabel,
		)
	}
}

func resolveSplitBoxes(layout model.LayoutConfig) (gp model.CropBox, fc model.CropBox) {
	switch layout.CropMode {
	case "split_left":
		return model.CropBox{X: 0.14, Y: 0.0, W: 0.72, H: 1.0},
			model.CropBox{X: 0.0, Y: 0.60, W: 0.30, H: 0.40}
	case "split_right":
		return model.CropBox{X: 0.14, Y: 0.0, W: 0.72, H: 1.0},
			model.CropBox{X: 0.70, Y: 0.60, W: 0.30, H: 0.40}
	case "split_top_left":
		return model.CropBox{X: 0.14, Y: 0.0, W: 0.72, H: 1.0},
			model.CropBox{X: 0.0, Y: 0.0, W: 0.30, H: 0.40}
	case "split_top_right":
		return model.CropBox{X: 0.14, Y: 0.0, W: 0.72, H: 1.0},
			model.CropBox{X: 0.70, Y: 0.0, W: 0.30, H: 0.40}
	default: // split_custom
		gp = sanitizeBox(layout.GameplayBox, model.CropBox{X: 0.14, Y: 0.0, W: 0.72, H: 1.0})
		fc = sanitizeBox(layout.FacecamBox, model.CropBox{X: 0.70, Y: 0.60, W: 0.30, H: 0.40})
		return gp, fc
	}
}

func sanitizeBox(b, def model.CropBox) model.CropBox {
	if b.W <= 0.05 || b.H <= 0.05 {
		return def
	}
	if b.X < 0 {
		b.X = 0
	}
	if b.Y < 0 {
		b.Y = 0
	}
	if b.X+b.W > 1.0 {
		b.W = 1.0 - b.X
	}
	if b.Y+b.H > 1.0 {
		b.H = 1.0 - b.Y
	}
	return b
}

func buildWatermarkFilter(baseLabel string, wmIdx, outW, outH int, wm model.WatermarkConfig, outLabel string) (string, string) {
	opacity := wm.Opacity
	if opacity <= 0 || opacity > 1.0 {
		opacity = 0.96
	}
	extra := ""
	if strings.EqualFold(filepath.Ext(wm.FilePath), ".gif") || strings.EqualFold(filepath.Ext(wm.FilePath), ".mov") || strings.EqualFold(filepath.Ext(wm.FilePath), ".webm") {
		extra = ":shortest=1"
	}

	// FULL-FRAME 9:16 TEMPLATE MODE (e.g. official MotionKlip 1080x1920 transparent overlay PNG!)
	// Overlays 1:1 across the entire output frame at exact official position & size!
	if wm.Position == "full-frame" {
		filter := fmt.Sprintf(
			"[%d:v]scale=%d:%d:force_original_aspect_ratio=decrease,pad=%d:%d:(ow-iw)/2:(oh-ih)/2:color=black@0,format=rgba,colorchannelmixer=aa=%.2f[wm_scaled];%s[wm_scaled]overlay=0:0%s%s",
			wmIdx, outW, outH, outW, outH, opacity, baseLabel, extra, outLabel,
		)
		return filter, outLabel
	}

	scaleRatio := wm.Scale
	if scaleRatio < 0.05 || scaleRatio > 1.0 {
		scaleRatio = 0.20
	}
	wmW := makeEven(int(float64(outW) * scaleRatio))
	if wmW < 32 {
		wmW = 32
	}
	margin := wm.MarginPx
	if margin < 0 {
		margin = 24
	}

	xExpr := fmt.Sprintf("W-w-%d", margin)
	yExpr := fmt.Sprintf("%d", margin)

	switch wm.Position {
	case "top-left":
		xExpr = fmt.Sprintf("%d", margin)
		yExpr = fmt.Sprintf("%d", margin)
	case "top-right":
		xExpr = fmt.Sprintf("W-w-%d", margin)
		yExpr = fmt.Sprintf("%d", margin)
	case "bottom-left":
		xExpr = fmt.Sprintf("%d", margin)
		yExpr = fmt.Sprintf("H-h-%d", margin)
	case "bottom-right":
		xExpr = fmt.Sprintf("W-w-%d", margin)
		yExpr = fmt.Sprintf("H-h-%d", margin)
	case "center-top":
		xExpr = "(W-w)/2"
		yExpr = fmt.Sprintf("%d", margin)
	case "custom":
		cx := wm.CustomX
		cy := wm.CustomY
		if cx < 0 {
			cx = 0
		}
		if cx > 0.95 {
			cx = 0.95
		}
		if cy < 0 {
			cy = 0
		}
		if cy > 0.95 {
			cy = 0.95
		}
		xExpr = fmt.Sprintf("W*%.4f", cx)
		yExpr = fmt.Sprintf("H*%.4f", cy)
	}

	filter := fmt.Sprintf(
		"[%d:v]scale=%d:-1,format=rgba,colorchannelmixer=aa=%.2f[wm_scaled];%s[wm_scaled]overlay=%s:%s%s%s",
		wmIdx, wmW, opacity, baseLabel, xExpr, yExpr, extra, outLabel,
	)
	return filter, outLabel
}

func makeEven(n int) int {
	if n < 2 {
		return 2
	}
	if n%2 != 0 {
		return n - 1
	}
	return n
}

func fileExists(p string) bool {
	if p == "" {
		return false
	}
	fi, err := os.Stat(p)
	return err == nil && !fi.IsDir()
}

func findBoldFont() string {
	if runtime.GOOS == "windows" {
		candidates := []string{
			`C:\Windows\Fonts\impact.ttf`,
			`C:\Windows\Fonts\arialbd.ttf`,
			`C:\Windows\Fonts\bahnschrift.ttf`,
			`C:\Windows\Fonts\calibrib.ttf`,
			`C:\Windows\Fonts\arial.ttf`,
		}
		for _, p := range candidates {
			if fileExists(p) {
				return p
			}
		}
	}
	return ""
}

func wrapText(s string, maxChars int) string {
	words := strings.Fields(s)
	if len(words) == 0 {
		return s
	}
	var lines []string
	var cur []string
	curLen := 0
	for _, w := range words {
		if curLen+len(w)+1 > maxChars && len(cur) > 0 {
			lines = append(lines, strings.Join(cur, " "))
			cur = []string{w}
			curLen = len(w)
		} else {
			cur = append(cur, w)
			curLen += len(w) + 1
		}
	}
	if len(cur) > 0 {
		lines = append(lines, strings.Join(cur, " "))
	}
	return strings.Join(lines, "\n")
}
