package renderer

import (
	"context"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"klipforge/internal/audiofx"
	"klipforge/internal/deps"
	"klipforge/internal/events"
	"klipforge/internal/model"
	"klipforge/internal/store"
	"klipforge/internal/youtube"
)

// WorkerPool runs clip download + FFmpeg render jobs concurrently using goroutines.
type WorkerPool struct {
	Store   *store.Store
	Deps    *deps.Manager
	YT      *youtube.Client
	Audio   *audiofx.Library
	Broker  *events.Broker

	queue   chan string
	mu      sync.Mutex
	cancels map[string]context.CancelFunc
}

func NewWorkerPool(st *store.Store, d *deps.Manager, yt *youtube.Client, audioLib *audiofx.Library, broker *events.Broker) *WorkerPool {
	wp := &WorkerPool{
		Store:   st,
		Deps:    d,
		YT:      yt,
		Audio:   audioLib,
		Broker:  broker,
		queue:   make(chan string, 128),
		cancels: make(map[string]context.CancelFunc),
	}
	workers := st.GetSettings().MaxWorkers
	if workers < 1 {
		workers = 2
	}
	for i := 0; i < workers; i++ {
		go wp.workerLoop(i + 1)
	}
	return wp
}

// Enqueue creates and queues a new RenderJob.
func (wp *WorkerPool) Enqueue(opts model.RenderOptions) (*model.RenderJob, error) {
	return wp.EnqueueWithReview(opts, "", "")
}

// EnqueueWithReview creates a RenderJob optionally tagged for Human-in-the-Loop (HITL) Autopilot Review.
func (wp *WorkerPool) EnqueueWithReview(opts model.RenderOptions, reviewStatus, autopilotID string) (*model.RenderJob, error) {
	proj, ok := wp.Store.GetProject(opts.ProjectID)
	if !ok {
		return nil, fmt.Errorf("project tidak ditemukan: %s", opts.ProjectID)
	}

	camp, _ := wp.Store.GetCampaign(opts.CampaignID)
	if camp == nil && proj.CampaignID != "" {
		camp, _ = wp.Store.GetCampaign(proj.CampaignID)
	}

	settings := wp.Store.GetSettings()
	if opts.Encoder == "" {
		opts.Encoder = settings.DefaultEncoder
	}
	if opts.Preset == "" {
		opts.Preset = settings.DefaultPreset
	}
	if opts.CRF <= 0 {
		opts.CRF = settings.DefaultCRF
	}
	if opts.EndSec <= opts.StartSec {
		return nil, fmt.Errorf("endSec harus lebih besar dari startSec")
	}

	// Infer peak offset inside clip if candidate ID matches
	if opts.VideoFX.PeakOffsetSec <= 0 && opts.CandidateID != "" {
		for _, c := range proj.Candidates {
			if c.ID == opts.CandidateID {
				offset := c.PeakSec - opts.StartSec
				if offset > 0.3 && offset < (opts.EndSec-opts.StartSec)-0.5 {
					opts.VideoFX.PeakOffsetSec = offset
				}
				break
			}
		}
	}

	campName := "Custom"
	campID := ""
	if camp != nil {
		campName = camp.Name
		campID = camp.ID
	}

	jobID := fmt.Sprintf("job-%d", time.Now().UnixNano())
	title := strings.TrimSpace(opts.Title)
	if title == "" {
		title = fmt.Sprintf("Clip %s", formatRange(opts.StartSec, opts.EndSec))
	}

	caption, hashtags := buildPostKit(title, opts.HookText, camp)
	compliance := evaluateCompliance(opts, camp)

	job := &model.RenderJob{
		ID:           jobID,
		ProjectID:    proj.ID,
		ProjectTitle: proj.Title,
		CampaignID:   campID,
		CampaignName: campName,
		Title:        title,
		Options:      opts,
		Status:       "queued",
		Stage:        "Menunggu antrean worker...",
		Progress:     0,
		PostCaption:  caption,
		HashtagLine:  hashtags,
		Compliance:   compliance,
		ReviewStatus: reviewStatus,
		AutopilotID:  autopilotID,
		CreatedAt:    time.Now(),
	}

	if err := wp.Store.SaveJob(job); err != nil {
		return nil, err
	}
	wp.Broker.Publish("job_update", job)
	wp.queue <- job.ID
	return job, nil
}

// Cancel stops a queued or running job.
func (wp *WorkerPool) Cancel(jobID string) {
	wp.mu.Lock()
	cancel, ok := wp.cancels[jobID]
	wp.mu.Unlock()
	if ok {
		cancel()
	}
	if job, exists := wp.Store.GetJob(jobID); exists {
		if job.Status == "queued" || job.Status == "downloading" || job.Status == "rendering" {
			job.Status = "canceled"
			job.Stage = "Dibatalkan pengguna"
			_ = wp.Store.SaveJob(job)
			wp.Broker.Publish("job_update", job)
		}
	}
}

// EnqueueCompilation creates and queues a multi-clip Top-N Countdown or Rapid Compilation job!
func (wp *WorkerPool) EnqueueCompilation(opts model.CompileOptions) (*model.RenderJob, error) {
	if len(opts.Segments) == 0 {
		return nil, fmt.Errorf("minimal 1 klip diperlukan untuk membuat Top-N / Kompilasi")
	}
	camp, _ := wp.Store.GetCampaign(opts.CampaignID)
	settings := wp.Store.GetSettings()
	if opts.Encoder == "" {
		opts.Encoder = settings.DefaultEncoder
	}
	if opts.Preset == "" {
		opts.Preset = settings.DefaultPreset
	}
	if opts.CRF <= 0 {
		opts.CRF = settings.DefaultCRF
	}
	if opts.Mode == "" {
		opts.Mode = "top_countdown"
	}
	if opts.BumperDuration <= 0 {
		opts.BumperDuration = 0.85
	}
	if opts.BumperSFXID == "" {
		opts.BumperSFXID = "sfx-boom"
	}

	campName := "Custom"
	campID := ""
	if camp != nil {
		campName = camp.Name
		campID = camp.ID
	}
	title := strings.TrimSpace(opts.Title)
	if title == "" {
		title = fmt.Sprintf("Top %d Viral Moments", len(opts.Segments))
	}

	jobID := fmt.Sprintf("comp-%d", time.Now().UnixNano())
	caption, hashtags := buildPostKit(title, title, camp)

	// Build compliance evaluation
	comp := model.ComplianceCheck{HasValueAdd: true}
	if camp != nil {
		if opts.UseWatermark && camp.Watermark.Enabled && camp.Watermark.FilePath != "" && fileExists(camp.Watermark.FilePath) {
			comp.WatermarkApplied = true
		} else if camp.Watermark.Enabled {
			comp.Warnings = append(comp.Warnings, "Watermark campaign belum di-upload!")
		}
		if opts.UseOutro && camp.Outro.Enabled && camp.Outro.FilePath != "" && fileExists(camp.Outro.FilePath) {
			comp.OutroApplied = true
			comp.OutroClean = !camp.Watermark.OnOutro
		} else if camp.Outro.Enabled {
			comp.Warnings = append(comp.Warnings, "Video Outro 3 detik belum di-upload!")
		}
		comp.HashtagsReady = len(camp.Hashtags) > 0
	}

	optsCopy := opts
	job := &model.RenderJob{
		ID:           jobID,
		JobType:      "compilation",
		ProjectID:    opts.Segments[0].ProjectID,
		ProjectTitle: fmt.Sprintf("Kompilasi %d Klip", len(opts.Segments)),
		CampaignID:   campID,
		CampaignName: campName,
		Title:        title,
		CompileOpts:  &optsCopy,
		Status:       "queued",
		Stage:        fmt.Sprintf("Antrean Top-%d Kompilasi (%d klip)...", len(opts.Segments), len(opts.Segments)),
		Progress:     0,
		PostCaption:  caption,
		HashtagLine:  hashtags,
		Compliance:   comp,
		CreatedAt:    time.Now(),
	}

	if err := wp.Store.SaveJob(job); err != nil {
		return nil, err
	}
	wp.Broker.Publish("job_update", job)
	wp.queue <- job.ID
	return job, nil
}

func (wp *WorkerPool) workerLoop(workerID int) {
	for jobID := range wp.queue {
		wp.processJob(workerID, jobID)
	}
}

func (wp *WorkerPool) processJob(workerID int, jobID string) {
	job, ok := wp.Store.GetJob(jobID)
	if !ok || job.Status == "canceled" {
		return
	}
	if job.JobType == "compilation" && job.CompileOpts != nil {
		wp.processCompilationJob(workerID, job)
		return
	}

	ctx, cancel := context.WithCancel(context.Background())
	wp.mu.Lock()
	wp.cancels[jobID] = cancel
	wp.mu.Unlock()
	defer func() {
		cancel()
		wp.mu.Lock()
		delete(wp.cancels, jobID)
		wp.mu.Unlock()
	}()

	proj, ok := wp.Store.GetProject(job.ProjectID)
	if !ok {
		wp.failJob(job, "Project tidak ditemukan")
		return
	}
	var camp *model.Campaign
	if job.CampaignID != "" {
		camp, _ = wp.Store.GetCampaign(job.CampaignID)
	}

	settings := wp.Store.GetSettings()
	workDir := filepath.Join(wp.Store.DataDir, "tmp", job.ID)
	_ = os.MkdirAll(workDir, 0o755)
	defer os.RemoveAll(workDir)

	opts := job.Options
	clipDur := opts.EndSec - opts.StartSec
	speed := opts.VideoFX.Speed
	if speed < 0.75 || speed > 1.50 {
		speed = 1.0
	}
	sourcePath := ""
	seekStart := 0.0

	// Step 1: Use local full video if available, otherwise fast-download just this clip section
	if proj.LocalVideoPath != "" && fileExists(proj.LocalVideoPath) {
		sourcePath = proj.LocalVideoPath
		seekStart = opts.StartSec
		wp.updateJob(job, "rendering", "Memotong langsung dari video lokal...", 12, "")
	} else {
		wp.updateJob(job, "downloading", fmt.Sprintf("Download segmen %s via yt-dlp...", formatRange(opts.StartSec, opts.EndSec)), 5, "")
		wp.Broker.Log("worker", "info", fmt.Sprintf("[W%d] Download segmen %s (%s)", workerID, job.Title, formatRange(opts.StartSec, opts.EndSec)))

		secFile := filepath.Join(workDir, "segment.mp4")
		err := wp.YT.DownloadSection(ctx, proj.SourceURL, opts.StartSec, opts.EndSec, secFile, settings, func(pct float64, line string) {
			if pct >= 0 {
				wp.updateJob(job, "downloading", fmt.Sprintf("Downloading segmen (%.0f%%)...", pct), 5+pct*0.30, "")
			}
		})
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			wp.failJob(job, err.Error())
			return
		}
		sourcePath = secFile
		seekStart = 0
	}

	// Step 2: Generate ASS Viral Word-by-Word Karaoke Subtitles + Animated Hook Banner (zero tofu boxes!)
	subASS := ""
	var cues []model.TranscriptCue
	if opts.Subtitles.Enabled && proj.SubtitlePath != "" && fileExists(proj.SubtitlePath) {
		if parsed, err := youtube.ParseSubtitleFile(proj.SubtitlePath); err == nil && len(parsed) > 0 {
			cues = parsed
		}
	}
	if len(cues) > 0 || strings.TrimSpace(opts.HookText) != "" {
		wp.updateJob(job, "rendering", "Menyiapkan Karaoke Subtitles & Animated Hook (.ASS)...", 36, "")
		outW, outH := ResolveOutputDimensions(opts.Layout, 1920, 1080)
		assPath := filepath.Join(workDir, "captions.ass")
		if count, err := GenerateASS(cues, opts.StartSec, opts.EndSec, speed, outW, outH, opts.Subtitles, opts.HookText, opts.Layout.CropMode, assPath); err == nil && count > 0 {
			subASS = assPath
		}
	}

	// Step 3: Resolve Backsound (BGM) + Sound Effects (SFX) cues from Audio Library
	bgmPath := ""
	if wp.Audio != nil && opts.AudioFX.BGMAssetID != "" {
		if a, ok := wp.Audio.Get(opts.AudioFX.BGMAssetID); ok && fileExists(a.FilePath) {
			bgmPath = a.FilePath
		}
	}
	peakOffset := opts.VideoFX.PeakOffsetSec
	if peakOffset <= 0.2 || peakOffset >= clipDur-0.5 {
		peakOffset = clipDur * 0.38
	}
	resolvedSFX := wp.resolveSFXCues(opts.AudioFX, peakOffset, clipDur)

	// Step 4: Prepare campaign watermark / intro / outro paths
	var wmCfg *model.WatermarkConfig
	var introCfg *model.ClipAssetVideo
	var outroCfg *model.ClipAssetVideo
	if camp != nil {
		if opts.UseWatermark && camp.Watermark.Enabled && camp.Watermark.FilePath != "" && fileExists(camp.Watermark.FilePath) {
			cp := camp.Watermark
			wmCfg = &cp
		}
		if opts.UseIntro && camp.Intro.Enabled && camp.Intro.FilePath != "" && fileExists(camp.Intro.FilePath) {
			cp := camp.Intro
			introCfg = &cp
		}
		if opts.UseOutro && camp.Outro.Enabled && camp.Outro.FilePath != "" && fileExists(camp.Outro.FilePath) {
			cp := camp.Outro
			outroCfg = &cp
		}
	}

	// Step 5: Output path organized cleanly by campaign folder
	campSlug := sanitizeFilename(job.CampaignName)
	if campSlug == "" {
		campSlug = "clips"
	}
	outDir := filepath.Join(settings.OutputDir, campSlug)
	_ = os.MkdirAll(outDir, 0o755)
	fileName := fmt.Sprintf("%s_%s_%s.mp4",
		sanitizeFilename(job.Title),
		strings.ReplaceAll(formatRange(opts.StartSec, opts.EndSec), ":", ""),
		job.ID[len(job.ID)-5:],
	)
	outPath := filepath.Join(outDir, fileName)

	wp.updateJob(job, "rendering", "Rendering FFmpeg (FX + BGM/SFX + Watermark + Outro)...", 40, "")
	wp.Broker.Log("worker", "info", fmt.Sprintf("[W%d] Rendering %s (BGM:%v, SFX:%d, Color:%s, Motion:%s)",
		workerID, fileName, bgmPath != "", len(resolvedSFX), opts.VideoFX.ColorPreset, opts.VideoFX.MotionPreset))

	depSt := wp.Deps.GetStatus()
	info, err := RenderClip(ctx, RenderInput{
		FFmpegPath:   depSt.FFmpegPath,
		FFprobePath:  depSt.FFprobePath,
		SourcePath:   sourcePath,
		SeekStart:    seekStart,
		ClipDuration: clipDur,
		OutputPath:   outPath,
		WorkDir:      workDir,
		Layout:       opts.Layout,
		Subtitles:    opts.Subtitles,
		SubtitleFile: subASS,
		HookText:     opts.HookText,
		AudioNorm:    opts.AudioNorm,
		VideoFX:      opts.VideoFX,
		BGMPath:      bgmPath,
		BGMVolume:    opts.AudioFX.BGMVolume,
		BGMAutoDuck:  opts.AudioFX.AutoDuck,
		SFXCues:      resolvedSFX,
		Watermark:    wmCfg,
		Intro:        introCfg,
		Outro:        outroCfg,
		Encoder:      opts.Encoder,
		Preset:       opts.Preset,
		CRF:          opts.CRF,
		OnProgress: func(pct float64, speed string) {
			overall := 40.0 + pct*0.59
			if overall > 99 {
				overall = 99
			}
			wp.updateJob(job, "rendering", fmt.Sprintf("Rendering FFmpeg (%.0f%%) %s", pct, speed), overall, speed)
		},
	})
	if err != nil {
		if ctx.Err() != nil {
			return
		}
		wp.failJob(job, err.Error())
		return
	}

	var size int64
	if fi, err := os.Stat(outPath); err == nil {
		size = fi.Size()
	}
	now := time.Now()
	job.Status = "done"
	job.Stage = "Selesai & siap upload!"
	job.Progress = 100
	job.OutputPath = outPath
	job.OutputFile = fileName
	job.FileSizeBytes = size
	job.FinalDuration = info.Duration
	job.CompletedAt = &now
	job.Compliance = evaluateCompliance(opts, camp)
	_ = wp.Store.SaveJob(job)
	wp.Broker.Publish("job_update", job)
	wp.Broker.Log("worker", "success", fmt.Sprintf("Clip selesai: %s (%.1fs, %.2f MB)", fileName, info.Duration, float64(size)/(1024*1024)))
}

func (wp *WorkerPool) processCompilationJob(workerID int, job *model.RenderJob) {
	ctx, cancel := context.WithCancel(context.Background())
	wp.mu.Lock()
	wp.cancels[job.ID] = cancel
	wp.mu.Unlock()
	defer func() {
		cancel()
		wp.mu.Lock()
		delete(wp.cancels, job.ID)
		wp.mu.Unlock()
	}()

	opts := *job.CompileOpts
	var camp *model.Campaign
	if job.CampaignID != "" {
		camp, _ = wp.Store.GetCampaign(job.CampaignID)
	}
	settings := wp.Store.GetSettings()
	workDir := filepath.Join(wp.Store.DataDir, "tmp", job.ID)
	_ = os.MkdirAll(workDir, 0o755)
	defer os.RemoveAll(workDir)

	total := len(opts.Segments)
	resolved := make([]ResolvedCompileSegment, 0, total)
	allRankLabels := make(map[int]string)

	// Step 1: Download or map each segment (countdown rank = total - i, e.g. 5, 4, 3, 2, 1!)
	for i, seg := range opts.Segments {
		if ctx.Err() != nil {
			return
		}
		rankNum := total - i
		lbl := strings.TrimSpace(seg.Label)
		if lbl == "" {
			lbl = fmt.Sprintf("Momen #%d", rankNum)
		}
		allRankLabels[rankNum] = lbl

		proj, ok := wp.Store.GetProject(seg.ProjectID)
		if !ok {
			wp.failJob(job, fmt.Sprintf("Project segmen #%d tidak ditemukan", rankNum))
			return
		}
		if seg.EndSec <= seg.StartSec {
			wp.failJob(job, fmt.Sprintf("Durasi segmen #%d tidak valid", rankNum))
			return
		}

		srcPath := ""
		seekStart := 0.0
		if proj.LocalVideoPath != "" && fileExists(proj.LocalVideoPath) {
			srcPath = proj.LocalVideoPath
			seekStart = seg.StartSec
		} else {
			dlPct := 5.0 + (float64(i)/float64(total))*30.0
			wp.updateJob(job, "downloading", fmt.Sprintf("Download segmen %d/%d (#%d %s)...", i+1, total, rankNum, lbl), dlPct, "")
			secFile := filepath.Join(workDir, fmt.Sprintf("dl_%d.mp4", i))
			if err := wp.YT.DownloadSection(ctx, proj.SourceURL, seg.StartSec, seg.EndSec, secFile, settings, nil); err != nil {
				if ctx.Err() != nil {
					return
				}
				wp.failJob(job, fmt.Sprintf("Gagal download segmen #%d: %v", rankNum, err))
				return
			}
			srcPath = secFile
			seekStart = 0
		}

		endSfxPath := ""
		if wp.Audio != nil && seg.EndSFXID != "" {
			if a, ok := wp.Audio.Get(seg.EndSFXID); ok && fileExists(a.FilePath) {
				endSfxPath = a.FilePath
			}
		}

		resolved = append(resolved, ResolvedCompileSegment{
			SourcePath:   srcPath,
			SeekStart:    seekStart,
			ClipDuration: seg.EndSec - seg.StartSec,
			RankNumber:   rankNum,
			Label:        lbl,
			OrigStart:    seg.StartSec,
			OrigEnd:      seg.EndSec,
			EndSFXPath:   endSfxPath,
			EndSFXOffset: seg.EndSFXOffset,
		})
	}

	bumperSfxPath := ""
	if wp.Audio != nil && opts.BumperSFXID != "" {
		if a, ok := wp.Audio.Get(opts.BumperSFXID); ok && fileExists(a.FilePath) {
			bumperSfxPath = a.FilePath
		}
	}

	var wmCfg *model.WatermarkConfig
	var outroCfg *model.ClipAssetVideo
	if camp != nil {
		if opts.UseWatermark && camp.Watermark.Enabled && camp.Watermark.FilePath != "" && fileExists(camp.Watermark.FilePath) {
			cp := camp.Watermark
			wmCfg = &cp
		}
		if opts.UseOutro && camp.Outro.Enabled && camp.Outro.FilePath != "" && fileExists(camp.Outro.FilePath) {
			cp := camp.Outro
			outroCfg = &cp
		}
	}

	campSlug := sanitizeFilename(job.CampaignName)
	if campSlug == "" {
		campSlug = "clips"
	}
	outDir := filepath.Join(settings.OutputDir, campSlug)
	_ = os.MkdirAll(outDir, 0o755)
	fileName := fmt.Sprintf("TOP%d_%s_%s.mp4", total, sanitizeFilename(job.Title), job.ID[len(job.ID)-5:])
	outPath := filepath.Join(outDir, fileName)

	wp.Broker.Log("worker", "info", fmt.Sprintf("[W%d] Rendering Kompilasi %s (%d klip)", workerID, fileName, total))
	depSt := wp.Deps.GetStatus()
	info, err := RenderCompilation(ctx, CompileInput{
		FFmpegPath:     depSt.FFmpegPath,
		FFprobePath:    depSt.FFprobePath,
		OutputPath:     outPath,
		WorkDir:        workDir,
		Title:          job.Title,
		Mode:           opts.Mode,
		Segments:       resolved,
		AllRankLabels:  allRankLabels,
		Layout:         opts.Layout,
		Subtitles:      opts.Subtitles,
		AudioNorm:      opts.AudioNorm,
		VideoFX:        opts.VideoFX,
		BumperDuration: opts.BumperDuration,
		BumperSFXPath:  bumperSfxPath,
		Watermark:      wmCfg,
		Outro:          outroCfg,
		Encoder:        opts.Encoder,
		Preset:         opts.Preset,
		CRF:            opts.CRF,
		OnProgress: func(pct float64, stage string) {
			overall := 35.0 + pct*0.64
			if overall > 99 {
				overall = 99
			}
			wp.updateJob(job, "rendering", stage, overall, "")
		},
	})
	if err != nil {
		if ctx.Err() != nil {
			return
		}
		wp.failJob(job, err.Error())
		return
	}

	var size int64
	if fi, err := os.Stat(outPath); err == nil {
		size = fi.Size()
	}
	now := time.Now()
	job.Status = "done"
	job.Stage = fmt.Sprintf("Kompilasi Top-%d selesai & siap upload!", total)
	job.Progress = 100
	job.OutputPath = outPath
	job.OutputFile = fileName
	job.FileSizeBytes = size
	job.FinalDuration = info.Duration
	job.CompletedAt = &now
	_ = wp.Store.SaveJob(job)
	wp.Broker.Publish("job_update", job)
	wp.Broker.Log("worker", "success", fmt.Sprintf("Kompilasi selesai: %s (%.1fs, %.2f MB)", fileName, info.Duration, float64(size)/(1024*1024)))
}

func (wp *WorkerPool) resolveSFXCues(cfg model.AudioFXConfig, peakOffset, clipDur float64) []ResolvedSFX {
	if wp.Audio == nil {
		return nil
	}
	var cues []model.SFXCue
	switch cfg.SFXAutoPreset {
	case "meme_punch":
		cues = []model.SFXCue{
			{AssetID: "sfx-whoosh", AtSec: 0.08, Volume: 0.75},
			{AssetID: "sfx-boom", AtPeak: true, Volume: 1.0},
			{AssetID: "sfx-ding", AtSec: math.Min(clipDur-1.0, peakOffset+1.6), Volume: 0.70},
		}
	case "hype_epic":
		cues = []model.SFXCue{
			{AssetID: "sfx-riser", AtSec: math.Max(0.1, peakOffset-1.7), Volume: 0.75},
			{AssetID: "sfx-bassdrop", AtPeak: true, Volume: 1.05},
		}
	case "kaget_alert":
		cues = []model.SFXCue{
			{AssetID: "sfx-alert", AtPeak: true, Volume: 0.95},
			{AssetID: "sfx-boom", AtSec: math.Min(clipDur-0.6, peakOffset+0.35), Volume: 0.90},
		}
	case "custom":
		cues = cfg.SFXCues
	default:
		// Also include any explicit custom cues if user added them
		if len(cfg.SFXCues) > 0 {
			cues = cfg.SFXCues
		}
	}

	var out []ResolvedSFX
	for _, c := range cues {
		a, ok := wp.Audio.Get(c.AssetID)
		if !ok || !fileExists(a.FilePath) {
			continue
		}
		at := c.AtSec
		if c.AtPeak {
			at = peakOffset
		}
		vol := c.Volume
		if vol <= 0 {
			vol = 0.85
		}
		out = append(out, ResolvedSFX{
			FilePath: a.FilePath,
			AtSec:    at,
			Volume:   vol,
		})
	}
	return out
}

func (wp *WorkerPool) updateJob(job *model.RenderJob, status, stage string, progress float64, speed string) {
	job.Status = status
	job.Stage = stage
	job.Progress = progress
	if speed != "" {
		job.Speed = speed
	}
	_ = wp.Store.SaveJob(job)
	wp.Broker.Publish("job_update", job)
}

func (wp *WorkerPool) failJob(job *model.RenderJob, errMsg string) {
	job.Status = "error"
	job.Stage = "Gagal render"
	job.Error = errMsg
	_ = wp.Store.SaveJob(job)
	wp.Broker.Publish("job_update", job)
	wp.Broker.Log("worker", "error", fmt.Sprintf("Render gagal (%s): %s", job.Title, errMsg))
}

func evaluateCompliance(opts model.RenderOptions, camp *model.Campaign) model.ComplianceCheck {
	hasFX := opts.AudioFX.BGMAssetID != "" ||
		(opts.AudioFX.SFXAutoPreset != "" && opts.AudioFX.SFXAutoPreset != "none") ||
		len(opts.AudioFX.SFXCues) > 0 ||
		(opts.VideoFX.ColorPreset != "" && opts.VideoFX.ColorPreset != "none") ||
		(opts.VideoFX.MotionPreset != "" && opts.VideoFX.MotionPreset != "none")

	c := model.ComplianceCheck{
		HasValueAdd: opts.Subtitles.Enabled || strings.TrimSpace(opts.HookText) != "" || opts.Layout.CropMode != "original" || hasFX,
	}
	if camp == nil {
		return c
	}

	if opts.UseWatermark && camp.Watermark.Enabled && camp.Watermark.FilePath != "" && fileExists(camp.Watermark.FilePath) {
		c.WatermarkApplied = true
	} else if camp.Watermark.Enabled {
		c.Warnings = append(c.Warnings, "Watermark campaign belum di-upload!")
	}

	if opts.UseOutro && camp.Outro.Enabled && camp.Outro.FilePath != "" && fileExists(camp.Outro.FilePath) {
		c.OutroApplied = true
		c.OutroClean = !camp.Watermark.OnOutro
	} else if camp.Outro.Enabled {
		c.Warnings = append(c.Warnings, "Video template Outro (3 detik) belum di-upload!")
	}

	if len(camp.Hashtags) > 0 {
		c.HashtagsReady = true
	}
	if !c.HasValueAdd {
		c.Warnings = append(c.Warnings, "Tambahkan subtitle, hook text, FX/SFX, atau layout split agar bukan reupload mentah!")
	}
	return c
}

func buildPostKit(title, hook string, camp *model.Campaign) (caption string, hashtags string) {
	lead := strings.TrimSpace(hook)
	if lead == "" {
		lead = title
	}
	if camp == nil {
		return lead, ""
	}
	hashtags = strings.Join(camp.Hashtags, " ")
	var lines []string
	lines = append(lines, lead)
	if len(camp.SocialMentions) > 0 {
		lines = append(lines, "Credit: "+strings.Join(camp.SocialMentions, " "))
	}
	if hashtags != "" {
		lines = append(lines, hashtags)
	}
	return strings.Join(lines, "\n\n"), hashtags
}

var nonAlnumRe = regexp.MustCompile(`[^a-zA-Z0-9_-]+`)

func sanitizeFilename(s string) string {
	s = strings.TrimSpace(s)
	s = nonAlnumRe.ReplaceAllString(s, "_")
	s = strings.Trim(s, "_")
	if len(s) > 48 {
		s = s[:48]
	}
	if s == "" {
		return "clip"
	}
	return s
}

func formatRange(start, end float64) string {
	f := func(sec float64) string {
		t := int(sec)
		m := t / 60
		s := t % 60
		return fmt.Sprintf("%02d:%02d", m, s)
	}
	return f(start) + "-" + f(end)
}
