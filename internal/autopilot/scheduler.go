package autopilot

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"klipforge/internal/events"
	"klipforge/internal/model"
	"klipforge/internal/renderer"
	"klipforge/internal/store"
	"klipforge/internal/youtube"
)

// AnalyzeFunc invokes the fast Phase-1 project scan synchronously and returns the analyzed project.
type AnalyzeFunc func(ctx context.Context, videoURL, campaignID string) (*model.Project, error)

// Scheduler runs autonomous scheduled channel watches and queues Top N clips into the Human-in-the-Loop Review Inbox.
type Scheduler struct {
	Store   *store.Store
	YT      *youtube.Client
	Worker  *renderer.WorkerPool
	Broker  *events.Broker
	Analyze AnalyzeFunc

	mu           sync.Mutex
	running      map[string]bool
	fetchChannel func(ctx context.Context, channelURL string, limit int, settings model.AppSettings) ([]model.ChannelVideo, error)
}

// SelectVerifiedCandidates enforces the Score-Ban Guardrail (I-05) for Autopilot:
// it NEVER selects candidates solely by auto-detection Score, and ONLY returns
// candidates whose Verdict is explicitly "keep" (respecting limit when limit > 0).
func SelectVerifiedCandidates(cands []model.ClipCandidate, limit int) []model.ClipCandidate {
	var selected []model.ClipCandidate
	for _, c := range cands {
		if c.Verdict == nil || !strings.EqualFold(strings.TrimSpace(c.Verdict.Verdict), "keep") {
			continue
		}
		selected = append(selected, c)
		if limit > 0 && len(selected) >= limit {
			break
		}
	}
	return selected
}

func NewScheduler(st *store.Store, yt *youtube.Client, wp *renderer.WorkerPool, broker *events.Broker, analyze AnalyzeFunc) *Scheduler {
	sc := &Scheduler{
		Store:   st,
		YT:      yt,
		Worker:  wp,
		Broker:  broker,
		Analyze: analyze,
		running: make(map[string]bool),
	}
	go sc.loop()
	return sc
}

func (sc *Scheduler) loop() {
	// Auto-Catchup on startup: 15s after PC wakes/engine boots, run any enabled overdue rules!
	time.AfterFunc(15*time.Second, func() {
		now := time.Now()
		for _, rule := range sc.Store.ListAutopilots() {
			if !rule.Enabled {
				continue
			}
			overdue := rule.NextRunAt == nil || !now.Before(*rule.NextRunAt)
			if overdue {
				sc.Broker.Log("autopilot", "info", fmt.Sprintf("[%s] Auto-Catchup setelah PC menyala — memeriksa VOD terbaru...", rule.Name))
				go sc.RunRule(rule.ID)
			}
		}
	})

	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for range ticker.C {
		now := time.Now()
		for _, rule := range sc.Store.ListAutopilots() {
			if !rule.Enabled {
				continue
			}
			due := rule.NextRunAt == nil || !now.Before(*rule.NextRunAt)
			if due {
				go sc.RunRule(rule.ID)
			}
		}
	}
}

// RunRule executes a single AutopilotRule cycle immediately (safe against concurrent runs of the same rule).
func (sc *Scheduler) RunRule(ruleID string) {
	sc.mu.Lock()
	if sc.running[ruleID] {
		sc.mu.Unlock()
		return
	}
	sc.running[ruleID] = true
	sc.mu.Unlock()
	defer func() {
		sc.mu.Lock()
		delete(sc.running, ruleID)
		sc.mu.Unlock()
	}()

	rule, ok := sc.Store.GetAutopilot(ruleID)
	if !ok {
		return
	}

	now := time.Now()
	next := now.Add(time.Duration(rule.IntervalMinutes) * time.Minute)
	rule.LastRunAt = &now
	rule.NextRunAt = &next
	rule.LastStatus = "Memindai channel YouTube untuk video/VOD baru..."
	_ = sc.Store.SaveAutopilot(rule)
	sc.Broker.Publish("autopilot_update", rule)
	sc.Broker.Log("autopilot", "info", fmt.Sprintf("[%s] Memindai %s ...", rule.Name, rule.ChannelURL))

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()

	fetchFn := sc.fetchChannel
	if fetchFn == nil {
		fetchFn = sc.YT.FetchChannelVideos
	}
	videos, err := fetchFn(ctx, rule.ChannelURL, 15, sc.Store.GetSettings())
	if err != nil {
		rule.LastStatus = "Gagal scan channel: " + err.Error()
		_ = sc.Store.SaveAutopilot(rule)
		sc.Broker.Publish("autopilot_update", rule)
		sc.Broker.Log("autopilot", "error", fmt.Sprintf("[%s] %s", rule.Name, rule.LastStatus))
		return
	}

	processed := make(map[string]bool)
	for _, vid := range rule.ProcessedVideoIDs {
		processed[vid] = true
	}

	var matched []model.ChannelVideo
	for _, v := range videos {
		if processed[v.ID] {
			continue
		}
		if v.LiveStatus == "is_live" || v.LiveStatus == "is_upcoming" {
			continue
		}
		if !matchesKeywords(v.Title, rule.KeywordFilter) {
			continue
		}
		matched = append(matched, v)
		if len(matched) >= rule.MaxVideosPerRun {
			break
		}
	}

	if len(matched) == 0 {
		rule.LastStatus = fmt.Sprintf("Selesai cek (%d video dicek) — belum ada VOD baru yang cocok filter.", len(videos))
		_ = sc.Store.SaveAutopilot(rule)
		sc.Broker.Publish("autopilot_update", rule)
		return
	}

	camp, _ := sc.Store.GetCampaign(rule.CampaignID)
	totalQueued := 0
	var queuedTitles []string

	for _, vid := range matched {
		rule.LastStatus = fmt.Sprintf("Menganalisis VOD baru: %s ...", vid.Title)
		_ = sc.Store.SaveAutopilot(rule)
		sc.Broker.Publish("autopilot_update", rule)

		proj, err := sc.Analyze(ctx, vid.URL, rule.CampaignID)
		if err != nil {
			sc.Broker.Log("autopilot", "error", fmt.Sprintf("[%s] Gagal analisis %s: %v", rule.Name, vid.Title, err))
			continue
		}

		verified := SelectVerifiedCandidates(proj.Candidates, rule.TopClipsPerVideo)
		if len(verified) == 0 {
			sc.Broker.Log("autopilot", "info", fmt.Sprintf("[%s] %s: VOD dianalisis — 0 klip auto-render (Score-Ban aktif: wajib verifikasi transkrip + frame & verdict 'keep' dulu).", rule.Name, vid.Title))
		}
		for _, cand := range verified {
			opts := buildCampaignRenderOptions(proj, cand, camp)
			job, err := sc.Worker.EnqueueWithReview(opts, "pending_review", rule.ID)
			if err == nil {
				totalQueued++
				queuedTitles = append(queuedTitles, job.Title)
			}
		}

		rule.ProcessedVideoIDs = append(rule.ProcessedVideoIDs, vid.ID)
		if len(rule.ProcessedVideoIDs) > 500 {
			rule.ProcessedVideoIDs = rule.ProcessedVideoIDs[len(rule.ProcessedVideoIDs)-500:]
		}
	}

	if totalQueued == 0 {
		rule.LastStatus = "VOD dianalisis — 0 klip auto-render (Score-Ban aktif: wajib verifikasi transkrip + frame & verdict 'keep' dulu)."
		_ = sc.Store.SaveAutopilot(rule)
		sc.Broker.Publish("autopilot_update", rule)
		sc.Broker.Log("autopilot", "info", fmt.Sprintf("[%s] %s", rule.Name, rule.LastStatus))
	} else {
		rule.LastStatus = fmt.Sprintf("Sukses! %d VOD baru diproses → %d klip masuk Review Inbox (Menunggu Approval Anda).", len(matched), totalQueued)
		_ = sc.Store.SaveAutopilot(rule)
		sc.Broker.Publish("autopilot_update", rule)
		sc.Broker.Log("autopilot", "success", fmt.Sprintf("[%s] %s", rule.Name, rule.LastStatus))
	}

	// Fire optional n8n / Discord Webhook notification!
	if rule.NotifyWebhookURL != "" && totalQueued > 0 {
		go SendWebhook(rule.NotifyWebhookURL, map[string]interface{}{
			"event":          "autopilot_clips_queued",
			"ruleId":         rule.ID,
			"ruleName":       rule.Name,
			"campaignId":     rule.CampaignID,
			"videosMatched":  len(matched),
			"clipsQueued":    totalQueued,
			"clipTitles":     queuedTitles,
			"reviewInboxUrl": "https://klip.breakthematrix.id",
			"message": fmt.Sprintf(
				"🤖 **KlipForge Autopilot (%s)**\n%d klip baru otomatis diproses & masuk **Review Inbox** untuk analisis akhir kamu!\n• Klip: %s\n• Review sekarang: https://klip.breakthematrix.id",
				rule.Name, totalQueued, strings.Join(queuedTitles, ", "),
			),
		})
	}
}

// FireApproveWebhook triggers the optional n8n Approve Webhook when a human approves a clip in HITL Inbox.
func FireApproveWebhook(webhookURL string, job *model.RenderJob) {
	if strings.TrimSpace(webhookURL) == "" || job == nil {
		return
	}
	go SendWebhook(webhookURL, map[string]interface{}{
		"event":         "clip_approved",
		"jobId":         job.ID,
		"title":         job.Title,
		"campaignName":  job.CampaignName,
		"postCaption":   job.PostCaption,
		"hashtagLine":   job.HashtagLine,
		"reviewNote":    job.ReviewNote,
		"outputFile":    job.OutputFile,
		"outputPath":    job.OutputPath,
		"finalDuration": job.FinalDuration,
		"videoUrl":      fmt.Sprintf("https://klip.breakthematrix.id/api/jobs/%s/video?download=1", job.ID),
		"message": fmt.Sprintf(
			"✅ **Clip Approved Siap Upload:** %s (%s)\nCaption:\n%s",
			job.Title, job.CampaignName, job.PostCaption,
		),
	})
}

// SendWebhook posts JSON to an n8n Webhook or formats cleanly for Discord Webhooks automatically.
func SendWebhook(webhookURL string, payload map[string]interface{}) {
	body := payload
	if strings.Contains(strings.ToLower(webhookURL), "discord.com/api/webhooks") ||
		strings.Contains(strings.ToLower(webhookURL), "discordapp.com/api/webhooks") {
		msg, _ := payload["message"].(string)
		body = map[string]interface{}{
			"username": "KlipForge Autopilot",
			"content":  msg,
		}
	}
	b, err := json.Marshal(body)
	if err != nil {
		return
	}
	client := &http.Client{Timeout: 12 * time.Second}
	resp, err := client.Post(webhookURL, "application/json", bytes.NewReader(b))
	if err == nil && resp != nil {
		resp.Body.Close()
	}
}

func matchesKeywords(title string, keywords []string) bool {
	if len(keywords) == 0 {
		return true
	}
	low := strings.ToLower(title)
	for _, kw := range keywords {
		k := strings.TrimSpace(strings.ToLower(kw))
		if k != "" && strings.Contains(low, k) {
			return true
		}
	}
	return false
}

func buildCampaignRenderOptions(proj *model.Project, cand model.ClipCandidate, camp *model.Campaign) model.RenderOptions {
	opts := model.RenderOptions{
		ProjectID:   proj.ID,
		CandidateID: cand.ID,
		Title:       cand.Title,
		StartSec:    cand.StartSec,
		EndSec:      cand.EndSec,
		HookText:    cand.HookText,
		AudioNorm:   true,
		SceneSnap:   true,
		SilenceTrim: true,
		PickCover:   true,
	}
	if camp != nil {
		opts.CampaignID = camp.ID
		opts.Layout = camp.DefaultLayout
		opts.Subtitles = camp.DefaultSubtitles
		opts.AudioFX = camp.DefaultAudioFX
		if opts.AudioFX.SFXAutoPreset == "" {
			opts.AudioFX.SFXAutoPreset = "auto_by_moment"
		}
		opts.VideoFX = camp.DefaultVideoFX
		opts.AudioNorm = camp.DefaultAudioNorm
		opts.UseWatermark = camp.Watermark.Enabled
		opts.UseOutro = camp.Outro.Enabled
		opts.UseIntro = camp.Intro.Enabled
	} else {
		opts.Layout = model.LayoutConfig{
			AspectRatio: "9:16",
			Resolution:  "1080p",
			CropMode:    "face_track",
		}
		opts.Subtitles = model.SubtitleStyle{
			Enabled: true, Preset: "karaoke", FontName: "Impact", FontSize: 64, Position: "bottom", MaxWordsPerRow: 3, Uppercase: true,
		}
		opts.AudioFX = model.AudioFXConfig{
			BGMVolume:     0.18,
			AutoDuck:      true,
			VocalEnhance:  true,
			SFXAutoPreset: "auto_by_moment",
		}
	}
	opts.VideoFX.PeakOffsetSec = cand.PeakSec - cand.StartSec
	return opts
}
