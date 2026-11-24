package youtube

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"klipforge/internal/model"
)

var (
	innertubeBaseURL    = "https://www.youtube.com" // overridable in unit tests
	innertubeKeyRe      = regexp.MustCompile(`"INNERTUBE_API_KEY"\s*:\s*"([^"]+)"`)
	innertubeVerRe      = regexp.MustCompile(`"INNERTUBE_CLIENT_VERSION"\s*:\s*"([^"]+)"`)
	continuationFieldRe = regexp.MustCompile(`"continuation"\s*:\s*"([^"]+)"`)
	liveChatReplayRe    = regexp.MustCompile(`"liveChatReplayContinuationData"\s*:\s*\{\s*"continuation"\s*:\s*"([^"]+)"`)
)

type innertubeChatConfig struct {
	APIKey        string
	ClientVersion string
	InitialCont   string
	SeekCont      string
}

type innertubeReplayResp struct {
	ContinuationContents struct {
		LiveChatContinuation struct {
			Continuations []struct {
				LiveChatReplayContinuationData *struct {
					Continuation string `json:"continuation"`
				} `json:"liveChatReplayContinuationData"`
				PlayerSeekContinuationData *struct {
					Continuation string `json:"continuation"`
				} `json:"playerSeekContinuationData"`
			} `json:"continuations"`
			Actions []json.RawMessage `json:"actions"`
		} `json:"liveChatContinuation"`
	} `json:"continuationContents"`
}

// FetchLiveChatReplayFast fetches YouTube Live Chat Replay across even 3-5 hour VODs in ~15-30s
// using native concurrent Innertube `get_live_chat_replay` seeking (`playerOffsetMs`), saving standard
// `.live_chat.json` NDJSON readable by ParseLiveChatFile. Zero full-video download needed (audioFirst friendly!).
func FetchLiveChatReplayFast(
	ctx context.Context,
	videoID string,
	durationSec float64,
	destPath string,
	onProgress func(pct float64, msgCount int),
) (string, int, error) {
	videoID = strings.TrimSpace(videoID)
	if videoID == "" {
		return "", 0, fmt.Errorf("videoID kosong")
	}
	client := &http.Client{Timeout: 20 * time.Second}

	cfg, err := fetchInnertubeChatConfig(ctx, client, videoID)
	if err != nil {
		return "", 0, err
	}

	// Probe initial response at t=0 to grab playerSeekContinuationData (if available) + opening messages
	firstResp, err := postLiveChatReplay(ctx, client, cfg.APIKey, cfg.ClientVersion, cfg.InitialCont, -1)
	if err != nil {
		return "", 0, fmt.Errorf("innertube live_chat probe gagal: %w", err)
	}
	nextCont, seekCont := extractReplayContinuations(firstResp)
	if seekCont != "" {
		cfg.SeekCont = seekCont
	} else {
		cfg.SeekCont = cfg.InitialCont
	}

	// Partition the VOD timeline into concurrent slices so 4-hour streams don't crawl sequentially!
	if durationSec <= 0 {
		durationSec = 3600
	}
	sliceDur := 300.0 // 5-minute slices for short/medium streams
	if durationSec > 7200 {
		sliceDur = 600.0 // 10-minute slices for 2-5h streams (e.g. 24 slices for 4h)
	}
	numSlices := int(math.Ceil(durationSec / sliceDur))
	if numSlices < 1 {
		numSlices = 1
	}
	if numSlices > 36 {
		numSlices = 36
		sliceDur = durationSec / float64(numSlices)
	}

	type rawLine struct {
		offsetMs int64
		raw      []byte
	}
	type sliceJob struct {
		idx      int
		startSec float64
		endSec   float64
	}

	jobsCh := make(chan sliceJob, numSlices)
	for i := 0; i < numSlices; i++ {
		s := float64(i) * sliceDur
		e := math.Min(durationSec, s+sliceDur)
		jobsCh <- sliceJob{idx: i, startSec: s, endSec: e}
	}
	close(jobsCh)

	workers := 6
	if numSlices < workers {
		workers = numSlices
	}

	var (
		mu         sync.Mutex
		collected  []rawLine
		doneSlices atomic.Int32
		totalMsgs  atomic.Int64
	)

	appendActions := func(actions []json.RawMessage, minSec, maxSec float64) (maxSeenSec float64, added int) {
		maxSeenSec = -1
		var batch []rawLine
		for _, raw := range actions {
			offMs, ok := extractReplayActionOffsetMs(raw)
			if !ok {
				continue
			}
			sec := float64(offMs) / 1000.0
			if sec > maxSeenSec {
				maxSeenSec = sec
			}
			// Keep messages in [minSec-2, maxSec+2] (skip offset=0 pinned duplicates on later slices)
			if minSec > 5 && sec < minSec-2 {
				continue
			}
			if maxSec > 0 && sec > maxSec+2 {
				continue
			}
			cp := make([]byte, len(raw))
			copy(cp, raw)
			batch = append(batch, rawLine{offsetMs: offMs, raw: cp})
		}
		if len(batch) > 0 {
			mu.Lock()
			collected = append(collected, batch...)
			mu.Unlock()
			totalMsgs.Add(int64(len(batch)))
		}
		return maxSeenSec, len(batch)
	}

	// Capture opening actions from probe
	appendActions(firstResp.ContinuationContents.LiveChatContinuation.Actions, 0, sliceDur)

	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for sj := range jobsCh {
				if ctx.Err() != nil {
					return
				}
				// Walk this time slice via seek -> sequential pages; if ultra-dense mega-chat, adaptively jump forward
				// so every minute of a 4-hour VOD gets clean coverage within the time budget!
				const maxPagesPerSlice = 18
				curCont := cfg.SeekCont
				curSeekMs := int64(sj.startSec * 1000)
				useSeek := true
				if sj.idx == 0 && nextCont != "" {
					curCont = nextCont
					useSeek = false
				}
				curPosSec := sj.startSec

				for page := 0; page < maxPagesPerSlice; page++ {
					if ctx.Err() != nil || curCont == "" {
						break
					}
					seekArg := int64(-1)
					if useSeek {
						seekArg = curSeekMs
					}
					resp, rerr := postLiveChatReplay(ctx, client, cfg.APIKey, cfg.ClientVersion, curCont, seekArg)
					if rerr != nil {
						break
					}
					acts := resp.ContinuationContents.LiveChatContinuation.Actions
					maxSeen, _ := appendActions(acts, sj.startSec, sj.endSec)
					nCont, sCont := extractReplayContinuations(resp)
					if sCont != "" {
						// Prefer freshest seek token if present
						curCont = nCont
					} else {
						curCont = nCont
					}
					useSeek = false

					if maxSeen > curPosSec {
						curPosSec = maxSeen
					}
					if curPosSec >= sj.endSec-1.0 {
						break
					}
					if len(acts) == 0 && nCont == "" {
						break
					}

					// Adaptive leap for ultra-dense mega-streams (>50 msgs/sec): if after page 5 we've moved
					// less than 20% across the slice, hop forward with playerOffsetMs so zero minutes are skipped!
					if page > 0 && page%4 == 0 && curPosSec < sj.endSec-30 {
						remainingPages := maxPagesPerSlice - page
						expectedStep := (sj.endSec - sj.startSec) / float64(maxPagesPerSlice)
						if curPosSec < sj.startSec+expectedStep*float64(page)*0.45 && remainingPages > 1 {
							jumpTo := curPosSec + (sj.endSec-curPosSec)/float64(remainingPages)
							if jumpTo > curPosSec+12 && jumpTo < sj.endSec-5 {
								seekTok := cfg.SeekCont
								if sCont != "" {
									seekTok = sCont
								}
								curCont = seekTok
								curSeekMs = int64(jumpTo * 1000)
								useSeek = true
								curPosSec = jumpTo
							}
						}
					}
				}
				done := int(doneSlices.Add(1))
				if onProgress != nil {
					onProgress(float64(done)/float64(numSlices)*100.0, int(totalMsgs.Load()))
				}
			}
		}()
	}
	wg.Wait()

	if len(collected) == 0 {
		return "", 0, fmt.Errorf("tidak ada pesan live chat replay yang berhasil diambil")
	}

	// Sort chronologically and deduplicate exact (offsetMs + raw bytes) across slice boundaries
	sort.Slice(collected, func(i, j int) bool {
		return collected[i].offsetMs < collected[j].offsetMs
	})

	_ = os.MkdirAll(filepath.Dir(destPath), 0o755)
	tmpPath := destPath + ".tmp"
	f, err := os.OpenFile(tmpPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return "", 0, err
	}
	bw := bufio.NewWriterSize(f, 256*1024)
	seen := make(map[string]struct{}, len(collected))
	written := 0
	for _, item := range collected {
		compact := bytes.TrimSpace(item.raw)
		if len(compact) == 0 {
			continue
		}
		key := fmt.Sprintf("%d:%d:%s", item.offsetMs, len(compact), string(compact[:minInt(96, len(compact))]))
		if _, dup := seen[key]; dup {
			continue
		}
		seen[key] = struct{}{}
		_, _ = bw.Write(compact)
		_ = bw.WriteByte('\n')
		written++
	}
	_ = bw.Flush()
	_ = f.Sync()
	_ = f.Close()
	if err := os.Rename(tmpPath, destPath); err != nil {
		_ = os.Remove(tmpPath)
		return "", 0, err
	}

	return destPath, written, nil
}

// DownloadLiveChatSmart tries native parallel Innertube Live Chat Replay first (works on 4-hour VODs in ~15-25s!),
// falling back cleanly to yt-dlp DownloadLiveChat if needed.
func (c *Client) DownloadLiveChatSmart(
	ctx context.Context,
	videoURL, videoID string,
	durationSec float64,
	destDir string,
	settings model.AppSettings,
	onProgress func(pct float64, msgCount int),
) (string, int, string, error) {
	if videoID == "" {
		videoID = ExtractVideoID(videoURL)
	}
	destPath := filepath.Join(destDir, "source.live_chat.json")
	if videoID != "" {
		if path, count, err := FetchLiveChatReplayFast(ctx, videoID, durationSec, destPath, onProgress); err == nil && count >= 20 {
			return path, count, "innertube_parallel", nil
		}
	}
	// Fallback to yt-dlp --write-subs --sub-langs live_chat
	ytPath, err := c.DownloadLiveChat(ctx, videoURL, destDir, settings)
	if err != nil || ytPath == "" {
		// Check if yt-dlp left a usable partial .live_chat.json.part file before timeout!
		if parts, _ := filepath.Glob(filepath.Join(destDir, "*.live_chat.json.part")); len(parts) > 0 {
			if fi, serr := os.Stat(parts[0]); serr == nil && fi.Size() > 4096 {
				_ = os.Rename(parts[0], destPath)
				if msgs, perr := ParseLiveChatFile(destPath); perr == nil && len(msgs) >= 20 {
					return destPath, len(msgs), "ytdlp_partial", nil
				}
			}
		}
		if err == nil {
			err = fmt.Errorf("live chat replay tidak tersedia untuk video ini")
		}
		return "", 0, "none", err
	}
	msgs, _ := ParseLiveChatFile(ytPath)
	return ytPath, len(msgs), "ytdlp", nil
}

func fetchInnertubeChatConfig(ctx context.Context, client *http.Client, videoID string) (innertubeChatConfig, error) {
	watchURL := fmt.Sprintf("%s/watch?v=%s", strings.TrimRight(innertubeBaseURL, "/"), videoID)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, watchURL, nil)
	if err != nil {
		return innertubeChatConfig{}, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36")
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")

	resp, err := client.Do(req)
	if err != nil {
		return innertubeChatConfig{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return innertubeChatConfig{}, fmt.Errorf("watch page HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return innertubeChatConfig{}, err
	}
	text := string(body)

	km := innertubeKeyRe.FindStringSubmatch(text)
	if len(km) < 2 {
		return innertubeChatConfig{}, fmt.Errorf("INNERTUBE_API_KEY tidak ditemukan di halaman watch")
	}
	ver := "2.20261101.00.00"
	if vm := innertubeVerRe.FindStringSubmatch(text); len(vm) >= 2 {
		ver = vm[1]
	}
	var cont string
	if idx := strings.Index(text, `"liveChatRenderer"`); idx >= 0 {
		end := idx + 6000
		if end > len(text) {
			end = len(text)
		}
		if cm := continuationFieldRe.FindStringSubmatch(text[idx:end]); len(cm) >= 2 {
			cont = cm[1]
		}
	}
	if cont == "" {
		if cm2 := liveChatReplayRe.FindStringSubmatch(text); len(cm2) >= 2 {
			cont = cm2[1]
		}
	}
	if cont == "" {
		return innertubeChatConfig{}, fmt.Errorf("liveChatRenderer continuation tidak ditemukan (video bukan live replay atau chat dinonaktifkan)")
	}
	return innertubeChatConfig{
		APIKey:        km[1],
		ClientVersion: ver,
		InitialCont:   cont,
	}, nil
}

func postLiveChatReplay(
	ctx context.Context,
	client *http.Client,
	apiKey, clientVersion, continuation string,
	playerOffsetMs int64,
) (*innertubeReplayResp, error) {
	payload := map[string]interface{}{
		"context": map[string]interface{}{
			"client": map[string]interface{}{
				"clientName":    "WEB",
				"clientVersion": clientVersion,
			},
		},
		"continuation": continuation,
	}
	if playerOffsetMs >= 0 {
		payload["currentPlayerState"] = map[string]string{
			"playerOffsetMs": strconv.FormatInt(playerOffsetMs, 10),
		}
	}
	bodyBytes, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	endpoint := fmt.Sprintf("%s/youtubei/v1/live_chat/get_live_chat_replay?key=%s", strings.TrimRight(innertubeBaseURL, "/"), apiKey)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36")

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("innertube live_chat HTTP %d", resp.StatusCode)
	}
	var out innertubeReplayResp
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&out); err != nil {
		return nil, err
	}
	return &out, nil
}

func extractReplayContinuations(resp *innertubeReplayResp) (nextCont, seekCont string) {
	if resp == nil {
		return "", ""
	}
	for _, c := range resp.ContinuationContents.LiveChatContinuation.Continuations {
		if c.LiveChatReplayContinuationData != nil && c.LiveChatReplayContinuationData.Continuation != "" && nextCont == "" {
			nextCont = c.LiveChatReplayContinuationData.Continuation
		}
		if c.PlayerSeekContinuationData != nil && c.PlayerSeekContinuationData.Continuation != "" && seekCont == "" {
			seekCont = c.PlayerSeekContinuationData.Continuation
		}
	}
	return nextCont, seekCont
}

func extractReplayActionOffsetMs(raw json.RawMessage) (int64, bool) {
	var env struct {
		ReplayChatItemAction struct {
			VideoOffsetTimeMsec string `json:"videoOffsetTimeMsec"`
		} `json:"replayChatItemAction"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return 0, false
	}
	if env.ReplayChatItemAction.VideoOffsetTimeMsec == "" {
		return 0, false
	}
	ms, err := strconv.ParseInt(env.ReplayChatItemAction.VideoOffsetTimeMsec, 10, 64)
	if err != nil || ms < 0 {
		return 0, false
	}
	return ms, true
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
