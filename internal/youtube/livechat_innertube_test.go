package youtube

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"testing"
)

func TestFetchLiveChatReplayFast_ParallelSeekingAndParse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/watch" {
			_, _ = w.Write([]byte(`<html><script>{"INNERTUBE_API_KEY":"TESTKEY","INNERTUBE_CLIENT_VERSION":"2.20261124.00.00","liveChatRenderer":{"continuations":[{"reloadContinuationData":{"continuation":"INIT_CONT"}}]}}</script></html>`))
			return
		}
		if r.URL.Path == "/youtubei/v1/live_chat/get_live_chat_replay" {
			var req struct {
				Continuation       string `json:"continuation"`
				CurrentPlayerState *struct {
					PlayerOffsetMs string `json:"playerOffsetMs"`
				} `json:"currentPlayerState"`
			}
			_ = json.NewDecoder(r.Body).Decode(&req)
			baseMs := int64(0)
			if req.CurrentPlayerState != nil {
				if v, err := strconv.ParseInt(req.CurrentPlayerState.PlayerOffsetMs, 10, 64); err == nil {
					baseMs = v
				}
			}
			// Emit 3 messages spaced across this slice so ParseLiveChatFile reads them cleanly!
			var acts []map[string]interface{}
			for i := 0; i < 3; i++ {
				off := baseMs + int64(i*60000+5000)
				acts = append(acts, map[string]interface{}{
					"replayChatItemAction": map[string]interface{}{
						"videoOffsetTimeMsec": strconv.FormatInt(off, 10),
						"actions": []map[string]interface{}{
							{
								"addChatItemAction": map[string]interface{}{
									"item": map[string]interface{}{
										"liveChatTextMessageRenderer": map[string]interface{}{
											"authorExternalChannelId": fmt.Sprintf("UC_user_%d_%d", baseMs, i),
											"authorName":              map[string]string{"simpleText": fmt.Sprintf("Viewer%d", i)},
											"message":                 map[string]interface{}{"runs": []map[string]string{{"text": "WKWKWK KOCAK BANGET CLIP!"}}},
										},
									},
								},
							},
						},
					},
				})
			}
			resp := map[string]interface{}{
				"continuationContents": map[string]interface{}{
					"liveChatContinuation": map[string]interface{}{
						"continuations": []map[string]interface{}{
							{"playerSeekContinuationData": map[string]string{"continuation": "SEEK_CONT"}},
						},
						"actions": acts,
					},
				},
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(resp)
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()

	origBase := innertubeBaseURL
	innertubeBaseURL = srv.URL
	defer func() { innertubeBaseURL = origBase }()

	dest := filepath.Join(t.TempDir(), "source.live_chat.json")
	path, written, err := FetchLiveChatReplayFast(context.Background(), "45r3OyaxvFE", 1200, dest, nil)
	if err != nil {
		t.Fatalf("FetchLiveChatReplayFast failed: %v", err)
	}
	if path != dest || written < 9 {
		t.Fatalf("expected >=9 unique messages across 4 slices, got written=%d path=%s", written, path)
	}

	msgs, err := ParseLiveChatFile(path)
	if err != nil {
		t.Fatalf("ParseLiveChatFile failed: %v", err)
	}
	if len(msgs) != written {
		t.Fatalf("expected %d parsed messages, got %d", written, len(msgs))
	}
	if msgs[0].AuthorID == "" || msgs[0].Author == "" {
		t.Fatalf("expected AuthorID & Author populated, got %+v", msgs[0])
	}
}
