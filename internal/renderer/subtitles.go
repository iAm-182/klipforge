package renderer

import (
	"fmt"
	"os"
	"runtime"
	"strings"
	"unicode"

	"klipforge/internal/model"
)

// resolveASSFontAndScaleX avoids wide DejaVu Sans overflow on Linux servers when Windows Impact is absent:
// uses Anton (OFL Impact metric twin) if installed, else condensed Liberation Sans (ScaleX=82).
func resolveASSFontAndScaleX(requested string) (string, int) {
	req := strings.TrimSpace(requested)
	if req == "" {
		req = "Impact"
	}
	if runtime.GOOS == "windows" {
		return req, 100
	}
	if strings.EqualFold(req, "Impact") {
		if fileExists("/usr/share/fonts/truetype/anton/Anton-Regular.ttf") {
			return "Anton", 100
		}
		if fileExists("/usr/share/fonts/truetype/msttcorefonts/Impact.ttf") {
			return "Impact", 100
		}
		return "Liberation Sans", 82
	}
	if strings.EqualFold(req, "Arial") {
		return "Liberation Sans", 94
	}
	return req, 100
}

// GenerateASS creates a pro TikTok/CapCut-style ASS subtitle + animated Hook Title overlay file.
// Uses YouTube's word-level timestamps when available for TRUE word-by-word karaoke highlighting
// with pop-in bounce animation, plus a sleek Hook Title banner (white-ribbon black text when cropMode=="facecam_top_ribbon",
// or 4.8s animated viral headline otherwise — zero emoji tofu boxes!).
func GenerateASS(cues []model.TranscriptCue, clipStart, clipEnd, speed float64, outW, outH int, style model.SubtitleStyle, hookText, cropMode, destPath string) (int, error) {
	if speed < 0.50 || speed > 2.0 {
		speed = 1.0
	}
	cleanHook := StripUnsupportedEmojis(strings.TrimSpace(hookText))
	if (!style.Enabled || len(cues) == 0) && cleanHook == "" {
		return 0, nil
	}

	fontName, fontScaleX := resolveASSFontAndScaleX(style.FontName)
	impactName, impactScaleX := resolveASSFontAndScaleX("Impact")
	arialName, arialScaleX := resolveASSFontAndScaleX("Arial")
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
		"Style: Viral,%s,%d,%s,%s,%s,%s,-1,0,0,0,%d,100,1,0,%d,%d,%d,%d,56,56,%d,1\n",
		fontName, scaledSize, baseColor, highlightColor, outlineColor, backColor, fontScaleX, borderStyle, outline, shadow, alignment, marginV,
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
			"Style: HookBanner,%s,%d,&H00FFFFFF,&H0000EAFF,&H00000000,&H00000000,-1,0,0,0,%d,100,1.5,0,1,3,1,5,36,36,0,1\n\n",
			impactName, barFont, impactScaleX,
		))
	} else if isFloat {
		flFont := int(float64(outW) * 0.058)
		if flFont < 32 {
			flFont = 32
		}
		sb.WriteString(fmt.Sprintf(
			"Style: HookBanner,%s,%d,&H0000EAFF,&H00FFFFFF,&H00000000,&HAA000000,-1,0,0,0,%d,100,1.5,0,1,9,4,5,48,48,0,1\n\n",
			impactName, flFont, impactScaleX,
		))
	} else if isRibbon {
		ribFont := int(float64(outW) * 0.062)
		if ribFont < 32 {
			ribFont = 32
		}
		sb.WriteString(fmt.Sprintf(
			"Style: HookBanner,%s,%d,&H00111111,&H00111111,&H00FFFFFF,&H00000000,-1,0,0,0,%d,100,1.2,0,1,0,0,5,36,36,0,1\n\n",
			arialName, ribFont, arialScaleX,
		))
	} else {
		sb.WriteString(fmt.Sprintf(
			"Style: HookBanner,%s,%d,&H0000EAFF,&H00FFFFFF,&H00000000,&HAA000000,-1,0,0,0,%d,100,1.5,0,1,8,4,8,64,64,%d,1\n\n",
			fontName, hookFontSize, fontScaleX, hookMarginV,
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
		subLines, cnt := buildSubtitleEventLines(cues, clipStart, clipEnd, speed, nil, style, baseColor, highlightColor, keyPunchColor, isKaraoke)
		sb.WriteString(subLines)
		written += cnt
	}

	if written == 0 {
		return 0, nil
	}
	if err := os.WriteFile(destPath, []byte(sb.String()), 0o644); err != nil {
		return 0, err
	}
	return written, nil
}

// buildSubtitleEventLines emits word-by-word ASS Dialogue events for [clipStart, clipEnd],
// supporting karaoke active-word highlight + viral keyword punch-in zoom AND optional SpeedRamp time mapping!
func buildSubtitleEventLines(
	cues []model.TranscriptCue,
	clipStart, clipEnd, speed float64,
	ramp *model.SpeedRamp,
	style model.SubtitleStyle,
	baseColor, highlightColor, keyPunchColor string,
	isKaraoke bool,
) (string, int) {
	if !style.Enabled || len(cues) == 0 {
		return "", 0
	}
	if speed < 0.50 || speed > 2.00 {
		speed = 1.0
	}
	rawDur := clipEnd - clipStart
	if rawDur <= 0 {
		return "", 0
	}
	clipDur := MapTimeWithRamp(rawDur, speed, ramp)
	maxWords := style.MaxWordsPerRow
	if maxWords <= 0 || maxWords > 4 {
		maxWords = 3
	}

	type styledWord struct {
		model.WordTiming
		Speaker string
	}
	var allWords []styledWord
	for _, cue := range cues {
		if cue.End <= clipStart || cue.Start >= clipEnd {
			continue
		}
		if len(cue.Words) > 0 {
			for _, w := range cue.Words {
				if w.End <= clipStart || w.Start >= clipEnd {
					continue
				}
				ws := MapTimeWithRamp(w.Start-clipStart, speed, ramp)
				we := MapTimeWithRamp(w.End-clipStart, speed, ramp)
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
					allWords = append(allWords, styledWord{WordTiming: model.WordTiming{Start: ws, End: we, Text: txt}, Speaker: cue.Speaker})
				}
			}
		} else {
			cs := MapTimeWithRamp(cue.Start-clipStart, speed, ramp)
			ce := MapTimeWithRamp(cue.End-clipStart, speed, ramp)
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
			for _, wt := range distributeWordTimings(toks, cs, ce) {
				allWords = append(allWords, styledWord{
					WordTiming: wt,
					Speaker:    cue.Speaker,
				})
			}
		}
	}

	speakerHighlight := func(spk, fallback string) string {
		switch spk {
		case "SPEAKER_1":
			return "&H00FFD000" // Crisp Cyan for NPC / guest speaker
		case "SPEAKER_2":
			return "&H0066FF88" // Mint Green for 3rd speaker
		default:
			return fallback
		}
	}

	var sb strings.Builder
	written := 0
	for i := 0; i < len(allWords); {
		endIdx := i + maxWords
		if endIdx > len(allWords) {
			endIdx = len(allWords)
		}
		for j := i + 1; j < endIdx; j++ {
			if allWords[j].Start-allWords[j-1].End > 0.65 || allWords[j].Speaker != allWords[j-1].Speaker {
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
				hiCol := speakerHighlight(gw.Speaker, highlightColor)
				if k == activeIdx {
					if isKaraoke && isPunchKeyWord(gw.Text) {
						lineParts = append(lineParts, fmt.Sprintf("{\\c%s\\fscx132\\fscy132\\t(0,90,\\fscx118\\fscy118)\\t(90,210,\\fscx112\\fscy112)}%s{\\r}", keyPunchColor, wordSafe))
					} else if isKaraoke {
						lineParts = append(lineParts, fmt.Sprintf("{\\c%s\\fscx122\\fscy122\\t(0,85,\\fscx112\\fscy112)}%s{\\r}", hiCol, wordSafe))
					} else {
						lineParts = append(lineParts, fmt.Sprintf("{\\c%s\\fscx114\\fscy114\\t(0,95,\\fscx104\\fscy104)}%s{\\r}", hiCol, wordSafe))
					}
				} else {
					lineParts = append(lineParts, fmt.Sprintf("{\\c%s}%s", baseColor, wordSafe))
				}
			}
			popAnim := ""
			if activeIdx == 0 {
				popAnim = `{\fscx92\fscy92\t(0,70,\fscx100\fscy100)}`
			}
			sb.WriteString(fmt.Sprintf(
				"Dialogue: 1,%s,%s,Viral,,0,0,0,,%s%s\n",
				formatASSTime(s), formatASSTime(e), popAnim, strings.Join(lineParts, " "),
			))
			written++
		}
	}
	return sb.String(), written
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

// distributeWordTimings splits [startSec, endSec] across words proportionally by character/vowel syllable
// complexity and punctuation pause weight so karaoke sweeps track natural Indonesian/English speech cadence.
func distributeWordTimings(toks []string, startSec, endSec float64) []model.WordTiming {
	n := len(toks)
	if n == 0 || endSec <= startSec {
		return nil
	}
	weights := make([]float64, n)
	totalW := 0.0
	for i, tok := range toks {
		letters := 0
		vowels := 0
		for _, r := range strings.ToLower(tok) {
			if unicode.IsLetter(r) || unicode.IsDigit(r) {
				letters++
				switch r {
				case 'a', 'e', 'i', 'o', 'u':
					vowels++
				}
			}
		}
		if letters == 0 {
			letters = 1
		}
		w := 1.0 + float64(letters)*0.45 + float64(vowels)*0.55
		trimmed := strings.TrimSpace(tok)
		if strings.HasSuffix(trimmed, ".") || strings.HasSuffix(trimmed, "!") || strings.HasSuffix(trimmed, "?") {
			w += 1.25
		} else if strings.HasSuffix(trimmed, ",") || strings.HasSuffix(trimmed, ";") || strings.HasSuffix(trimmed, ":") {
			w += 0.65
		}
		weights[i] = w
		totalW += w
	}
	if totalW <= 0 {
		totalW = float64(n)
	}

	out := make([]model.WordTiming, 0, n)
	cur := startSec
	span := endSec - startSec
	for i, tok := range toks {
		var nxt float64
		if i == n-1 {
			nxt = endSec
		} else {
			nxt = cur + span*(weights[i]/totalW)
			if nxt < cur+0.02 {
				nxt = cur + 0.02
			}
			if nxt > endSec {
				nxt = endSec
			}
		}
		if nxt <= cur {
			nxt = cur + 0.02
		}
		out = append(out, model.WordTiming{
			Start: cur,
			End:   nxt,
			Text:  tok,
		})
		cur = nxt
	}
	return out
}
