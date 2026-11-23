package audiofx

import (
	"encoding/json"
	"os"
	"testing"
	"time"

	"klipforge/internal/deps"
)

func TestMomentEditMap_AllMomentTypesAndOverrides(t *testing.T) {
	tmp := t.TempDir()
	lib := NewLibrary(tmp, deps.NewManager(tmp))

	cases := []struct {
		moment  string
		wantSFX string
		wantBGM string
	}{
		{"fail", "sfx-record-scratch", "bgm-funny-bouncy"},
		{"meme", "sfx-record-scratch", "bgm-funny-bouncy"},
		{"crash", "sfx-metal-pipe", "bgm-gaming-pulse"},
		{"victory", "sfx-airhorn", "bgm-victory-fanfare"},
		{"hype", "sfx-riser", "bgm-phonk-drift"},
		{"twist", "sfx-dun-dun-dun", "bgm-suspense"},
	}

	for _, tc := range cases {
		dec := lib.ResolveAutoEditForMoment(tc.moment, "", "")
		if dec.SFXID != tc.wantSFX || dec.BGMID != tc.wantBGM {
			t.Fatalf("moment %q: expected (%s, %s), got (%s, %s) reason=%q",
				tc.moment, tc.wantSFX, tc.wantBGM, dec.SFXID, dec.BGMID, dec.Reason)
		}
		if dec.Reason == "" {
			t.Fatalf("moment %q: expected non-empty reason", tc.moment)
		}
	}

	// Explicit SFX / BGM MUST always win over auto mapping!
	ov := lib.ResolveAutoEditForMoment("crash", "sfx-bonk", "bgm-chill-lofi")
	if ov.SFXID != "sfx-bonk" || ov.BGMID != "bgm-chill-lofi" {
		t.Fatalf("expected explicit overrides to win, got %+v", ov)
	}

	// Partial override: explicit SFX wins while BGM still comes from auto map
	part := lib.ResolveAutoEditForMoment("fail", "sfx-bruh", "")
	if part.SFXID != "sfx-bruh" || part.BGMID != "bgm-funny-bouncy" {
		t.Fatalf("expected explicit SFX + auto BGM, got %+v", part)
	}

	// Editable JSON file in audio dir must hot-reload WITHOUT rebuilding!
	mapPath := lib.MomentMapPath()
	if _, err := os.Stat(mapPath); err != nil {
		t.Fatalf("expected default moment_edit_map.json to be written at %s: %v", mapPath, err)
	}
	customMap := map[string]MomentEditRule{
		"crash": {
			SFXID:  "sfx-explosion-thud",
			BGMID:  "bgm-boss-battle",
			Reason: "custom json override for crash",
		},
	}
	raw, _ := json.MarshalIndent(customMap, "", "  ")
	// Ensure mtime advances even on fast filesystems
	time.Sleep(15 * time.Millisecond)
	if err := os.WriteFile(mapPath, raw, 0o644); err != nil {
		t.Fatalf("write custom moment_edit_map.json: %v", err)
	}
	future := time.Now().Add(2 * time.Second)
	_ = os.Chtimes(mapPath, future, future)

	reloaded := lib.ResolveAutoEditForMoment("crash", "", "")
	if reloaded.SFXID != "sfx-explosion-thud" || reloaded.BGMID != "bgm-boss-battle" {
		t.Fatalf("expected hot-reloaded JSON override, got %+v", reloaded)
	}
}
