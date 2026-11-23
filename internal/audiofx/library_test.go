package audiofx

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"klipforge/internal/deps"
	"klipforge/internal/model"
)

func TestAudioLibraryBuiltInCountsAndSynthesis(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg tidak ditemukan di PATH")
	}
	tmp := t.TempDir()
	d := deps.NewManager(tmp)
	lib := &Library{
		AudioDir: filepath.Join(tmp, "audio"),
		MetaPath: filepath.Join(tmp, "audio_library.json"),
		Deps:     d,
		assets:   make(map[string]*model.AudioAsset),
	}
	_ = os.MkdirAll(lib.AudioDir, 0o755)
	lib.EnsureBuiltIns()

	list := lib.List()
	var sfxCnt, bgmCnt int
	for _, a := range list {
		fi, err := os.Stat(a.FilePath)
		if err != nil || fi.Size() < 1024 {
			t.Fatalf("synthesized audio %s (%s) missing or too small: err=%v", a.ID, a.FilePath, err)
		}
		if a.Kind == "sfx" {
			sfxCnt++
		} else if a.Kind == "bgm" {
			bgmCnt++
		}
	}
	if sfxCnt < 30 {
		t.Fatalf("expected >= 30 built-in SFX, got %d", sfxCnt)
	}
	if bgmCnt < 12 {
		t.Fatalf("expected >= 12 built-in BGM, got %d", bgmCnt)
	}

	// Verify presets CRUD
	presets := lib.ListPresets()
	if len(presets) < 6 {
		t.Fatalf("expected >= 6 built-in smart presets, got %d", len(presets))
	}
	saved, err := lib.SavePreset(model.AudioPreset{
		Name:       "Preset Custom Windah",
		MomentType: "custom",
		Icon:       "⭐",
		BGMAssetID: "bgm-phonk-drift",
		BGMVolume:  0.18,
		SFXCues:    []model.SFXCue{{AssetID: "sfx-roblox-oof", AtSec: 0, Volume: 1.0}},
	})
	if err != nil || saved.ID == "" {
		t.Fatalf("SavePreset failed: %v", err)
	}
	if err := lib.DeletePreset(saved.ID); err != nil {
		t.Fatalf("DeletePreset failed: %v", err)
	}
}
