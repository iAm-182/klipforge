package server

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"klipforge/internal/model"
)

// validateCandidateRenderGuardrail enforces the Score-Ban Guardrail (I-05):
//   - Manual range cuts (empty CandidateID) are ALWAYS allowed so humans picking
//     timestamps from video/transcript/storyboard are never blocked.
//   - When a CandidateID is supplied by an agent principal OR with RequireVerified=true,
//     the candidate MUST exist, have Verdict == "keep", AND have visual verification
//     evidence (StartFrameURL, PeakFrameURL, FrameVerified=true, or project storyboard JPG).
func (s *Server) validateCandidateRenderGuardrail(p model.Principal, opts model.RenderOptions) (int, error) {
	candID := strings.TrimSpace(opts.CandidateID)
	if candID == "" {
		return 0, nil
	}
	if p.Role != "agent" && !opts.RequireVerified {
		return 0, nil
	}

	proj, ok := s.Store.GetProject(opts.ProjectID)
	if !ok {
		return http.StatusBadRequest, fmt.Errorf("project tidak ditemukan: %s", opts.ProjectID)
	}

	var cand *model.ClipCandidate
	for i := range proj.Candidates {
		if proj.Candidates[i].ID == candID {
			cand = &proj.Candidates[i]
			break
		}
	}
	if cand == nil {
		return http.StatusBadRequest, fmt.Errorf("kandidat %s tidak ditemukan pada project %s", candID, opts.ProjectID)
	}

	// Check 1 (Verdict gate): must be explicitly verified with verdict == "keep"
	if cand.Verdict == nil || !strings.EqualFold(strings.TrimSpace(cand.Verdict.Verdict), "keep") {
		return http.StatusConflict, fmt.Errorf(
			"Score-Ban Guardrail (I-05): kandidat %s belum diverifikasi (verdict wajib 'keep' dari transkrip + frame, bukan dari skor auto-detection)",
			cand.ID,
		)
	}

	// Check 2 (Visual proof gate): candidate frame URL, explicit frameVerified flag, or project storyboard JPG
	hasVisualProof := strings.TrimSpace(cand.StartFrameURL) != "" ||
		strings.TrimSpace(cand.PeakFrameURL) != "" ||
		opts.FrameVerified ||
		hasProjectStoryboardJPG(s.Store.ProjectDir(proj.ID))
	if !hasVisualProof {
		return http.StatusConflict, fmt.Errorf(
			"Score-Ban Guardrail (I-05): kandidat %s wajib punya bukti verifikasi frame/storyboard sebelum dirender",
			cand.ID,
		)
	}

	return 0, nil
}

func hasProjectStoryboardJPG(projDir string) bool {
	if strings.TrimSpace(projDir) == "" {
		return false
	}
	entries, err := os.ReadDir(filepath.Join(projDir, "storyboards"))
	if err != nil {
		return false
	}
	for _, ent := range entries {
		if !ent.IsDir() && strings.HasSuffix(strings.ToLower(ent.Name()), ".jpg") {
			return true
		}
	}
	return false
}
