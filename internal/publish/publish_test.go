package publish

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestYouTubeUploadAndStatsCollector(t *testing.T) {
	var gotPublishAt string
	var gotVideoBytes int

	mux := http.NewServeMux()
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{"access_token": "mock-access-token"})
	})
	var srvURL string
	mux.HandleFunc("/upload", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]map[string]interface{}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if pa, ok := body["status"]["publishAt"].(string); ok {
			gotPublishAt = pa
		}
		w.Header().Set("Location", srvURL+"/put-bytes")
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("/put-bytes", func(w http.ResponseWriter, r *http.Request) {
		buf := make([]byte, 4096)
		n, _ := r.Body.Read(buf)
		gotVideoBytes = n
		_ = json.NewEncoder(w).Encode(map[string]string{"id": "ytShort12345"})
	})
	mux.HandleFunc("/videos", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"items": []map[string]interface{}{
				{
					"id": "ytShort12345",
					"statistics": map[string]string{
						"viewCount":    "12450",
						"likeCount":    "980",
						"commentCount": "74",
					},
				},
			},
		})
	})

	ts := httptest.NewServer(mux)
	defer ts.Close()
	srvURL = ts.URL

	origOAuth, origUp, origVid := GoogleOAuthTokenURL, YouTubeUploadAPIURL, YouTubeVideosAPIURL
	defer func() {
		GoogleOAuthTokenURL, YouTubeUploadAPIURL, YouTubeVideosAPIURL = origOAuth, origUp, origVid
	}()
	GoogleOAuthTokenURL = ts.URL + "/token"
	YouTubeUploadAPIURL = ts.URL + "/upload"
	YouTubeVideosAPIURL = ts.URL + "/videos"

	mp4 := filepath.Join(t.TempDir(), "clip.mp4")
	_ = os.WriteFile(mp4, []byte("fake-mp4-content-bytes"), 0o644)

	sched := time.Now().Add(2 * time.Hour)
	res, err := UploadYouTubeVideo(context.Background(), YouTubeUploadInput{
		FilePath:     mp4,
		Title:        "Windah GTA VI Nabrak #Shorts",
		Description:  "Credit @windahbasudara #GTAVI",
		Tags:         []string{"windahbasudara", "GTAVI"},
		PublishAt:    &sched,
		ClientID:     "cid",
		ClientSecret: "csec",
		RefreshToken: "rtok",
	})
	if err != nil {
		t.Fatalf("UploadYouTubeVideo failed: %v", err)
	}
	if res.VideoID != "ytShort12345" || gotVideoBytes == 0 || gotPublishAt == "" {
		t.Fatalf("unexpected YouTube upload result: %+v bytes=%d publishAt=%q", res, gotVideoBytes, gotPublishAt)
	}

	stats, err := FetchYouTubeStats(context.Background(), "cid", "csec", "rtok", res.VideoID)
	if err != nil {
		t.Fatalf("FetchYouTubeStats failed: %v", err)
	}
	if stats.Views != 12450 || stats.Likes != 980 || stats.Comments != 74 {
		t.Fatalf("unexpected YouTube stats: %+v", stats)
	}
}

func TestInstagramReelUpload(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/ig123/media", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{"id": "cont-99"})
	})
	mux.HandleFunc("/ig123/media_publish", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{"id": "reel-77"})
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()

	orig := InstagramGraphBaseURL
	defer func() { InstagramGraphBaseURL = orig }()
	InstagramGraphBaseURL = ts.URL

	res, err := UploadInstagramReel(context.Background(), InstagramReelInput{
		IGUserID:    "ig123",
		AccessToken: "tok",
		VideoURL:    "https://klip.breakthematrix.id/api/jobs/j1/video",
		Caption:     "Windah GTA VI #GTAVI",
	})
	if err != nil {
		t.Fatalf("UploadInstagramReel failed: %v", err)
	}
	if res.MediaID != "reel-77" || res.ContainerID != "cont-99" {
		t.Fatalf("unexpected IG result: %+v", res)
	}
}

func TestSAUSidecarMissingBinaryReturnsActionableError(t *testing.T) {
	mp4 := filepath.Join(t.TempDir(), "clip.mp4")
	_ = os.WriteFile(mp4, []byte("mp4"), 0o644)
	_, err := RunSAUSidecar(context.Background(), SAUUploadInput{
		BinaryPath: "nonexistent-sau-binary-xyz",
		Platform:   "tiktok",
		FilePath:   mp4,
	})
	if err == nil || !strings.Contains(err.Error(), "social-auto-upload") {
		t.Fatalf("expected actionable sau missing error, got %v", err)
	}
}
