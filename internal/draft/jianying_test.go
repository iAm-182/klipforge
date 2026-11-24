package draft

import (
	"archive/zip"
	"encoding/json"
	"io"
	"testing"

	"klipforge/internal/model"
)

func TestExportJianyingDraft_ValidZipAndSchemaPin(t *testing.T) {
	job := &model.RenderJob{
		ID:            "comp-test-999",
		Title:         "Windah GTA VI Nabrak",
		OutputPath:    "/data/workspace/klipforge/outputs/clip.mp4",
		FinalDuration: 30,
		CompileOpts: &model.CompileOptions{
			Mode: "edit_script",
			AudioFX: model.AudioFXConfig{
				BGMAssetID: "bgm-funny-bouncy",
				BGMVolume:  0.18,
			},
			Segments: []model.CompileSegment{
				{
					StartSec: 10, EndSec: 25, Label: "baku tembak",
					Speed:       1.0,
					Zoom:        &model.ZoomPunch{Scale: 1.25},
					TextOverlay: &model.SegmentTextOverlay{Text: "DIKIT LAGI MATI"},
					EndSFXID:    "sfx-record-scratch",
				},
				{
					StartSec: 40, EndSec: 55, Label: "nabrak",
					Speed:    1.15,
					EndSFXID: "sfx-metal-pipe",
				},
			},
		},
	}

	cues := []model.TranscriptCue{
		{Start: 1, End: 3, Text: "gua jago nyetir", Speaker: "SPEAKER_0"},
		{Start: 4, End: 6, Text: "stop right there!", Speaker: "SPEAKER_1"},
	}

	info, content, err := ExportJianyingDraft(ExportInput{
		Job:    job,
		Cues:   cues,
		OutDir: t.TempDir(),
		AudioLookup: func(id string) (string, string, float64, bool) {
			return "/audio/" + id + ".wav", id, 1.5, true
		},
	})
	if err != nil {
		t.Fatalf("ExportJianyingDraft failed: %v", err)
	}
	if info.SchemaVersion != JianyingSchemaVersion {
		t.Fatalf("expected pinned schema %q, got %q", JianyingSchemaVersion, info.SchemaVersion)
	}
	if info.TrackCount < 4 || info.SegmentCount < 5 || info.DurationUS <= 0 {
		t.Fatalf("unexpected draft stats: %+v", info)
	}
	if content["schema_pin"] != JianyingSchemaVersion {
		t.Fatalf("expected schema_pin inside draft_content.json")
	}

	// Open the generated ZIP and verify both draft_content.json and draft_meta_info.json parse cleanly!
	zr, err := zip.OpenReader(info.FilePath)
	if err != nil {
		t.Fatalf("zip open failed: %v", err)
	}
	defer zr.Close()
	if len(zr.File) != 2 {
		t.Fatalf("expected 2 files in draft zip, got %d", len(zr.File))
	}
	for _, f := range zr.File {
		rc, err := f.Open()
		if err != nil {
			t.Fatalf("open %s: %v", f.Name, err)
		}
		raw, _ := io.ReadAll(rc)
		rc.Close()
		var check map[string]interface{}
		if err := json.Unmarshal(raw, &check); err != nil {
			t.Fatalf("invalid JSON in %s: %v", f.Name, err)
		}
	}
}
