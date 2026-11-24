package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"klipforge/internal/analyzer"
	"klipforge/internal/draft"
	"klipforge/internal/model"
	"klipforge/internal/renderer"
	"klipforge/internal/youtube"
)

// handleExportJianyingDraft serializes a RenderJob's segments, SFX, BGM, text overlays, and subtitles
// into a version-pinned Jianying / CapCut desktop draft bundle (.zip). Agent-allowed (HTTP 200).
func (s *Server) handleExportJianyingDraft(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	job, ok := s.Store.GetJob(id)
	if !ok {
		writeErr(w, http.StatusNotFound, "Job tidak ditemukan")
		return
	}

	var cues []model.TranscriptCue
	if job.ProjectID != "" {
		if proj, ok := s.Store.GetProject(job.ProjectID); ok {
			cues = s.loadProjectCuesPreferDiarized(proj)
		}
	}

	draftsDir := filepath.Join(s.Store.DataDir, "drafts")
	info, content, err := draft.ExportJianyingDraft(draft.ExportInput{
		Job:    job,
		Cues:   cues,
		OutDir: draftsDir,
		AudioLookup: func(assetID string) (string, string, float64, bool) {
			if s.Audio == nil {
				return "", "", 0, false
			}
			a, ok := s.Audio.Get(assetID)
			if !ok {
				return "", "", 0, false
			}
			return a.FilePath, a.Name, a.Duration, true
		},
	})
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "Gagal ekspor Jianying draft: "+err.Error())
		return
	}

	job.Draft = info
	_ = s.Store.SaveJob(job)
	s.Broker.Publish("job_update", job)
	p := getPrincipal(r)
	s.recordActivity(p, "draft_export", "job", job.ID,
		fmt.Sprintf("%s mengekspor Jianying draft (%s) untuk %s", p.Name, info.SchemaVersion, job.Title), info.FileName, "info")

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"ok":            true,
		"jobId":         job.ID,
		"schemaVersion": info.SchemaVersion,
		"draft":         info,
		"draftContent":  content,
	})
}

func (s *Server) handleDownloadJianyingDraft(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	job, ok := s.Store.GetJob(id)
	if !ok || job.Draft == nil || job.Draft.FilePath == "" || !pathExists(job.Draft.FilePath) {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, job.Draft.FileName))
	http.ServeFile(w, r, job.Draft.FilePath)
}

func (s *Server) handleServeJobCover(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	job, ok := s.Store.GetJob(id)
	if !ok {
		http.NotFound(w, r)
		return
	}
	coverPath := job.CoverFramePath
	if coverPath == "" {
		cand := filepath.Join(s.Store.DataDir, "covers", id+".jpg")
		if pathExists(cand) {
			coverPath = cand
		}
	}
	if coverPath == "" || !pathExists(coverPath) {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Cache-Control", "public, max-age=3600")
	http.ServeFile(w, r, coverPath)
}

func (s *Server) loadProjectCuesPreferDiarized(proj *model.Project) []model.TranscriptCue {
	if proj == nil {
		return nil
	}
	diarPath := filepath.Join(s.Store.ProjectDir(proj.ID), "diarized_cues.json")
	if raw, err := os.ReadFile(diarPath); err == nil {
		var cues []model.TranscriptCue
		if json.Unmarshal(raw, &cues) == nil && len(cues) > 0 {
			return cues
		}
	}
	if proj.SubtitlePath != "" && pathExists(proj.SubtitlePath) {
		if cues, err := youtube.ParseSubtitleFile(proj.SubtitlePath); err == nil {
			return cues
		}
	}
	return nil
}

// handleDetectProjectScenes runs PySceneDetect (or offline FFmpeg scene fallback) on a project's video
// (or accepts explicit cuts[]) and snaps candidate boundaries to the nearest real shot cuts.
func (s *Server) handleDetectProjectScenes(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	proj, ok := s.Store.GetProject(id)
	if !ok {
		writeErr(w, http.StatusNotFound, "Project tidak ditemukan")
		return
	}
	var req struct {
		MediaPath  string    `json:"mediaPath"`
		Cuts       []float64 `json:"cuts"`
		MaxSnapSec float64   `json:"maxSnapSec"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	if req.MaxSnapSec <= 0 {
		req.MaxSnapSec = 3.0
	}

	var res analyzer.SceneCutResult
	if len(req.Cuts) > 0 {
		res = analyzer.SceneCutResult{Cuts: req.Cuts, Engine: "supplied"}
	} else {
		media := strings.TrimSpace(req.MediaPath)
		if media == "" {
			media = proj.LocalVideoPath
		}
		if media == "" || !pathExists(media) {
			writeErr(w, http.StatusBadRequest, "Video lokal project belum tersedia untuk scene-detect — kirim mediaPath atau jalankan render dengan sceneSnap=true")
			return
		}
		var err error
		res, err = analyzer.DetectSceneCuts(r.Context(), s.Store.GetSettings().SceneDetectBin, s.Deps.GetStatus().FFmpegPath, media)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
	}

	// Snap project candidate boundaries to nearest shot cuts!
	cands := make([]model.ClipCandidate, len(proj.Candidates))
	copy(cands, proj.Candidates)
	snappedCount := 0
	for i := range cands {
		ns, ne, snapped := analyzer.SnapBoundariesToScenes(cands[i].StartSec, cands[i].EndSec, cands[i].PeakSec, res.Cuts, req.MaxSnapSec, 10.0)
		if snapped {
			cands[i].StartSec = ns
			cands[i].EndSec = ne
			cands[i].ArcStartSec = ns
			cands[i].ArcEndSec = ne
			cands[i].DurationSec = ne - ns
			cands[i].SceneSnapped = true
			snappedCount++
		}
	}
	proj.Candidates = cands
	_ = s.Store.SaveProject(proj)
	s.Broker.Publish("project_update", proj)

	// Persist scene cuts cache for future re-analysis
	if b, err := json.Marshal(res); err == nil {
		_ = os.WriteFile(filepath.Join(s.Store.ProjectDir(proj.ID), "scenes.json"), b, 0o644)
	}

	p := getPrincipal(r)
	s.recordActivity(p, "scene_detect", "project", proj.ID,
		fmt.Sprintf("%s menyelaraskan %d kandidat ke %d shot cut (%s)", p.Name, snappedCount, len(res.Cuts), res.Engine), "", "info")

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"ok":           true,
		"projectId":    proj.ID,
		"engine":       res.Engine,
		"cutsCount":    len(res.Cuts),
		"cuts":         res.Cuts,
		"snappedCount": snappedCount,
	})
}

// handleDiarizeProject runs sherpa-onnx speaker diarization + VAD (with graceful offline fallback)
// to populate TranscriptCue.Speaker and re-score candidates with real Windah vs NPC speaker turns!
func (s *Server) handleDiarizeProject(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	proj, ok := s.Store.GetProject(id)
	if !ok {
		writeErr(w, http.StatusNotFound, "Project tidak ditemukan")
		return
	}

	cs := s.loadCachedSignals(proj)
	cues := cs.Transcript
	if len(cues) == 0 {
		cues = s.loadProjectCuesPreferDiarized(proj)
	}

	audioPath := proj.LocalVideoPath
	diar := analyzer.DiarizeMedia(r.Context(), s.Store.GetSettings().SherpaURL, audioPath, cues)
	diarCues := analyzer.AssignSpeakersToCues(cues, diar.Segments)

	// Save speaker-annotated cues to project dir so karaoke subtitles & Jianying drafts use them!
	projDir := s.Store.ProjectDir(proj.ID)
	if len(diarCues) > 0 {
		if b, err := json.Marshal(diarCues); err == nil {
			_ = os.WriteFile(filepath.Join(projDir, "diarized_cues.json"), b, 0o644)
		}
	}

	// Update in-memory cache and re-score candidates with speaker turns!
	s.cacheMu.Lock()
	if cur, exists := s.rawSignals[proj.ID]; exists {
		cur.Transcript = diarCues
	}
	s.cacheMu.Unlock()

	if len(diarCues) > 0 {
		// Attach SpeakerSummary onto existing candidates without discarding verdicts/board positions!
		cands := make([]model.ClipCandidate, len(proj.Candidates))
		copy(cands, proj.Candidates)
		for i := range cands {
			cands[i].SpeakerSummary = analyzer.SummarizeSpeakersInWindow(diarCues, cands[i].StartSec, cands[i].EndSec)
		}
		proj.Candidates = cands
		_ = s.Store.SaveProject(proj)
		s.Broker.Publish("project_update", proj)
	}

	// Build concise sample speaker turns (first 12) for verification & UI inspection
	sampleTurns := diar.Segments
	if len(sampleTurns) > 12 {
		sampleTurns = sampleTurns[:12]
	}

	p := getPrincipal(r)
	s.recordActivity(p, "diarize", "project", proj.ID,
		fmt.Sprintf("%s menjalankan diarization (%s, %d speaker turns) pada %s", p.Name, diar.Engine, len(diar.Segments), proj.Title), diar.Note, "info")

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"ok":             true,
		"projectId":      proj.ID,
		"engine":         diar.Engine,
		"fallback":       diar.Fallback,
		"primarySpeaker": diar.PrimarySpeaker,
		"speakerTotals":  diar.SpeakerTotals,
		"turnCount":      len(diar.Segments),
		"vadCount":       len(diar.VADSegments),
		"sampleTurns":    sampleTurns,
		"note":           diar.Note,
		"ttsIndonesian": map[string]string{
			"verified": "true",
			"model":    analyzer.VerifiedIndonesianTTSModel,
			"url":      analyzer.VerifiedIndonesianTTSURL,
		},
	})
}

// handleDetectProjectFacecam auto-detects streamer webcam coordinates (edge/corner YuNet/OpenCV scan
// over local video frames or preview thumbnail in Audio-First mode) and saves them on the Project.
func (s *Server) handleDetectProjectFacecam(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	proj, ok := s.Store.GetProject(id)
	if !ok {
		writeErr(w, http.StatusNotFound, "Project tidak ditemukan")
		return
	}

	videoPath := ""
	if proj.LocalVideoPath != "" && pathExists(proj.LocalVideoPath) {
		videoPath = proj.LocalVideoPath
	}
	previewPath := ""
	if proj.PreviewFrame != "" && pathExists(proj.PreviewFrame) {
		previewPath = proj.PreviewFrame
	} else {
		cand := filepath.Join(s.Store.ProjectDir(proj.ID), "preview.jpg")
		if pathExists(cand) {
			previewPath = cand
		}
	}

	res, err := renderer.DetectProjectFacecam(r.Context(), videoPath, previewPath, proj.DurationSec)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}

	boxCopy := res.FacecamBox
	proj.FacecamBox = &boxCopy
	proj.FacecamCorner = res.Corner
	_ = s.Store.SaveProject(proj)
	s.Broker.Publish("project_update", proj)

	p := getPrincipal(r)
	s.recordActivity(p, "facecam_detect", "project", proj.ID,
		fmt.Sprintf("%s mendeteksi area facecam (%s, %s) untuk %s", p.Name, res.Corner, res.Engine, proj.Title),
		fmt.Sprintf("found=%v samples=%d", res.Found, res.SampleCount), "info")

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"ok":          true,
		"projectId":   proj.ID,
		"found":       res.Found,
		"corner":      res.Corner,
		"facecamBox":  res.FacecamBox,
		"gameplayBox": res.GameplayBox,
		"sampleCount": res.SampleCount,
		"engine":      res.Engine,
	})
}
