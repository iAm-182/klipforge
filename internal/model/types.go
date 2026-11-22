package model

import "time"

// CropBox represents a normalized crop rectangle (0.0 to 1.0) on the source 16:9 video.
type CropBox struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
	W float64 `json:"w"`
	H float64 `json:"h"`
}

// WatermarkConfig defines watermark overlay appearance and placement.
type WatermarkConfig struct {
	Enabled   bool    `json:"enabled"`
	FilePath  string  `json:"filePath"`  // Relative to data/assets or absolute
	FileName  string  `json:"fileName"`
	Position  string  `json:"position"`  // top-left, top-right, bottom-left, bottom-right, custom
	CustomX   float64 `json:"customX"`   // 0.0 - 1.0 (when custom)
	CustomY   float64 `json:"customY"`   // 0.0 - 1.0 (when custom)
	Scale     float64 `json:"scale"`     // Fraction of output width (e.g. 0.22 = 22%)
	Opacity   float64 `json:"opacity"`   // 0.0 - 1.0
	MarginPx  int     `json:"marginPx"`  // Padding from edges in pixels
	OnOutro   bool    `json:"onOutro"`   // Usually false (keep outro clean without filters/overlay)
}

// ClipAssetVideo defines an intro or outro video template (e.g. mandatory 3s outro).
type ClipAssetVideo struct {
	Enabled  bool    `json:"enabled"`
	FilePath string  `json:"filePath"`
	FileName string  `json:"fileName"`
	Duration float64 `json:"duration"`
	KeepRaw  bool    `json:"keepRaw"` // Do not apply color filters/watermark/subtitles
}

// SubtitleStyle defines burned-in subtitle styling.
type SubtitleStyle struct {
	Enabled        bool   `json:"enabled"`
	Preset         string `json:"preset"`         // viral-yellow, clean-white, gaming-neon, minimal
	FontName       string `json:"fontName"`       // Impact, Arial, Bahnschrift
	FontSize       int    `json:"fontSize"`       // Default 54 for 1080x1920
	Position       string `json:"position"`       // bottom, center, split-seam
	MaxWordsPerRow int    `json:"maxWordsPerRow"` // Default 4 for snappy viral captions
	Uppercase      bool   `json:"uppercase"`
}

// LayoutConfig defines how source 16:9 video is framed into output aspect ratio.
type LayoutConfig struct {
	AspectRatio string  `json:"aspectRatio"` // 9:16, 1:1, 16:9, original
	Resolution  string  `json:"resolution"`  // 1080p, 720p
	CropMode    string  `json:"cropMode"`    // split_custom, split_right, split_left, split_top_right, split_top_left, blur_bg, center, original
	SplitRatio  float64 `json:"splitRatio"`  // Fraction of height for top gameplay (e.g. 0.62 -> 62% gameplay, 38% facecam)
	GameplayBox CropBox `json:"gameplayBox"` // Normalized crop for gameplay
	FacecamBox  CropBox `json:"facecamBox"`  // Normalized crop for facecam
}

// AudioAsset represents a BGM or SFX file stored in KlipForge's audio library.
type AudioAsset struct {
	ID       string  `json:"id"`
	Name     string  `json:"name"`
	Kind     string  `json:"kind"`     // "bgm" or "sfx"
	Category string  `json:"category"` // e.g. "Meme", "Transition", "Hype", "Gaming", "Custom"
	FilePath string  `json:"filePath"`
	FileName string  `json:"fileName"`
	Duration float64 `json:"duration"`
	BuiltIn  bool    `json:"builtIn"`
}

// SFXCue triggers a specific sound effect at a clip-relative timestamp.
type SFXCue struct {
	AssetID string  `json:"assetId"`
	AtSec   float64 `json:"atSec"`  // Seconds from clip start (ignored when AtPeak is true)
	AtPeak  bool    `json:"atPeak"` // Automatically align to the detected viral peak moment!
	Volume  float64 `json:"volume"` // 0.1 - 1.5 (default 0.85)
}

// AudioFXConfig defines Backsound (BGM), ReaCut-style Auto-Ducking, and Sound Effects (SFX).
type AudioFXConfig struct {
	BGMAssetID    string   `json:"bgmAssetId"`    // Empty = no BGM
	BGMVolume     float64  `json:"bgmVolume"`     // 0.05 - 0.80 (default 0.18)
	AutoDuck      bool     `json:"autoDuck"`      // Automatically lower BGM when streamer speaks (ReaCut-style)
	SFXAutoPreset string   `json:"sfxAutoPreset"` // none, meme_punch, hype_epic, kaget_alert, custom
	SFXCues       []SFXCue `json:"sfxCues"`       // Used when SFXAutoPreset == "custom" (or additional cues)
}

// VideoFXConfig defines visual polish, color look, and viral motion effects (ONLY applied to main clip, NEVER outro).
type VideoFXConfig struct {
	ColorPreset   string  `json:"colorPreset"`   // none, vivid_gaming, cinematic, meme_deep
	MotionPreset  string  `json:"motionPreset"`  // none, slow_zoom, peak_punch, jedag_jedug
	PeakOffsetSec float64 `json:"peakOffsetSec"` // Peak offset relative to clip start (for peak_punch & peak SFX)
	FadeIn        bool    `json:"fadeIn"`        // Smooth 0.25s fade-in at clip start
	Speed         float64 `json:"speed"`         // 1.0 (normal), 1.05, 1.10, 1.15
	Sharpen       bool    `json:"sharpen"`       // Crisp unsharp mask for gameplay clarity
}

// Campaign represents a MotionKlip (or any platform) creator campaign profile.
type Campaign struct {
	ID                 string          `json:"id"`
	Name               string          `json:"name"`
	Platform           string          `json:"platform"`           // e.g. MotionKlip
	CreatorName        string          `json:"creatorName"`        // e.g. Windah Basudara
	CreatorChannelURL  string          `json:"creatorChannelUrl"`  // e.g. https://www.youtube.com/@WindahBasudara
	DiscordTag         string          `json:"discordTag"`         // e.g. MKLP
	EligibleStartDate  string          `json:"eligibleStartDate"`  // e.g. 2026-11-18
	Hashtags           []string        `json:"hashtags"`           // Strict order!
	SocialMentions     []string        `json:"socialMentions"`     // e.g. @windahbasudara, @rockstargames
	BriefSummary       string          `json:"briefSummary"`
	Rules              []string        `json:"rules"`
	ForbiddenWords     []string        `json:"forbiddenWords,omitempty"` // Words banned from auto-generated hooks/captions
	ReferenceDriveURL  string          `json:"referenceDriveUrl"`
	Watermark          WatermarkConfig `json:"watermark"`
	Intro              ClipAssetVideo  `json:"intro"`
	Outro              ClipAssetVideo  `json:"outro"`
	DefaultLayout      LayoutConfig    `json:"defaultLayout"`
	DefaultSubtitles   SubtitleStyle   `json:"defaultSubtitles"`
	DefaultAudioFX     AudioFXConfig   `json:"defaultAudioFx"`
	DefaultVideoFX     VideoFXConfig   `json:"defaultVideoFx"`
	DefaultAudioNorm   bool            `json:"defaultAudioNorm"`
	HookBannerEnabled  bool            `json:"hookBannerEnabled"`
	CreatedAt          time.Time       `json:"createdAt"`
	UpdatedAt          time.Time       `json:"updatedAt"`
}

// TimelinePoint represents normalized engagement signals at a specific timestamp bucket.
type TimelinePoint struct {
	TimeSec        float64 `json:"timeSec"`
	Heatmap        float64 `json:"heatmap"`        // 0.0 - 1.0
	ChatHype       float64 `json:"chatHype"`       // 0.0 - 1.0
	AudioPeak      float64 `json:"audioPeak"`      // 0.0 - 1.0
	TranscriptHype float64 `json:"transcriptHype"` // 0.0 - 1.0 (Indonesian speech reaction density)
	Combined       float64 `json:"combined"`       // 0.0 - 1.0
	ChatCount      int     `json:"chatCount"`      // Raw messages in bucket
}

// TranscriptHit is a search result from the instant VOD transcript search bar.
type TranscriptHit struct {
	StartSec float64 `json:"startSec"`
	EndSec   float64 `json:"endSec"`
	TimeText string  `json:"timeText"`
	Text     string  `json:"text"`
	Context  string  `json:"context"`
}

// Chapter represents a YouTube video chapter.
type Chapter struct {
	Title     string  `json:"title"`
	StartTime float64 `json:"startTime"`
	EndTime   float64 `json:"endTime"`
}

// WordTiming stores exact per-word start/end timestamps extracted from YouTube VTT <c> tags.
type WordTiming struct {
	Start float64 `json:"start"`
	End   float64 `json:"end"`
	Text  string  `json:"text"`
}

// TranscriptCue represents a single subtitle/caption line with optional word-level timings.
type TranscriptCue struct {
	Start float64      `json:"start"`
	End   float64      `json:"end"`
	Text  string       `json:"text"`
	Words []WordTiming `json:"words,omitempty"`
}

// ClipCandidate is a detected viral/hype moment candidate ready to be clipped.
type ClipCandidate struct {
	ID            string   `json:"id"`
	Rank          int      `json:"rank"`
	Title         string   `json:"title"`
	StartSec      float64  `json:"startSec"`
	EndSec        float64  `json:"endSec"`
	PeakSec       float64  `json:"peakSec"`
	DurationSec   float64  `json:"durationSec"`
	Score         float64  `json:"score"`         // 0 - 100
	HeatmapScore  float64  `json:"heatmapScore"`  // 0 - 100
	ChatScore     float64  `json:"chatScore"`     // 0 - 100
	AudioScore    float64  `json:"audioScore"`    // 0 - 100
	Reasons       []string `json:"reasons"`       // e.g. ["Most Replayed", "Chat Meledak"]
	SampleChats   []string `json:"sampleChats"`   // Top hype chat lines around peak
	Transcript    string   `json:"transcript"`    // Snippet of speech during segment
	HookText      string   `json:"hookText"`      // Optional editable top headline
	Selected      bool     `json:"selected"`
}

// DetectionSettings configures how multi-signal hype detection scores moments.
type DetectionSettings struct {
	HeatmapWeight   float64 `json:"heatmapWeight"`   // default 0.45
	ChatWeight      float64 `json:"chatWeight"`      // default 0.35
	AudioWeight     float64 `json:"audioWeight"`     // default 0.20
	MinScore        float64 `json:"minScore"`        // default 35 (0-100)
	MinDurationSec  float64 `json:"minDurationSec"`  // default 20
	MaxDurationSec  float64 `json:"maxDurationSec"`  // default 55
	PrePaddingSec   float64 `json:"prePaddingSec"`   // default 12
	PostPaddingSec  float64 `json:"postPaddingSec"`  // default 22
	MaxClips        int     `json:"maxClips"`        // default 15
	SkipIntroSec    float64 `json:"skipIntroSec"`    // default 15 (avoid YouTube 0:00 heatmap bias)
}

// SignalAvailability tracks which signals were successfully collected for a video.
type SignalAvailability struct {
	HasHeatmap        bool `json:"hasHeatmap"`
	HasLiveChat       bool `json:"hasLiveChat"`
	HasAudio          bool `json:"hasAudio"`
	HasSubtitles      bool `json:"hasSubtitles"`
	HasTranscriptHype bool `json:"hasTranscriptHype"`
	ChatMsgCount      int  `json:"chatMsgCount"`
	SubCueCount       int  `json:"subCueCount"`
	HeatmapPoints     int  `json:"heatmapPoints"`
}

// Project represents an analyzed YouTube video/VOD.
type Project struct {
	ID              string             `json:"id"`
	VideoID         string             `json:"videoId"`
	SourceURL       string             `json:"sourceUrl"`
	Title           string             `json:"title"`
	Channel         string             `json:"channel"`
	ChannelURL      string             `json:"channelUrl"`
	ThumbnailURL    string             `json:"thumbnailUrl"`
	PreviewFrame    string             `json:"previewFrame"`    // Local JPG path for visual cropper
	DurationSec     float64            `json:"durationSec"`
	ViewCount       int64              `json:"viewCount"`
	UploadDate      string             `json:"uploadDate"`
	IsLiveVOD       bool               `json:"isLiveVod"`
	Status          string             `json:"status"`          // scanning, ready, downloading, error
	StatusMessage   string             `json:"statusMessage"`
	LocalVideoPath  string             `json:"localVideoPath"`  // Set when full source video is downloaded
	SubtitlePath    string             `json:"subtitlePath"`    // Local VTT path if available
	LiveChatPath    string             `json:"liveChatPath"`    // Local live_chat.json path if available
	CampaignID      string             `json:"campaignId"`
	Signals         SignalAvailability `json:"signals"`
	DetectConfig    DetectionSettings  `json:"detectConfig"`
	Chapters        []Chapter          `json:"chapters"`
	Timeline        []TimelinePoint    `json:"timeline"`
	Candidates      []ClipCandidate    `json:"candidates"`
	CreatedAt       time.Time          `json:"createdAt"`
	UpdatedAt       time.Time          `json:"updatedAt"`
}

// ComplianceCheck reports whether a rendered clip satisfies campaign rules.
type ComplianceCheck struct {
	WatermarkApplied bool     `json:"watermarkApplied"`
	OutroApplied     bool     `json:"outroApplied"`
	OutroClean       bool     `json:"outroClean"`
	HasValueAdd      bool     `json:"hasValueAdd"`
	HashtagsReady    bool     `json:"hashtagsReady"`
	Warnings         []string `json:"warnings"`
}

// RenderOptions holds all parameters for rendering a single clip.
type RenderOptions struct {
	ProjectID    string          `json:"projectId"`
	CampaignID   string          `json:"campaignId"`
	CandidateID  string          `json:"candidateId"`
	Title        string          `json:"title"`
	StartSec     float64         `json:"startSec"`
	EndSec       float64         `json:"endSec"`
	HookText     string          `json:"hookText"`
	Layout       LayoutConfig    `json:"layout"`
	Subtitles    SubtitleStyle   `json:"subtitles"`
	AudioFX      AudioFXConfig   `json:"audioFx"`
	VideoFX      VideoFXConfig   `json:"videoFx"`
	AudioNorm    bool            `json:"audioNorm"`
	UseWatermark bool            `json:"useWatermark"`
	UseOutro     bool            `json:"useOutro"`
	UseIntro     bool            `json:"useIntro"`
	Encoder      string          `json:"encoder"` // auto, libx264, h264_qsv, h264_nvenc, h264_amf
	Preset       string          `json:"preset"`  // ultrafast, veryfast, fast, medium
	CRF          int             `json:"crf"`     // default 20
}

// CompileSegment is one clip slot inside a Top-N Countdown or Rapid Compilation video.
type CompileSegment struct {
	ProjectID   string  `json:"projectId"`
	StartSec    float64 `json:"startSec"`
	EndSec      float64 `json:"endSec"`
	Label       string  `json:"label"`       // e.g. "ngetrail", "el alasan", "paling jago"
	EndSFXID    string  `json:"endSfxId"`    // Optional punchline SFX near end (e.g. "sfx-metal-pipe", "sfx-boom")
	EndSFXOffset float64 `json:"endSfxOffset"` // Seconds before segment end (default 1.0s)
}

// CompileOptions configures an Auto Top-N Countdown (5->1) or Rapid Compilation render.
type CompileOptions struct {
	CampaignID       string           `json:"campaignId"`
	Title            string           `json:"title"`            // e.g. "Top 5 Windah Basudara Nyetir"
	Mode             string           `json:"mode"`             // "top_countdown" (5->1 + black bumper) or "rapid_montage" (fast multi-cut)
	Segments         []CompileSegment `json:"segments"`         // Ordered in playback order (e.g. #5, #4, #3, #2, #1)
	Layout           LayoutConfig     `json:"layout"`
	Subtitles        SubtitleStyle    `json:"subtitles"`
	AudioFX          AudioFXConfig    `json:"audioFx"`
	VideoFX          VideoFXConfig    `json:"videoFx"`
	BumperDuration   float64          `json:"bumperDuration"`   // Default 0.85s black bumper between Top-N clips
	BumperSFXID      string           `json:"bumperSfxId"`      // Default "sfx-boom"
	AudioNorm        bool             `json:"audioNorm"`
	UseWatermark     bool             `json:"useWatermark"`
	UseOutro         bool             `json:"useOutro"`
	Encoder          string           `json:"encoder"`
	Preset           string           `json:"preset"`
	CRF              int              `json:"crf"`
}

// PostEditOptions lets the user trim an already-rendered clip's start/end timestamps
// and stamp unlimited additional SFX cues (or BGM) directly onto the local MP4 without re-downloading!
type PostEditOptions struct {
	SourceJobID   string   `json:"sourceJobId"`
	Title         string   `json:"title"`
	TrimStartSec  float64  `json:"trimStartSec"`  // Seconds in rendered clip (0 = from beginning)
	TrimEndSec    float64  `json:"trimEndSec"`    // Seconds in rendered clip (<=0 = until end of main body)
	PreserveOutro bool     `json:"preserveOutro"` // Keep campaign's clean Outro template untouched at the end
	SFXCues       []SFXCue `json:"sfxCues"`       // Additional SFX cues stamped at timestamps on the source clip timeline
	BGMAssetID    string   `json:"bgmAssetId"`    // Optional additional BGM overlay
	BGMVolume     float64  `json:"bgmVolume"`     // 0.05 - 0.60
	BGMAutoDuck   bool     `json:"bgmAutoDuck"`
	Speed         float64  `json:"speed"` // Optional 1.0 - 1.20
}

// RenderJob tracks a queued, running, completed, or failed clip render.
type RenderJob struct {
	ID            string           `json:"id"`
	JobType       string           `json:"jobType,omitempty"` // "" / "single", "compilation", or "post_edit"
	ProjectID     string           `json:"projectId"`
	ProjectTitle  string           `json:"projectTitle"`
	CampaignID    string           `json:"campaignId"`
	CampaignName  string           `json:"campaignName"`
	Title         string           `json:"title"`
	Options       RenderOptions    `json:"options"`
	CompileOpts   *CompileOptions  `json:"compileOpts,omitempty"`
	PostEditOpts  *PostEditOptions `json:"postEditOpts,omitempty"`
	Status        string          `json:"status"`        // queued, downloading, rendering, done, error, canceled
	Stage         string          `json:"stage"`         // human readable stage
	Progress      float64         `json:"progress"`      // 0 - 100
	Speed         string          `json:"speed"`         // e.g. "2.4x"
	OutputPath    string          `json:"outputPath"`
	OutputFile    string          `json:"outputFile"`
	FileSizeBytes int64           `json:"fileSizeBytes"`
	FinalDuration float64         `json:"finalDuration"`
	PostCaption   string          `json:"postCaption"`
	HashtagLine   string          `json:"hashtagLine"`
	Compliance    ComplianceCheck `json:"compliance"`
	ReviewStatus  string          `json:"reviewStatus"` // "", "pending_review", "approved", "rejected"
	ReviewNote    string          `json:"reviewNote,omitempty"`
	AutopilotID   string          `json:"autopilotId,omitempty"`
	Error         string          `json:"error,omitempty"`
	CreatedAt     time.Time       `json:"createdAt"`
	CompletedAt   *time.Time      `json:"completedAt,omitempty"`
}

// AutopilotRule defines a scheduled autonomous channel watcher & HITL clipper workflow (n8n-style).
type AutopilotRule struct {
	ID                string     `json:"id"`
	Name              string     `json:"name"`
	Enabled           bool       `json:"enabled"`
	ChannelURL        string     `json:"channelUrl"`        // e.g. https://www.youtube.com/@WindahBasudara/streams
	KeywordFilter     []string   `json:"keywordFilter"`     // Match any keyword in title (empty = match all)
	CampaignID        string     `json:"campaignId"`        // Campaign preset to apply
	IntervalMinutes   int        `json:"intervalMinutes"`   // Schedule interval (default 60)
	MaxVideosPerRun   int        `json:"maxVideosPerRun"`   // Default 1
	TopClipsPerVideo  int        `json:"topClipsPerVideo"`  // Default 5
	NotifyWebhookURL  string     `json:"notifyWebhookUrl"`  // n8n Webhook or Discord Webhook when clips ready for review
	ApproveWebhookURL string     `json:"approveWebhookUrl"` // Optional n8n Webhook triggered when human clicks Approve
	ProcessedVideoIDs []string   `json:"processedVideoIds"`
	LastRunAt         *time.Time `json:"lastRunAt,omitempty"`
	NextRunAt         *time.Time `json:"nextRunAt,omitempty"`
	LastStatus        string     `json:"lastStatus"`
	CreatedAt         time.Time  `json:"createdAt"`
	UpdatedAt         time.Time  `json:"updatedAt"`
}

// ChannelVideo is a lightweight video entry returned by channel scanning.
type ChannelVideo struct {
	ID         string  `json:"id"`
	Title      string  `json:"title"`
	URL        string  `json:"url"`
	Duration   float64 `json:"duration"`
	LiveStatus string  `json:"liveStatus"`
}

// AppSettings stores user-configurable runtime settings.
type AppSettings struct {
	MaxWorkers          int     `json:"maxWorkers"`          // Parallel render workers (default 2)
	DefaultEncoder      string  `json:"defaultEncoder"`      // libx264, h264_qsv, etc.
	DefaultPreset       string  `json:"defaultPreset"`       // veryfast, fast, medium
	DefaultCRF          int     `json:"defaultCrf"`          // 20
	CookiesBrowser      string  `json:"cookiesBrowser"`      // "", "chrome", "edge", "firefox"
	CookiesFile         string  `json:"cookiesFile"`         // Optional path to cookies.txt
	DownloadQuality     string  `json:"downloadQuality"`     // 1080p, 720p, best
	OutputDir           string  `json:"outputDir"`           // Default E:\klipforge\outputs
	NotifyWebhookURL    string  `json:"notifyWebhookUrl"`    // Optional Discord/n8n webhook for render done/fail & engine up/down
	MinFreeDiskGB       float64 `json:"minFreeDiskGb"`       // Block new renders below this free GB threshold (default 2.0)
	OutputRetentionDays int     `json:"outputRetentionDays"` // Optional cleanup threshold in days (0 = manual)
}

// DiskStats summarizes available storage & KlipForge disk usage.
type DiskStats struct {
	FreeBytes     uint64  `json:"freeBytes"`
	TotalBytes    uint64  `json:"totalBytes"`
	FreeGB        float64 `json:"freeGb"`
	TotalGB       float64 `json:"totalGb"`
	OutputsBytes  int64   `json:"outputsBytes"`
	ProjectsBytes int64   `json:"projectsBytes"`
	MinFreeGB     float64 `json:"minFreeGb"`
	LowDisk       bool    `json:"lowDisk"`
}

// DepStatus describes the health of required external binaries.
type DepStatus struct {
	FFmpegPath    string   `json:"ffmpegPath"`
	FFmpegVersion string   `json:"ffmpegVersion"`
	FFprobePath   string   `json:"ffprobePath"`
	YtDlpPath     string   `json:"ytDlpPath"`
	YtDlpVersion  string   `json:"ytDlpVersion"`
	Encoders      []string `json:"encoders"`
	Ready         bool     `json:"ready"`
}

// SSEEvent is broadcast to connected dashboard clients in real time.
type SSEEvent struct {
	Type    string      `json:"type"` // project_update, job_update, log, status
	Payload interface{} `json:"payload"`
}

// DefaultDetectionSettings returns sensible tuned defaults for streamer VODs & standard videos.
func DefaultDetectionSettings() DetectionSettings {
	return DetectionSettings{
		HeatmapWeight:  0.45,
		ChatWeight:     0.35,
		AudioWeight:    0.20,
		MinScore:       35,
		MinDurationSec: 20,
		MaxDurationSec: 55,
		PrePaddingSec:  12,
		PostPaddingSec: 22,
		MaxClips:       15,
		SkipIntroSec:   15,
	}
}
