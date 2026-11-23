package renderer

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"klipforge/internal/model"
)

func TestEvaluateCompliance_WatermarkCases(t *testing.T) {
	tmp := t.TempDir()
	wmFile := filepath.Join(tmp, "wm.png")
	if err := os.WriteFile(wmFile, []byte("fake-png"), 0o644); err != nil {
		t.Fatalf("write fake watermark: %v", err)
	}

	hasWMWarning := func(c model.ComplianceCheck) bool {
		for _, w := range c.Warnings {
			if strings.Contains(strings.ToLower(w), "watermark") {
				return true
			}
		}
		return false
	}

	// Case 1: Watermark ON + file present -> applied=true, NO warning
	campPresent := &model.Campaign{
		Watermark: model.WatermarkConfig{Enabled: true, FilePath: wmFile},
		Hashtags:  []string{"#motionklip"},
	}
	c1 := evaluateCompliance(model.RenderOptions{UseWatermark: true, HookText: "TES"}, campPresent)
	if !c1.WatermarkApplied {
		t.Fatalf("case 1: expected WatermarkApplied=true, got false")
	}
	if hasWMWarning(c1) {
		t.Fatalf("case 1: expected no watermark warning when file exists, got %v", c1.Warnings)
	}

	// Case 2: Watermark ON + file missing -> applied=false, WARNING present
	campMissing := &model.Campaign{
		Watermark: model.WatermarkConfig{Enabled: true, FilePath: filepath.Join(tmp, "does-not-exist.png")},
		Hashtags:  []string{"#motionklip"},
	}
	c2 := evaluateCompliance(model.RenderOptions{UseWatermark: true, HookText: "TES"}, campMissing)
	if c2.WatermarkApplied {
		t.Fatalf("case 2: expected WatermarkApplied=false when file is missing")
	}
	if !hasWMWarning(c2) {
		t.Fatalf("case 2: expected watermark missing warning, got %v", c2.Warnings)
	}

	// Case 3: Watermark OFF (even if campaign has Watermark.Enabled=true & file missing) -> NO false alarm!
	c3 := evaluateCompliance(model.RenderOptions{UseWatermark: false, HookText: "TES"}, campMissing)
	if c3.WatermarkApplied {
		t.Fatalf("case 3: expected WatermarkApplied=false when UseWatermark=false")
	}
	if hasWMWarning(c3) {
		t.Fatalf("case 3: false alarm! expected NO watermark warning when UseWatermark=false, got %v", c3.Warnings)
	}
}
