package renderer

import (
	"context"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"klipforge/internal/model"
)

// ResolvedCompileSegment holds a downloaded/local clip segment ready for compilation rendering.
type ResolvedCompileSegment struct {
	SourcePath   string
	SeekStart    float64
	ClipDuration float64
	RankNumber   int    // e.g. 5, 4, 3, 2, 1 (countdown order)
	Label        string // e.g. "ngetrail", "el alasan", "paling jago"
	Cues         []model.TranscriptCue
	OrigStart    float64
	OrigEnd      float64
	EndSFXPath   string
	EndSFXOffset float64
}

// CompileInput bundles everything needed to render an Auto Top-N Countdown or Rapid Compilation MP4.
type CompileInput struct {
	FFmpegPath     string
	FFprobePath    string
	OutputPath     string
	WorkDir        string
	Title          string // e.g. "Top 5 Windah Basudara Nyetir"
	Mode           string // "top_countdown" or "rapid_montage"
	Segments       []ResolvedCompileSegment
	AllRankLabels  map[int]string // Maps rank 1..N -> label for progressive countdown display
	Layout         model.LayoutConfig
	Subtitles      model.SubtitleStyle
	AudioNorm      bool
	VideoFX        model.VideoFXConfig
	BumperDuration float64 // e.g. 0.85s black title bumper between countdown clips
	BumperSFXPath  string  // e.g. sfx-boom
	Watermark      *model.WatermarkConfig
	Outro          *model.ClipAssetVideo
	Encoder        string
	Preset         string
	CRF            int
	OnProgress     func(pct float64, stage string)
}

// RenderCompilation renders each segment with progressive Top-N Countdown / Montage overlays,
// generates crisp black Vine-Boom bumper transitions between segments, concatenates everything seamlessly,
// and appends the mandatory clean MotionKlip Outro at the very end!
func RenderCompilation(ctx context.Context, in CompileInput) (MediaInfo, error) {
	if len(in.Segments) == 0 {
		return MediaInfo{}, fmt.Errorf("minimal 1 segmen diperlukan untuk kompilasi")
	}
	_ = os.MkdirAll(in.WorkDir, 0o755)

	outW, outH := ResolveOutputDimensions(in.Layout, 1920, 1080)
	hasOutro := in.Outro != nil && in.Outro.Enabled && in.Outro.FilePath != "" && fileExists(in.Outro.FilePath)
	hasWM := in.Watermark != nil && in.Watermark.Enabled && in.Watermark.FilePath != "" && fileExists(in.Watermark.FilePath)
	totalRanks := len(in.Segments)
	isCountdown := in.Mode != "rapid_montage"

	bumperDur := in.BumperDuration
	if bumperDur <= 0 {
		bumperDur = 0.85
	}

	// Pre-render reusable Black Bumper Title Card (for Top-N Countdown transitions!)
	var bumperFile string
	if isCountdown && totalRanks > 1 {
		if in.OnProgress != nil {
			in.OnProgress(10, "Membuat transisi bumper Top-N + Vine Boom...")
		}
		bumperFile = filepath.Join(in.WorkDir, "bumper.mp4")
		if err := renderBlackBumper(ctx, in, outW, outH, bumperDur, hasWM, bumperFile); err != nil {
			return MediaInfo{}, fmt.Errorf("gagal bikin bumper transisi: %w", err)
		}
	}

	var piecePaths []string
	revealedRanks := make(map[int]bool)

	for i, seg := range in.Segments {
		if ctx.Err() != nil {
			return MediaInfo{}, ctx.Err()
		}
		revealedRanks[seg.RankNumber] = true

		segPctBase := 15.0 + (float64(i)/float64(totalRanks))*68.0
		if in.OnProgress != nil {
			in.OnProgress(segPctBase, fmt.Sprintf("Rendering klip %d/%d (#%d %s)...", i+1, totalRanks, seg.RankNumber, seg.Label))
		}

		// Build ASS overlay for this segment (Progressive Top-N List OR Montage Theme + optional Karaoke Subtitles)
		segASS := filepath.Join(in.WorkDir, fmt.Sprintf("seg_%d.ass", i))
		err := generateCompilationSegmentASS(in.Title, in.Mode, totalRanks, seg, in.AllRankLabels, revealedRanks, outW, outH, in.Layout.CropMode, in.Subtitles, in.VideoFX.Speed, segASS)
		if err != nil {
			return MediaInfo{}, err
		}

		var sfxCues []ResolvedSFX
		if seg.EndSFXPath != "" && fileExists(seg.EndSFXPath) {
			off := seg.EndSFXOffset
			if off <= 0 {
				off = 1.1
			}
			at := math.Max(0.2, seg.ClipDuration-off)
			sfxCues = append(sfxCues, ResolvedSFX{
				FilePath: seg.EndSFXPath,
				AtSec:    at,
				Volume:   1.05,
			})
		}

		segOut := filepath.Join(in.WorkDir, fmt.Sprintf("seg_%d.mp4", i))
		segWork := filepath.Join(in.WorkDir, fmt.Sprintf("w_%d", i))
		_ = os.MkdirAll(segWork, 0o755)

		vfx := in.VideoFX
		// Avoid awkward fade-in mid-compilation
		vfx.FadeIn = (i == 0 && vfx.FadeIn)

		_, err = RenderClip(ctx, RenderInput{
			FFmpegPath:   in.FFmpegPath,
			FFprobePath:  in.FFprobePath,
			SourcePath:   seg.SourcePath,
			SeekStart:    seg.SeekStart,
			ClipDuration: seg.ClipDuration,
			OutputPath:   segOut,
			WorkDir:      segWork,
			Layout:       in.Layout,
			Subtitles:    in.Subtitles,
			SubtitleFile: segASS,
			HookText:     "", // Handled cleanly inside segASS
			AudioNorm:    in.AudioNorm,
			VideoFX:      vfx,
			SFXCues:      sfxCues,
			Watermark:    in.Watermark,
			Outro:        nil, // Outro appended ONCE cleanly at the very end!
			Encoder:      in.Encoder,
			Preset:       "veryfast",
			CRF:          19,
		})
		if err != nil {
			return MediaInfo{}, fmt.Errorf("gagal render segmen #%d: %w", seg.RankNumber, err)
		}

		piecePaths = append(piecePaths, segOut)
		// Add black bumper transition between segments (and optionally right before Outro if countdown)
		if bumperFile != "" && i < totalRanks-1 {
			piecePaths = append(piecePaths, bumperFile)
		}
	}

	// Render normalized CLEAN Outro once (ZERO filters, ZERO watermark, ZERO text!)
	if hasOutro {
		if in.OnProgress != nil {
			in.OnProgress(86, "Menyiapkan Outro wajib (100% bersih tanpa filter)...")
		}
		outroNorm := filepath.Join(in.WorkDir, "outro_clean.mp4")
		if err := renderNormalizedCleanOutro(ctx, in, outW, outH, outroNorm); err != nil {
			return MediaInfo{}, fmt.Errorf("gagal normalisasi outro: %w", err)
		}
		piecePaths = append(piecePaths, outroNorm)
	}

	// Final rock-solid concat (stream-copy since every piece has identical 30fps yuv420p outWxoutH 48kHz stereo!)
	if in.OnProgress != nil {
		in.OnProgress(92, "Menggabungkan semua klip kompilasi menjadi 1 MP4 final...")
	}
	listFile := filepath.Join(in.WorkDir, "concat_list.txt")
	var lb strings.Builder
	for _, p := range piecePaths {
		abs, _ := filepath.Abs(p)
		safe := strings.ReplaceAll(abs, `\`, `/`)
		safe = strings.ReplaceAll(safe, `'`, `'\''`)
		lb.WriteString(fmt.Sprintf("file '%s'\n", safe))
	}
	if err := os.WriteFile(listFile, []byte(lb.String()), 0o644); err != nil {
		return MediaInfo{}, err
	}

	// Fast concat demuxer with stream copy (instantaneous < 1s & preserves 100% exact quality!)
	concatArgs := []string{
		"-y", "-hide_banner", "-loglevel", "error",
		"-f", "concat", "-safe", "0",
		"-i", listFile,
		"-c", "copy",
		"-movflags", "+faststart",
		in.OutputPath,
	}
	if err := runSimpleFFmpeg(ctx, in.FFmpegPath, concatArgs); err != nil {
		// Fallback re-encode if codecs somehow differed
		reArgs := []string{
			"-y", "-hide_banner", "-loglevel", "error",
			"-f", "concat", "-safe", "0",
			"-i", listFile,
			"-c:v", "libx264", "-preset", "veryfast", "-crf", "20", "-pix_fmt", "yuv420p",
			"-c:a", "aac", "-b:a", "192k", "-ar", "48000", "-ac", "2",
			"-movflags", "+faststart",
			in.OutputPath,
		}
		if err2 := runSimpleFFmpeg(ctx, in.FFmpegPath, reArgs); err2 != nil {
			return MediaInfo{}, fmt.Errorf("gagal concat kompilasi: %w", err2)
		}
	}

	if in.OnProgress != nil {
		in.OnProgress(100, "Kompilasi selesai & siap upload!")
	}
	return ProbeMedia(ctx, in.FFprobePath, in.OutputPath)
}

func renderBlackBumper(ctx context.Context, in CompileInput, outW, outH int, dur float64, hasWM bool, destMP4 string) error {
	cleanTitle := StripUnsupportedEmojis(strings.TrimSpace(in.Title))
	if cleanTitle == "" {
		cleanTitle = "NEXT MOMENT"
	}

	// Build ASS bumper card text
	bumperASS := filepath.Join(in.WorkDir, "bumper.ass")
	fontSize := int(float64(outW) * 0.062)
	if fontSize < 34 {
		fontSize = 34
	}
	ass := fmt.Sprintf(`[Script Info]
ScriptType: v4.00+
PlayResX: %d
PlayResY: %d
WrapStyle: 2

[V4+ Styles]
Format: Name, Fontname, Fontsize, PrimaryColour, SecondaryColour, OutlineColour, BackColour, Bold, Italic, Underline, StrikeOut, ScaleX, ScaleY, Spacing, Angle, BorderStyle, Outline, Shadow, Alignment, MarginL, MarginR, MarginV, Encoding
Style: Bumper,Impact,%d,&H0000EAFF,&H00FFFFFF,&H00000000,&H00000000,-1,0,0,0,100,100,1,0,1,6,2,5,48,48,0,1

[Events]
Format: Layer, Start, End, Style, Name, MarginL, MarginR, MarginV, Effect, Text
Dialogue: 0,0:00:00.00,%s,Bumper,,0,0,0,,{\fscx112\fscy112\t(0,160,\fscx100\fscy100)}%s
`, outW, outH, fontSize, formatASSTime(dur), sanitizeASSText(strings.ReplaceAll(wrapText(cleanTitle, 24), "\n", `\N`)))
	if err := os.WriteFile(bumperASS, []byte(ass), 0o644); err != nil {
		return err
	}

	args := []string{
		"-y", "-hide_banner", "-loglevel", "error",
		"-f", "lavfi", "-i", fmt.Sprintf("color=c=black:s=%dx%d:r=30:d=%.3f", outW, outH, dur),
	}

	hasSFX := in.BumperSFXPath != "" && fileExists(in.BumperSFXPath)
	audioIdx := -1
	wmIdx := -1
	nextIdx := 1

	if hasSFX {
		audioIdx = nextIdx
		nextIdx++
		args = append(args, "-i", in.BumperSFXPath)
	} else {
		audioIdx = nextIdx
		nextIdx++
		args = append(args, "-f", "lavfi", "-i", fmt.Sprintf("anullsrc=r=48000:cl=stereo:d=%.3f", dur))
	}
	if hasWM {
		wmIdx = nextIdx
		nextIdx++
		args = append(args, "-i", in.Watermark.FilePath)
	}

	var filters []string
	filters = append(filters, fmt.Sprintf("[0:v]ass='%s'[bv0]", EscapeFFmpegFilterPath(bumperASS)))
	vOut := "[bv0]"
	if hasWM {
		wf, lbl := buildWatermarkFilter(vOut, wmIdx, outW, outH, *in.Watermark, "[bv1]")
		filters = append(filters, wf)
		vOut = lbl
	}
	filters = append(filters, fmt.Sprintf("%sfps=30,setsar=1,format=yuv420p[out_v]", vOut))
	filters = append(filters, fmt.Sprintf("[%d:a]aformat=sample_fmts=fltp:sample_rates=48000:channel_layouts=stereo,apad=whole_dur=%.3f,atrim=0:%.3f,asetpts=PTS-STARTPTS[out_a]", audioIdx, dur, dur))

	args = append(args,
		"-filter_complex", strings.Join(filters, ";"),
		"-map", "[out_v]", "-map", "[out_a]",
		"-c:v", "libx264", "-preset", "veryfast", "-crf", "19", "-pix_fmt", "yuv420p",
		"-c:a", "aac", "-b:a", "192k", "-ar", "48000", "-ac", "2",
		destMP4,
	)
	return runSimpleFFmpeg(ctx, in.FFmpegPath, args)
}

func renderNormalizedCleanOutro(ctx context.Context, in CompileInput, outW, outH int, destMP4 string) error {
	info, err := ProbeMedia(ctx, in.FFprobePath, in.Outro.FilePath)
	if err != nil {
		return err
	}
	args := []string{"-y", "-hide_banner", "-loglevel", "error", "-i", in.Outro.FilePath}
	var filters []string
	filters = append(filters, fmt.Sprintf(
		"[0:v]scale=%d:%d:force_original_aspect_ratio=decrease,pad=%d:%d:(ow-iw)/2:(oh-ih)/2:black,fps=30,setsar=1,format=yuv420p,setpts=PTS-STARTPTS[out_v]",
		outW, outH, outW, outH,
	))
	if info.HasAudio {
		filters = append(filters, "[0:a]aformat=sample_fmts=fltp:sample_rates=48000:channel_layouts=stereo,asetpts=PTS-STARTPTS[out_a]")
	} else {
		d := math.Max(0.5, info.Duration)
		filters = append(filters, fmt.Sprintf("anullsrc=r=48000:cl=stereo:d=%.3f[out_a]", d))
	}
	args = append(args,
		"-filter_complex", strings.Join(filters, ";"),
		"-map", "[out_v]", "-map", "[out_a]",
		"-c:v", "libx264", "-preset", "veryfast", "-crf", "19", "-pix_fmt", "yuv420p",
		"-c:a", "aac", "-b:a", "192k", "-ar", "48000", "-ac", "2",
		destMP4,
	)
	return runSimpleFFmpeg(ctx, in.FFmpegPath, args)
}

func generateCompilationSegmentASS(
	masterTitle, mode string,
	totalRanks int,
	seg ResolvedCompileSegment,
	allLabels map[int]string,
	revealed map[int]bool,
	outW, outH int,
	cropMode string,
	subStyle model.SubtitleStyle,
	speed float64,
	destPath string,
) error {
	if speed < 0.75 || speed > 1.50 {
		speed = 1.0
	}
	effDur := seg.ClipDuration / speed
	cleanTitle := StripUnsupportedEmojis(strings.TrimSpace(masterTitle))
	if cleanTitle == "" {
		cleanTitle = fmt.Sprintf("Top %d Viral Moments", totalRanks)
	}

	// Build base ASS header + optional karaoke subtitles first
	var sb strings.Builder
	sb.WriteString("[Script Info]\nScriptType: v4.00+\n")
	sb.WriteString(fmt.Sprintf("PlayResX: %d\nPlayResY: %d\nWrapStyle: 2\nScaledBorderAndShadow: yes\n\n", outW, outH))

	listFontSize := int(float64(outW) * 0.056)
	if listFontSize < 30 {
		listFontSize = 30
	}
	montageFontSize := int(float64(outW) * 0.052)
	if montageFontSize < 28 {
		montageFontSize = 28
	}

	// Top-N List Position: right below the facecam split line on left side (exact @bng.ghan 23K style!)
	listTopY := int(float64(outH) * 0.355)
	if cropMode == "facecam_top_ribbon" {
		listTopY = int(float64(outH) * 0.435)
	} else if cropMode == "blur_bg" || cropMode == "gaming_cinema" {
		listTopY = int(float64(outH) * 0.08)
	}

	sb.WriteString("[V4+ Styles]\n")
	sb.WriteString("Format: Name, Fontname, Fontsize, PrimaryColour, SecondaryColour, OutlineColour, BackColour, Bold, Italic, Underline, StrikeOut, ScaleX, ScaleY, Spacing, Angle, BorderStyle, Outline, Shadow, Alignment, MarginL, MarginR, MarginV, Encoding\n")
	// Iconic Bold Yellow Top-N Countdown list (left-aligned, thick black border!)
	sb.WriteString(fmt.Sprintf(
		"Style: TopList,Impact,%d,&H0000EAFF,&H00FFFFFF,&H00000000,&H90000000,-1,0,0,0,100,100,1,0,1,6,2,7,38,38,%d,1\n",
		listFontSize, listTopY,
	))
	// Rapid Montage clean centered theme text (gaya @rapzz05 170K views!)
	sb.WriteString(fmt.Sprintf(
		"Style: MontageTitle,Arial,%d,&H00FFFFFF,&H00FFFFFF,&H00000000,&H80000000,-1,0,0,0,100,100,0.5,0,1,5,2,5,48,48,0,1\n",
		montageFontSize,
	))
	// White ribbon text if user combines Top-N with facecam_top_ribbon
	ribFont := int(float64(outW) * 0.058)
	sb.WriteString(fmt.Sprintf(
		"Style: RibbonTitle,Arial,%d,&H00111111,&H00111111,&H00FFFFFF,&H00000000,-1,0,0,0,100,100,1,0,1,0,0,5,32,32,0,1\n\n",
		ribFont,
	))

	sb.WriteString("[Events]\n")
	sb.WriteString("Format: Layer, Start, End, Style, Name, MarginL, MarginR, MarginV, Effect, Text\n")

	endTS := formatASSTime(effDur)
	if mode == "rapid_montage" {
		// Clean centered theme title (gaya @rapzz05!)
		if cropMode == "facecam_top_ribbon" {
			ribY := int(float64(outH)*0.33) + int(float64(outH)*0.085)/2
			sb.WriteString(fmt.Sprintf("Dialogue: 2,0:00:00.00,%s,RibbonTitle,,0,0,0,,{\\an5\\pos(%d,%d)}%s\n",
				endTS, outW/2, ribY, sanitizeASSText(strings.ToUpper(cleanTitle))))
		} else {
			sb.WriteString(fmt.Sprintf("Dialogue: 2,0:00:00.00,%s,MontageTitle,,0,0,0,,{\\an5\\pos(%d,%d)}%s\n",
				endTS, outW/2, int(float64(outH)*0.48), sanitizeASSText(cleanTitle)))
		}
	} else {
		// Progressive Top-N Countdown List (1..N, revealing from N down to 1!)
		var lines []string
		lines = append(lines, fmt.Sprintf("{\\c&H0000EAFF&}%s", sanitizeASSText(cleanTitle)))
		for rank := 1; rank <= totalRanks; rank++ {
			lbl := StripUnsupportedEmojis(strings.TrimSpace(allLabels[rank]))
			if rank == seg.RankNumber {
				// Active current rank pops bright white/yellow!
				lines = append(lines, fmt.Sprintf("{\\c&H00FFFFFF&\\fscx108\\fscy108}%d. %s{\\r}", rank, sanitizeASSText(lbl)))
			} else if revealed[rank] && lbl != "" {
				// Already revealed ranks stay visible in yellow
				lines = append(lines, fmt.Sprintf("{\\c&H0000EAFF&}%d. %s", rank, sanitizeASSText(lbl)))
			} else {
				// Mystery ranks not yet shown stay empty ("1.", "2.", ...) to build suspense!
				lines = append(lines, fmt.Sprintf("{\\c&H0000EAFF&}%d.", rank))
			}
		}
		sb.WriteString(fmt.Sprintf("Dialogue: 2,0:00:00.00,%s,TopList,,0,0,0,,%s\n", endTS, strings.Join(lines, `\N`)))
		if cropMode == "facecam_top_ribbon" {
			ribY := int(float64(outH)*0.33) + int(float64(outH)*0.085)/2
			sb.WriteString(fmt.Sprintf("Dialogue: 2,0:00:00.00,%s,RibbonTitle,,0,0,0,,{\\an5\\pos(%d,%d)}#%d - %s\n",
				endTS, outW/2, ribY, seg.RankNumber, sanitizeASSText(strings.ToUpper(seg.Label))))
		}
	}

	return os.WriteFile(destPath, []byte(sb.String()), 0o644)
}

func runSimpleFFmpeg(ctx context.Context, ffmpegPath string, args []string) error {
	cmd := exec.CommandContext(ctx, ffmpegPath, args...)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("ffmpeg: %v — %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}
