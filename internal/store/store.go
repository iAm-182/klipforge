package store

import (
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"klipforge/internal/model"
)

// Store manages thread-safe persistent JSON storage for campaigns, projects, jobs, team members, and settings.
type Store struct {
	mu        sync.RWMutex
	BaseDir   string
	DataDir   string
	AssetsDir string
	OutputDir string

	campaigns       map[string]*model.Campaign
	projects        map[string]*model.Project
	jobs            map[string]*model.RenderJob
	autopilots      map[string]*model.AutopilotRule
	team            map[string]*model.TeamMember
	publishAccounts map[string]*model.PublishAccount
	agentTrendQuota map[string]int
	activity        []model.ActivityEntry
	settings        model.AppSettings
}

func NewStore(baseDir string) (*Store, error) {
	dataDir := filepath.Join(baseDir, "data")
	assetsDir := filepath.Join(dataDir, "assets")
	outputDir := filepath.Join(baseDir, "outputs")
	projectsDir := filepath.Join(dataDir, "projects")

	for _, d := range []string{dataDir, assetsDir, outputDir, projectsDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return nil, err
		}
	}

	s := &Store{
		BaseDir:   baseDir,
		DataDir:   dataDir,
		AssetsDir: assetsDir,
		OutputDir: outputDir,
		campaigns:       make(map[string]*model.Campaign),
		projects:        make(map[string]*model.Project),
		jobs:            make(map[string]*model.RenderJob),
		autopilots:      make(map[string]*model.AutopilotRule),
		team:            make(map[string]*model.TeamMember),
		publishAccounts: make(map[string]*model.PublishAccount),
		agentTrendQuota: make(map[string]int),
		settings: model.AppSettings{
			MaxWorkers:      2,
			DefaultEncoder:  "libx264",
			DefaultPreset:   "veryfast",
			DefaultCRF:      20,
			DownloadQuality: "1080p",
			OutputDir:       outputDir,
			MinFreeDiskGB:   2.0,
		},
	}

	s.loadAll()
	s.ensureDefaultCampaigns()
	s.ensureDefaultAutopilots()
	return s, nil
}

// readJSONWithBackupFallback reads a JSON data file and automatically falls back to .bak.1/.bak.2/.bak.3
// if the primary file is corrupted, preserving the corrupted file for inspection instead of silently wiping data!
func readJSONWithBackupFallback(path string, dest interface{}) bool {
	if b, err := os.ReadFile(path); err == nil {
		if err := json.Unmarshal(b, dest); err == nil {
			return true
		}
		// Preserve corrupt file so user can inspect it later
		corruptPath := fmt.Sprintf("%s.corrupt.%d", path, time.Now().Unix())
		_ = os.WriteFile(corruptPath, b, 0o644)
		log.Printf("[Store] WARNING: %s corrupt! Saved copy to %s — attempting .bak recovery...", filepath.Base(path), filepath.Base(corruptPath))
	}
	for i := 1; i <= 3; i++ {
		bakPath := fmt.Sprintf("%s.bak.%d", path, i)
		if b, err := os.ReadFile(bakPath); err == nil {
			if err := json.Unmarshal(b, dest); err == nil {
				log.Printf("[Store] RECOVERED %s from %s!", filepath.Base(path), filepath.Base(bakPath))
				// Restore healthy backup into primary file immediately
				_ = os.WriteFile(path, b, 0o644)
				return true
			}
		}
	}
	return false
}

func (s *Store) loadAll() {
	// Load settings
	var cfg model.AppSettings
	if readJSONWithBackupFallback(filepath.Join(s.DataDir, "settings.json"), &cfg) {
		if cfg.MaxWorkers <= 0 {
			cfg.MaxWorkers = 2
		}
		if cfg.DefaultEncoder == "" {
			cfg.DefaultEncoder = "libx264"
		}
		if cfg.DefaultPreset == "" {
			cfg.DefaultPreset = "veryfast"
		}
		if cfg.DefaultCRF <= 0 {
			cfg.DefaultCRF = 20
		}
		if cfg.OutputDir == "" {
			cfg.OutputDir = s.OutputDir
		}
		if cfg.MinFreeDiskGB <= 0 {
			cfg.MinFreeDiskGB = 2.0
		}
		s.settings = cfg
	}

	// Load campaigns
	var camps []*model.Campaign
	if readJSONWithBackupFallback(filepath.Join(s.DataDir, "campaigns.json"), &camps) {
		for _, c := range camps {
			if c.ID != "" {
				s.campaigns[c.ID] = c
			}
		}
	}

	// Load projects
	var projs []*model.Project
	if readJSONWithBackupFallback(filepath.Join(s.DataDir, "projects.json"), &projs) {
		for _, p := range projs {
			if p.ID != "" {
				// Reset interrupted scanning states on restart
				if p.Status == "scanning" || p.Status == "downloading" {
					p.Status = "ready"
					p.StatusMessage = "Loaded from cache"
				}
				s.projects[p.ID] = p
			}
		}
	}

	// Load jobs
	var jobs []*model.RenderJob
	if readJSONWithBackupFallback(filepath.Join(s.DataDir, "jobs.json"), &jobs) {
		for _, j := range jobs {
			if j.ID != "" {
				if j.Status == "queued" || j.Status == "rendering" || j.Status == "downloading" {
					j.Status = "error"
					j.Error = "Interrupted by server restart — click retry to re-render"
				}
				for idx := range j.PublishRecords {
					if j.PublishRecords[idx].Status == "publishing" {
						j.PublishRecords[idx].Status = "error"
						j.PublishRecords[idx].Error = "Terputus saat server restart"
					}
				}
				s.jobs[j.ID] = j
			}
		}
	}

	// Load autopilot rules
	var rules []*model.AutopilotRule
	if readJSONWithBackupFallback(filepath.Join(s.DataDir, "autopilot.json"), &rules) {
		for _, r := range rules {
			if r.ID != "" {
				s.autopilots[r.ID] = r
			}
		}
	}

	// Load team members (personal web AccessKey + personal Muse AgentKey)
	var members []*model.TeamMember
	if readJSONWithBackupFallback(filepath.Join(s.DataDir, "team.json"), &members) {
		for _, m := range members {
			if m.ID != "" {
				if m.MaxActive <= 0 {
					m.MaxActive = 3
				}
				s.team[m.ID] = m
			}
		}
	}

	// Load daily agent trending import quota
	var quota map[string]int
	if readJSONWithBackupFallback(filepath.Join(s.DataDir, "agent_trending_quota.json"), &quota) {
		for k, v := range quota {
			if k != "" && v > 0 {
				s.agentTrendQuota[k] = v
			}
		}
	}

	// Load publish accounts (YouTube OAuth / Instagram Graph / SAU sessions)
	var accs []*model.PublishAccount
	if readJSONWithBackupFallback(filepath.Join(s.DataDir, "publish_accounts.json"), &accs) {
		for _, a := range accs {
			if a.ID != "" {
				s.publishAccounts[a.ID] = a
			}
		}
	}

	// Load persisted activity feed
	s.loadActivity()
}

func (s *Store) ensureDefaultAutopilots() {
	if len(s.autopilots) > 0 {
		return
	}
	now := time.Now()
	rule := &model.AutopilotRule{
		ID:               "auto-windah-gtavi",
		Name:             "Windah Basudara — GTA VI Auto-Watcher",
		Enabled:          false, // Toggle ON when ready for autonomous scheduled clipping!
		ChannelURL:       "https://www.youtube.com/@WindahBasudara/streams",
		KeywordFilter:    []string{"GTA VI", "GTA 6", "Grand Theft Auto"},
		CampaignID:       "windah-gtavi-motionklip",
		IntervalMinutes:  60,
		MaxVideosPerRun:  1,
		TopClipsPerVideo: 5,
		LastStatus:       "Belum pernah dijalankan (aktifkan toggle atau klik Jalankan Sekarang)",
		CreatedAt:        now,
		UpdatedAt:        now,
	}
	s.autopilots[rule.ID] = rule
	_ = s.saveAutopilotsLocked()
}

func (s *Store) saveAutopilotsLocked() error {
	list := make([]*model.AutopilotRule, 0, len(s.autopilots))
	for _, r := range s.autopilots {
		list = append(list, r)
	}
	sort.Slice(list, func(i, j int) bool {
		return list[i].CreatedAt.Before(list[j].CreatedAt)
	})
	return writeJSONAtomic(filepath.Join(s.DataDir, "autopilot.json"), list)
}

// --- Autopilot CRUD ---

func (s *Store) ListAutopilots() []*model.AutopilotRule {
	s.mu.RLock()
	defer s.mu.RUnlock()
	list := make([]*model.AutopilotRule, 0, len(s.autopilots))
	for _, r := range s.autopilots {
		cp := *r
		list = append(list, &cp)
	}
	sort.Slice(list, func(i, j int) bool {
		return list[i].CreatedAt.Before(list[j].CreatedAt)
	})
	return list
}

func (s *Store) GetAutopilot(id string) (*model.AutopilotRule, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	r, ok := s.autopilots[id]
	if !ok {
		return nil, false
	}
	cp := *r
	return &cp, true
}

func (s *Store) SaveAutopilot(r *model.AutopilotRule) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	if ex, ok := s.autopilots[r.ID]; ok {
		r.CreatedAt = ex.CreatedAt
	} else if r.CreatedAt.IsZero() {
		r.CreatedAt = now
	}
	if r.IntervalMinutes < 5 {
		r.IntervalMinutes = 60
	}
	if r.TopClipsPerVideo < 1 {
		r.TopClipsPerVideo = 3
	}
	if r.MaxVideosPerRun < 1 {
		r.MaxVideosPerRun = 1
	}
	r.UpdatedAt = now
	cp := *r
	s.autopilots[r.ID] = &cp
	return s.saveAutopilotsLocked()
}

func (s *Store) DeleteAutopilot(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.autopilots, id)
	return s.saveAutopilotsLocked()
}

func (s *Store) ensureDefaultCampaigns() {
	if len(s.campaigns) > 0 {
		return
	}

	now := time.Now()
	windah := &model.Campaign{
		ID:                "windah-gtavi-motionklip",
		Name:              "Windah Basudara — GTA VI",
		Platform:          "MotionKlip",
		CreatorName:       "Windah Basudara",
		CreatorChannelURL: "https://www.youtube.com/@WindahBasudara",
		DiscordTag:        "MKLP",
		EligibleStartDate: "2026-11-18",
		Hashtags: []string{
			"#windahbasudara",
			"#GTAVI",
			"#windahGTAVI",
			"#motionklip",
			"#ezklip",
		},
		SocialMentions: []string{
			"@windahbasudara",
			"@rockstargames",
		},
		ReferenceDriveURL: "https://drive.google.com/drive/folders/1gZiKc3mwSabV4QdWKS2_Ts6zqhnzapTO?usp=sharing",
		BriefSummary:      "Klip momen gameplay Windah Basudara main GTA VI (wajib ada unsur Gameplay + Windah). Wajib pasang Watermark MotionKlip & video template 3 detik di akhir video (tanpa filter/efek pada template 3 detik). Boleh edit meme/subtitle/hook agar punya value tambahan.",
		Rules: []string{
			"Clip eligible mulai upload tanggal 18 November ke atas hingga budget habis",
			"Wajib pakai Template Watermark MotionKlip sesuai panduan penempatan",
			"Wajib pasang Video Template 3 detik di AKHIR video (utuh, tanpa filter/efek)",
			"Wajib hashtag urutan persis: #windahbasudara #GTAVI #windahGTAVI #motionklip #ezklip",
			"Akun Discord wajib pakai Server Tag 'MKLP' untuk claim reward",
			"Wajib ada Gameplay GTA VI + Windah Basudara (bukan reupload mentah tanpa value edit)",
			"Dilarang SARA, narasi negatif, manipulasi konteks, duplikat clipper lain, atau bot views",
			"Konten yang sudah disubmit wajib tetap tayang (jangan hapus/hide/private)",
		},
		Watermark: model.WatermarkConfig{
			Enabled:  true,
			Position: "top-right",
			Scale:    0.20,
			Opacity:  0.92,
			MarginPx: 32,
			OnOutro:  false,
		},
		Outro: model.ClipAssetVideo{
			Enabled: true,
			KeepRaw: true,
		},
		Intro: model.ClipAssetVideo{
			Enabled: false,
			KeepRaw: true,
		},
		DefaultLayout: model.LayoutConfig{
			AspectRatio: "9:16",
			Resolution:  "1080p",
			CropMode:    "split_custom",
			SplitRatio:  0.62,
			GameplayBox: model.CropBox{X: 0.14, Y: 0.0, W: 0.72, H: 1.0},
			FacecamBox:  model.CropBox{X: 0.72, Y: 0.62, W: 0.28, H: 0.38},
		},
		DefaultSubtitles: model.SubtitleStyle{
			Enabled:        true,
			Preset:         "viral-yellow",
			FontName:       "Impact",
			FontSize:       58,
			Position:       "split-seam",
			MaxWordsPerRow: 4,
			Uppercase:      true,
		},
		DefaultAudioFX: model.AudioFXConfig{
			BGMAssetID:    "",
			BGMVolume:     0.18,
			AutoDuck:      true,
			SFXAutoPreset: "meme_punch",
		},
		DefaultVideoFX: model.VideoFXConfig{
			ColorPreset:  "vivid_gaming",
			MotionPreset: "peak_punch",
			FadeIn:       true,
			Speed:        1.0,
			Sharpen:      true,
		},
		DefaultAudioNorm:  true,
		HookBannerEnabled: true,
		CreatedAt:         now,
		UpdatedAt:         now,
	}

	generic := &model.Campaign{
		ID:                "motionklip-general",
		Name:              "General Streamer / Gaming (Flexible)",
		Platform:          "MotionKlip",
		CreatorName:       "Any Creator",
		CreatorChannelURL: "https://www.youtube.com",
		DiscordTag:        "MKLP",
		EligibleStartDate: now.Format("2006-01-02"),
		Hashtags: []string{
			"#motionklip",
			"#gaming",
			"#shorts",
			"#fyp",
		},
		SocialMentions: []string{},
		BriefSummary:   "Template fleksibel untuk campaign MotionKlip lainnya. Tinggal ganti nama kreator, hashtag wajib, watermark, dan outro template.",
		Rules: []string{
			"Sesuaikan watermark, outro, dan urutan hashtag dengan brief campaign aktif",
			"Tambahkan subtitle/hook atau editing value tambahan (hindari reupload mentah)",
		},
		Watermark: model.WatermarkConfig{
			Enabled:  false,
			Position: "top-right",
			Scale:    0.18,
			Opacity:  0.90,
			MarginPx: 30,
			OnOutro:  false,
		},
		Outro: model.ClipAssetVideo{
			Enabled: false,
			KeepRaw: true,
		},
		DefaultLayout: model.LayoutConfig{
			AspectRatio: "9:16",
			Resolution:  "1080p",
			CropMode:    "blur_bg",
			SplitRatio:  0.65,
			GameplayBox: model.CropBox{X: 0.15, Y: 0.0, W: 0.70, H: 1.0},
			FacecamBox:  model.CropBox{X: 0.0, Y: 0.62, W: 0.28, H: 0.38},
		},
		DefaultSubtitles: model.SubtitleStyle{
			Enabled:        true,
			Preset:         "viral-yellow",
			FontName:       "Impact",
			FontSize:       56,
			Position:       "bottom",
			MaxWordsPerRow: 4,
			Uppercase:      true,
		},
		DefaultAudioNorm:  true,
		HookBannerEnabled: false,
		CreatedAt:         now,
		UpdatedAt:         now,
	}

	s.campaigns[windah.ID] = windah
	s.campaigns[generic.ID] = generic
	_ = s.saveCampaignsLocked()
}

func writeBytesAtomic(path string, b []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	if _, err := tmp.Write(b); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
		return err
	}
	_ = tmp.Sync()
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}
	_ = os.Chmod(tmpPath, perm)
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		if err := os.Rename(tmpPath, path); err == nil {
			return nil
		} else {
			lastErr = err
		}
		time.Sleep(time.Duration(attempt+1) * 20 * time.Millisecond)
	}
	_ = os.Remove(tmpPath)
	return lastErr
}

func writeJSONAtomic(path string, v interface{}) error {
	return writeJSONAtomicPerm(path, v, 0o644)
}

func writeJSONAtomicPerm(path string, v interface{}, perm os.FileMode) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	// Rotate rolling backups atomically (.bak.2 -> .bak.3, .bak.1 -> .bak.2, current -> .bak.1)
	if orig, err := os.ReadFile(path); err == nil && len(orig) > 2 {
		_ = os.Remove(path + ".bak.3")
		_ = os.Rename(path+".bak.2", path+".bak.3")
		_ = os.Rename(path+".bak.1", path+".bak.2")
		_ = writeBytesAtomic(path+".bak.1", orig, perm)
	}
	return writeBytesAtomic(path, b, perm)
}

func cloneProject(p *model.Project) *model.Project {
	if p == nil {
		return nil
	}
	cp := *p
	if p.Candidates != nil {
		cp.Candidates = make([]model.ClipCandidate, len(p.Candidates))
		copy(cp.Candidates, p.Candidates)
	}
	if p.Timeline != nil {
		cp.Timeline = make([]model.TimelinePoint, len(p.Timeline))
		copy(cp.Timeline, p.Timeline)
	}
	if p.Chapters != nil {
		cp.Chapters = make([]model.Chapter, len(p.Chapters))
		copy(cp.Chapters, p.Chapters)
	}
	if p.FacecamBox != nil {
		fb := *p.FacecamBox
		cp.FacecamBox = &fb
	}
	return &cp
}

func cloneJob(j *model.RenderJob) *model.RenderJob {
	if j == nil {
		return nil
	}
	cp := *j
	if j.PublishRecords != nil {
		cp.PublishRecords = make([]model.PublishRecord, len(j.PublishRecords))
		copy(cp.PublishRecords, j.PublishRecords)
	}
	if j.CompileOpts != nil {
		co := *j.CompileOpts
		if j.CompileOpts.Segments != nil {
			co.Segments = make([]model.CompileSegment, len(j.CompileOpts.Segments))
			copy(co.Segments, j.CompileOpts.Segments)
		}
		cp.CompileOpts = &co
	}
	if j.PostEditOpts != nil {
		pe := *j.PostEditOpts
		if j.PostEditOpts.SFXCues != nil {
			pe.SFXCues = make([]model.SFXCue, len(j.PostEditOpts.SFXCues))
			copy(pe.SFXCues, j.PostEditOpts.SFXCues)
		}
		cp.PostEditOpts = &pe
	}
	if j.PublishKit != nil {
		pk := *j.PublishKit
		cp.PublishKit = &pk
	}
	if j.Draft != nil {
		dr := *j.Draft
		cp.Draft = &dr
	}
	return &cp
}

func (s *Store) removeJobArtifactsLocked(id string, j *model.RenderJob) {
	if j != nil && j.OutputPath != "" {
		_ = os.Remove(j.OutputPath)
	}
	_ = os.Remove(filepath.Join(s.DataDir, "peeks", id+".jpg"))
	_ = os.Remove(filepath.Join(s.DataDir, "covers", id+".jpg"))
	_ = os.Remove(filepath.Join(s.DataDir, "drafts", "klipforge_draft_"+id+".zip"))
	if j != nil && j.Draft != nil && j.Draft.FilePath != "" {
		_ = os.Remove(j.Draft.FilePath)
	}
}

// DiskStats returns current free space on the output volume plus KlipForge storage usage.
func (s *Store) DiskStats() model.DiskStats {
	cfg := s.GetSettings()
	outDir := cfg.OutputDir
	if outDir == "" {
		outDir = s.OutputDir
	}
	return GetDiskStats(outDir, s.DataDir, cfg.MinFreeDiskGB)
}

// CleanupStorage removes temp render directories and optionally removes non-approved rendered MP4 files older than olderThanDays.
func (s *Store) CleanupStorage(olderThanDays int) (deletedFiles int, freedBytes int64) {
	// 1. Always wipe stale data/tmp directories
	tmpRoot := filepath.Join(s.DataDir, "tmp")
	if entries, err := os.ReadDir(tmpRoot); err == nil {
		for _, ent := range entries {
			p := filepath.Join(tmpRoot, ent.Name())
			freedBytes += dirSizeBytes(p)
			if os.RemoveAll(p) == nil {
				deletedFiles++
			}
		}
	}

	// 2. Optionally prune old unapproved output clips older than olderThanDays
	if olderThanDays > 0 {
		cutoff := time.Now().Add(-time.Duration(olderThanDays) * 24 * time.Hour)
		s.mu.Lock()
		changed := false
		for id, j := range s.jobs {
			if j.ReviewStatus == "approved" {
				continue
			}
			if j.Status == "done" && j.OutputPath != "" && j.CreatedAt.Before(cutoff) {
				if fi, err := os.Stat(j.OutputPath); err == nil {
					freedBytes += fi.Size()
				}
				s.removeJobArtifactsLocked(id, j)
				delete(s.jobs, id)
				deletedFiles++
				changed = true
			}
		}
		if changed {
			_ = s.saveJobsLocked()
		}
		s.mu.Unlock()
	}
	return deletedFiles, freedBytes
}

func (s *Store) saveCampaignsLocked() error {
	list := make([]*model.Campaign, 0, len(s.campaigns))
	for _, c := range s.campaigns {
		list = append(list, c)
	}
	sort.Slice(list, func(i, j int) bool {
		return list[i].CreatedAt.Before(list[j].CreatedAt)
	})
	return writeJSONAtomic(filepath.Join(s.DataDir, "campaigns.json"), list)
}

func (s *Store) saveProjectsLocked() error {
	list := make([]*model.Project, 0, len(s.projects))
	for _, p := range s.projects {
		list = append(list, p)
	}
	sort.Slice(list, func(i, j int) bool {
		return list[i].UpdatedAt.After(list[j].UpdatedAt)
	})
	return writeJSONAtomic(filepath.Join(s.DataDir, "projects.json"), list)
}

func (s *Store) saveJobsLocked() error {
	list := make([]*model.RenderJob, 0, len(s.jobs))
	for _, j := range s.jobs {
		list = append(list, j)
	}
	sort.Slice(list, func(i, j int) bool {
		return list[i].CreatedAt.After(list[j].CreatedAt)
	})
	return writeJSONAtomic(filepath.Join(s.DataDir, "jobs.json"), list)
}

// --- Campaign CRUD ---

func (s *Store) ListCampaigns() []*model.Campaign {
	s.mu.RLock()
	defer s.mu.RUnlock()
	list := make([]*model.Campaign, 0, len(s.campaigns))
	for _, c := range s.campaigns {
		cp := *c
		list = append(list, &cp)
	}
	sort.Slice(list, func(i, j int) bool {
		return list[i].CreatedAt.Before(list[j].CreatedAt)
	})
	return list
}

func (s *Store) GetCampaign(id string) (*model.Campaign, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	c, ok := s.campaigns[id]
	if !ok {
		return nil, false
	}
	cp := *c
	return &cp, true
}

func (s *Store) SaveCampaign(c *model.Campaign) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	if existing, ok := s.campaigns[c.ID]; ok {
		c.CreatedAt = existing.CreatedAt
	} else if c.CreatedAt.IsZero() {
		c.CreatedAt = now
	}
	c.UpdatedAt = now
	cp := *c
	s.campaigns[c.ID] = &cp
	return s.saveCampaignsLocked()
}

func (s *Store) DeleteCampaign(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.campaigns, id)
	return s.saveCampaignsLocked()
}

// --- Project CRUD ---

func (s *Store) ProjectDir(projectID string) string {
	dir := filepath.Join(s.DataDir, "projects", projectID)
	_ = os.MkdirAll(dir, 0o755)
	return dir
}

func (s *Store) ListProjects() []*model.Project {
	s.mu.RLock()
	defer s.mu.RUnlock()
	list := make([]*model.Project, 0, len(s.projects))
	for _, p := range s.projects {
		list = append(list, cloneProject(p))
	}
	sort.Slice(list, func(i, j int) bool {
		return list[i].UpdatedAt.After(list[j].UpdatedAt)
	})
	return list
}

func (s *Store) GetProject(id string) (*model.Project, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	p, ok := s.projects[id]
	if !ok {
		return nil, false
	}
	return cloneProject(p), true
}

func (s *Store) SaveProject(p *model.Project) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	if p.CreatedAt.IsZero() {
		p.CreatedAt = now
	}
	p.UpdatedAt = now
	s.projects[p.ID] = cloneProject(p)
	return s.saveProjectsLocked()
}

func (s *Store) DeleteProject(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.projects, id)
	_ = os.RemoveAll(filepath.Join(s.DataDir, "projects", id))
	return s.saveProjectsLocked()
}

// --- Job CRUD ---

func (s *Store) ListJobs() []*model.RenderJob {
	s.mu.RLock()
	defer s.mu.RUnlock()
	list := make([]*model.RenderJob, 0, len(s.jobs))
	for _, j := range s.jobs {
		list = append(list, cloneJob(j))
	}
	sort.Slice(list, func(i, j int) bool {
		return list[i].CreatedAt.After(list[j].CreatedAt)
	})
	return list
}

func (s *Store) GetJob(id string) (*model.RenderJob, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	j, ok := s.jobs[id]
	if !ok {
		return nil, false
	}
	return cloneJob(j), true
}

func (s *Store) SaveJob(j *model.RenderJob) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.jobs[j.ID] = cloneJob(j)
	return s.saveJobsLocked()
}

// UpdateJob atomically mutates a RenderJob under Store.mu.Lock() so concurrent worker/HTTP/peek updates never clobber each other!
func (s *Store) UpdateJob(id string, fn func(*model.RenderJob)) (*model.RenderJob, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	j, ok := s.jobs[id]
	if !ok {
		return nil, false
	}
	fn(j)
	_ = s.saveJobsLocked()
	return cloneJob(j), true
}

func (s *Store) DeleteJob(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if j, ok := s.jobs[id]; ok {
		s.removeJobArtifactsLocked(id, j)
	}
	delete(s.jobs, id)
	return s.saveJobsLocked()
}

// --- Settings ---

func (s *Store) GetSettings() model.AppSettings {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.settings
}

func (s *Store) SaveSettings(cfg model.AppSettings) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if cfg.MaxWorkers <= 0 {
		cfg.MaxWorkers = 1
	}
	if cfg.MaxWorkers > 8 {
		cfg.MaxWorkers = 8
	}
	if cfg.OutputDir == "" {
		cfg.OutputDir = s.OutputDir
	}
	_ = os.MkdirAll(cfg.OutputDir, 0o755)
	s.settings = cfg
	return writeJSONAtomic(filepath.Join(s.DataDir, "settings.json"), s.settings)
}

// --- Team Members CRUD & Key Lookup ---

func constantTimeStrEq(a, b string) bool {
	sa := strings.TrimSpace(a)
	sb := strings.TrimSpace(b)
	if sa == "" || len(sa) != len(sb) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(sa), []byte(sb)) == 1
}

func (s *Store) saveTeamLocked() error {
	list := make([]*model.TeamMember, 0, len(s.team))
	for _, m := range s.team {
		list = append(list, m)
	}
	sort.Slice(list, func(i, j int) bool {
		return list[i].CreatedAt.Before(list[j].CreatedAt)
	})
	return writeJSONAtomic(filepath.Join(s.DataDir, "team.json"), list)
}

func (s *Store) ListTeam() []*model.TeamMember {
	s.mu.RLock()
	defer s.mu.RUnlock()
	list := make([]*model.TeamMember, 0, len(s.team))
	for _, m := range s.team {
		cp := *m
		list = append(list, &cp)
	}
	sort.Slice(list, func(i, j int) bool {
		return list[i].CreatedAt.Before(list[j].CreatedAt)
	})
	return list
}

func (s *Store) GetTeamMember(id string) (*model.TeamMember, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	m, ok := s.team[id]
	if !ok {
		return nil, false
	}
	cp := *m
	return &cp, true
}

func (s *Store) SaveTeamMember(m *model.TeamMember) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	if ex, ok := s.team[m.ID]; ok {
		m.CreatedAt = ex.CreatedAt
		if m.LastSeenAt == nil {
			m.LastSeenAt = ex.LastSeenAt
		}
	} else if m.CreatedAt.IsZero() {
		m.CreatedAt = now
	}
	if m.MaxActive <= 0 {
		m.MaxActive = 3
	}
	if m.Role != "owner" && m.Role != "qc" && m.Role != "clipper" {
		m.Role = "clipper"
	}
	cp := *m
	s.team[m.ID] = &cp
	return s.saveTeamLocked()
}

func (s *Store) DeleteTeamMember(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.team, id)
	return s.saveTeamLocked()
}

// FindTeamByAccessKey looks up an enabled team member by personal web AccessKey in constant time.
func (s *Store) FindTeamByAccessKey(key string) (*model.TeamMember, bool) {
	k := strings.TrimSpace(key)
	if k == "" {
		return nil, false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	var match *model.TeamMember
	for _, m := range s.team {
		if m.Enabled && constantTimeStrEq(m.AccessKey, k) {
			cp := *m
			match = &cp
		}
	}
	if match == nil {
		return nil, false
	}
	return match, true
}

// FindTeamByAgentKey looks up an enabled team member by personal Muse AgentKey in constant time.
func (s *Store) FindTeamByAgentKey(key string) (*model.TeamMember, bool) {
	k := strings.TrimSpace(key)
	if k == "" {
		return nil, false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	var match *model.TeamMember
	for _, m := range s.team {
		if m.Enabled && constantTimeStrEq(m.AgentKey, k) {
			cp := *m
			match = &cp
		}
	}
	if match == nil {
		return nil, false
	}
	return match, true
}

// TouchTeamMemberLastSeen updates LastSeenAt in memory and persists at most once per 60 seconds.
func (s *Store) TouchTeamMemberLastSeen(id string) {
	if id == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	m, ok := s.team[id]
	if !ok {
		return
	}
	now := time.Now()
	shouldPersist := m.LastSeenAt == nil || now.Sub(*m.LastSeenAt) >= 60*time.Second
	m.LastSeenAt = &now
	if shouldPersist {
		_ = s.saveTeamLocked()
	}
}

// CountActiveJobsByMember counts queued/downloading/rendering jobs created by a specific member ID.
func (s *Store) CountActiveJobsByMember(memberID string) int {
	if memberID == "" {
		return 0
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	n := 0
	for _, j := range s.jobs {
		if j.CreatedByID == memberID && (j.Status == "queued" || j.Status == "downloading" || j.Status == "rendering") {
			n++
		}
	}
	return n
}

func agentTrendQuotaKey(memberID string, now time.Time) string {
	id := strings.TrimSpace(memberID)
	if id == "" {
		id = "owner-agent"
	}
	return id + "::" + now.Format("2006-01-02")
}

// GetAgentTrendImportsToday returns how many trending sounds an agent member has imported today.
func (s *Store) GetAgentTrendImportsToday(memberID string) int {
	key := agentTrendQuotaKey(memberID, time.Now())
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.agentTrendQuota[key]
}

// CheckAgentTrendQuota checks whether memberID is within the daily agent trending import cap.
func (s *Store) CheckAgentTrendQuota(memberID string, dailyLimit int) (used int, remaining int, allowed bool) {
	if dailyLimit <= 0 {
		dailyLimit = 5
	}
	used = s.GetAgentTrendImportsToday(memberID)
	remaining = dailyLimit - used
	if remaining < 0 {
		remaining = 0
	}
	return used, remaining, used < dailyLimit
}

// IncrementAgentTrendImportsToday increments today's trending import counter for memberID and persists it.
func (s *Store) IncrementAgentTrendImportsToday(memberID string) int {
	now := time.Now()
	key := agentTrendQuotaKey(memberID, now)
	todayPrefix := now.Format("2006-01-02")
	yesterdayPrefix := now.AddDate(0, 0, -1).Format("2006-01-02")

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.agentTrendQuota == nil {
		s.agentTrendQuota = make(map[string]int)
	}
	s.agentTrendQuota[key]++
	count := s.agentTrendQuota[key]

	// Prune old date buckets so quota file stays compact
	for k := range s.agentTrendQuota {
		if !strings.HasSuffix(k, "::"+todayPrefix) && !strings.HasSuffix(k, "::"+yesterdayPrefix) {
			delete(s.agentTrendQuota, k)
		}
	}
	_ = writeJSONAtomic(filepath.Join(s.DataDir, "agent_trending_quota.json"), s.agentTrendQuota)
	return count
}

// --- Publish Accounts CRUD (Owner-managed OAuth / sidecar session configs) ---

func (s *Store) savePublishAccountsLocked() error {
	list := make([]*model.PublishAccount, 0, len(s.publishAccounts))
	for _, a := range s.publishAccounts {
		list = append(list, a)
	}
	sort.Slice(list, func(i, j int) bool {
		return list[i].CreatedAt.Before(list[j].CreatedAt)
	})
	return writeJSONAtomicPerm(filepath.Join(s.DataDir, "publish_accounts.json"), list, 0o600)
}

func (s *Store) ListPublishAccounts() []*model.PublishAccount {
	s.mu.RLock()
	defer s.mu.RUnlock()
	list := make([]*model.PublishAccount, 0, len(s.publishAccounts))
	for _, a := range s.publishAccounts {
		cp := *a
		list = append(list, &cp)
	}
	sort.Slice(list, func(i, j int) bool {
		return list[i].CreatedAt.Before(list[j].CreatedAt)
	})
	return list
}

func (s *Store) GetPublishAccount(id string) (*model.PublishAccount, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	a, ok := s.publishAccounts[id]
	if !ok {
		return nil, false
	}
	cp := *a
	return &cp, true
}

func (s *Store) FindPublishAccountByPlatform(platform, nameOrID string) (*model.PublishAccount, bool) {
	plat := strings.ToLower(strings.TrimSpace(platform))
	target := strings.TrimSpace(nameOrID)
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, a := range s.publishAccounts {
		if strings.ToLower(a.Platform) != plat {
			continue
		}
		if target == "" || a.ID == target || strings.EqualFold(a.Name, target) {
			cp := *a
			return &cp, true
		}
	}
	return nil, false
}

func (s *Store) SavePublishAccount(a *model.PublishAccount) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	if ex, ok := s.publishAccounts[a.ID]; ok {
		a.CreatedAt = ex.CreatedAt
	} else if a.CreatedAt.IsZero() {
		a.CreatedAt = now
	}
	a.UpdatedAt = now
	cp := *a
	s.publishAccounts[a.ID] = &cp
	return s.savePublishAccountsLocked()
}

func (s *Store) DeletePublishAccount(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.publishAccounts, id)
	return s.savePublishAccountsLocked()
}
