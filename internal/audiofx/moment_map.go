package audiofx

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"klipforge/internal/model"
)

// MomentEditRule maps a candidate/segment momentType (fail, meme, crash, victory, hype, twist)
// to preferred & fallback SFX and BGM asset IDs from the local Audio Library.
// Editable at runtime via <AudioDir>/moment_edit_map.json without rebuilding the binary.
type MomentEditRule struct {
	SFXID           string `json:"sfxId"`
	FallbackSFXID   string `json:"fallbackSfxId,omitempty"`
	SecondarySFXID  string `json:"secondarySfxId,omitempty"` // optional second beat SFX (e.g. riser -> bassdrop)
	BGMID           string `json:"bgmId"`
	FallbackBGMID   string `json:"fallbackBgmId,omitempty"`
	Reason          string `json:"reason,omitempty"`
}

// DefaultMomentEditMap returns the built-in offline momentType -> SFX/BGM editing table.
func DefaultMomentEditMap() map[string]MomentEditRule {
	return map[string]MomentEditRule{
		"fail": {
			SFXID:         "sfx-record-scratch",
			FallbackSFXID: "sfx-sad-trombone",
			BGMID:         "bgm-funny-bouncy",
			FallbackBGMID: "bgm-sad-piano",
			Reason:        "fail moment: freeze record-scratch + comedy/sad BGM",
		},
		"meme": {
			SFXID:         "sfx-record-scratch",
			FallbackSFXID: "sfx-sad-trombone",
			BGMID:         "bgm-funny-bouncy",
			FallbackBGMID: "bgm-sneaky-troll",
			Reason:        "meme moment: punch record-scratch + funny bouncy BGM",
		},
		"crash": {
			SFXID:         "sfx-metal-pipe",
			FallbackSFXID: "sfx-boom",
			BGMID:         "bgm-gaming-pulse",
			FallbackBGMID: "bgm-phonk-drift",
			Reason:        "crash moment: metal-pipe impact + hype gaming BGM",
		},
		"victory": {
			SFXID:         "sfx-airhorn",
			FallbackSFXID: "sfx-applause-burst",
			BGMID:         "bgm-victory-fanfare",
			FallbackBGMID: "bgm-gaming-pulse",
			Reason:        "victory moment: MLG airhorn + victory fanfare BGM",
		},
		"hype": {
			SFXID:          "sfx-riser",
			FallbackSFXID:  "sfx-bassdrop",
			SecondarySFXID: "sfx-bassdrop",
			BGMID:          "bgm-phonk-drift",
			FallbackBGMID:  "bgm-gaming-pulse",
			Reason:         "hype moment: tension riser/bassdrop + phonk hype BGM",
		},
		"twist": {
			SFXID:         "sfx-dun-dun-dun",
			FallbackSFXID: "sfx-alert",
			BGMID:         "bgm-suspense",
			FallbackBGMID: "bgm-heist-stealth",
			Reason:        "twist moment: dramatic dun-dun-dun + suspense BGM",
		},
	}
}

// MomentMapPath returns the path to the editable JSON moment-to-edit mapping file in the audio library dir.
func (l *Library) MomentMapPath() string {
	return filepath.Join(l.AudioDir, "moment_edit_map.json")
}

// ensureMomentMapFileLocked writes the default moment_edit_map.json if it does not yet exist on disk.
func (l *Library) ensureMomentMapFileLocked() {
	p := l.MomentMapPath()
	if _, err := os.Stat(p); err == nil {
		return
	}
	defaults := DefaultMomentEditMap()
	if b, err := json.MarshalIndent(defaults, "", "  "); err == nil {
		_ = os.WriteFile(p, b, 0o644)
	}
}

// GetMomentEditMap loads the momentType -> SFX/BGM table, hot-reloading <AudioDir>/moment_edit_map.json
// whenever it changes on disk (zero rebuild or server restart needed).
func (l *Library) GetMomentEditMap() map[string]MomentEditRule {
	defaults := DefaultMomentEditMap()
	p := l.MomentMapPath()

	l.mu.Lock()
	l.ensureMomentMapFileLocked()
	fi, statErr := os.Stat(p)
	if statErr == nil && l.momentMap != nil && !fi.ModTime().After(l.momentMapModTime) {
		out := cloneMomentMap(l.momentMap)
		l.mu.Unlock()
		return out
	}
	l.mu.Unlock()

	merged := cloneMomentMap(defaults)
	var modTime time.Time
	if statErr == nil {
		modTime = fi.ModTime()
		if raw, err := os.ReadFile(p); err == nil {
			var parsed map[string]MomentEditRule
			if err := json.Unmarshal(raw, &parsed); err != nil {
				log.Printf("[AudioFX] WARNING: moment_edit_map.json invalid (%v), falling back to built-in defaults", err)
			} else {
				for k, v := range parsed {
					nk := strings.ToLower(strings.TrimSpace(k))
					if nk != "" {
						merged[nk] = v
					}
				}
			}
		}
	}

	l.mu.Lock()
	l.momentMap = cloneMomentMap(merged)
	l.momentMapModTime = modTime
	l.mu.Unlock()
	return merged
}

func cloneMomentMap(src map[string]MomentEditRule) map[string]MomentEditRule {
	out := make(map[string]MomentEditRule, len(src))
	for k, v := range src {
		out[k] = v
	}
	return out
}

func isBuiltInAudioID(id string) bool {
	if id == "" {
		return false
	}
	for _, spec := range builtInPack {
		if spec.ID == id {
			return true
		}
	}
	return false
}

// HasKnownAsset returns true if id is currently loaded in the library OR is part of the built-in pack.
func (l *Library) HasKnownAsset(id string) bool {
	id = strings.TrimSpace(id)
	if id == "" {
		return false
	}
	if _, ok := l.Get(id); ok {
		return true
	}
	return isBuiltInAudioID(id)
}

func (l *Library) pickAvailableAsset(primary, fallback string) string {
	if p := strings.TrimSpace(primary); p != "" && l.HasKnownAsset(p) {
		return p
	}
	if fb := strings.TrimSpace(fallback); fb != "" && l.HasKnownAsset(fb) {
		return fb
	}
	return ""
}

// ResolveAutoEditForMoment resolves the SFX & BGM picks for a given momentType.
// Explicit explicitSFX / explicitBGM ALWAYS win over automatic mapping.
func (l *Library) ResolveAutoEditForMoment(momentType, explicitSFX, explicitBGM string) model.AutoEditDecision {
	mt := strings.ToLower(strings.TrimSpace(momentType))
	expSFX := strings.TrimSpace(explicitSFX)
	expBGM := strings.TrimSpace(explicitBGM)

	dec := model.AutoEditDecision{
		MomentType: mt,
	}

	rules := l.GetMomentEditMap()
	rule, hasRule := rules[mt]

	var reasons []string

	// 1. SFX selection (explicit always wins!)
	if expSFX != "" {
		dec.SFXID = expSFX
		reasons = append(reasons, fmt.Sprintf("SFX explicit (%s)", expSFX))
	} else if hasRule {
		if picked := l.pickAvailableAsset(rule.SFXID, rule.FallbackSFXID); picked != "" {
			dec.SFXID = picked
			reasons = append(reasons, fmt.Sprintf("SFX auto[%s]=%s", mt, picked))
		}
	}

	// 2. BGM selection (explicit always wins!)
	if expBGM != "" {
		dec.BGMID = expBGM
		reasons = append(reasons, fmt.Sprintf("BGM explicit (%s)", expBGM))
	} else if hasRule {
		if picked := l.pickAvailableAsset(rule.BGMID, rule.FallbackBGMID); picked != "" {
			dec.BGMID = picked
			reasons = append(reasons, fmt.Sprintf("BGM auto[%s]=%s", mt, picked))
		}
	}

	if hasRule && rule.Reason != "" && expSFX == "" && expBGM == "" {
		dec.Reason = fmt.Sprintf("%s (%s)", rule.Reason, strings.Join(reasons, ", "))
	} else if len(reasons) > 0 {
		dec.Reason = strings.Join(reasons, "; ")
	} else if mt != "" {
		dec.Reason = fmt.Sprintf("no auto-edit rule for momentType '%s'", mt)
	}

	return dec
}
