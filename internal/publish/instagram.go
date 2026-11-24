package publish

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// InstagramGraphBaseURL is a var so unit tests can point it at httptest.Server.
var InstagramGraphBaseURL = "https://graph.facebook.com/v21.0"

type InstagramReelInput struct {
	IGUserID    string
	AccessToken string
	VideoURL    string // Publicly reachable HTTPS URL for the approved MP4
	Caption     string
}

type InstagramReelResult struct {
	ContainerID string `json:"containerId"`
	MediaID     string `json:"mediaId"`
	Status      string `json:"status"`
}

// UploadInstagramReel publishes an approved clip as an Instagram Reel via the official Instagram Graph API
// (Business/Creator account + linked Facebook Page; never uses unofficial instagrapi).
func UploadInstagramReel(ctx context.Context, in InstagramReelInput) (InstagramReelResult, error) {
	if strings.TrimSpace(in.IGUserID) == "" || strings.TrimSpace(in.AccessToken) == "" {
		return InstagramReelResult{}, fmt.Errorf("OAuth accessToken / igUserId Instagram belum dikonfigurasi")
	}
	if strings.TrimSpace(in.VideoURL) == "" {
		return InstagramReelResult{}, fmt.Errorf("videoPublicUrl HTTPS diperlukan oleh Instagram Graph API untuk menarik MP4")
	}

	client := &http.Client{Timeout: 30 * time.Second}

	// Step 1: Create REELS media container
	form1 := url.Values{}
	form1.Set("media_type", "REELS")
	form1.Set("video_url", strings.TrimSpace(in.VideoURL))
	form1.Set("caption", strings.TrimSpace(in.Caption))
	form1.Set("access_token", strings.TrimSpace(in.AccessToken))

	u1 := fmt.Sprintf("%s/%s/media", strings.TrimRight(InstagramGraphBaseURL, "/"), url.PathEscape(in.IGUserID))
	req1, err := http.NewRequestWithContext(ctx, http.MethodPost, u1, strings.NewReader(form1.Encode()))
	if err != nil {
		return InstagramReelResult{}, err
	}
	req1.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp1, err := client.Do(req1)
	if err != nil {
		return InstagramReelResult{}, fmt.Errorf("gagal membuat container Instagram Reels: %w", err)
	}
	raw1, _ := io.ReadAll(io.LimitReader(resp1.Body, 4096))
	resp1.Body.Close()
	if resp1.StatusCode >= 300 {
		return InstagramReelResult{}, fmt.Errorf("Instagram /media gagal (HTTP %d): %s", resp1.StatusCode, strings.TrimSpace(string(raw1)))
	}
	var c1 struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(raw1, &c1)
	if c1.ID == "" {
		return InstagramReelResult{}, fmt.Errorf("Instagram tidak mengembalikan container ID")
	}

	// Step 2: Publish container
	form2 := url.Values{}
	form2.Set("creation_id", c1.ID)
	form2.Set("access_token", strings.TrimSpace(in.AccessToken))

	u2 := fmt.Sprintf("%s/%s/media_publish", strings.TrimRight(InstagramGraphBaseURL, "/"), url.PathEscape(in.IGUserID))
	req2, err := http.NewRequestWithContext(ctx, http.MethodPost, u2, strings.NewReader(form2.Encode()))
	if err != nil {
		return InstagramReelResult{}, err
	}
	req2.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp2, err := client.Do(req2)
	if err != nil {
		return InstagramReelResult{}, fmt.Errorf("gagal publish container Instagram Reels: %w", err)
	}
	raw2, _ := io.ReadAll(io.LimitReader(resp2.Body, 4096))
	resp2.Body.Close()
	if resp2.StatusCode >= 300 {
		return InstagramReelResult{}, fmt.Errorf("Instagram /media_publish gagal (HTTP %d): %s", resp2.StatusCode, strings.TrimSpace(string(raw2)))
	}
	var c2 struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(raw2, &c2)

	return InstagramReelResult{
		ContainerID: c1.ID,
		MediaID:     c2.ID,
		Status:      "published",
	}, nil
}
