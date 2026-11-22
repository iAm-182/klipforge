package deps

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"klipforge/internal/model"
)

// Manager locates and inspects ffmpeg, ffprobe, and yt-dlp binaries.
type Manager struct {
	BaseDir string
	mu      sync.RWMutex
	Status  model.DepStatus
}

// NewManager finds binary paths immediately (<10ms) and probes versions/GPU encoders asynchronously
// so the HTTP dashboard starts instantaneously.
func NewManager(baseDir string) *Manager {
	m := &Manager{BaseDir: baseDir}
	m.detectPaths()
	go m.probeDetails()
	return m
}

// GetStatus returns a thread-safe copy of the current dependency status.
func (m *Manager) GetStatus() model.DepStatus {
	m.mu.RLock()
	defer m.mu.RUnlock()
	cp := m.Status
	cp.Encoders = append([]string{}, m.Status.Encoders...)
	return cp
}

// Refresh re-scans paths and probes versions/encoders synchronously.
func (m *Manager) Refresh() model.DepStatus {
	m.detectPaths()
	m.probeDetails()
	return m.GetStatus()
}

// UpdateYtDlp runs `yt-dlp -U` to keep YouTube extraction up-to-date and refreshes version metadata.
func (m *Manager) UpdateYtDlp(ctx context.Context) (oldVer, newVer, output string, err error) {
	st := m.GetStatus()
	if st.YtDlpPath == "" {
		return "", "", "", os.ErrNotExist
	}
	oldVer = st.YtDlpVersion
	uCtx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()

	cmd := exec.CommandContext(uCtx, st.YtDlpPath, "-U")
	outBytes, runErr := cmd.CombinedOutput()
	output = strings.TrimSpace(string(outBytes))

	// Re-probe version regardless so dashboard reflects accurate version
	newVer = firstLineOutput(st.YtDlpPath, "--version")
	if newVer != "" {
		m.mu.Lock()
		m.Status.YtDlpVersion = newVer
		m.mu.Unlock()
	}
	return oldVer, newVer, output, runErr
}

func (m *Manager) detectPaths() {
	ff := m.findBinary("ffmpeg")
	fp := m.findBinary("ffprobe")
	yt := m.findBinary("yt-dlp")

	m.mu.Lock()
	m.Status = model.DepStatus{
		FFmpegPath:  ff,
		FFprobePath: fp,
		YtDlpPath:   yt,
		Encoders:    []string{"libx264"},
		Ready:       ff != "" && fp != "" && yt != "",
	}
	m.mu.Unlock()
}

func (m *Manager) probeDetails() {
	st := m.GetStatus()
	var wg sync.WaitGroup
	var ffVer, ytVer string
	var encoders []string

	if st.FFmpegPath != "" {
		wg.Add(2)
		go func() {
			defer wg.Done()
			ffVer = firstLineOutput(st.FFmpegPath, "-version")
		}()
		go func() {
			defer wg.Done()
			encoders = probeEncoders(st.FFmpegPath)
		}()
	}
	if st.YtDlpPath != "" {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ytVer = firstLineOutput(st.YtDlpPath, "--version")
		}()
	}
	wg.Wait()

	m.mu.Lock()
	if ffVer != "" {
		m.Status.FFmpegVersion = ffVer
	}
	if ytVer != "" {
		m.Status.YtDlpVersion = ytVer
	}
	if len(encoders) > 0 {
		m.Status.Encoders = encoders
	}
	m.mu.Unlock()
}

// FFmpegDir returns the directory containing ffmpeg/ffprobe for passing to yt-dlp --ffmpeg-location.
func (m *Manager) FFmpegDir() string {
	st := m.GetStatus()
	if st.FFmpegPath == "" {
		return ""
	}
	return filepath.Dir(st.FFmpegPath)
}

func (m *Manager) findBinary(name string) string {
	exeName := name
	if runtime.GOOS == "windows" {
		exeName += ".exe"
	}

	// 1. Check local ./bin/ directory first (self-contained)
	localPath := filepath.Join(m.BaseDir, "bin", exeName)
	if fi, err := os.Stat(localPath); err == nil && !fi.IsDir() {
		return localPath
	}

	// 2. Check standard PATH
	if p, err := exec.LookPath(name); err == nil {
		return p
	}

	// 3. Windows WinGet & common Python Scripts auto-discovery
	if runtime.GOOS == "windows" {
		localApp := os.Getenv("LOCALAPPDATA")
		if localApp != "" {
			wingetDir := filepath.Join(localApp, "Microsoft", "WinGet", "Packages")
			if found := searchShallow(wingetDir, exeName, 4); found != "" {
				return found
			}
			pyDir := filepath.Join(localApp, "Programs", "Python")
			if found := searchShallow(pyDir, exeName, 3); found != "" {
				return found
			}
		}
		commonPaths := []string{
			filepath.Join(`C:\ffmpeg\bin`, exeName),
			filepath.Join(`C:\ProgramData\chocolatey\bin`, exeName),
		}
		for _, p := range commonPaths {
			if fi, err := os.Stat(p); err == nil && !fi.IsDir() {
				return p
			}
		}
	}

	return ""
}

func searchShallow(root, target string, maxDepth int) string {
	if _, err := os.Stat(root); err != nil {
		return ""
	}
	targetLower := strings.ToLower(target)
	baseDepth := strings.Count(filepath.Clean(root), string(os.PathSeparator))
	var found string
	_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil || found != "" {
			return filepath.SkipDir
		}
		depth := strings.Count(filepath.Clean(path), string(os.PathSeparator)) - baseDepth
		if d.IsDir() && depth > maxDepth {
			return filepath.SkipDir
		}
		if !d.IsDir() && strings.ToLower(d.Name()) == targetLower {
			found = path
			return filepath.SkipAll
		}
		return nil
	})
	return found
}

func firstLineOutput(bin string, args ...string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, bin, args...).Output()
	if err != nil {
		return ""
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(lines) == 0 {
		return ""
	}
	return strings.TrimSpace(lines[0])
}

func probeEncoders(ffmpegPath string) []string {
	encoders := []string{"libx264"}
	candidates := []string{"h264_nvenc", "h264_qsv", "h264_amf"}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, ffmpegPath, "-hide_banner", "-encoders").Output()
	if err != nil {
		return encoders
	}
	text := string(out)

	var wg sync.WaitGroup
	var mu sync.Mutex
	for _, enc := range candidates {
		if !strings.Contains(text, enc) {
			continue
		}
		wg.Add(1)
		go func(e string) {
			defer wg.Done()
			if testEncoder(ffmpegPath, e) {
				mu.Lock()
				encoders = append(encoders, e)
				mu.Unlock()
			}
		}(enc)
	}
	wg.Wait()
	return encoders
}

func testEncoder(ffmpegPath, enc string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, ffmpegPath,
		"-hide_banner", "-loglevel", "error",
		"-f", "lavfi", "-i", "color=c=black:s=128x128:d=0.1",
		"-c:v", enc, "-frames:v", "1", "-f", "null", "-",
	)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	return cmd.Run() == nil
}
