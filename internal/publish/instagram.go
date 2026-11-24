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
	ContainerID  string `json:"containerId"`
	MediaID      string `json:"mediaId"`
	PublishedURL string `json:"publishedUrl,omitempty"`
	Status       string `json:"status"`
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

	// Step 1b: Poll container readiness when Meta processes video asynchronously (prevents 9007 errors in production)
	base := strings.TrimRight(InstagramGraphBaseURL, "/")
	for attempt := 0; attempt < 12; attempt++ {
		stURL := fmt.Sprintf("%s/%s?fields=status_code,status&access_token=%s", base, url.PathEscape(c1.ID), url.QueryEscape(strings.TrimSpace(in.AccessToken)))
		stReq, err := http.NewRequestWithContext(ctx, http.MethodGet, stURL, nil)
		if err != nil {
			break
		}
		stResp, err := client.Do(stReq)
		if err != nil {
			break
		}
		stRaw, _ := io.ReadAll(io.LimitReader(stResp.Body, 2048))
		stResp.Body.Close()
		if stResp.StatusCode >= 300 {
			break // Mock or unsupported status probe -> proceed straight to publish
		}
		var stData struct {
			StatusCode string `json:"status_code"`
			Status     string `json:"status"`
		}
		_ = json.Unmarshal(stRaw, &stData)
		code := strings.ToUpper(strings.TrimSpace(stData.StatusCode))
		if code == "" || code == "FINISHED" || code == "PUBLISHED" {
			break
		}
		if code == "ERROR" || code == "EXPIRED" {
			return InstagramReelResult{}, fmt.Errorf("Instagram container gagal diproses (%s): %s", code, strings.TrimSpace(stData.Status))
		}
		select {
		case <-ctx.Done():
			return InstagramReelResult{}, ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}

	// Step 2: Publish container
	form2 := url.Values{}
	form2.Set("creation_id", c1.ID)
	form2.Set("access_token", strings.TrimSpace(in.AccessToken))

	u2 := fmt.Sprintf("%s/%s/media_publish", base, url.PathEscape(in.IGUserID))
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

	// Step 3: Best-effort permalink fetch
	permalink := ""
	if c2.ID != "" {
		pURL := fmt.Sprintf("%s/%s?fields=permalink&access_token=%s", base, url.PathEscape(c2.ID), url.QueryEscape(strings.TrimSpace(in.AccessToken)))
		if pReq, err := http.NewRequestWithContext(ctx, http.MethodGet, pURL, nil); err == nil {
			if pResp, err := client.Do(pReq); err == nil {
				pRaw, _ := io.ReadAll(io.LimitReader(pResp.Body, 2048))
				pResp.Body.Close()
				if pResp.StatusCode < 300 {
					var pData struct {
						Permalink string `json:"permalink"`
					}
					_ = json.Unmarshal(pRaw, &pData)
					permalink = strings.TrimSpace(pData.Permalink)
				}
			}
		}
	}

	return InstagramReelResult{
		ContainerID:  c1.ID,
		MediaID:      c2.ID,
		PublishedURL: permalink,
		Status:       "published",
	}, nil
}
