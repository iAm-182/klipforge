package audiofx

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"klipforge/internal/deps"
	"klipforge/internal/model"
)

// Library manages built-in royalty-free SFX/BGM packs plus user-uploaded custom audio files.
type Library struct {
	mu       sync.RWMutex
	AudioDir string
	MetaPath string
	Deps     *deps.Manager
	assets   map[string]*model.AudioAsset
}

type synthSpec struct {
	ID       string
	Name     string
	Kind     string
	Category string
	FileName string
	Duration float64
	Args     []string // FFmpeg filter/input args before output path
}

// Built-in 100% DMCA-free synthesized starter pack (SFX + BGM) ready immediately with zero copyright strikes!
var builtInPack = []synthSpec{
	// --- Viral Clipper SFX ---
	{
		ID:       "sfx-boom",
		Name:     "Deep Bass Boom (Vine Impact)",
		Kind:     "sfx",
		Category: "Meme / Impact",
		FileName: "builtin_sfx_boom.wav",
		Duration: 1.4,
		Args: []string{
			"-f", "lavfi", "-i",
			"aevalsrc=exprs='0.95*sin(2*PI*(95*exp(-3.2*t)+34)*t)*exp(-2.6*t)+0.35*sin(2*PI*190*t)*exp(-9*t)':s=48000:d=1.4",
			"-af", "lowpass=f=260,bass=g=8:f=65,afade=t=out:st=1.0:d=0.4",
		},
	},
	{
		ID:       "sfx-whoosh",
		Name:     "Whoosh Transition",
		Kind:     "sfx",
		Category: "Transition",
		FileName: "builtin_sfx_whoosh.wav",
		Duration: 0.7,
		Args: []string{
			"-f", "lavfi", "-i", "anoisesrc=c=pink:r=48000:d=0.7:a=0.8",
			"-af", "bandpass=f=850:w=500,afade=t=in:st=0:d=0.22,afade=t=out:st=0.28:d=0.42",
		},
	},
	{
		ID:       "sfx-riser",
		Name:     "Hype Tension Riser",
		Kind:     "sfx",
		Category: "Hype",
		FileName: "builtin_sfx_riser.wav",
		Duration: 1.8,
		Args: []string{
			"-f", "lavfi", "-i",
			"aevalsrc=exprs='0.55*sin(2*PI*(180+620*t*t)*t)*(t/1.8)':s=48000:d=1.8",
			"-af", "highpass=f=140,afade=t=in:st=0:d=0.4,afade=t=out:st=1.65:d=0.15",
		},
	},
	{
		ID:       "sfx-ding",
		Name:     "Pop Ding Highlight",
		Kind:     "sfx",
		Category: "Meme / Pop",
		FileName: "builtin_sfx_ding.wav",
		Duration: 0.85,
		Args: []string{
			"-f", "lavfi", "-i",
			"aevalsrc=exprs='(0.65*sin(2*PI*1318.5*t)+0.35*sin(2*PI*2637*t))*exp(-5.5*t)':s=48000:d=0.85",
			"-af", "afade=t=out:st=0.6:d=0.25",
		},
	},
	{
		ID:       "sfx-alert",
		Name:     "Kaget / Dramatic Sting",
		Kind:     "sfx",
		Category: "Meme / Kaget",
		FileName: "builtin_sfx_alert.wav",
		Duration: 0.9,
		Args: []string{
			"-f", "lavfi", "-i",
			"aevalsrc=exprs='(0.55*sin(2*PI*740*t)+0.45*sin(2*PI*1046.5*t)+0.3*sin(2*PI*1108.7*t))*exp(-3.6*t)':s=48000:d=0.9",
			"-af", "aecho=0.8:0.7:45:0.35,afade=t=out:st=0.6:d=0.3",
		},
	},
	{
		ID:       "sfx-glitch",
		Name:     "Glitch Zap Cut",
		Kind:     "sfx",
		Category: "Transition",
		FileName: "builtin_sfx_glitch.wav",
		Duration: 0.35,
		Args: []string{
			"-f", "lavfi", "-i",
			"aevalsrc=exprs='0.6*sin(2*PI*(300+1400*mod(floor(t*45),3))*t)*exp(-4*t)':s=48000:d=0.35",
			"-af", "acrusher=bits=6:mode=lin:aa=1,afade=t=out:st=0.22:d=0.13",
		},
	},
	{
		ID:       "sfx-bassdrop",
		Name:     "Jedag-Jedug Sub Kick",
		Kind:     "sfx",
		Category: "Hype / Bass",
		FileName: "builtin_sfx_bassdrop.wav",
		Duration: 1.0,
		Args: []string{
			"-f", "lavfi", "-i",
			"aevalsrc=exprs='0.95*sin(2*PI*(140*exp(-12*t)+46)*t)*exp(-3.4*t)':s=48000:d=1.0",
			"-af", "bass=g=10:f=55,alimiter=limit=0.95",
		},
	},
	{
		ID:       "sfx-coin",
		Name:     "Cha-Ching / Coin Pop",
		Kind:     "sfx",
		Category: "Gaming",
		FileName: "builtin_sfx_coin.wav",
		Duration: 0.75,
		Args: []string{
			"-f", "lavfi", "-i",
			"aevalsrc=exprs='(lt(t,0.12)*sin(2*PI*987.77*t)+gte(t,0.12)*sin(2*PI*1318.51*t))*exp(-4.5*max(0,t-0.12))':s=48000:d=0.75",
			"-af", "afade=t=out:st=0.55:d=0.2",
		},
	},
	{
		ID:       "sfx-metal-pipe",
		Name:     "Metal Pipe Crash Clang (Meme Nabrak!)",
		Kind:     "sfx",
		Category: "Meme / Crash",
		FileName: "builtin_sfx_metal_pipe.wav",
		Duration: 1.5,
		Args: []string{
			"-f", "lavfi", "-i",
			"aevalsrc=exprs='(0.45*sin(2*PI*682*t)+0.40*sin(2*PI*1194*t)+0.35*sin(2*PI*1873*t)+0.30*sin(2*PI*2741*t)+0.22*sin(2*PI*3910*t))*exp(-2.8*t)':s=48000:d=1.5",
			"-af", "aecho=0.8:0.75:35:0.4,alimiter=limit=0.95,afade=t=out:st=1.1:d=0.4",
		},
	},
	{
		ID:       "sfx-record-scratch",
		Name:     "Record Scratch / Fail Stop (Meme)",
		Kind:     "sfx",
		Category: "Meme / Fail",
		FileName: "builtin_sfx_record_scratch.wav",
		Duration: 0.65,
		Args: []string{
			"-f", "lavfi", "-i",
			"aevalsrc=exprs='0.75*sin(2*PI*(1100*exp(-5.5*t)+120*sin(2*PI*18*t))*t)*exp(-2.2*t)':s=48000:d=0.65",
			"-af", "bandpass=f=900:w=700,afade=t=out:st=0.48:d=0.17",
		},
	},

	// --- DMCA-Free Backsound (BGM) Loops ---
	{
		ID:       "bgm-gaming-pulse",
		Name:     "Gaming Synth Pulse (120 BPM)",
		Kind:     "bgm",
		Category: "Gaming / Hype",
		FileName: "builtin_bgm_gaming_pulse.mp3",
		Duration: 16.0,
		Args: []string{
			"-f", "lavfi", "-i",
			// 120 BPM bassline + gentle arpeggio pad (Am - F - C - G)
			"aevalsrc=exprs='0.28*sin(2*PI*(110*(lt(mod(t,8),2))+87.31*(gte(mod(t,8),2)*lt(mod(t,8),4))+130.81*(gte(mod(t,8),4)*lt(mod(t,8),6))+98*(gte(mod(t,8),6)))*t)*(0.4+0.6*exp(-8*mod(t,0.5)))+0.12*sin(2*PI*(440+110*floor(mod(t*4,3)))*t)*exp(-5*mod(t,0.25))':s=48000:d=16",
			"-af", "lowpass=f=2200,aecho=0.6:0.5:125:0.25,afade=t=in:st=0:d=0.3,afade=t=out:st=15.5:d=0.5",
			"-c:a", "libmp3lame", "-b:a", "160k",
		},
	},
	{
		ID:       "bgm-suspense",
		Name:     "Suspense Tension Drone (Horror/GTA)",
		Kind:     "bgm",
		Category: "Suspense / Intense",
		FileName: "builtin_bgm_suspense.mp3",
		Duration: 16.0,
		Args: []string{
			"-f", "lavfi", "-i",
			"aevalsrc=exprs='(0.32*sin(2*PI*55*t)+0.22*sin(2*PI*82.41*t)+0.15*sin(2*PI*110.5*t))*(0.7+0.3*sin(2*PI*0.25*t))+0.18*sin(2*PI*48*t)*exp(-6*mod(t,1.0))':s=48000:d=16",
			"-af", "lowpass=f=600,afade=t=in:st=0:d=0.5,afade=t=out:st=15.3:d=0.7",
			"-c:a", "libmp3lame", "-b:a", "160k",
		},
	},
	{
		ID:       "bgm-chill-lofi",
		Name:     "Chill Lo-Fi Warm Pad (Funny/Story)",
		Kind:     "bgm",
		Category: "Chill / Meme",
		FileName: "builtin_bgm_chill_lofi.mp3",
		Duration: 16.0,
		Args: []string{
			"-f", "lavfi", "-i",
			"aevalsrc=exprs='(0.18*sin(2*PI*220*t)+0.15*sin(2*PI*261.63*t)+0.14*sin(2*PI*329.63*t)+0.11*sin(2*PI*392*t))*(0.75+0.25*sin(2*PI*0.5*t))':s=48000:d=16",
			"-af", "lowpass=f=1200,aecho=0.7:0.6:200:0.3,afade=t=in:st=0:d=0.5,afade=t=out:st=15.3:d=0.7",
			"-c:a", "libmp3lame", "-b:a", "160k",
		},
	},
}

func NewLibrary(dataDir string, d *deps.Manager) *Library {
	audioDir := filepath.Join(dataDir, "audio")
	_ = os.MkdirAll(audioDir, 0o755)
	lib := &Library{
		AudioDir: audioDir,
		MetaPath: filepath.Join(dataDir, "audio_library.json"),
		Deps:     d,
		assets:   make(map[string]*model.AudioAsset),
	}
	lib.load()
	// Generate missing built-in royalty-free assets asynchronously so startup stays instant!
	go lib.EnsureBuiltIns()
	return lib
}

func (l *Library) load() {
	if b, err := os.ReadFile(l.MetaPath); err == nil {
		var list []*model.AudioAsset
		if json.Unmarshal(b, &list) == nil {
			for _, a := range list {
				if a.ID != "" && fileExists(a.FilePath) {
					l.assets[a.ID] = a
				}
			}
		}
	}
}

func (l *Library) saveLocked() {
	list := make([]*model.AudioAsset, 0, len(l.assets))
	for _, a := range l.assets {
		list = append(list, a)
	}
	sort.Slice(list, func(i, j int) bool {
		if list[i].Kind != list[j].Kind {
			return list[i].Kind < list[j].Kind
		}
		if list[i].BuiltIn != list[j].BuiltIn {
			return list[i].BuiltIn
		}
		return list[i].Name < list[j].Name
	})
	if b, err := json.MarshalIndent(list, "", "  "); err == nil {
		_ = os.WriteFile(l.MetaPath, b, 0o644)
	}
}

// EnsureBuiltIns synthesizes any missing royalty-free SFX/BGM files using FFmpeg.
func (l *Library) EnsureBuiltIns() {
	ff := l.Deps.GetStatus().FFmpegPath
	if ff == "" {
		return
	}

	var wg sync.WaitGroup
	changed := false
	var mu sync.Mutex

	for _, spec := range builtInPack {
		dest := filepath.Join(l.AudioDir, spec.FileName)
		if fileExists(dest) {
			l.mu.Lock()
			if _, ok := l.assets[spec.ID]; !ok {
				l.assets[spec.ID] = &model.AudioAsset{
					ID:       spec.ID,
					Name:     spec.Name,
					Kind:     spec.Kind,
					Category: spec.Category,
					FilePath: dest,
					FileName: spec.FileName,
					Duration: spec.Duration,
					BuiltIn:  true,
				}
				changed = true
			}
			l.mu.Unlock()
			continue
		}

		wg.Add(1)
		go func(sp synthSpec, outPath string) {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			args := append([]string{"-y", "-hide_banner", "-loglevel", "error"}, sp.Args...)
			args = append(args, "-ac", "2", "-ar", "48000", outPath)
			if err := exec.CommandContext(ctx, ff, args...).Run(); err == nil && fileExists(outPath) {
				mu.Lock()
				changed = true
				mu.Unlock()
				l.mu.Lock()
				l.assets[sp.ID] = &model.AudioAsset{
					ID:       sp.ID,
					Name:     sp.Name,
					Kind:     sp.Kind,
					Category: sp.Category,
					FilePath: outPath,
					FileName: sp.FileName,
					Duration: sp.Duration,
					BuiltIn:  true,
				}
				l.mu.Unlock()
			}
		}(spec, dest)
	}
	wg.Wait()

	if changed {
		l.mu.Lock()
		l.saveLocked()
		l.mu.Unlock()
	}
}

func (l *Library) List() []*model.AudioAsset {
	l.mu.RLock()
	defer l.mu.RUnlock()
	list := make([]*model.AudioAsset, 0, len(l.assets))
	for _, a := range l.assets {
		cp := *a
		list = append(list, &cp)
	}
	sort.Slice(list, func(i, j int) bool {
		if list[i].Kind != list[j].Kind {
			return list[i].Kind < list[j].Kind
		}
		if list[i].BuiltIn != list[j].BuiltIn {
			return !list[i].BuiltIn // Show user custom uploads first!
		}
		return list[i].Name < list[j].Name
	})
	return list
}

func (l *Library) Get(id string) (*model.AudioAsset, bool) {
	l.mu.RLock()
	defer l.mu.RUnlock()
	a, ok := l.assets[id]
	if !ok {
		return nil, false
	}
	cp := *a
	return &cp, true
}

func (l *Library) AddUpload(kind, displayName, category, origFilename string, r io.Reader, duration float64) (*model.AudioAsset, error) {
	if kind != "bgm" && kind != "sfx" {
		return nil, fmt.Errorf("kind harus bgm atau sfx")
	}
	ext := strings.ToLower(filepath.Ext(origFilename))
	if ext == "" {
		ext = ".mp3"
	}
	id := fmt.Sprintf("%s-custom-%d", kind, time.Now().UnixNano())
	fileName := fmt.Sprintf("%s%s", id, ext)
	destPath := filepath.Join(l.AudioDir, fileName)

	f, err := os.Create(destPath)
	if err != nil {
		return nil, err
	}
	if _, err := io.Copy(f, r); err != nil {
		f.Close()
		_ = os.Remove(destPath)
		return nil, err
	}
	f.Close()

	if strings.TrimSpace(displayName) == "" {
		displayName = strings.TrimSuffix(origFilename, filepath.Ext(origFilename))
	}
	if strings.TrimSpace(category) == "" {
		category = "Custom Upload"
	}

	asset := &model.AudioAsset{
		ID:       id,
		Name:     displayName,
		Kind:     kind,
		Category: category,
		FilePath: destPath,
		FileName: fileName,
		Duration: duration,
		BuiltIn:  false,
	}

	l.mu.Lock()
	l.assets[id] = asset
	l.saveLocked()
	l.mu.Unlock()
	return asset, nil
}

func (l *Library) Delete(id string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	a, ok := l.assets[id]
	if !ok {
		return nil
	}
	if a.BuiltIn {
		return fmt.Errorf("asset bawaan tidak bisa dihapus")
	}
	_ = os.Remove(a.FilePath)
	delete(l.assets, id)
	l.saveLocked()
	return nil
}

func fileExists(p string) bool {
	if p == "" {
		return false
	}
	fi, err := os.Stat(p)
	return err == nil && !fi.IsDir()
}
