package renderer

import (
	"fmt"
	"os"
	"strings"

	"klipforge/internal/model"
)

// GenerateASS creates a styled ASS subtitle file for the clip segment [clipStart, clipEnd].
// Cue timestamps are shifted relative to 0.0 and adjusted by speed so they stay synced.
func GenerateASS(cues []model.TranscriptCue, clipStart, clipEnd, speed float64, outW, outH int, style model.SubtitleStyle, destPath string) (int, error) {
	if !style.Enabled || len(cues) == 0 {
		return 0, nil
	}
	if speed <= 0.5 || speed > 2.0 {
		speed = 1.0
	}

	fontName := style.FontName
	if fontName == "" {
		fontName = "Impact"
	}
	fontSize := style.FontSize
	if fontSize <= 0 {
		fontSize = 58
	}
	// Scale font size proportionally if output width differs from 1080
	scaledSize := int(float64(fontSize) * (float64(outW) / 1080.0))
	if scaledSize < 24 {
		scaledSize = 24
	}
	maxWords := style.MaxWordsPerRow
	if maxWords <= 0 {
		maxWords = 4
	}

	// ASS colors are &HAABBGGRR
	primaryColor := "&H0000EAFF" // Viral Yellow
	outlineColor := "&H00000000" // Pure Black
	backColor := "&H96000000"    // Semi-transparent shadow
	borderStyle := 1
	outline := 5
	shadow := 2

	switch style.Preset {
	case "clean-white":
		primaryColor = "&H00FFFFFF"
		outline = 4
		shadow = 2
	case "gaming-neon":
		primaryColor = "&H0066FF88"
		outline = 5
		shadow = 2
	case "minimal":
		primaryColor = "&H00FFFFFF"
		backColor = "&HAA000000"
		borderStyle = 3
		outline = 8
		shadow = 0
	}

	alignment := 2 // Bottom-center
	marginV := int(float64(outH) * 0.16)
	switch style.Position {
	case "center":
		alignment = 5
		marginV = 0
	case "split-seam":
		alignment = 2
		// Placed near the split boundary (~38% from bottom)
		marginV = int(float64(outH) * 0.34)
	case "top":
		alignment = 8
		marginV = int(float64(outH) * 0.14)
	}

	var sb strings.Builder
	sb.WriteString("[Script Info]\n")
	sb.WriteString("ScriptType: v4.00+\n")
	sb.WriteString(fmt.Sprintf("PlayResX: %d\n", outW))
	sb.WriteString(fmt.Sprintf("PlayResY: %d\n", outH))
	sb.WriteString("WrapStyle: 2\n")
	sb.WriteString("ScaledBorderAndShadow: yes\n\n")

	sb.WriteString("[V4+ Styles]\n")
	sb.WriteString("Format: Name, Fontname, Fontsize, PrimaryColour, SecondaryColour, OutlineColour, BackColour, Bold, Italic, Underline, StrikeOut, ScaleX, ScaleY, Spacing, Angle, BorderStyle, Outline, Shadow, Alignment, MarginL, MarginR, MarginV, Encoding\n")
	sb.WriteString(fmt.Sprintf(
		"Style: Viral,%s,%d,%s,&H000000FF,%s,%s,-1,0,0,0,100,100,1,0,%d,%d,%d,%d,48,48,%d,1\n\n",
		fontName, scaledSize, primaryColor, outlineColor, backColor, borderStyle, outline, shadow, alignment, marginV,
	))

	sb.WriteString("[Events]\n")
	sb.WriteString("Format: Layer, Start, End, Style, Name, MarginL, MarginR, MarginV, Effect, Text\n")

	clipDur := clipEnd - clipStart
	written := 0

	for _, cue := range cues {
		if cue.End <= clipStart || cue.Start >= clipEnd {
			continue
		}
		s := cue.Start - clipStart
		e := cue.End - clipStart
		if s < 0 {
			s = 0
		}
		if e > clipDur {
			e = clipDur
		}
		s /= speed
		e /= speed
		if e-s < 0.10 {
			continue
		}

		txt := strings.TrimSpace(cue.Text)
		if txt == "" {
			continue
		}
		if style.Uppercase {
			txt = strings.ToUpper(txt)
		}

		// Chunk long captions into fast 3-5 word viral bursts
		words := strings.Fields(txt)
		if len(words) == 0 {
			continue
		}
		if len(words) <= maxWords {
			writeDialogue(&sb, s, e, strings.Join(words, " "))
			written++
			continue
		}

		chunks := chunkWords(words, maxWords)
		chunkDur := (e - s) / float64(len(chunks))
		for idx, ch := range chunks {
			cs := s + float64(idx)*chunkDur
			ce := cs + chunkDur
			if idx == len(chunks)-1 {
				ce = e
			}
			writeDialogue(&sb, cs, ce, ch)
			written++
		}
	}

	if written == 0 {
		return 0, nil
	}
	if err := os.WriteFile(destPath, []byte(sb.String()), 0o644); err != nil {
		return 0, err
	}
	return written, nil
}

func chunkWords(words []string, maxWords int) []string {
	var out []string
	for i := 0; i < len(words); i += maxWords {
		end := i + maxWords
		if end > len(words) {
			end = len(words)
		}
		out = append(out, strings.Join(words[i:end], " "))
	}
	return out
}

func writeDialogue(sb *strings.Builder, start, end float64, text string) {
	// Sanitize ASS control characters
	safe := strings.ReplaceAll(text, "\n", " ")
	safe = strings.ReplaceAll(safe, "{", "(")
	safe = strings.ReplaceAll(safe, "}", ")")
	sb.WriteString(fmt.Sprintf("Dialogue: 0,%s,%s,Viral,,0,0,0,,%s\n", formatASSTime(start), formatASSTime(end), safe))
}

func formatASSTime(sec float64) string {
	if sec < 0 {
		sec = 0
	}
	totalCs := int(sec*100 + 0.5)
	h := totalCs / 360000
	m := (totalCs % 360000) / 6000
	s := (totalCs % 6000) / 100
	cs := totalCs % 100
	return fmt.Sprintf("%d:%02d:%02d.%02d", h, m, s, cs)
}
