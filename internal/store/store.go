package store

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"klipforge/internal/model"
)

// Store manages thread-safe persistent JSON storage for campaigns, projects, jobs, and settings.
type Store struct {
	mu        sync.RWMutex
	BaseDir   string
	DataDir   string
	AssetsDir string
	OutputDir string

	campaigns map[string]*model.Campaign
	projects  map[string]*model.Project
	jobs      map[string]*model.RenderJob
	settings  model.AppSettings
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
		campaigns: make(map[string]*model.Campaign),
		projects:  make(map[string]*model.Project),
		jobs:      make(map[string]*model.RenderJob),
		settings: model.AppSettings{
			MaxWorkers:      2,
			DefaultEncoder:  "libx264",
			DefaultPreset:   "veryfast",
			DefaultCRF:      20,
			DownloadQuality: "1080p",
			OutputDir:       outputDir,
		},
	}

	s.loadAll()
	s.ensureDefaultCampaigns()
	return s, nil
}

func (s *Store) loadAll() {
	// Load settings
	if b, err := os.ReadFile(filepath.Join(s.DataDir, "settings.json")); err == nil {
		var cfg model.AppSettings
		if json.Unmarshal(b, &cfg) == nil {
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
			s.settings = cfg
		}
	}

	// Load campaigns
	if b, err := os.ReadFile(filepath.Join(s.DataDir, "campaigns.json")); err == nil {
		var list []*model.Campaign
		if json.Unmarshal(b, &list) == nil {
			for _, c := range list {
				if c.ID != "" {
					s.campaigns[c.ID] = c
				}
			}
		}
	}

	// Load projects
	if b, err := os.ReadFile(filepath.Join(s.DataDir, "projects.json")); err == nil {
		var list []*model.Project
		if json.Unmarshal(b, &list) == nil {
			for _, p := range list {
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
	}

	// Load jobs
	if b, err := os.ReadFile(filepath.Join(s.DataDir, "jobs.json")); err == nil {
		var list []*model.RenderJob
		if json.Unmarshal(b, &list) == nil {
			for _, j := range list {
				if j.ID != "" {
					if j.Status == "queued" || j.Status == "rendering" || j.Status == "downloading" {
						j.Status = "error"
						j.Error = "Interrupted by server restart — click retry to re-render"
					}
					s.jobs[j.ID] = j
				}
			}
		}
	}
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

func writeJSONAtomic(path string, v interface{}) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
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
		cp := *p
		list = append(list, &cp)
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
	cp := *p
	return &cp, true
}

func (s *Store) SaveProject(p *model.Project) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	if p.CreatedAt.IsZero() {
		p.CreatedAt = now
	}
	p.UpdatedAt = now
	cp := *p
	s.projects[p.ID] = &cp
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
		cp := *j
		list = append(list, &cp)
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
	cp := *j
	return &cp, true
}

func (s *Store) SaveJob(j *model.RenderJob) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := *j
	s.jobs[j.ID] = &cp
	return s.saveJobsLocked()
}

func (s *Store) DeleteJob(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if j, ok := s.jobs[id]; ok && j.OutputPath != "" {
		_ = os.Remove(j.OutputPath)
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
