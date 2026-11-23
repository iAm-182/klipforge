package renderer

import (
	"fmt"
	"os"
	"strings"
	"unicode"

	"klipforge/internal/model"
)

// GenerateASS creates a pro TikTok/CapCut-style ASS subtitle + animated Hook Title overlay file.
// Uses YouTube's word-level timestamps when available for TRUE word-by-word karaoke highlighting
// with pop-in bounce animation, plus a sleek Hook Title banner (white-ribbon black text when cropMode=="facecam_top_ribbon",
// or 4.8s animated viral headline otherwise — zero emoji tofu boxes!).
func GenerateASS(cues []model.TranscriptCue, clipStart, clipEnd, speed float64, outW, outH int, style model.SubtitleStyle, hookText, cropMode, destPath string) (int, error) {
	if speed <= 0.5 || speed > 2.0 {
		speed = 1.0
	}
	cleanHook := StripUnsupportedEmojis(strings.TrimSpace(hookText))
	if (!style.Enabled || len(cues) == 0) && cleanHook == "" {
		return 0, nil
	}

	fontName := style.FontName
	if fontName == "" {
		fontName = "Impact"
	}
	fontSize := style.FontSize
	if fontSize <= 0 {
		fontSize = 68
	}
	// Make viral captions punchy & bold on mobile screens
	if fontSize < 62 {
		fontSize = 66
	}
	scaledSize := int(float64(fontSize) * (float64(outW) / 1080.0))
	if scaledSize < 30 {
		scaledSize = 30
	}
	maxWords := style.MaxWordsPerRow
	if maxWords <= 0 || maxWords > 4 {
		maxWords = 3 // 3-word snappy CapCut/Hormozi bursts look WAY cleaner on 9:16!
	}

	// ASS colors are &HAABBGGRR
	baseColor := "&H00FFFFFF"      // Crisp white for non-active words
	highlightColor := "&H0000EAFF" // Electric Viral Yellow for active spoken word
	outlineColor := "&H00000000"   // Thick pure black stroke
	backColor := "&H90000000"
	borderStyle := 1
	outline := 7
	shadow := 3

	isKaraoke := style.Preset == "karaoke"
	keyPunchColor := "&H002A85FF" // Fiery Vice Orange/Pink punch accent for viral keywords in karaoke mode!

	switch style.Preset {
	case "karaoke":
		baseColor = "&H00FFFFFF"
		highlightColor = "&H0000EAFF" // Electric viral yellow active word
		outline = 8
		shadow = 4
	case "clean-white":
		baseColor = "&H00E8E8E8"
		highlightColor = "&H00FFFFFF"
		outline = 6
	case "gaming-neon":
		baseColor = "&H00FFFFFF"
		highlightColor = "&H0055FF33" // Neon gaming green!
		outline = 7
	case "minimal":
		baseColor = "&H00FFFFFF"
		highlightColor = "&H0000EAFF"
		backColor = "&HAA000000"
		borderStyle = 3
		outline = 10
		shadow = 0
	}

	alignment := 2
	marginV := int(float64(outH) * 0.20)
	switch style.Position {
	case "center":
		alignment = 5
		marginV = 0
	case "split-seam":
		alignment = 2
		// Sits right on the clean split boundary / above facecam (never hides eyes!)
		marginV = int(float64(outH) * 0.35)
	case "top":
		alignment = 8
		marginV = int(float64(outH) * 0.16)
	}

	hookFontSize := int(float64(outW) * 0.056)
	if hookFontSize < 30 {
		hookFontSize = 30
	}
	hookMarginV := int(float64(outH) * 0.085)

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
		"Style: Viral,%s,%d,%s,%s,%s,%s,-1,0,0,0,100,100,1,0,%d,%d,%d,%d,56,56,%d,1\n",
		fontName, scaledSize, baseColor, highlightColor, outlineColor, backColor, borderStyle, outline, shadow, alignment, marginV,
	))
	// Hook Banner Style tailored cleanly to the chosen layout (100% ATM Original Branding!):
	// - "facecam_top_neon": Crisp electric white/yellow text centered inside the Dark Vice Neon Bar!
	// - "facecam_top_float": Kinetic floating headline across the seamless split seam (pops in for first 5.5s, then fades cleanly!)
	// - "facecam_top_ribbon": Black bold text centered inside the white strip
	// - All others: Top animated headline for first 4.8s
	isNeon := cropMode == "facecam_top_neon"
	isFloat := cropMode == "facecam_top_float"
	isRibbon := cropMode == "facecam_top_ribbon"
	if isNeon {
		barFont := int(float64(outW) * 0.056)
		if barFont < 30 {
			barFont = 30
		}
		sb.WriteString(fmt.Sprintf(
			"Style: HookBanner,Impact,%d,&H00FFFFFF,&H0000EAFF,&H00000000,&H00000000,-1,0,0,0,100,100,1.5,0,1,3,1,5,36,36,0,1\n\n",
			barFont,
		))
	} else if isFloat {
		flFont := int(float64(outW) * 0.058)
		if flFont < 32 {
			flFont = 32
		}
		sb.WriteString(fmt.Sprintf(
			"Style: HookBanner,Impact,%d,&H0000EAFF,&H00FFFFFF,&H00000000,&HAA000000,-1,0,0,0,100,100,1.5,0,1,9,4,5,48,48,0,1\n\n",
			flFont,
		))
	} else if isRibbon {
		ribFont := int(float64(outW) * 0.062)
		if ribFont < 32 {
			ribFont = 32
		}
		sb.WriteString(fmt.Sprintf(
			"Style: HookBanner,Arial,%d,&H00111111,&H00111111,&H00FFFFFF,&H00000000,-1,0,0,0,100,100,1.2,0,1,0,0,5,36,36,0,1\n\n",
			ribFont,
		))
	} else {
		sb.WriteString(fmt.Sprintf(
			"Style: HookBanner,%s,%d,&H0000EAFF,&H00FFFFFF,&H00000000,&HAA000000,-1,0,0,0,100,100,1.5,0,1,8,4,8,64,64,%d,1\n\n",
			fontName, hookFontSize, hookMarginV,
		))
	}

	sb.WriteString("[Events]\n")
	sb.WriteString("Format: Layer, Start, End, Style, Name, MarginL, MarginR, MarginV, Effect, Text\n")

	clipDur := (clipEnd - clipStart) / speed
	written := 0

	// 1. Sleek Signature Hook Headline
	if cleanHook != "" {
		if isNeon {
			barCenterY := int(float64(outH)*0.33) + int(float64(outH)*0.072)/2
			wrapped := strings.ReplaceAll(wrapText(strings.ToUpper(cleanHook), 28), "\n", `\N`)
			sb.WriteString(fmt.Sprintf(
				"Dialogue: 2,0:00:00.00,%s,HookBanner,,0,0,0,,{\\an5\\pos(%d,%d)\\fscx108\\fscy108\\t(0,160,\\fscx100\\fscy100)}%s\n",
				formatASSTime(clipDur), outW/2, barCenterY, sanitizeASSText(wrapped),
			))
		} else if isFloat {
			seamY := int(float64(outH) * 0.34)
			hookEnd := 5.5
			if clipDur < hookEnd {
				hookEnd = clipDur
			}
			wrapped := strings.ReplaceAll(wrapText(strings.ToUpper(cleanHook), 24), "\n", `\N`)
			sb.WriteString(fmt.Sprintf(
				"Dialogue: 2,0:00:00.00,%s,HookBanner,,0,0,0,,{\\an5\\pos(%d,%d)\\fad(120,400)\\fscx116\\fscy116\\t(0,150,\\fscx100\\fscy100)}%s\n",
				formatASSTime(hookEnd), outW/2, seamY, sanitizeASSText(wrapped),
			))
		} else if isRibbon {
			ribCenterY := int(float64(outH)*0.33) + int(float64(outH)*0.085)/2
			wrapped := strings.ReplaceAll(wrapText(strings.ToUpper(cleanHook), 26), "\n", `\N`)
			sb.WriteString(fmt.Sprintf(
				"Dialogue: 2,0:00:00.00,%s,HookBanner,,0,0,0,,{\\an5\\pos(%d,%d)}%s\n",
				formatASSTime(clipDur), outW/2, ribCenterY, sanitizeASSText(wrapped),
			))
		} else {
			hookEnd := 4.8
			if clipDur < hookEnd {
				hookEnd = clipDur
			}
			wrapped := strings.ReplaceAll(wrapText(strings.ToUpper(cleanHook), 22), "\n", `\N`)
			sb.WriteString(fmt.Sprintf(
				"Dialogue: 2,0:00:00.00,%s,HookBanner,,0,0,0,,{\\fad(140,420)\\fscx115\\fscy115\\t(0,160,\\fscx100\\fscy100)}%s\n",
				formatASSTime(hookEnd), sanitizeASSText(wrapped),
			))
		}
		written++
	}

	// 2. Word-by-Word Viral Karaoke Subtitles
	if style.Enabled && len(cues) > 0 {
		// Flatten all words inside [clipStart, clipEnd] with exact or interpolated timings
		var allWords []model.WordTiming
		for _, cue := range cues {
			if cue.End <= clipStart || cue.Start >= clipEnd {
				continue
			}
			if len(cue.Words) > 0 {
				for _, w := range cue.Words {
					if w.End <= clipStart || w.Start >= clipEnd {
						continue
					}
					ws := (w.Start - clipStart) / speed
					we := (w.End - clipStart) / speed
					if ws < 0 {
						ws = 0
					}
					if we > clipDur {
						we = clipDur
					}
					txt := StripUnsupportedEmojis(strings.TrimSpace(w.Text))
					if txt != "" && we > ws+0.03 {
						if style.Uppercase {
							txt = strings.ToUpper(txt)
						}
						allWords = append(allWords, model.WordTiming{Start: ws, End: we, Text: txt})
					}
				}
			} else {
				// Interpolate word timings evenly across the cue
				cs := (cue.Start - clipStart) / speed
				ce := (cue.End - clipStart) / speed
				if cs < 0 {
					cs = 0
				}
				if ce > clipDur {
					ce = clipDur
				}
				txt := StripUnsupportedEmojis(strings.TrimSpace(cue.Text))
				if style.Uppercase {
					txt = strings.ToUpper(txt)
				}
				toks := strings.Fields(txt)
				if len(toks) == 0 || ce <= cs+0.05 {
					continue
				}
				step := (ce - cs) / float64(len(toks))
				for idx, tok := range toks {
					allWords = append(allWords, model.WordTiming{
						Start: cs + float64(idx)*step,
						End:   cs + float64(idx+1)*step,
						Text:  tok,
					})
				}
			}
		}

		// Group into punchy 2-3 word bursts and emit word-by-word active-color karaoke dialogues!
		for i := 0; i < len(allWords); {
			endIdx := i + maxWords
			if endIdx > len(allWords) {
				endIdx = len(allWords)
			}
			// Break early if there is a natural pause > 0.65s between words
			for j := i + 1; j < endIdx; j++ {
				if allWords[j].Start-allWords[j-1].End > 0.65 {
					endIdx = j
					break
				}
			}
			group := allWords[i:endIdx]
			i = endIdx

			for activeIdx, aw := range group {
				s := aw.Start
				e := aw.End
				if activeIdx+1 < len(group) && group[activeIdx+1].Start > s {
					e = group[activeIdx+1].Start
				}
				if e-s < 0.04 {
					continue
				}

				var lineParts []string
				for k, gw := range group {
					wordSafe := sanitizeASSText(gw.Text)
					if k == activeIdx {
						if isKaraoke && isPunchKeyWord(gw.Text) {
							// Karaoke Punch-In Zoom on key viral words (hook / laugh / crash / hype words)!
							lineParts = append(lineParts, fmt.Sprintf("{\\c%s\\fscx132\\fscy132\\t(0,90,\\fscx118\\fscy118)\\t(90,210,\\fscx112\\fscy112)}%s{\\r}", keyPunchColor, wordSafe))
						} else if isKaraoke {
							// Enlarged vibrant active spoken word (reelsaw / CapCut karaoke feel)
							lineParts = append(lineParts, fmt.Sprintf("{\\c%s\\fscx122\\fscy122\\t(0,85,\\fscx112\\fscy112)}%s{\\r}", highlightColor, wordSafe))
						} else {
							// Active spoken word pops in bright highlight color + slight scale pulse!
							lineParts = append(lineParts, fmt.Sprintf("{\\c%s\\fscx114\\fscy114\\t(0,95,\\fscx104\\fscy104)}%s{\\r}", highlightColor, wordSafe))
						}
					} else {
						lineParts = append(lineParts, fmt.Sprintf("{\\c%s}%s", baseColor, wordSafe))
					}
				}
				popAnim := ""
				if activeIdx == 0 {
					// Snappy pop entrance for each new 3-word burst
					popAnim = `{\fscx92\fscy92\t(0,70,\fscx100\fscy100)}`
				}
				sb.WriteString(fmt.Sprintf(
					"Dialogue: 1,%s,%s,Viral,,0,0,0,,%s%s\n",
					formatASSTime(s), formatASSTime(e), popAnim, strings.Join(lineParts, " "),
				))
				written++
			}
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

var karaokePunchWords = map[string]bool{
	"jago": true, "nyetir": true, "nabrak": true, "tabrak": true, "polisi": true,
	"wkwk": true, "wkwkwk": true, "ngakak": true, "haha": true, "hahaha": true,
	"anjir": true, "njir": true, "anjay": true, "gila": true, "parah": true,
	"kaget": true, "panik": true, "meledak": true, "mati": true, "kabur": true,
	"clutch": true, "epic": true, "wasted": true, "busted": true, "tamat": true,
	"astaga": true, "waduh": true, "buset": true, "kocak": true, "bohong": true,
	"tembak": true, "hancur": true, "kebalik": true, "terbang": true, "sumpah": true,
}

func isPunchKeyWord(word string) bool {
	clean := strings.TrimFunc(strings.ToLower(word), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
	if karaokePunchWords[clean] {
		return true
	}
	return strings.HasPrefix(clean, "wkwk") || strings.HasPrefix(clean, "haha")
}

// StripUnsupportedEmojis removes non-BMP emoji glyphs (> 0xFFFF) that render as empty tofu boxes (□) in Windows Impact/Arial fonts.
func StripUnsupportedEmojis(s string) string {
	var sb strings.Builder
	for _, r := range s {
		if r > 0xFFFF || (r >= 0x2600 && r <= 0x27BF) || r == 0xFE0F || r == 0x200D {
			continue
		}
		if unicode.IsControl(r) && r != '\n' {
			continue
		}
		sb.WriteRune(r)
	}
	return strings.TrimSpace(strings.Join(strings.Fields(sb.String()), " "))
}

func sanitizeASSText(text string) string {
	safe := strings.ReplaceAll(text, "{", "(")
	safe = strings.ReplaceAll(safe, "}", ")")
	return safe
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
