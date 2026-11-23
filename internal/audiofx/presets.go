package audiofx

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"klipforge/internal/model"
)

var defaultAudioPresets = []model.AudioPreset{
	{
		ID:            "preset-crash",
		Name:          "Crash / Nabrak!",
		MomentType:    "crash",
		Icon:          "💥",
		BGMAssetID:    "",
		BGMVolume:     0.15,
		BGMFadeInSec:  0.3,
		BGMFadeOutSec: 0.8,
		BGMAutoDuck:   true,
		SFXCues: []model.SFXCue{
			{AssetID: "sfx-metal-pipe", AtSec: 0.0, Volume: 1.05},
			{AssetID: "sfx-boom", AtSec: 0.25, Volume: 0.95},
		},
		BuiltIn: true,
	},
	{
		ID:            "preset-fail",
		Name:          "Fail / Gagal Kocak",
		MomentType:    "fail",
		Icon:          "💀",
		BGMAssetID:    "bgm-funny-bouncy",
		BGMVolume:     0.14,
		BGMFadeInSec:  0.3,
		BGMFadeOutSec: 0.9,
		BGMAutoDuck:   true,
		SFXCues: []model.SFXCue{
			{AssetID: "sfx-record-scratch", AtSec: 0.0, Volume: 1.0},
			{AssetID: "sfx-sad-trombone", AtSec: 0.35, Volume: 0.9},
		},
		BuiltIn: true,
	},
	{
		ID:            "preset-hype",
		Name:          "Hype / Clutch Moment",
		MomentType:    "hype",
		Icon:          "🔥",
		BGMAssetID:    "bgm-phonk-drift",
		BGMVolume:     0.16,
		BGMFadeInSec:  0.4,
		BGMFadeOutSec: 0.9,
		BGMAutoDuck:   true,
		SFXCues: []model.SFXCue{
			{AssetID: "sfx-riser", AtSec: -1.5, Volume: 0.85},
			{AssetID: "sfx-bassdrop", AtSec: 0.0, Volume: 1.1},
		},
		BuiltIn: true,
	},
	{
		ID:            "preset-twist",
		Name:          "Kaget / Plot Twist",
		MomentType:    "twist",
		Icon:          "😱",
		BGMAssetID:    "bgm-suspense",
		BGMVolume:     0.15,
		BGMFadeInSec:  0.3,
		BGMFadeOutSec: 0.8,
		BGMAutoDuck:   true,
		SFXCues: []model.SFXCue{
			{AssetID: "sfx-dun-dun-dun", AtSec: 0.0, Volume: 1.0},
			{AssetID: "sfx-alert", AtSec: 0.15, Volume: 0.9},
		},
		BuiltIn: true,
	},
	{
		ID:            "preset-victory",
		Name:          "Victory / GGWP!",
		MomentType:    "victory",
		Icon:          "🏆",
		BGMAssetID:    "bgm-victory-fanfare",
		BGMVolume:     0.15,
		BGMFadeInSec:  0.3,
		BGMFadeOutSec: 1.0,
		BGMAutoDuck:   true,
		SFXCues: []model.SFXCue{
			{AssetID: "sfx-airhorn", AtSec: 0.0, Volume: 0.95},
			{AssetID: "sfx-coin", AtSec: 0.45, Volume: 0.9},
		},
		BuiltIn: true,
	},
	{
		ID:            "preset-meme-roblox",
		Name:          "Meme Oof / Ngakak",
		MomentType:    "meme",
		Icon:          "🤡",
		BGMAssetID:    "bgm-sneaky-troll",
		BGMVolume:     0.13,
		BGMFadeInSec:  0.3,
		BGMFadeOutSec: 0.8,
		BGMAutoDuck:   true,
		SFXCues: []model.SFXCue{
			{AssetID: "sfx-roblox-oof", AtSec: 0.0, Volume: 1.05},
			{AssetID: "sfx-boing", AtSec: 0.35, Volume: 0.9},
			{AssetID: "sfx-honk", AtSec: 0.75, Volume: 0.85},
		},
		BuiltIn: true,
	},
}

func (l *Library) presetsPath() string {
	return filepath.Join(filepath.Dir(l.MetaPath), "audio_presets.json")
}

func (l *Library) loadCustomPresetsLocked() []model.AudioPreset {
	var custom []model.AudioPreset
	if b, err := os.ReadFile(l.presetsPath()); err == nil {
		_ = json.Unmarshal(b, &custom)
	}
	return custom
}

func (l *Library) saveCustomPresetsLocked(list []model.AudioPreset) error {
	b, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(l.presetsPath(), b, 0o644)
}

// ListPresets returns all built-in Smart Moment Presets plus user-saved custom presets.
func (l *Library) ListPresets() []model.AudioPreset {
	l.mu.RLock()
	defer l.mu.RUnlock()
	custom := l.loadCustomPresetsLocked()
	sort.Slice(custom, func(i, j int) bool { return custom[i].Name < custom[j].Name })
	out := make([]model.AudioPreset, 0, len(defaultAudioPresets)+len(custom))
	out = append(out, defaultAudioPresets...)
	out = append(out, custom...)
	return out
}

// SavePreset stores a reusable user custom SFX+BGM preset.
func (l *Library) SavePreset(p model.AudioPreset) (model.AudioPreset, error) {
	p.Name = strings.TrimSpace(p.Name)
	if p.Name == "" {
		return model.AudioPreset{}, fmt.Errorf("nama preset wajib diisi")
	}
	if p.ID == "" || strings.HasPrefix(p.ID, "preset-") {
		p.ID = fmt.Sprintf("custom-preset-%d", time.Now().UnixNano())
	}
	if p.Icon == "" {
		p.Icon = "⭐"
	}
	if p.MomentType == "" {
		p.MomentType = "custom"
	}
	if p.BGMFadeInSec <= 0 {
		p.BGMFadeInSec = 0.35
	}
	if p.BGMFadeOutSec <= 0 {
		p.BGMFadeOutSec = 0.85
	}
	p.BuiltIn = false

	l.mu.Lock()
	defer l.mu.Unlock()
	custom := l.loadCustomPresetsLocked()
	replaced := false
	for i := range custom {
		if custom[i].ID == p.ID {
			custom[i] = p
			replaced = true
			break
		}
	}
	if !replaced {
		custom = append(custom, p)
	}
	if err := l.saveCustomPresetsLocked(custom); err != nil {
		return model.AudioPreset{}, err
	}
	return p, nil
}

// DeletePreset deletes a user-saved custom preset (built-ins cannot be deleted).
func (l *Library) DeletePreset(id string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	custom := l.loadCustomPresetsLocked()
	next := make([]model.AudioPreset, 0, len(custom))
	for _, p := range custom {
		if p.ID != id {
			next = append(next, p)
		}
	}
	return l.saveCustomPresetsLocked(next)
}
