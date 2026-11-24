package draft

import (
	"archive/zip"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"strings"
	"time"

	"klipforge/internal/model"
)

// Pinned Jianying / CapCut desktop draft schema version (modeled on GuanYixuan/pyJianYingDraft).
// Logged in API responses so users know the exact targeted CapCut/Jianying version.
const JianyingSchemaVersion = "jianying-draft-5.9.0"

// ExportInput bundles everything needed to serialize a RenderJob into an editable Jianying/CapCut desktop draft.
type ExportInput struct {
	Job         *model.RenderJob
	Cues        []model.TranscriptCue
	AudioLookup func(assetID string) (filePath string, name string, duration float64, ok bool)
	OutDir      string
}

type jyTimerange struct {
	Start    int64 `json:"start"`    // microseconds
	Duration int64 `json:"duration"` // microseconds
}

type jySegment struct {
	ID              string      `json:"id"`
	MaterialID      string      `json:"material_id"`
	TargetTimerange jyTimerange `json:"target_timerange"`
	SourceTimerange jyTimerange `json:"source_timerange"`
	Speed           float64     `json:"speed"`
	Volume          float64     `json:"volume"`
	ExtraRefs       []string    `json:"extra_material_refs"`
	Clip            map[string]interface{} `json:"clip,omitempty"`
}

type jyTrack struct {
	ID       string      `json:"id"`
	Type     string      `json:"type"` // "video", "audio", "text"
	Name     string      `json:"name"`
	Flag     int         `json:"flag"`
	Segments []jySegment `json:"segments"`
}

func secToUS(sec float64) int64 {
	if sec < 0 {
		sec = 0
	}
	return int64(math.Round(sec * 1_000_000.0))
}

func makeUID(prefix string, idx int) string {
	return fmt.Sprintf("%s-%04d-%d", prefix, idx, time.Now().UnixNano()%1_000_000)
}

// ExportJianyingDraft serializes a RenderJob's segments, SFX, BGM, text overlays, and subtitles
// into Jianying / CapCut desktop draft JSON files (draft_content.json + draft_meta_info.json)
// packaged into a downloadable .zip folder ready to extract into JianyingPro Drafts.
func ExportJianyingDraft(in ExportInput) (*model.JianyingDraftInfo, map[string]interface{}, error) {
	if in.Job == nil {
		return nil, nil, fmt.Errorf("job tidak boleh kosong")
	}
	if in.OutDir == "" {
		return nil, nil, fmt.Errorf("outDir wajib diisi")
	}
	_ = os.MkdirAll(in.OutDir, 0o755)

	outW, outH := 1080, 1920
	if in.Job.Options.Layout.AspectRatio == "16:9" {
		outW, outH = 1920, 1080
	} else if in.Job.Options.Layout.AspectRatio == "1:1" {
		outW, outH = 1080, 1080
	}

	var videosMat []map[string]interface{}
	var audiosMat []map[string]interface{}
	var textsMat []map[string]interface{}
	var speedsMat []map[string]interface{}

	videoTrack := jyTrack{ID: makeUID("trk-vid", 1), Type: "video", Name: "Main Video"}
	audioBGMTrack := jyTrack{ID: makeUID("trk-bgm", 2), Type: "audio", Name: "BGM"}
	audioSFXTrack := jyTrack{ID: makeUID("trk-sfx", 3), Type: "audio", Name: "SFX"}
	textTrack := jyTrack{ID: makeUID("trk-txt", 4), Type: "text", Name: "Subtitles & Hooks"}

	var cursorUS int64 = 0
	segCounter := 0

	addVideoSeg := func(path, name string, srcStartSec, durSec, speed float64, zoomScale float64, overlayText string, endSfxID string, endSfxOffset float64) {
		if durSec <= 0 {
			durSec = 10
		}
		if speed < 0.5 || speed > 2.0 {
			speed = 1.0
		}
		segCounter++
		matID := makeUID("mat-vid", segCounter)
		spdID := makeUID("mat-spd", segCounter)
		effDurUS := secToUS(durSec / speed)
		srcDurUS := secToUS(durSec)

		videosMat = append(videosMat, map[string]interface{}{
			"id":            matID,
			"material_name": name,
			"path":          path,
			"type":          "video",
			"width":         outW,
			"height":        outH,
			"duration":      srcDurUS,
		})
		speedsMat = append(speedsMat, map[string]interface{}{
			"id":    spdID,
			"mode":  0,
			"speed": speed,
			"type":  "speed",
		})
		scale := 1.0
		if zoomScale >= 1.02 && zoomScale <= 1.60 {
			scale = zoomScale
		}
		videoTrack.Segments = append(videoTrack.Segments, jySegment{
			ID:              makeUID("seg-vid", segCounter),
			MaterialID:      matID,
			SourceTimerange: jyTimerange{Start: secToUS(srcStartSec), Duration: srcDurUS},
			TargetTimerange: jyTimerange{Start: cursorUS, Duration: effDurUS},
			Speed:           speed,
			Volume:          1.0,
			ExtraRefs:       []string{spdID},
			Clip: map[string]interface{}{
				"scale": map[string]float64{"x": scale, "y": scale},
			},
		})

		if strings.TrimSpace(overlayText) != "" {
			tID := makeUID("mat-txt", segCounter)
			textsMat = append(textsMat, map[string]interface{}{
				"id":        tID,
				"type":      "text",
				"content":   fmt.Sprintf(`{"text":%q,"styles":[{"size":12.0,"fill":{"content":{"solid":{"color":[1,0.92,0]}}}}]}`, overlayText),
				"font_size": 12.0,
			})
			ovDur := effDurUS
			if ovDur > 4_000_000 {
				ovDur = 4_000_000
			}
			textTrack.Segments = append(textTrack.Segments, jySegment{
				ID:              makeUID("seg-txt", segCounter),
				MaterialID:      tID,
				SourceTimerange: jyTimerange{Start: 0, Duration: ovDur},
				TargetTimerange: jyTimerange{Start: cursorUS, Duration: ovDur},
				Speed:           1.0,
				Volume:          1.0,
			})
		}

		if endSfxID != "" && in.AudioLookup != nil {
			if fpath, aname, adur, ok := in.AudioLookup(endSfxID); ok {
				if adur <= 0 {
					adur = 1.2
				}
				aID := makeUID("mat-sfx", segCounter)
				sfxDurUS := secToUS(adur)
				audiosMat = append(audiosMat, map[string]interface{}{
					"id":       aID,
					"name":     aname,
					"path":     fpath,
					"type":     "sound",
					"duration": sfxDurUS,
				})
				off := endSfxOffset
				if off <= 0 {
					off = 1.0
				}
				atUS := cursorUS + effDurUS - secToUS(off)
				if atUS < cursorUS {
					atUS = cursorUS
				}
				audioSFXTrack.Segments = append(audioSFXTrack.Segments, jySegment{
					ID:              makeUID("seg-sfx", segCounter),
					MaterialID:      aID,
					SourceTimerange: jyTimerange{Start: 0, Duration: sfxDurUS},
					TargetTimerange: jyTimerange{Start: atUS, Duration: sfxDurUS},
					Speed:           1.0,
					Volume:          0.95,
				})
			}
		}

		cursorUS += effDurUS
	}

	// Populate video segments from either CompileOpts (Top-N / Edit-Script) or single RenderOptions
	if in.Job.CompileOpts != nil && len(in.Job.CompileOpts.Segments) > 0 {
		for idx, seg := range in.Job.CompileOpts.Segments {
			spd := seg.Speed
			if spd <= 0 {
				spd = 1.0
			}
			zScale := 1.0
			if seg.Zoom != nil && seg.Zoom.Scale > 1.0 {
				zScale = seg.Zoom.Scale
			}
			ovTxt := ""
			if seg.TextOverlay != nil {
				ovTxt = seg.TextOverlay.Text
			}
			lbl := seg.Label
			if lbl == "" {
				lbl = fmt.Sprintf("Segment %d", idx+1)
			}
			srcPath := in.Job.OutputPath
			if srcPath == "" {
				srcPath = fmt.Sprintf("segment_%d.mp4", idx+1)
			}
			addVideoSeg(srcPath, lbl, seg.StartSec, seg.EndSec-seg.StartSec, spd, zScale, ovTxt, seg.EndSFXID, seg.EndSFXOffset)
		}
	} else {
		dur := in.Job.FinalDuration
		if dur <= 0 {
			dur = in.Job.Options.EndSec - in.Job.Options.StartSec
		}
		if dur <= 0 {
			dur = 25
		}
		srcPath := in.Job.OutputPath
		if srcPath == "" {
			srcPath = in.Job.OutputFile
		}
		if srcPath == "" {
			srcPath = "clip.mp4"
		}
		spd := in.Job.Options.VideoFX.Speed
		if spd <= 0 {
			spd = 1.0
		}
		addVideoSeg(srcPath, in.Job.Title, 0, dur, spd, 1.0, in.Job.Options.HookText, "", 0)
	}

	// Optional BGM track spanning full timeline
	bgmID := in.Job.Options.AudioFX.BGMAssetID
	if bgmID == "" && in.Job.CompileOpts != nil {
		bgmID = in.Job.CompileOpts.AudioFX.BGMAssetID
	}
	if bgmID == "" && in.Job.AppliedPlan != nil {
		bgmID = in.Job.AppliedPlan.BGMAssetID
	}
	if bgmID != "" && in.AudioLookup != nil && cursorUS > 0 {
		if fpath, aname, adur, ok := in.AudioLookup(bgmID); ok {
			if adur <= 0 {
				adur = 16
			}
			aID := makeUID("mat-bgm", 1)
			audiosMat = append(audiosMat, map[string]interface{}{
				"id":       aID,
				"name":     aname,
				"path":     fpath,
				"type":     "music",
				"duration": secToUS(adur),
			})
			vol := in.Job.Options.AudioFX.BGMVolume
			if vol <= 0 {
				vol = 0.18
			}
			audioBGMTrack.Segments = append(audioBGMTrack.Segments, jySegment{
				ID:              makeUID("seg-bgm", 1),
				MaterialID:      aID,
				SourceTimerange: jyTimerange{Start: 0, Duration: cursorUS},
				TargetTimerange: jyTimerange{Start: 0, Duration: cursorUS},
				Speed:           1.0,
				Volume:          vol,
			})
		}
	}

	// Optional Subtitle cues onto Text track
	clipStart := in.Job.Options.StartSec
	clipEnd := in.Job.Options.EndSec
	subIdx := 0
	for _, cue := range in.Cues {
		if clipEnd > clipStart && (cue.End <= clipStart || cue.Start >= clipEnd) {
			continue
		}
		relS := cue.Start - clipStart
		relE := cue.End - clipStart
		if relS < 0 {
			relS = 0
		}
		if relE <= relS+0.05 {
			continue
		}
		subIdx++
		tID := makeUID("mat-sub", subIdx)
		textsMat = append(textsMat, map[string]interface{}{
			"id":        tID,
			"type":      "subtitle",
			"speaker":   cue.Speaker,
			"content":   fmt.Sprintf(`{"text":%q,"styles":[{"size":8.0,"fill":{"content":{"solid":{"color":[1,1,1]}}}}]}`, strings.TrimSpace(cue.Text)),
			"font_size": 8.0,
		})
		textTrack.Segments = append(textTrack.Segments, jySegment{
			ID:              makeUID("seg-sub", subIdx),
			MaterialID:      tID,
			SourceTimerange: jyTimerange{Start: 0, Duration: secToUS(relE - relS)},
			TargetTimerange: jyTimerange{Start: secToUS(relS), Duration: secToUS(relE - relS)},
			Speed:           1.0,
			Volume:          1.0,
		})
	}

	tracks := []jyTrack{videoTrack}
	if len(audioBGMTrack.Segments) > 0 {
		tracks = append(tracks, audioBGMTrack)
	}
	if len(audioSFXTrack.Segments) > 0 {
		tracks = append(tracks, audioSFXTrack)
	}
	if len(textTrack.Segments) > 0 {
		tracks = append(tracks, textTrack)
	}

	totalSegs := 0
	for _, tr := range tracks {
		totalSegs += len(tr.Segments)
	}

	nowUS := time.Now().UnixMicro()
	draftID := fmt.Sprintf("KF-DRAFT-%s", in.Job.ID)

	draftContent := map[string]interface{}{
		"id":            draftID,
		"version":       360000,
		"schema_pin":    JianyingSchemaVersion,
		"generator":     "KlipForge",
		"fps":           30.0,
		"duration":      cursorUS,
		"canvas_config": map[string]interface{}{"width": outW, "height": outH, "ratio": "original"},
		"materials": map[string]interface{}{
			"videos": videosMat,
			"audios": audiosMat,
			"texts":  textsMat,
			"speeds": speedsMat,
		},
		"tracks": tracks,
	}

	draftMeta := map[string]interface{}{
		"draft_id":             draftID,
		"draft_name":           in.Job.Title,
		"draft_schema_version": JianyingSchemaVersion,
		"tm_draft_create":      nowUS,
		"tm_draft_modified":    nowUS,
		"tm_duration":          cursorUS,
		"draft_fold_path":      "",
		"draft_root_path":      "",
	}

	contentBytes, err := json.MarshalIndent(draftContent, "", "  ")
	if err != nil {
		return nil, nil, err
	}
	metaBytes, err := json.MarshalIndent(draftMeta, "", "  ")
	if err != nil {
		return nil, nil, err
	}

	// Write standalone draft_content.json AND zip bundle (<jobID>_jianying_draft.zip)
	folderSlug := fmt.Sprintf("klipforge_draft_%s", in.Job.ID)
	zipName := folderSlug + ".zip"
	zipPath := filepath.Join(in.OutDir, zipName)
	zf, err := os.Create(zipPath)
	if err != nil {
		return nil, nil, err
	}
	zw := zip.NewWriter(zf)
	writeZipEntry := func(name string, data []byte) error {
		w, err := zw.Create(name)
		if err != nil {
			return err
		}
		_, err = io.Copy(w, strings.NewReader(string(data)))
		return err
	}
	if err := writeZipEntry(folderSlug+"/draft_content.json", contentBytes); err != nil {
		zw.Close()
		zf.Close()
		return nil, nil, err
	}
	if err := writeZipEntry(folderSlug+"/draft_meta_info.json", metaBytes); err != nil {
		zw.Close()
		zf.Close()
		return nil, nil, err
	}
	if err := zw.Close(); err != nil {
		zf.Close()
		return nil, nil, err
	}
	_ = zf.Close()

	info := &model.JianyingDraftInfo{
		FileName:      zipName,
		FilePath:      zipPath,
		DownloadURL:   fmt.Sprintf("/api/jobs/%s/draft/download", in.Job.ID),
		SchemaVersion: JianyingSchemaVersion,
		TrackCount:    len(tracks),
		SegmentCount:  totalSegs,
		DurationUS:    cursorUS,
		GeneratedAt:   time.Now(),
	}
	return info, draftContent, nil
}
