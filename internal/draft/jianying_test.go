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

func TestExportJianyingDraft_SourceTimerangeTracksAndExtraRefs(t *testing.T) {
	// 1. Compilation WITHOUT local VOD: segment 2 must use accumulated cursorUS on rendered OutputPath (not VOD StartSec=40 > FinalDuration=30!)
	job := &model.RenderJob{
		ID:            "comp-range-1",
		Title:         "Multi Clip Draft",
		OutputPath:    "/outputs/rendered_30s.mp4",
		FinalDuration: 30,
		CompileOpts: &model.CompileOptions{
			Mode: "edit_script",
			Segments: []model.CompileSegment{
				{ProjectID: "p1", StartSec: 100, EndSec: 115, Label: "seg1", Speed: 1.0, TextOverlay: &model.SegmentTextOverlay{Text: "HOOK 1"}},
				{ProjectID: "p1", StartSec: 500, EndSec: 515, Label: "seg2", Speed: 1.0, TextOverlay: &model.SegmentTextOverlay{Text: "HOOK 2"}},
			},
		},
	}
	cues := []model.TranscriptCue{
		{Start: 102, End: 104, Text: "hello seg1"},
		{Start: 502, End: 505, Text: "hello seg2"},
	}
	_, content, err := ExportJianyingDraft(ExportInput{
		Job:    job,
		Cues:   cues,
		OutDir: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("export failed: %v", err)
	}
	raw, _ := json.Marshal(content)
	// Verify no `"extra_material_refs":null` anywhere in draft JSON
	if string(raw) == "" || containsSub(string(raw), `"extra_material_refs":null`) {
		t.Fatalf("found null extra_material_refs in draft_content.json")
	}

	// Verify separate hook & subtitle tracks exist and video segment 2 source start is 15s (not 500s!)
	tracks, ok := content["tracks"].([]jyTrack)
	if !ok {
		t.Fatalf("unexpected tracks type: %T", content["tracks"])
	}
	textTracks := 0
	for _, tr := range tracks {
		if tr.Type == "text" {
			textTracks++
		}
		if tr.Type == "video" {
			if len(tr.Segments) != 2 {
				t.Fatalf("expected 2 video segments, got %d", len(tr.Segments))
			}
			if tr.Segments[1].SourceTimerange.Start != 15_000_000 {
				t.Fatalf("expected seg2 source start=15000000us on rendered clip, got %+v", tr.Segments[1].SourceTimerange)
			}
		}
	}
	if textTracks < 2 {
		t.Fatalf("expected separate hook & subtitle text tracks (>=2), got %d", textTracks)
	}

	// 2. With ProjectLookup providing local VOD, segment 2 should reference local VOD at 500s
	_, contentLocal, err := ExportJianyingDraft(ExportInput{
		Job:    job,
		Cues:   cues,
		OutDir: t.TempDir(),
		ProjectLookup: func(projectID string) (string, float64) {
			return "/vods/full_vod.mp4", 1200
		},
	})
	if err != nil {
		t.Fatalf("export with ProjectLookup failed: %v", err)
	}
	tracksLocal := contentLocal["tracks"].([]jyTrack)
	for _, tr := range tracksLocal {
		if tr.Type == "video" {
			if tr.Segments[1].SourceTimerange.Start != 500_000_000 {
				t.Fatalf("expected seg2 local VOD source start=500000000us, got %+v", tr.Segments[1].SourceTimerange)
			}
		}
	}
}

func containsSub(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
