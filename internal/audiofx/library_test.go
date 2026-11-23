package audiofx

import (
	"os/exec"
	"testing"

	"klipforge/internal/deps"
	"klipforge/internal/model"
)

func TestMegaAudioPackAndPresets(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg tidak ditemukan di PATH")
	}
	tmp := t.TempDir()
	depMgr := deps.NewManager(tmp)
	lib := NewLibrary(tmp, depMgr)
	lib.EnsureBuiltIns()

	all := lib.List()
	bgmCount, sfxCount := 0, 0
	for _, a := range all {
		if a.Kind == "bgm" {
			bgmCount++
		} else if a.Kind == "sfx" {
			sfxCount++
		}
	}
	if bgmCount < 12 {
		t.Fatalf("expected >= 12 built-in BGM tracks, got %d", bgmCount)
	}
	if sfxCount < 30 {
		t.Fatalf("expected >= 30 built-in SFX tracks, got %d", sfxCount)
	}

	// Check viral Roblox-style & iconic CapCut SFX exist
	for _, reqID := range []string{"sfx-oof-roblox", "sfx-metal-pipe", "sfx-boom", "sfx-sad-trombone", "sfx-bruh", "sfx-bonk", "bgm-hype-phonk", "bgm-funny-bouncy"} {
		if _, ok := lib.Get(reqID); !ok {
			t.Fatalf("missing expected built-in audio asset %s", reqID)
		}
	}

	// Verify Smart Audio Presets (built-in + custom save/reload/delete)
	presets := lib.ListPresets()
	if len(presets) < 6 {
		t.Fatalf("expected >= 6 built-in smart audio presets, got %d", len(presets))
	}
	saved, err := lib.SavePreset(model.AudioPreset{
		Name:       "My Windah Crash Combo",
		MomentType: "crash",
		BGMAssetID: "bgm-hype-phonk",
		BGMVolume:  0.15,
		SFXCues:    []model.SFXCue{{AssetID: "sfx-oof-roblox", AtSec: 0, Volume: 1.0}},
	})
	if err != nil {
		t.Fatalf("SavePreset failed: %v", err)
	}
	if err := lib.DeletePreset(saved.ID); err != nil {
		t.Fatalf("DeletePreset failed: %v", err)
	}
}
