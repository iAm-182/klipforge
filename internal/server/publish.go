package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"klipforge/internal/analyzer"
	"klipforge/internal/model"
	"klipforge/internal/publish"
)

type publishKitOverride struct {
	HookText          string `json:"hookText,omitempty"`
	TikTokCaption     string `json:"tiktokCaption,omitempty"`
	ShortsTitle       string `json:"shortsTitle,omitempty"`
	ShortsDescription string `json:"shortsDescription,omitempty"`
	ReelsCaption      string `json:"reelsCaption,omitempty"`
}

// buildPublishKitForJob generates TikTok + YouTube Shorts (<=100 chars title) + Instagram Reels copy
// plus the 4-point MotionKlip SOP PublishChecklist, strictly rejecting campaign forbidden words.
func (s *Server) buildPublishKitForJob(job *model.RenderJob, ov *publishKitOverride) (*model.PublishKit, error) {
	if job == nil {
		return nil, fmt.Errorf("job kosong")
	}
	var camp *model.Campaign
	if job.CampaignID != "" {
		camp, _ = s.Store.GetCampaign(job.CampaignID)
	}
	var forbidden []string
	var hashtagsList []string
	var mentionsList []string
	if camp != nil {
		forbidden = camp.ForbiddenWords
		hashtagsList = camp.Hashtags
		mentionsList = camp.SocialMentions
	}

	// Pull candidate hook / momentType when available
	hook := strings.TrimSpace(job.Options.HookText)
	momentType := ""
	coverURL := job.CoverFrameURL
	if job.ProjectID != "" && job.Options.CandidateID != "" {
		if proj, ok := s.Store.GetProject(job.ProjectID); ok {
			for _, c := range proj.Candidates {
				if c.ID == job.Options.CandidateID {
					if hook == "" {
						hook = strings.TrimSpace(c.HookText)
					}
					momentType = strings.TrimSpace(c.MomentType)
					if coverURL == "" {
						coverURL = c.CoverFrameURL
					}
					if coverURL == "" {
						coverURL = c.PeakFrameURL
					}
					break
				}
			}
		}
	}
	if momentType == "" && job.AutoEdit != nil {
		momentType = job.AutoEdit.MomentType
	}
	if coverURL == "" {
		coverURL = job.PeekFrameURL
	}
	if ov != nil && strings.TrimSpace(ov.HookText) != "" {
		hook = strings.TrimSpace(ov.HookText)
	}
	if hook == "" {
		hook = strings.TrimSpace(job.Title)
	}

	hashtags := strings.TrimSpace(strings.Join(hashtagsList, " "))
	if hashtags == "" {
		hashtags = strings.TrimSpace(job.HashtagLine)
	}
	creditLine := ""
	if len(mentionsList) > 0 {
		creditLine = "Credit: " + strings.Join(mentionsList, " ")
	}

	momentTag := ""
	switch momentType {
	case "crash":
		momentTag = "Momen nabrak parah!"
	case "fail":
		momentTag = "Detik-detik gagal total!"
	case "victory":
		momentTag = "Clutch gila!"
	case "twist":
		momentTag = "Plot twist tak terduga!"
	case "meme":
		momentTag = "Ngakak parah!"
	case "hype":
		momentTag = "Momen paling pecah!"
	}

	// 1. TikTok variant
	ttParts := []string{hook}
	if momentTag != "" && !strings.Contains(strings.ToLower(hook), strings.ToLower(momentTag)) {
		ttParts = append(ttParts, momentTag)
	}
	if creditLine != "" {
		ttParts = append(ttParts, creditLine)
	}
	if hashtags != "" {
		ttParts = append(ttParts, hashtags)
	}
	ttCaption := strings.Join(ttParts, "\n\n")
	if ov != nil && strings.TrimSpace(ov.TikTokCaption) != "" {
		ttCaption = strings.TrimSpace(ov.TikTokCaption)
	}

	// 2. YouTube Shorts variant (title strictly <= 100 chars!)
	shortsTitle := hook
	if !strings.Contains(strings.ToLower(shortsTitle), "#shorts") {
		shortsTitle = strings.TrimSpace(shortsTitle) + " #Shorts"
	}
	if ov != nil && strings.TrimSpace(ov.ShortsTitle) != "" {
		shortsTitle = strings.TrimSpace(ov.ShortsTitle)
	}
	if r := []rune(shortsTitle); len(r) > 100 {
		shortsTitle = strings.TrimSpace(string(r[:97])) + "..."
	}
	shDescParts := []string{hook}
	if momentTag != "" {
		shDescParts = append(shDescParts, momentTag)
	}
	if creditLine != "" {
		shDescParts = append(shDescParts, creditLine)
	}
	if hashtags != "" {
		shDescParts = append(shDescParts, hashtags)
	}
	shortsDesc := strings.Join(shDescParts, "\n\n")
	if ov != nil && strings.TrimSpace(ov.ShortsDescription) != "" {
		shortsDesc = strings.TrimSpace(ov.ShortsDescription)
	}

	// 3. Instagram Reels variant
	reelsParts := []string{hook}
	if creditLine != "" {
		reelsParts = append(reelsParts, creditLine)
	}
	if hashtags != "" {
		reelsParts = append(reelsParts, hashtags)
	}
	reelsCaption := strings.Join(reelsParts, "\n\n")
	if ov != nil && strings.TrimSpace(ov.ReelsCaption) != "" {
		reelsCaption = strings.TrimSpace(ov.ReelsCaption)
	}

	// Strict campaign forbidden-word check across ALL platform texts!
	if len(forbidden) > 0 {
		for field, txt := range map[string]string{
			"tiktokCaption":     ttCaption,
			"shortsTitle":       shortsTitle,
			"shortsDescription": shortsDesc,
			"reelsCaption":      reelsCaption,
		} {
			if analyzer.ContainsForbiddenWord(txt, forbidden) {
				return nil, fmt.Errorf("%s mengandung kata terlarang campaign — perbaiki caption sebelum publish", field)
			}
		}
	}

	// 4-point SOP Publish Checklist
	wmReq := camp != nil && camp.Watermark.Enabled
	outroReq := camp != nil && camp.Outro.Enabled
	wmOK := !wmReq || job.Compliance.WatermarkApplied
	outroOK := !outroReq || (job.Compliance.OutroApplied && job.Compliance.OutroClean)
	forbOK := true
	creditHashOK := true
	if camp != nil && len(camp.Hashtags) > 0 && !strings.Contains(ttCaption, camp.Hashtags[0]) {
		creditHashOK = false
	}
	if camp != nil && len(camp.SocialMentions) > 0 && !strings.Contains(ttCaption, camp.SocialMentions[0]) {
		creditHashOK = false
	}

	var missing []string
	if !wmOK {
		missing = append(missing, "Watermark belum menempel")
	}
	if !outroOK {
		missing = append(missing, "Outro 3s belum bersih/terpasang")
	}
	if !forbOK {
		missing = append(missing, "Mengandung kata terlarang")
	}
	if !creditHashOK {
		missing = append(missing, "Credit / hashtag wajib belum lengkap")
	}

	return &model.PublishKit{
		TikTok: model.TikTokVariant{
			Caption:    ttCaption,
			Hashtags:   hashtags,
			CreditLine: creditLine,
		},
		Shorts: model.ShortsVariant{
			Title:       shortsTitle,
			Description: shortsDesc,
			Hashtags:    hashtags,
		},
		Reels: model.ReelsVariant{
			Caption:  reelsCaption,
			Hashtags: hashtags,
			CoverURL: coverURL,
		},
		Checklist: model.PublishChecklist{
			WatermarkOK:      wmOK,
			OutroCleanOK:     outroOK,
			ForbiddenCleanOK: forbOK,
			CreditHashtagsOK: creditHashOK,
			ReadyToPost:      wmOK && outroOK && forbOK && creditHashOK,
			Missing:          missing,
		},
		GeneratedAt: time.Now(),
	}, nil
}

// handleGeneratePublishKit generates and stores per-platform TikTok/Shorts/Reels copy + SOP checklist on the job.
// Agent-allowed (returns HTTP 200; rejects forbidden words with HTTP 400).
func (s *Server) handleGeneratePublishKit(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	job, ok := s.Store.GetJob(id)
	if !ok {
		writeErr(w, http.StatusNotFound, "Job tidak ditemukan")
		return
	}
	var ov publishKitOverride
	_ = json.NewDecoder(r.Body).Decode(&ov)

	kit, err := s.buildPublishKitForJob(job, &ov)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}

	job.PublishKit = kit
	job.PostCaption = kit.TikTok.Caption
	job.HashtagLine = kit.TikTok.Hashtags
	if err := s.Store.SaveJob(job); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.Broker.Publish("job_update", job)
	p := getPrincipal(r)
	s.recordActivity(p, "publish_kit", "job", job.ID,
		fmt.Sprintf("%s membuat publish kit untuk %s", p.Name, job.Title), "", "info")

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"ok":         true,
		"jobId":      job.ID,
		"publishKit": kit,
	})
}

// handlePublishJob executes an approved clip publish via:
// - TikTok: social-auto-upload (sau) sidecar ONLY (NEVER official TikTok Content Posting API)
// - YouTube: native Go YouTube Data API v3 (OAuth refresh token per team account; 401 if missing!)
// - Instagram: native Go Instagram Graph API Reels (OAuth accessToken + igUserId; 401 if missing!)
// Strictly enforces human-approve gate (409 if not approved) and forbidden-word check (400).
func (s *Server) handlePublishJob(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	job, ok := s.Store.GetJob(id)
	if !ok {
		writeErr(w, http.StatusNotFound, "Job tidak ditemukan")
		return
	}

	// STRICT HUMAN-APPROVE GATE (never headless auto-post!)
	if job.ReviewStatus != "approved" {
		writeErr(w, http.StatusConflict, "Publish diblokir (409): klip harus disetujui QC (ReviewStatus=approved) sebelum boleh dipublish")
		return
	}

	var req struct {
		Platform       string     `json:"platform"` // tiktok | youtube | instagram
		AccountID      string     `json:"accountId"`
		Caption        string     `json:"caption"`
		Title          string     `json:"title"`
		PrivacyStatus  string     `json:"privacyStatus"`
		ScheduledAt    *time.Time `json:"scheduledAt"`
		VideoPublicURL string     `json:"videoPublicUrl"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "Format JSON publish tidak valid")
		return
	}
	platform := strings.ToLower(strings.TrimSpace(req.Platform))
	if platform == "" {
		platform = "tiktok"
	}
	if platform != "tiktok" && platform != "youtube" && platform != "instagram" {
		writeErr(w, http.StatusBadRequest, "platform harus salah satu dari: tiktok, youtube, instagram")
		return
	}

	// Build/validate publish kit & enforce forbidden-word guard
	ov := &publishKitOverride{}
	if strings.TrimSpace(req.Caption) != "" {
		ov.TikTokCaption = req.Caption
		ov.ShortsDescription = req.Caption
		ov.ReelsCaption = req.Caption
	}
	if strings.TrimSpace(req.Title) != "" {
		ov.ShortsTitle = req.Title
	}
	kit, err := s.buildPublishKitForJob(job, ov)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	job.PublishKit = kit

	p := getPrincipal(r)
	settings := s.Store.GetSettings()

	switch platform {
	case "youtube":
		acc, ok := s.Store.FindPublishAccountByPlatform("youtube", req.AccountID)
		if !ok || strings.TrimSpace(acc.RefreshToken) == "" {
			writeErr(w, http.StatusUnauthorized, "OAuth token YouTube belum tersimpan untuk akun tim — Owner perlu menambahkan YouTube OAuth refreshToken di /api/publish/accounts terlebih dahulu")
			return
		}
		if job.OutputPath == "" || !pathExists(job.OutputPath) {
			writeErr(w, http.StatusBadRequest, "File MP4 hasil render tidak ditemukan di disk")
			return
		}
		var tags []string
		for _, t := range strings.Fields(kit.Shorts.Hashtags) {
			tags = append(tags, strings.TrimPrefix(t, "#"))
		}
		rec := model.PublishRecord{
			Platform:    "youtube",
			Method:      "youtube_api",
			AccountName: acc.Name,
			Status:      "publishing",
			ScheduledAt: req.ScheduledAt,
			UpdatedAt:   time.Now(),
		}
		job.PublishRecords = append(job.PublishRecords, rec)
		_ = s.Store.SaveJob(job)
		s.Broker.Publish("job_update", job)

		go func(jID string, a model.PublishAccount, k model.PublishKit) {
			res, err := publish.UploadYouTubeVideo(context.Background(), publish.YouTubeUploadInput{
				FilePath:      job.OutputPath,
				Title:         k.Shorts.Title,
				Description:   k.Shorts.Description,
				Tags:          tags,
				PrivacyStatus: req.PrivacyStatus,
				PublishAt:     req.ScheduledAt,
				ClientID:      a.ClientID,
				ClientSecret:  a.ClientSecret,
				RefreshToken:  a.RefreshToken,
			})
			latest, ok := s.Store.GetJob(jID)
			if !ok {
				return
			}
			now := time.Now()
			if err != nil {
				latest.PublishRecords = append(latest.PublishRecords, model.PublishRecord{
					Platform:    "youtube",
					Method:      "youtube_api",
					AccountName: a.Name,
					Status:      "error",
					Error:       err.Error(),
					UpdatedAt:   now,
				})
				_ = s.Store.SaveJob(latest)
				s.Broker.Publish("job_update", latest)
				s.recordActivity(p, "publish_error", "job", jID, fmt.Sprintf("Upload YouTube gagal (%s)", latest.Title), err.Error(), "error")
				return
			}
			latest.PublishRecords = append(latest.PublishRecords, model.PublishRecord{
				Platform:      "youtube",
				Method:        "youtube_api",
				AccountName:   a.Name,
				Status:        "published",
				RemoteVideoID: res.VideoID,
				PublishedURL:  res.URL,
				ScheduledAt:   req.ScheduledAt,
				UpdatedAt:     now,
			})
			latest.BoardColumn = "diposting"
			_ = s.Store.SaveJob(latest)
			s.Broker.Publish("job_update", latest)
			s.recordActivity(p, "published", "job", jID, fmt.Sprintf("%s mempublikasikan %s ke YouTube Shorts", p.Name, latest.Title), res.URL, "success")
		}(job.ID, *acc, *kit)

		writeJSON(w, http.StatusAccepted, map[string]interface{}{
			"ok":       true,
			"jobId":    job.ID,
			"platform": "youtube",
			"method":   "youtube_api",
			"status":   "publishing",
		})
		return

	case "instagram":
		acc, ok := s.Store.FindPublishAccountByPlatform("instagram", req.AccountID)
		if !ok || strings.TrimSpace(acc.AccessToken) == "" || strings.TrimSpace(acc.IGUserID) == "" {
			writeErr(w, http.StatusUnauthorized, "OAuth token Instagram Graph API belum tersimpan — Owner perlu menambahkan accessToken & igUserId di /api/publish/accounts")
			return
		}
		vidURL := strings.TrimSpace(req.VideoPublicURL)
		if vidURL == "" {
			vidURL = fmt.Sprintf("%s/api/jobs/%s/video", s.edgeBaseURL(), job.ID)
		}
		go func(jID string, a model.PublishAccount, k model.PublishKit, vURL string) {
			res, err := publish.UploadInstagramReel(context.Background(), publish.InstagramReelInput{
				IGUserID:    a.IGUserID,
				AccessToken: a.AccessToken,
				VideoURL:    vURL,
				Caption:     k.Reels.Caption,
			})
			latest, ok := s.Store.GetJob(jID)
			if !ok {
				return
			}
			now := time.Now()
			if err != nil {
				latest.PublishRecords = append(latest.PublishRecords, model.PublishRecord{
					Platform:    "instagram",
					Method:      "instagram_api",
					AccountName: a.Name,
					Status:      "error",
					Error:       err.Error(),
					UpdatedAt:   now,
				})
				_ = s.Store.SaveJob(latest)
				s.Broker.Publish("job_update", latest)
				return
			}
			latest.PublishRecords = append(latest.PublishRecords, model.PublishRecord{
				Platform:      "instagram",
				Method:        "instagram_api",
				AccountName:   a.Name,
				Status:        "published",
				RemoteVideoID: res.MediaID,
				UpdatedAt:     now,
			})
			latest.BoardColumn = "diposting"
			_ = s.Store.SaveJob(latest)
			s.Broker.Publish("job_update", latest)
			s.recordActivity(p, "published", "job", jID, fmt.Sprintf("%s mempublikasikan %s ke Instagram Reels", p.Name, latest.Title), res.MediaID, "success")
		}(job.ID, *acc, *kit, vidURL)

		writeJSON(w, http.StatusAccepted, map[string]interface{}{
			"ok":       true,
			"jobId":    job.ID,
			"platform": "instagram",
			"method":   "instagram_api",
			"status":   "publishing",
		})
		return

	default: // tiktok -> social-auto-upload (sau) sidecar ONLY!
		if publish.ResolveSAUBinary(settings.SAUPath) == "" {
			writeErr(w, http.StatusServiceUnavailable, "Sidecar social-auto-upload ('sau') belum terpasang di server. Pasang dreammis/social-auto-upload atau gunakan tombol Download MP4 + Copy Caption.")
			return
		}
		if job.OutputPath == "" || !pathExists(job.OutputPath) {
			writeErr(w, http.StatusBadRequest, "File MP4 hasil render tidak ditemukan di disk")
			return
		}
		accName := strings.TrimSpace(req.AccountID)
		if acc, ok := s.Store.FindPublishAccountByPlatform("tiktok_sau", accName); ok && acc.SAUAccount != "" {
			accName = acc.SAUAccount
		}
		var tags []string
		for _, t := range strings.Fields(kit.TikTok.Hashtags) {
			tags = append(tags, strings.TrimPrefix(t, "#"))
		}
		go func(jID string, k model.PublishKit) {
			out, err := publish.RunSAUSidecar(context.Background(), publish.SAUUploadInput{
				BinaryPath:  settings.SAUPath,
				Platform:    "tiktok",
				AccountName: accName,
				FilePath:    job.OutputPath,
				Title:       job.Title,
				Caption:     k.TikTok.Caption,
				Tags:        tags,
				ScheduledAt: req.ScheduledAt,
			})
			latest, ok := s.Store.GetJob(jID)
			if !ok {
				return
			}
			now := time.Now()
			if err != nil {
				latest.PublishRecords = append(latest.PublishRecords, model.PublishRecord{
					Platform:    "tiktok",
					Method:      "sau_sidecar",
					AccountName: accName,
					Status:      "error",
					Error:       err.Error(),
					UpdatedAt:   now,
				})
				_ = s.Store.SaveJob(latest)
				s.Broker.Publish("job_update", latest)
				return
			}
			latest.PublishRecords = append(latest.PublishRecords, model.PublishRecord{
				Platform:     "tiktok",
				Method:       "sau_sidecar",
				AccountName:  accName,
				Status:       "published",
				PublishedURL: out,
				ScheduledAt:  req.ScheduledAt,
				UpdatedAt:    now,
			})
			latest.BoardColumn = "diposting"
			_ = s.Store.SaveJob(latest)
			s.Broker.Publish("job_update", latest)
			s.recordActivity(p, "published", "job", jID, fmt.Sprintf("%s mempublikasikan %s ke TikTok (sau)", p.Name, latest.Title), out, "success")
		}(job.ID, *kit)

		writeJSON(w, http.StatusAccepted, map[string]interface{}{
			"ok":       true,
			"jobId":    job.ID,
			"platform": "tiktok",
			"method":   "sau_sidecar",
			"status":   "publishing",
		})
	}
}

// handleSyncJobAnalytics pulls YouTube views/likes/comments via YouTube Data API v3 videos.list and stores it on the job.
func (s *Server) handleSyncJobAnalytics(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	job, ok := s.Store.GetJob(id)
	if !ok {
		writeErr(w, http.StatusNotFound, "Job tidak ditemukan")
		return
	}
	var req struct {
		VideoID   string `json:"videoId"`
		AccountID string `json:"accountId"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)

	vidID := strings.TrimSpace(req.VideoID)
	if vidID == "" {
		for i := len(job.PublishRecords) - 1; i >= 0; i-- {
			if job.PublishRecords[i].Platform == "youtube" && job.PublishRecords[i].RemoteVideoID != "" {
				vidID = job.PublishRecords[i].RemoteVideoID
				break
			}
		}
	}
	if vidID == "" {
		writeErr(w, http.StatusBadRequest, "videoId YouTube belum tersedia — sertakan videoId atau publish ke YouTube terlebih dahulu")
		return
	}
	acc, ok := s.Store.FindPublishAccountByPlatform("youtube", req.AccountID)
	if !ok || strings.TrimSpace(acc.RefreshToken) == "" {
		writeErr(w, http.StatusUnauthorized, "OAuth token YouTube belum tersimpan untuk mengambil statistik analytics")
		return
	}
	snap, err := publish.FetchYouTubeStats(r.Context(), acc.ClientID, acc.ClientSecret, acc.RefreshToken, vidID)
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	job.Analytics = &snap
	_ = s.Store.SaveJob(job)
	s.Broker.Publish("job_update", job)
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"ok":        true,
		"jobId":     job.ID,
		"analytics": snap,
	})
}

// --- Owner-Only Publish Accounts Management (/api/publish/accounts) ---

func (s *Server) handleListPublishAccounts(w http.ResponseWriter, r *http.Request) {
	all := s.Store.ListPublishAccounts()
	// Mask OAuth secrets in list output
	safe := make([]map[string]interface{}, 0, len(all))
	for _, a := range all {
		safe = append(safe, map[string]interface{}{
			"id":              a.ID,
			"name":            a.Name,
			"platform":        a.Platform,
			"channelTitle":    a.ChannelTitle,
			"igUserId":        a.IGUserID,
			"sauAccount":      a.SAUAccount,
			"hasRefreshToken": strings.TrimSpace(a.RefreshToken) != "",
			"hasAccessToken":  strings.TrimSpace(a.AccessToken) != "",
			"updatedAt":       a.UpdatedAt,
		})
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"accounts": safe})
}

func (s *Server) handleSavePublishAccount(w http.ResponseWriter, r *http.Request) {
	var acc model.PublishAccount
	if err := json.NewDecoder(r.Body).Decode(&acc); err != nil {
		writeErr(w, http.StatusBadRequest, "Format JSON akun publish tidak valid")
		return
	}
	acc.Platform = strings.ToLower(strings.TrimSpace(acc.Platform))
	if acc.Platform != "youtube" && acc.Platform != "instagram" && acc.Platform != "tiktok_sau" {
		writeErr(w, http.StatusBadRequest, "platform harus youtube, instagram, atau tiktok_sau")
		return
	}
	if strings.TrimSpace(acc.Name) == "" {
		writeErr(w, http.StatusBadRequest, "nama akun wajib diisi")
		return
	}
	if acc.ID == "" {
		acc.ID = fmt.Sprintf("acc-%s-%d", acc.Platform, time.Now().UnixNano())
	}
	if err := s.Store.SavePublishAccount(&acc); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, map[string]interface{}{
		"ok":       true,
		"id":       acc.ID,
		"name":     acc.Name,
		"platform": acc.Platform,
	})
}

func (s *Server) handleDeletePublishAccount(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.Store.DeletePublishAccount(id); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"deleted": true})
}
