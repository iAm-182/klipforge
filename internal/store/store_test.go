package store

import (
	"os"
	"path/filepath"
	"testing"

	"klipforge/internal/model"
)

func TestStoreRollingBackupAndCorruptRecovery(t *testing.T) {
	tmp := t.TempDir()
	st, err := NewStore(tmp)
	if err != nil {
		t.Fatalf("NewStore failed: %v", err)
	}

	// Save custom campaign twice so .bak.1 is populated with valid data
	custom := &model.Campaign{
		ID:   "camp-test-recovery",
		Name: "Campaign Penting Jangan Hilang",
	}
	if err := st.SaveCampaign(custom); err != nil {
		t.Fatalf("SaveCampaign 1 failed: %v", err)
	}
	custom.Name = "Campaign Penting V2"
	if err := st.SaveCampaign(custom); err != nil {
		t.Fatalf("SaveCampaign 2 failed: %v", err)
	}

	campFile := filepath.Join(tmp, "data", "campaigns.json")
	if _, err := os.Stat(campFile + ".bak.1"); err != nil {
		t.Fatalf("expected rolling backup .bak.1 to exist: %v", err)
	}

	// Simulate disk corruption of primary campaigns.json!
	if err := os.WriteFile(campFile, []byte("{corrupt broken json!!!"), 0o644); err != nil {
		t.Fatalf("failed to corrupt file: %v", err)
	}

	// Reload store: MUST auto-recover from .bak.1 instead of dropping campaigns!
	st2, err := NewStore(tmp)
	if err != nil {
		t.Fatalf("NewStore reload failed: %v", err)
	}
	got, ok := st2.GetCampaign("camp-test-recovery")
	if !ok {
		t.Fatalf("expected camp-test-recovery to be recovered from .bak.1!")
	}
	if got.Name == "" {
		t.Fatalf("recovered campaign has empty name")
	}

	// Also verify GetDiskStats returns valid positive volume metrics
	ds := GetDiskStats(st2.OutputDir, st2.DataDir, 2.0)
	if ds.TotalBytes == 0 || ds.FreeGB <= 0 {
		t.Fatalf("expected valid disk stats, got %+v", ds)
	}
}
