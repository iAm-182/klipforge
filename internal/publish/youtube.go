package publish

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"klipforge/internal/model"
)

// Endpoints are package vars so offline unit tests can override them with httptest.Server.
var (
	GoogleOAuthTokenURL = "https://oauth2.googleapis.com/token"
	YouTubeUploadAPIURL = "https://www.googleapis.com/upload/youtube/v3/videos?uploadType=resumable&part=snippet,status"
	YouTubeVideosAPIURL = "https://www.googleapis.com/youtube/v3/videos"
)

type YouTubeUploadInput struct {
	FilePath      string
	Title         string
	Description   string
	Tags          []string
	PrivacyStatus string     // "public", "unlisted", "private" (scheduled requires "private" + publishAt)
	PublishAt     *time.Time // Optional RFC3339 scheduled Shorts time
	ClientID      string
	ClientSecret  string
	RefreshToken  string
}

type YouTubeUploadResult struct {
	VideoID string `json:"videoId"`
	URL     string `json:"url"`
	Status  string `json:"status"`
}

// ExchangeRefreshToken exchanges a stored team-account OAuth refresh_token for a fresh access_token.
func ExchangeRefreshToken(ctx context.Context, clientID, clientSecret, refreshToken string) (string, error) {
	if strings.TrimSpace(refreshToken) == "" {
		return "", fmt.Errorf("refresh_token YouTube kosong")
	}
	form := url.Values{}
	form.Set("client_id", strings.TrimSpace(clientID))
	form.Set("client_secret", strings.TrimSpace(clientSecret))
	form.Set("refresh_token", strings.TrimSpace(refreshToken))
	form.Set("grant_type", "refresh_token")

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, GoogleOAuthTokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("gagal menghubungi Google OAuth: %w", err)
	}
	defer resp.Body.Close()

	var parsed struct {
		AccessToken string `json:"access_token"`
		Error       string `json:"error"`
		ErrorDesc   string `json:"error_description"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&parsed)
	if resp.StatusCode >= 300 || parsed.AccessToken == "" {
		return "", fmt.Errorf("OAuth YouTube ditolak (HTTP %d): %s %s", resp.StatusCode, parsed.Error, parsed.ErrorDesc)
	}
	return parsed.AccessToken, nil
}

// UploadYouTubeVideo uploads an approved clip via YouTube Data API v3 resumable upload
// (1 unit per upload in YouTube's dedicated 100-uploads/day bucket), supporting status.publishAt scheduling.
func UploadYouTubeVideo(ctx context.Context, in YouTubeUploadInput) (YouTubeUploadResult, error) {
	if strings.TrimSpace(in.RefreshToken) == "" {
		return YouTubeUploadResult{}, fmt.Errorf("OAuth refresh token YouTube belum dikonfigurasi")
	}
	fi, err := os.Stat(in.FilePath)
	if err != nil || fi.IsDir() {
		return YouTubeUploadResult{}, fmt.Errorf("file MP4 tidak ditemukan: %s", in.FilePath)
	}

	accessToken, err := ExchangeRefreshToken(ctx, in.ClientID, in.ClientSecret, in.RefreshToken)
	if err != nil {
		return YouTubeUploadResult{}, err
	}

	title := strings.TrimSpace(in.Title)
	if len([]rune(title)) > 100 {
		title = string([]rune(title)[:97]) + "..."
	}
	if title == "" {
		title = "KlipForge Short #Shorts"
	}

	privacy := strings.ToLower(strings.TrimSpace(in.PrivacyStatus))
	if privacy != "public" && privacy != "unlisted" && privacy != "private" {
		privacy = "public"
	}
	statusObj := map[string]interface{}{
		"privacyStatus":           privacy,
		"selfDeclaredMadeForKids": false,
	}
	if in.PublishAt != nil && in.PublishAt.After(time.Now()) {
		// YouTube API requires privacyStatus="private" when status.publishAt is set
		statusObj["privacyStatus"] = "private"
		statusObj["publishAt"] = in.PublishAt.UTC().Format(time.RFC3339)
	}

	metaBody, _ := json.Marshal(map[string]interface{}{
		"snippet": map[string]interface{}{
			"title":       title,
			"description": in.Description,
			"tags":        in.Tags,
			"categoryId":  "20", // Gaming
		},
		"status": statusObj,
	})

	// Step 1: Initiate resumable upload session
	initReq, err := http.NewRequestWithContext(ctx, http.MethodPost, YouTubeUploadAPIURL, bytes.NewReader(metaBody))
	if err != nil {
		return YouTubeUploadResult{}, err
	}
	initReq.Header.Set("Authorization", "Bearer "+accessToken)
	initReq.Header.Set("Content-Type", "application/json; charset=UTF-8")
	initReq.Header.Set("X-Upload-Content-Length", strconv.FormatInt(fi.Size(), 10))
	initReq.Header.Set("X-Upload-Content-Type", "video/mp4")

	client := &http.Client{Timeout: 10 * time.Minute}
	initResp, err := client.Do(initReq)
	if err != nil {
		return YouTubeUploadResult{}, fmt.Errorf("gagal inisiasi upload YouTube: %w", err)
	}
	initRaw, _ := io.ReadAll(io.LimitReader(initResp.Body, 4096))
	initResp.Body.Close()
	if initResp.StatusCode >= 300 {
		return YouTubeUploadResult{}, fmt.Errorf("YouTube upload init gagal (HTTP %d): %s", initResp.StatusCode, strings.TrimSpace(string(initRaw)))
	}
	uploadURL := strings.TrimSpace(initResp.Header.Get("Location"))
	if uploadURL == "" {
		return YouTubeUploadResult{}, fmt.Errorf("YouTube tidak mengembalikan header Location untuk resumable upload")
	}

	// Step 2: Stream MP4 file bytes to upload session URL
	f, err := os.Open(in.FilePath)
	if err != nil {
		return YouTubeUploadResult{}, err
	}
	defer f.Close()

	putReq, err := http.NewRequestWithContext(ctx, http.MethodPut, uploadURL, f)
	if err != nil {
		return YouTubeUploadResult{}, err
	}
	putReq.ContentLength = fi.Size()
	putReq.Header.Set("Authorization", "Bearer "+accessToken)
	putReq.Header.Set("Content-Type", "video/mp4")

	putResp, err := client.Do(putReq)
	if err != nil {
		return YouTubeUploadResult{}, fmt.Errorf("gagal upload MP4 ke YouTube: %w", err)
	}
	defer putResp.Body.Close()
	putRaw, _ := io.ReadAll(io.LimitReader(putResp.Body, 8192))
	if putResp.StatusCode >= 300 {
		return YouTubeUploadResult{}, fmt.Errorf("YouTube upload MP4 gagal (HTTP %d): %s", putResp.StatusCode, strings.TrimSpace(string(putRaw)))
	}

	var uploaded struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(putRaw, &uploaded)
	if uploaded.ID == "" {
		return YouTubeUploadResult{}, fmt.Errorf("upload sukses namun YouTube tidak mengembalikan video ID")
	}

	return YouTubeUploadResult{
		VideoID: uploaded.ID,
		URL:     "https://www.youtube.com/shorts/" + uploaded.ID,
		Status:  "published",
	}, nil
}

// FetchYouTubeStats collects views, likes, and comments for a published YouTube Short via videos.list.
func FetchYouTubeStats(ctx context.Context, clientID, clientSecret, refreshToken, videoID string) (model.AnalyticsSnapshot, error) {
	if strings.TrimSpace(videoID) == "" {
		return model.AnalyticsSnapshot{}, fmt.Errorf("videoId YouTube kosong")
	}
	accessToken, err := ExchangeRefreshToken(ctx, clientID, clientSecret, refreshToken)
	if err != nil {
		return model.AnalyticsSnapshot{}, err
	}

	reqURL := fmt.Sprintf("%s?part=statistics&id=%s", YouTubeVideosAPIURL, url.QueryEscape(videoID))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return model.AnalyticsSnapshot{}, err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return model.AnalyticsSnapshot{}, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 16384))
	if resp.StatusCode >= 300 {
		return model.AnalyticsSnapshot{}, fmt.Errorf("YouTube videos.list gagal (HTTP %d): %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}

	var parsed struct {
		Items []struct {
			ID         string `json:"id"`
			Statistics struct {
				ViewCount    string `json:"viewCount"`
				LikeCount    string `json:"likeCount"`
				CommentCount string `json:"commentCount"`
			} `json:"statistics"`
		} `json:"items"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil || len(parsed.Items) == 0 {
		return model.AnalyticsSnapshot{}, fmt.Errorf("statistik video %s tidak ditemukan di YouTube", videoID)
	}
	st := parsed.Items[0].Statistics
	views, _ := strconv.ParseInt(st.ViewCount, 10, 64)
	likes, _ := strconv.ParseInt(st.LikeCount, 10, 64)
	comments, _ := strconv.ParseInt(st.CommentCount, 10, 64)

	return model.AnalyticsSnapshot{
		Platform:  "youtube",
		VideoID:   videoID,
		Views:     views,
		Likes:     likes,
		Comments:  comments,
		FetchedAt: time.Now(),
	}, nil
}
