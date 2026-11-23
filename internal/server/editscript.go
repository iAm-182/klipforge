package server

import (
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"strings"
	"time"

	"klipforge/internal/analyzer"
	"klipforge/internal/model"
	"klipforge/internal/renderer"
)

// EditScriptRequest is the single-pass CapCut-Pro editing timeline accepted by POST /api/jobs/edit-script.
type EditScriptRequest struct {
	Title          string                 `json:"title"`
	ProjectID      string                 `json:"projectId,omitempty"`
	CampaignID     string                 `json:"campaignId,omitempty"`
	Mode           string                 `json:"mode,omitempty"` // default "edit_script"
	Layout         model.LayoutConfig     `json:"layout"`
	Subtitles      model.SubtitleStyle    `json:"subtitles"`
	Segments       []model.CompileSegment `json:"segments"`
	AudioFX        *model.AudioFXConfig   `json:"audioFx,omitempty"`
	VideoFX        model.VideoFXConfig    `json:"videoFx"`
	AudioNorm      *bool                  `json:"audioNorm,omitempty"`
	UseWatermark   *bool                  `json:"useWatermark,omitempty"`
	UseOutro       *bool                  `json:"useOutro,omitempty"`
	BumperDuration float64                `json:"bumperDuration,omitempty"`
	BumperSFXID    string                 `json:"bumperSfxId,omitempty"`
	Encoder        string                 `json:"encoder,omitempty"`
	Preset         string                 `json:"preset,omitempty"`
	CRF            int                    `json:"crf,omitempty"`
}

func isValidColorFilter(f string) bool {
	switch strings.ToLower(strings.TrimSpace(f)) {
	case "", "none", "vivid_gaming", "cinematic", "meme_deep":
		return true
	default:
		return false
	}
}

// handleEditScript validates a CapCut-Pro timeline JSON, applies offline momentType -> SFX/BGM mapping,
// clamps out-of-range controls cleanly, skips unknown assets without crashing, reuses the compile worker,
// and returns HTTP 200 echoing back appliedPlan.
func (s *Server) handleEditScript(w http.ResponseWriter, r *http.Request) {
	p := getPrincipal(r)
	if err := s.checkMemberQueueLimit(p); err != nil {
		writeErr(w, http.StatusTooManyRequests, err.Error())
		return
	}

	var req EditScriptRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "Format JSON edit-script tidak valid")
		return
	}
	if len(req.Segments) == 0 {
		writeErr(w, http.StatusBadRequest, "Minimal 1 segmen diperlukan di edit-script")
		return
	}

	// Resolve default projectId / campaignId
	campaignID := strings.TrimSpace(req.CampaignID)
	for i := range req.Segments {
		if strings.TrimSpace(req.Segments[i].ProjectID) == "" {
			req.Segments[i].ProjectID = strings.TrimSpace(req.ProjectID)
		}
		if campaignID == "" && req.Segments[i].ProjectID != "" {
			if proj, ok := s.Store.GetProject(req.Segments[i].ProjectID); ok && proj.CampaignID != "" {
				campaignID = proj.CampaignID
			}
		}
	}

	// Strict campaign forbidden-words enforcement on title, segment labels, and text overlays!
	forbidden := s.campaignForbiddenWords(campaignID)
	if len(forbidden) > 0 {
		if strings.TrimSpace(req.Title) != "" && analyzer.ContainsForbiddenWord(req.Title, forbidden) {
			writeErr(w, http.StatusBadRequest, fmt.Sprintf("title mengandung kata terlarang campaign: %q", req.Title))
			return
		}
		for i, seg := range req.Segments {
			if strings.TrimSpace(seg.Label) != "" && analyzer.ContainsForbiddenWord(seg.Label, forbidden) {
				writeErr(w, http.StatusBadRequest, fmt.Sprintf("segmen #%d label mengandung kata terlarang campaign: %q", i+1, seg.Label))
				return
			}
			if seg.TextOverlay != nil && strings.TrimSpace(seg.TextOverlay.Text) != "" && analyzer.ContainsForbiddenWord(seg.TextOverlay.Text, forbidden) {
				writeErr(w, http.StatusBadRequest, fmt.Sprintf("segmen #%d textOverlay mengandung kata terlarang campaign: %q", i+1, seg.TextOverlay.Text))
				return
			}
		}
	}

	// Defaults for layout, mode, audioFx, compliance booleans
	mode := strings.TrimSpace(req.Mode)
	if mode == "" {
		mode = "edit_script"
	}
	layout := req.Layout
	if layout.AspectRatio == "" {
		layout.AspectRatio = "9:16"
	}
	if layout.Resolution == "" {
		layout.Resolution = "1080p"
	}
	if layout.CropMode == "" {
		layout.CropMode = "face_track"
	}

	audioFX := model.AudioFXConfig{
		BGMVolume: 0.18,
		AutoDuck:  true,
	}
	if req.AudioFX != nil {
		audioFX = *req.AudioFX
		if audioFX.BGMVolume <= 0 {
			audioFX.BGMVolume = 0.18
		}
	}

	audioNorm := true
	if req.AudioNorm != nil {
		audioNorm = *req.AudioNorm
	}
	useWM := true
	if req.UseWatermark != nil {
		useWM = *req.UseWatermark
	}
	useOutro := true
	if req.UseOutro != nil {
		useOutro = *req.UseOutro
	}

	plan := model.AppliedEditPlan{
		Segments: make([]model.SegmentEditReport, 0, len(req.Segments)),
	}

	// Validate global BGM asset cleanly (skip unknown instead of crashing)
	globalBGM := strings.TrimSpace(audioFX.BGMAssetID)
	if globalBGM != "" {
		if s.Audio != nil && !s.Audio.HasKnownAsset(globalBGM) {
			plan.Warnings = append(plan.Warnings, fmt.Sprintf("global bgmAssetId %q tidak dikenal — dilewati", globalBGM))
			globalBGM = ""
			audioFX.BGMAssetID = ""
		} else {
			plan.BGMAssetID = globalBGM
		}
	}

	// Validate bumper SFX if set
	bumperSFX := strings.TrimSpace(req.BumperSFXID)
	if bumperSFX != "" && s.Audio != nil && !s.Audio.HasKnownAsset(bumperSFX) {
		plan.Warnings = append(plan.Warnings, fmt.Sprintf("bumperSfxId %q tidak dikenal — dilewati", bumperSFX))
		bumperSFX = ""
	}

	autoMapEnabled := audioFX.SFXAutoPreset == "auto_by_moment" || audioFX.SFXAutoPreset == ""
	segments := make([]model.CompileSegment, len(req.Segments))

	for i, seg := range req.Segments {
		rep := model.SegmentEditReport{
			Index:      i,
			Label:      strings.TrimSpace(seg.Label),
			MomentType: strings.ToLower(strings.TrimSpace(seg.MomentType)),
		}
		seg.MomentType = rep.MomentType

		segDur := seg.EndSec - seg.StartSec
		if segDur <= 0.5 {
			writeErr(w, http.StatusBadRequest, fmt.Sprintf("segmen #%d (%s): endSec (%.1f) harus lebih besar dari startSec (%.1f)", i+1, seg.Label, seg.EndSec, seg.StartSec))
			return
		}

		// 1. Speed clamp (0.50 .. 2.00)
		if seg.Speed != 0 {
			orig := seg.Speed
			if seg.Speed < 0.50 {
				seg.Speed = 0.50
			} else if seg.Speed > 2.00 {
				seg.Speed = 2.00
			}
			if math.Abs(seg.Speed-orig) > 0.001 {
				rep.Clamped = append(rep.Clamped, fmt.Sprintf("speed %.2f -> %.2f", orig, seg.Speed))
			}
			rep.Speed = seg.Speed
		} else {
			rep.Speed = 1.0
		}

		// 2. SpeedRamp clamp
		if seg.SpeedRamp != nil {
			orig := *seg.SpeedRamp
			clamped := renderer.ClampSpeedRamp(orig, segDur)
			if math.Abs(clamped.FromSpeed-orig.FromSpeed) > 0.001 ||
				math.Abs(clamped.ToSpeed-orig.ToSpeed) > 0.001 ||
				math.Abs(clamped.AtSec-orig.AtSec) > 0.01 {
				rep.Clamped = append(rep.Clamped, fmt.Sprintf("speedRamp (at=%.1fs %.2fx->%.2fx) -> (at=%.1fs %.2fx->%.2fx)",
					orig.AtSec, orig.FromSpeed, orig.ToSpeed, clamped.AtSec, clamped.FromSpeed, clamped.ToSpeed))
			}
			seg.SpeedRamp = &clamped
			rep.SpeedRamp = &clamped
		}

		// 3. ZoomPunch clamp
		if seg.Zoom != nil {
			cz, notes := renderer.ClampZoomPunch(*seg.Zoom, segDur)
			if len(notes) > 0 {
				rep.Clamped = append(rep.Clamped, notes...)
			}
			seg.Zoom = &cz
			rep.Zoom = &cz
		}

		// 4. Filter validation
		if f := strings.TrimSpace(seg.Filter); f != "" {
			if !isValidColorFilter(f) {
				rep.Skipped = append(rep.Skipped, fmt.Sprintf("filter %q tidak dikenal", f))
				seg.Filter = ""
			} else {
				seg.Filter = strings.ToLower(f)
				rep.Filter = seg.Filter
			}
		}

		// 5. Transition validation (cut | fade | zoom)
		tr := strings.ToLower(strings.TrimSpace(seg.Transition))
		switch tr {
		case "", "cut":
			seg.Transition = "cut"
		case "fade", "zoom":
			seg.Transition = tr
		default:
			rep.Clamped = append(rep.Clamped, fmt.Sprintf("transition %q -> cut", tr))
			seg.Transition = "cut"
		}
		rep.Transition = seg.Transition

		// 6. TextOverlay position/style normalization
		if seg.TextOverlay != nil {
			seg.TextOverlay.Text = strings.TrimSpace(seg.TextOverlay.Text)
			if seg.TextOverlay.Text == "" {
				seg.TextOverlay = nil
			} else {
				pos := strings.ToLower(strings.TrimSpace(seg.TextOverlay.Position))
				if pos != "" && pos != "top" && pos != "center" && pos != "bottom" {
					rep.Clamped = append(rep.Clamped, fmt.Sprintf("textOverlay.position %q -> top", pos))
					seg.TextOverlay.Position = "top"
				}
			}
		}

		// 7. Unknown explicit asset IDs skipped cleanly (never crash whole render!)
		explicitSFX := strings.TrimSpace(seg.EndSFXID)
		if explicitSFX != "" && s.Audio != nil && !s.Audio.HasKnownAsset(explicitSFX) {
			rep.Skipped = append(rep.Skipped, fmt.Sprintf("endSfxId %q tidak ditemukan", explicitSFX))
			explicitSFX = ""
			seg.EndSFXID = ""
		}
		explicitBGM := strings.TrimSpace(seg.BGMAssetID)
		if explicitBGM != "" && s.Audio != nil && !s.Audio.HasKnownAsset(explicitBGM) {
			rep.Skipped = append(rep.Skipped, fmt.Sprintf("bgmAssetId %q tidak ditemukan", explicitBGM))
			explicitBGM = ""
			seg.BGMAssetID = ""
		}

		// 8. Offline momentType -> SFX/BGM mapping (explicit ALWAYS wins!)
		if autoMapEnabled && s.Audio != nil && seg.MomentType != "" && seg.MomentType != "custom" {
			effExplicitBGM := explicitBGM
			if effExplicitBGM == "" {
				effExplicitBGM = globalBGM
			}
			dec := s.Audio.ResolveAutoEditForMoment(seg.MomentType, explicitSFX, effExplicitBGM)
			if explicitSFX == "" && dec.SFXID != "" {
				seg.EndSFXID = dec.SFXID
			}
			if effExplicitBGM == "" && dec.BGMID != "" {
				seg.BGMAssetID = dec.BGMID
				if plan.BGMAssetID == "" {
					plan.BGMAssetID = dec.BGMID
				}
			}
			decCopy := dec
			rep.AutoEdit = &decCopy
			if plan.AutoEdit == nil {
				plan.AutoEdit = &decCopy
			}
		}

		rep.EndSFXID = seg.EndSFXID
		if seg.BGMAssetID != "" {
			rep.BGMAssetID = seg.BGMAssetID
		} else {
			rep.BGMAssetID = globalBGM
		}

		if len(rep.Clamped) > 0 {
			s.Broker.Log("edit-script", "warn", fmt.Sprintf("Segmen #%d (%s) clamped: %s", i+1, rep.Label, strings.Join(rep.Clamped, "; ")))
		}
		if len(rep.Skipped) > 0 {
			s.Broker.Log("edit-script", "warn", fmt.Sprintf("Segmen #%d (%s) skipped: %s", i+1, rep.Label, strings.Join(rep.Skipped, "; ")))
		}

		segments[i] = seg
		plan.Segments = append(plan.Segments, rep)
	}

	compileOpts := model.CompileOptions{
		CampaignID:     campaignID,
		Title:          strings.TrimSpace(req.Title),
		Mode:           mode,
		Segments:       segments,
		Layout:         layout,
		Subtitles:      req.Subtitles,
		AudioFX:        audioFX,
		VideoFX:        req.VideoFX,
		AudioNorm:      audioNorm,
		BumperDuration: req.BumperDuration,
		BumperSFXID:    bumperSFX,
		UseWatermark:   useWM,
		UseOutro:       useOutro,
		Encoder:        req.Encoder,
		Preset:         req.Preset,
		CRF:            req.CRF,
	}

	job, err := s.Worker.EnqueueCompilation(compileOpts)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}

	planCopy := plan
	job.AppliedPlan = &planCopy
	if plan.AutoEdit != nil {
		aeCopy := *plan.AutoEdit
		job.AutoEdit = &aeCopy
	}
	s.tagJobWithPrincipal(job, p)
	s.Broker.Log("edit-script", "info", fmt.Sprintf("🎬 Edit-Script '%s' (%d segmen) masuk antrean oleh %s (%s)", job.Title, len(segments), p.Name, p.Via))

	// Returns HTTP 200 OK echoing full job + appliedPlan!
	writeJSON(w, http.StatusOK, job)
}

func isValidMomentType(mt string) bool {
	switch mt {
	case "crash", "fail", "hype", "twist", "victory", "meme", "custom":
		return true
	default:
		return false
	}
}

// handleCandidateVerdict stores a human or agent (Muse) judgment ("keep"|"drop"|"maybe", funny 0-5, note, momentType)
// directly on a project candidate. NEVER auto-renders on "keep".
func (s *Server) handleCandidateVerdict(w http.ResponseWriter, r *http.Request) {
	projID := r.PathValue("id")
	candID := r.PathValue("candId")
	proj, ok := s.Store.GetProject(projID)
	if !ok {
		writeErr(w, http.StatusNotFound, "Project tidak ditemukan")
		return
	}

	var req struct {
		Verdict    string `json:"verdict"`    // keep | drop | maybe
		Funny      int    `json:"funny"`      // 0 - 5
		Note       string `json:"note"`       // free-form note
		MomentType string `json:"momentType"` // optional override
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "Format JSON verdict tidak valid")
		return
	}
	v := strings.ToLower(strings.TrimSpace(req.Verdict))
	if v != "keep" && v != "drop" && v != "maybe" {
		writeErr(w, http.StatusBadRequest, "verdict harus salah satu dari: keep, drop, maybe")
		return
	}
	funny := req.Funny
	if funny < 0 {
		funny = 0
	} else if funny > 5 {
		funny = 5
	}
	mt := strings.ToLower(strings.TrimSpace(req.MomentType))
	if mt != "" && !isValidMomentType(mt) {
		writeErr(w, http.StatusBadRequest, "momentType harus salah satu dari: crash, fail, hype, twist, victory, meme, custom")
		return
	}

	p := getPrincipal(r)
	verdictBy := p.Name
	if p.Via != "" {
		verdictBy = fmt.Sprintf("%s (%s)", p.Name, p.Via)
	}

	// Copy candidate slice before mutating so we never race with concurrent readers
	cands := make([]model.ClipCandidate, len(proj.Candidates))
	copy(cands, proj.Candidates)

	foundIdx := -1
	for i := range cands {
		if cands[i].ID == candID {
			foundIdx = i
			break
		}
	}
	if foundIdx < 0 {
		writeErr(w, http.StatusNotFound, "Kandidat tidak ditemukan pada project ini")
		return
	}

	if mt != "" {
		cands[foundIdx].MomentType = mt
	} else if cands[foundIdx].MomentType != "" {
		mt = cands[foundIdx].MomentType
	}
	if s.Audio != nil && cands[foundIdx].MomentType != "" {
		dec := s.Audio.ResolveAutoEditForMoment(cands[foundIdx].MomentType, "", "")
		cands[foundIdx].AutoEdit = &dec
	}

	cv := &model.CandidateVerdict{
		Verdict:    v,
		Funny:      funny,
		Note:       strings.TrimSpace(req.Note),
		MomentType: mt,
		VerdictBy:  verdictBy,
		MemberID:   p.ID,
		Via:        p.Via,
		UpdatedAt:  time.Now(),
	}
	cands[foundIdx].Verdict = cv
	proj.Candidates = cands

	if err := s.Store.SaveProject(proj); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.Broker.Publish("project_update", proj)
	s.Broker.Log("verdict", "info", fmt.Sprintf("📝 Verdict [%s] (funny=%d/5) pada %s/%s oleh %s", strings.ToUpper(v), funny, proj.ID, candID, verdictBy))

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"ok":          true,
		"projectId":   proj.ID,
		"candidateId": candID,
		"verdict":     cv,
		"candidate":   cands[foundIdx],
	})
}
