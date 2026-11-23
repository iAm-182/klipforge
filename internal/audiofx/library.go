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

// Library manages built-in royalty-free CapCut-Pro SFX/BGM packs, smart audio presets,
// plus user-uploaded custom MP3/WAV audio files.
type Library struct {
	mu          sync.RWMutex
	AudioDir    string
	MetaPath    string
	PresetsPath string
	Deps        *deps.Manager
	assets      map[string]*model.AudioAsset
	presets     map[string]*model.AudioPreset
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

// Built-in 100% DMCA-free CapCut-Pro Mega Pack:
// 33 Viral SFX (Meme, Impact, Transition, Fail, Alert, Crowd) + 12 Mood BGM Loops (Funny, Hype, Chill, Suspense, Victory, Fail, Horror, Retro)!
var builtInPack = []synthSpec{
	// ==================== MEME SFX ====================
	{
		ID:       "sfx-boom",
		Name:     "Deep Bass Boom (Vine Impact)",
		Kind:     "sfx",
		Category: "Impact",
		FileName: "builtin_sfx_boom.wav",
		Duration: 1.4,
		Args: []string{
			"-f", "lavfi", "-i",
			"aevalsrc=exprs='0.95*sin(2*PI*(95*exp(-3.2*t)+34)*t)*exp(-2.6*t)+0.35*sin(2*PI*190*t)*exp(-9*t)':s=48000:d=1.4",
			"-af", "lowpass=f=260,bass=g=8:f=65,afade=t=out:st=1.0:d=0.4",
		},
	},
	{
		ID:       "sfx-metal-pipe",
		Name:     "Metal Pipe Crash Clang (Nabrak!)",
		Kind:     "sfx",
		Category: "Meme",
		FileName: "builtin_sfx_metal_pipe.wav",
		Duration: 1.5,
		Args: []string{
			"-f", "lavfi", "-i",
			"aevalsrc=exprs='(0.45*sin(2*PI*682*t)+0.40*sin(2*PI*1194*t)+0.35*sin(2*PI*1873*t)+0.30*sin(2*PI*2741*t)+0.22*sin(2*PI*3910*t))*exp(-2.8*t)':s=48000:d=1.5",
			"-af", "aecho=0.8:0.75:35:0.4,alimiter=limit=0.95,afade=t=out:st=1.1:d=0.4",
		},
	},
	{
		ID:       "sfx-oof-roblox",
		Name:     "Oof / Ahh Roblox Impact (Meme Mati!)",
		Kind:     "sfx",
		Category: "Meme",
		FileName: "builtin_sfx_oof_roblox.wav",
		Duration: 0.55,
		Args: []string{
			"-f", "lavfi", "-i",
			"aevalsrc=exprs='(0.55*sin(2*PI*(245*exp(-3.4*t)+105)*t)+0.35*sin(2*PI*(520*exp(-4.0*t)+220)*t)+0.22*sin(2*PI*(980*exp(-5.0*t)+340)*t))*exp(-4.5*t)':s=48000:d=0.55",
			"-af", "bandpass=f=480:w=520,bass=g=5:f=180,afade=t=out:st=0.35:d=0.20",
		},
	},
	{
		ID:       "sfx-bruh",
		Name:     "Bruh Low Drop (Meme Datar)",
		Kind:     "sfx",
		Category: "Meme",
		FileName: "builtin_sfx_bruh.wav",
		Duration: 0.72,
		Args: []string{
			"-f", "lavfi", "-i",
			"aevalsrc=exprs='(0.52*sin(2*PI*(155*exp(-2.6*t)+78)*t)+0.32*sin(2*PI*(310*exp(-2.6*t)+156)*t)+0.20*sin(2*PI*(620*exp(-3.2*t)+280)*t))*exp(-3.0*t)':s=48000:d=0.72",
			"-af", "lowpass=f=950,bass=g=6:f=130,afade=t=out:st=0.48:d=0.24",
		},
	},
	{
		ID:       "sfx-bonk",
		Name:     "Cartoon Bonk Kepala (Kocak)",
		Kind:     "sfx",
		Category: "Meme",
		FileName: "builtin_sfx_bonk.wav",
		Duration: 0.45,
		Args: []string{
			"-f", "lavfi", "-i",
			"aevalsrc=exprs='(0.75*sin(2*PI*(560*exp(-14*t)+185)*t)+0.35*sin(2*PI*(1120*exp(-18*t)+370)*t))*exp(-8.5*t)':s=48000:d=0.45",
			"-af", "bandpass=f=420:w=450,afade=t=out:st=0.28:d=0.17",
		},
	},
	{
		ID:       "sfx-boing",
		Name:     "Cartoon Spring Boing",
		Kind:     "sfx",
		Category: "Meme",
		FileName: "builtin_sfx_boing.wav",
		Duration: 0.85,
		Args: []string{
			"-f", "lavfi", "-i",
			"aevalsrc=exprs='0.75*sin(2*PI*(170+260*t+65*sin(2*PI*14*t))*t)*exp(-2.8*t)':s=48000:d=0.85",
			"-af", "afade=t=out:st=0.6:d=0.25",
		},
	},
	{
		ID:       "sfx-slip-whistle",
		Name:     "Slide Whistle Terpeleset",
		Kind:     "sfx",
		Category: "Meme",
		FileName: "builtin_sfx_slip_whistle.wav",
		Duration: 0.75,
		Args: []string{
			"-f", "lavfi", "-i",
			"aevalsrc=exprs='0.65*sin(2*PI*(1450-1050*sin(PI*t/0.75))*t)*exp(-1.2*t)':s=48000:d=0.75",
			"-af", "afade=t=in:st=0:d=0.05,afade=t=out:st=0.58:d=0.17",
		},
	},
	{
		ID:       "sfx-fart-poot",
		Name:     "Meme Poot / Kentut Lucu",
		Kind:     "sfx",
		Category: "Meme",
		FileName: "builtin_sfx_fart_poot.wav",
		Duration: 0.6,
		Args: []string{
			"-f", "lavfi", "-i",
			"aevalsrc=exprs='0.65*sin(2*PI*(95-35*t+28*sin(2*PI*36*t))*t)*exp(-2.6*t)':s=48000:d=0.6",
			"-af", "acrusher=bits=5:mode=lin:aa=1,lowpass=f=680,afade=t=out:st=0.4:d=0.2",
		},
	},
	{
		ID:       "sfx-bleep",
		Name:     "Sensor Bleep 1kHz (Sensor Kata!)",
		Kind:     "sfx",
		Category: "Meme",
		FileName: "builtin_sfx_bleep.wav",
		Duration: 0.65,
		Args: []string{
			"-f", "lavfi", "-i",
			"sine=frequency=1000:sample_rate=48000:duration=0.65",
			"-af", "volume=0.7,afade=t=in:st=0:d=0.02,afade=t=out:st=0.6:d=0.05",
		},
	},
	{
		ID:       "sfx-pop",
		Name:     "Pop Bubble Teks Muncul",
		Kind:     "sfx",
		Category: "Meme",
		FileName: "builtin_sfx_pop.wav",
		Duration: 0.22,
		Args: []string{
			"-f", "lavfi", "-i",
			"aevalsrc=exprs='0.85*sin(2*PI*(240+1350*t)*t)*exp(-18*t)':s=48000:d=0.22",
			"-af", "afade=t=out:st=0.14:d=0.08",
		},
	},
	{
		ID:       "sfx-ding",
		Name:     "Pop Ding Highlight",
		Kind:     "sfx",
		Category: "Meme",
		FileName: "builtin_sfx_ding.wav",
		Duration: 0.85,
		Args: []string{
			"-f", "lavfi", "-i",
			"aevalsrc=exprs='(0.65*sin(2*PI*1318.5*t)+0.35*sin(2*PI*2637*t))*exp(-5.5*t)':s=48000:d=0.85",
			"-af", "afade=t=out:st=0.6:d=0.25",
		},
	},
	{
		ID:       "sfx-coin",
		Name:     "Cha-Ching / Coin Pop",
		Kind:     "sfx",
		Category: "Meme",
		FileName: "builtin_sfx_coin.wav",
		Duration: 0.75,
		Args: []string{
			"-f", "lavfi", "-i",
			"aevalsrc=exprs='(lt(t,0.12)*sin(2*PI*987.77*t)+gte(t,0.12)*sin(2*PI*1318.51*t))*exp(-4.5*max(0,t-0.12))':s=48000:d=0.75",
			"-af", "afade=t=out:st=0.55:d=0.2",
		},
	},

	// ==================== IMPACT SFX ====================
	{
		ID:       "sfx-bassdrop",
		Name:     "Jedag-Jedug Sub Kick",
		Kind:     "sfx",
		Category: "Impact",
		FileName: "builtin_sfx_bassdrop.wav",
		Duration: 1.0,
		Args: []string{
			"-f", "lavfi", "-i",
			"aevalsrc=exprs='0.95*sin(2*PI*(140*exp(-12*t)+46)*t)*exp(-3.4*t)':s=48000:d=1.0",
			"-af", "bass=g=10:f=55,alimiter=limit=0.95",
		},
	},
	{
		ID:       "sfx-dun-dun-dun",
		Name:     "Dramatic Dun-Dun-DUUN! (Plot Twist)",
		Kind:     "sfx",
		Category: "Impact",
		FileName: "builtin_sfx_dun_dun_dun.wav",
		Duration: 2.1,
		Args: []string{
			"-f", "lavfi", "-i",
			"aevalsrc=exprs='(lt(t,0.42)*(0.45*sin(2*PI*196*t)+0.3*sin(2*PI*392*t))*exp(-4*t)+gte(t,0.45)*lt(t,0.88)*(0.45*sin(2*PI*185*t)+0.3*sin(2*PI*370*t))*exp(-4*(t-0.45))+gte(t,0.92)*(0.5*sin(2*PI*146.83*t)+0.35*sin(2*PI*293.66*t)+0.25*sin(2*PI*73.42*t))*exp(-1.8*(t-0.92)))':s=48000:d=2.1",
			"-af", "lowpass=f=1400,aecho=0.8:0.75:55:0.35,afade=t=out:st=1.7:d=0.4",
		},
	},
	{
		ID:       "sfx-punch-slap",
		Name:     "Tamparan / Punch Smack!",
		Kind:     "sfx",
		Category: "Impact",
		FileName: "builtin_sfx_punch_slap.wav",
		Duration: 0.42,
		Args: []string{
			"-f", "lavfi", "-i", "anoisesrc=c=white:r=48000:d=0.42:a=0.9",
			"-f", "lavfi", "-i", "aevalsrc=exprs='0.9*sin(2*PI*(150*exp(-18*t)+55)*t)*exp(-10*t)':s=48000:d=0.42",
			"-filter_complex", "[0:a]bandpass=f=1400:w=1200,afade=t=out:st=0.05:d=0.18[n];[n][1:a]amix=inputs=2:normalize=0,alimiter=limit=0.95",
		},
	},
	{
		ID:       "sfx-explosion",
		Name:     "Ledakan Besar / Wasted Boom",
		Kind:     "sfx",
		Category: "Impact",
		FileName: "builtin_sfx_explosion.wav",
		Duration: 2.0,
		Args: []string{
			"-f", "lavfi", "-i", "anoisesrc=c=brown:r=48000:d=2.0:a=0.95",
			"-f", "lavfi", "-i", "aevalsrc=exprs='0.9*sin(2*PI*(85*exp(-4*t)+30)*t)*exp(-2.1*t)':s=48000:d=2.0",
			"-filter_complex", "[0:a]lowpass=f=520,afade=t=out:st=0.35:d=1.65[n];[n][1:a]amix=inputs=2:normalize=0,bass=g=8:f=60,alimiter=limit=0.95",
		},
	},
	{
		ID:       "sfx-thunder",
		Name:     "Petir Menyambar (Dramatis)",
		Kind:     "sfx",
		Category: "Impact",
		FileName: "builtin_sfx_thunder.wav",
		Duration: 2.2,
		Args: []string{
			"-f", "lavfi", "-i", "anoisesrc=c=pink:r=48000:d=2.2:a=0.9",
			"-af", "lowpass=f=380,aecho=0.8:0.8:120:0.45,afade=t=in:st=0:d=0.04,afade=t=out:st=0.6:d=1.6",
		},
	},

	// ==================== TRANSITION SFX ====================
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
		ID:       "sfx-swoosh-fast",
		Name:     "Whip Pan Swoosh Cepat",
		Kind:     "sfx",
		Category: "Transition",
		FileName: "builtin_sfx_swoosh_fast.wav",
		Duration: 0.38,
		Args: []string{
			"-f", "lavfi", "-i", "anoisesrc=c=white:r=48000:d=0.38:a=0.85",
			"-af", "bandpass=f=1600:w=900,afade=t=in:st=0:d=0.10,afade=t=out:st=0.15:d=0.23",
		},
	},
	{
		ID:       "sfx-riser",
		Name:     "Hype Tension Riser",
		Kind:     "sfx",
		Category: "Transition",
		FileName: "builtin_sfx_riser.wav",
		Duration: 1.8,
		Args: []string{
			"-f", "lavfi", "-i",
			"aevalsrc=exprs='0.55*sin(2*PI*(180+620*t*t)*t)*(t/1.8)':s=48000:d=1.8",
			"-af", "highpass=f=140,afade=t=in:st=0:d=0.4,afade=t=out:st=1.65:d=0.15",
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
		ID:       "sfx-shutter",
		Name:     "Camera Freeze-Frame Shutter",
		Kind:     "sfx",
		Category: "Transition",
		FileName: "builtin_sfx_shutter.wav",
		Duration: 0.32,
		Args: []string{
			"-f", "lavfi", "-i", "anoisesrc=c=white:r=48000:d=0.32:a=0.85",
			"-af", "highpass=f=1800,volume='if(lt(t,0.04),1,if(gt(t,0.09)*lt(t,0.14),0.8,0.05))':eval=frame,afade=t=out:st=0.14:d=0.18",
		},
	},
	{
		ID:       "sfx-rewind",
		Name:     "Tape Rewind Cepat",
		Kind:     "sfx",
		Category: "Transition",
		FileName: "builtin_sfx_rewind.wav",
		Duration: 0.65,
		Args: []string{
			"-f", "lavfi", "-i",
			"aevalsrc=exprs='0.6*sin(2*PI*(450+1800*t+220*sin(2*PI*30*t))*t)*(0.3+0.7*t/0.65)':s=48000:d=0.65",
			"-af", "bandpass=f=1500:w=1200,afade=t=out:st=0.52:d=0.13",
		},
	},

	// ==================== FAIL SFX ====================
	{
		ID:       "sfx-record-scratch",
		Name:     "Record Scratch / Fail Stop",
		Kind:     "sfx",
		Category: "Fail",
		FileName: "builtin_sfx_record_scratch.wav",
		Duration: 0.65,
		Args: []string{
			"-f", "lavfi", "-i",
			"aevalsrc=exprs='0.75*sin(2*PI*(1100*exp(-5.5*t)+120*sin(2*PI*18*t))*t)*exp(-2.2*t)':s=48000:d=0.65",
			"-af", "bandpass=f=900:w=700,afade=t=out:st=0.48:d=0.17",
		},
	},
	{
		ID:       "sfx-sad-trombone",
		Name:     "Sad Trombone (Wah-Wah-Waaah Fail)",
		Kind:     "sfx",
		Category: "Fail",
		FileName: "builtin_sfx_sad_trombone.wav",
		Duration: 2.3,
		Args: []string{
			"-f", "lavfi", "-i",
			"aevalsrc=exprs='(lt(t,0.48)*sin(2*PI*293.66*t)*exp(-1.5*t)+gte(t,0.50)*lt(t,0.98)*sin(2*PI*277.18*t)*exp(-1.5*(t-0.50))+gte(t,1.00)*lt(t,1.48)*sin(2*PI*261.63*t)*exp(-1.5*(t-1.00))+gte(t,1.50)*sin(2*PI*(246.94-18*(t-1.50)+6*sin(2*PI*6*t))*t)*exp(-1.1*(t-1.50)))*0.68':s=48000:d=2.3",
			"-af", "lowpass=f=1200,aecho=0.7:0.6:40:0.25,afade=t=out:st=1.95:d=0.35",
		},
	},
	{
		ID:       "sfx-win-error",
		Name:     "Windows Error Bong (Freeze Otak!)",
		Kind:     "sfx",
		Category: "Fail",
		FileName: "builtin_sfx_win_error.wav",
		Duration: 0.85,
		Args: []string{
			"-f", "lavfi", "-i",
			"aevalsrc=exprs='(0.45*sin(2*PI*220*t)+0.42*sin(2*PI*311.13*t)+0.28*sin(2*PI*440*t))*exp(-4.2*t)':s=48000:d=0.85",
			"-af", "aecho=0.7:0.6:30:0.3,afade=t=out:st=0.6:d=0.25",
		},
	},
	{
		ID:       "sfx-crickets",
		Name:     "Jangkrik Krik-Krik (Garing/Canggung)",
		Kind:     "sfx",
		Category: "Fail",
		FileName: "builtin_sfx_crickets.wav",
		Duration: 2.0,
		Args: []string{
			"-f", "lavfi", "-i",
			"aevalsrc=exprs='0.45*sin(2*PI*4200*t)*lt(mod(t,0.62),0.26)*(0.5+0.5*sin(2*PI*34*t))':s=48000:d=2.0",
			"-af", "bandpass=f=4200:w=900,afade=t=in:st=0:d=0.1,afade=t=out:st=1.7:d=0.3",
		},
	},

	// ==================== ALERT SFX ====================
	{
		ID:       "sfx-alert",
		Name:     "Kaget / Dramatic Alert Sting",
		Kind:     "sfx",
		Category: "Alert",
		FileName: "builtin_sfx_alert.wav",
		Duration: 0.9,
		Args: []string{
			"-f", "lavfi", "-i",
			"aevalsrc=exprs='(0.55*sin(2*PI*740*t)+0.45*sin(2*PI*1046.5*t)+0.3*sin(2*PI*1108.7*t))*exp(-3.6*t)':s=48000:d=0.9",
			"-af", "aecho=0.8:0.7:45:0.35,afade=t=out:st=0.6:d=0.3",
		},
	},
	{
		ID:       "sfx-airhorn",
		Name:     "MLG Airhorn Hype!",
		Kind:     "sfx",
		Category: "Alert",
		FileName: "builtin_sfx_airhorn.wav",
		Duration: 1.35,
		Args: []string{
			"-f", "lavfi", "-i",
			"aevalsrc=exprs='(0.32*sin(2*PI*415.3*t)+0.30*sin(2*PI*523.25*t)+0.28*sin(2*PI*622.25*t)+0.22*sin(2*PI*830.6*t))*(lt(t,0.18)+gte(t,0.23)*lt(t,0.40)+gte(t,0.46)*exp(-1.1*(t-0.46)))':s=48000:d=1.35",
			"-af", "aecho=0.8:0.7:40:0.3,alimiter=limit=0.95,afade=t=out:st=1.1:d=0.25",
		},
	},
	{
		ID:       "sfx-siren-police",
		Name:     "Sirine Polisi GTA Wiu-Wiu!",
		Kind:     "sfx",
		Category: "Alert",
		FileName: "builtin_sfx_siren_police.wav",
		Duration: 1.8,
		Args: []string{
			"-f", "lavfi", "-i",
			"aevalsrc=exprs='0.62*sin(2*PI*(820+280*sin(2*PI*1.35*t))*t)':s=48000:d=1.8",
			"-af", "bandpass=f=950:w=700,afade=t=in:st=0:d=0.1,afade=t=out:st=1.5:d=0.3",
		},
	},
	{
		ID:       "sfx-heartbeat",
		Name:     "Detak Jantung Tegang (Dug-Dug)",
		Kind:     "sfx",
		Category: "Alert",
		FileName: "builtin_sfx_heartbeat.wav",
		Duration: 1.6,
		Args: []string{
			"-f", "lavfi", "-i",
			"aevalsrc=exprs='0.9*sin(2*PI*58*t)*(exp(-16*mod(t,0.8))+0.75*exp(-16*mod(t+0.58,0.8)))':s=48000:d=1.6",
			"-af", "lowpass=f=180,bass=g=8:f=58,afade=t=out:st=1.4:d=0.2",
		},
	},

	// ==================== CROWD SFX ====================
	{
		ID:       "sfx-crowd-cheer",
		Name:     "Sorakan / Tepuk Tangan Crowd",
		Kind:     "sfx",
		Category: "Crowd",
		FileName: "builtin_sfx_crowd_cheer.wav",
		Duration: 2.0,
		Args: []string{
			"-f", "lavfi", "-i", "anoisesrc=c=pink:r=48000:d=2.0:a=0.7",
			"-af", "bandpass=f=1350:w=1100,tremolo=f=9:d=0.35,afade=t=in:st=0:d=0.35,afade=t=out:st=1.35:d=0.65",
		},
	},
	{
		ID:       "sfx-anime-sparkle",
		Name:     "Anime Wow / Sparkle Chime",
		Kind:     "sfx",
		Category: "Crowd",
		FileName: "builtin_sfx_anime_sparkle.wav",
		Duration: 1.15,
		Args: []string{
			"-f", "lavfi", "-i",
			"aevalsrc=exprs='(lt(t,0.18)*sin(2*PI*1046.5*t)+gte(t,0.12)*lt(t,0.34)*sin(2*PI*1318.5*t)+gte(t,0.24)*lt(t,0.52)*sin(2*PI*1568*t)+gte(t,0.36)*sin(2*PI*2093*t))*0.42*exp(-2.8*max(0,t-0.36))':s=48000:d=1.15",
			"-af", "aecho=0.8:0.75:50:0.4,afade=t=out:st=0.85:d=0.3",
		},
	},

	// ==================== 12 MOOD BGM LOOPS (100% DMCA-FREE) ====================
	{
		ID:       "bgm-gaming-pulse",
		Name:     "Gaming Synth Pulse (120 BPM)",
		Kind:     "bgm",
		Category: "Hype",
		FileName: "builtin_bgm_gaming_pulse.mp3",
		Duration: 16.0,
		Args: []string{
			"-f", "lavfi", "-i",
			"aevalsrc=exprs='0.28*sin(2*PI*(110*(lt(mod(t,8),2))+87.31*(gte(mod(t,8),2)*lt(mod(t,8),4))+130.81*(gte(mod(t,8),4)*lt(mod(t,8),6))+98*(gte(mod(t,8),6)))*t)*(0.4+0.6*exp(-8*mod(t,0.5)))+0.12*sin(2*PI*(440+110*floor(mod(t*4,3)))*t)*exp(-5*mod(t,0.25))':s=48000:d=16",
			"-af", "lowpass=f=2200,aecho=0.6:0.5:125:0.25,afade=t=in:st=0:d=0.3,afade=t=out:st=15.5:d=0.5",
			"-c:a", "libmp3lame", "-b:a", "160k",
		},
	},
	{
		ID:       "bgm-hype-phonk",
		Name:     "Phonk Vice Drift Bass (135 BPM)",
		Kind:     "bgm",
		Category: "Hype",
		FileName: "builtin_bgm_hype_phonk.mp3",
		Duration: 16.0,
		Args: []string{
			"-f", "lavfi", "-i",
			"aevalsrc=exprs='0.38*sin(2*PI*(55*exp(-6*mod(t,0.444))+41.2)*t)*exp(-3.5*mod(t,0.444))+0.14*sin(2*PI*(622.25*(lt(mod(t,0.888),0.444))+587.33*(gte(mod(t,0.888),0.444)))*t)*exp(-9*mod(t,0.222))':s=48000:d=16",
			"-af", "bass=g=7:f=60,lowpass=f=2400,alimiter=limit=0.92,afade=t=in:st=0:d=0.25,afade=t=out:st=15.5:d=0.5",
			"-c:a", "libmp3lame", "-b:a", "160k",
		},
	},
	{
		ID:       "bgm-hype-trap",
		Name:     "Hyper 808 Clutch Trap Beat",
		Kind:     "bgm",
		Category: "Hype",
		FileName: "builtin_bgm_hype_trap.mp3",
		Duration: 16.0,
		Args: []string{
			"-f", "lavfi", "-i",
			"aevalsrc=exprs='0.40*sin(2*PI*(49+32*exp(-14*mod(t,0.5)))*t)*exp(-2.8*mod(t,0.5))+0.12*sin(2*PI*(329.63+82*floor(mod(t*4,4)))*t)*exp(-10*mod(t,0.125))':s=48000:d=16",
			"-af", "bass=g=6:f=55,lowpass=f=2100,afade=t=in:st=0:d=0.25,afade=t=out:st=15.5:d=0.5",
			"-c:a", "libmp3lame", "-b:a", "160k",
		},
	},
	{
		ID:       "bgm-funny-bouncy",
		Name:     "Funny Comedy Pluck (Lucu / Kocak)",
		Kind:     "bgm",
		Category: "Funny",
		FileName: "builtin_bgm_funny_bouncy.mp3",
		Duration: 16.0,
		Args: []string{
			"-f", "lavfi", "-i",
			"aevalsrc=exprs='0.26*sin(2*PI*(261.63*(lt(mod(t,2),0.5))+329.63*(gte(mod(t,2),0.5)*lt(mod(t,2),1.0))+392*(gte(mod(t,2),1.0)*lt(mod(t,2),1.5))+523.25*(gte(mod(t,2),1.5)))*t)*exp(-8*mod(t,0.25))+0.18*sin(2*PI*130.81*t)*exp(-6*mod(t,0.5))':s=48000:d=16",
			"-af", "lowpass=f=2400,afade=t=in:st=0:d=0.2,afade=t=out:st=15.5:d=0.5",
			"-c:a", "libmp3lame", "-b:a", "160k",
		},
	},
	{
		ID:       "bgm-funny-sneaky",
		Name:     "Sneaky Tiptoe Meme (Iseng / Ngeles)",
		Kind:     "bgm",
		Category: "Funny",
		FileName: "builtin_bgm_funny_sneaky.mp3",
		Duration: 16.0,
		Args: []string{
			"-f", "lavfi", "-i",
			"aevalsrc=exprs='0.28*sin(2*PI*(196*(lt(mod(t,2),0.5))+207.65*(gte(mod(t,2),0.5)*lt(mod(t,2),1.0))+233.08*(gte(mod(t,2),1.0)*lt(mod(t,2),1.5))+196*(gte(mod(t,2),1.5)))*t)*exp(-11*mod(t,0.5))':s=48000:d=16",
			"-af", "bandpass=f=600:w=900,afade=t=in:st=0:d=0.2,afade=t=out:st=15.5:d=0.5",
			"-c:a", "libmp3lame", "-b:a", "160k",
		},
	},
	{
		ID:       "bgm-chill-lofi",
		Name:     "Chill Lo-Fi Warm Pad (Story)",
		Kind:     "bgm",
		Category: "Chill",
		FileName: "builtin_bgm_chill_lofi.mp3",
		Duration: 16.0,
		Args: []string{
			"-f", "lavfi", "-i",
			"aevalsrc=exprs='(0.18*sin(2*PI*220*t)+0.15*sin(2*PI*261.63*t)+0.14*sin(2*PI*329.63*t)+0.11*sin(2*PI*392*t))*(0.75+0.25*sin(2*PI*0.5*t))':s=48000:d=16",
			"-af", "lowpass=f=1200,aecho=0.7:0.6:200:0.3,afade=t=in:st=0:d=0.5,afade=t=out:st=15.3:d=0.7",
			"-c:a", "libmp3lame", "-b:a", "160k",
		},
	},
	{
		ID:       "bgm-suspense",
		Name:     "Suspense Tension Drone (Dark GTA)",
		Kind:     "bgm",
		Category: "Suspense",
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
		ID:       "bgm-suspense-chase",
		Name:     "Police Chase Pulse (Kejar-Kejaran)",
		Kind:     "bgm",
		Category: "Suspense",
		FileName: "builtin_bgm_suspense_chase.mp3",
		Duration: 16.0,
		Args: []string{
			"-f", "lavfi", "-i",
			"aevalsrc=exprs='0.32*sin(2*PI*(73.42*(lt(mod(t,4),2))+65.41*(gte(mod(t,4),2)))*t)*exp(-7*mod(t,0.25))+0.14*sin(2*PI*(293.66+15*sin(2*PI*2*t))*t)*exp(-6*mod(t,0.5))':s=48000:d=16",
			"-af", "lowpass=f=1500,afade=t=in:st=0:d=0.25,afade=t=out:st=15.5:d=0.5",
			"-c:a", "libmp3lame", "-b:a", "160k",
		},
	},
	{
		ID:       "bgm-victory-fanfare",
		Name:     "Victory Winner Chords (Epic Win)",
		Kind:     "bgm",
		Category: "Victory",
		FileName: "builtin_bgm_victory_fanfare.mp3",
		Duration: 16.0,
		Args: []string{
			"-f", "lavfi", "-i",
			"aevalsrc=exprs='(0.20*sin(2*PI*261.63*t)+0.18*sin(2*PI*329.63*t)+0.16*sin(2*PI*392*t)+0.14*sin(2*PI*523.25*t))*(0.55+0.45*exp(-5*mod(t,0.5)))':s=48000:d=16",
			"-af", "lowpass=f=2400,aecho=0.7:0.6:140:0.25,afade=t=in:st=0:d=0.3,afade=t=out:st=15.4:d=0.6",
			"-c:a", "libmp3lame", "-b:a", "160k",
		},
	},
	{
		ID:       "bgm-fail-sad",
		Name:     "Sad Piano Fail Loop (Wasted / Sedih)",
		Kind:     "bgm",
		Category: "Fail",
		FileName: "builtin_bgm_fail_sad.mp3",
		Duration: 16.0,
		Args: []string{
			"-f", "lavfi", "-i",
			"aevalsrc=exprs='(0.22*sin(2*PI*(220*(lt(mod(t,8),4))+174.61*(gte(mod(t,8),4)))*t)+0.18*sin(2*PI*(261.63*(lt(mod(t,8),4))+220*(gte(mod(t,8),4)))*t)+0.14*sin(2*PI*329.63*t))*exp(-0.9*mod(t,2.0))':s=48000:d=16",
			"-af", "lowpass=f=1300,aecho=0.75:0.7:220:0.35,afade=t=in:st=0:d=0.4,afade=t=out:st=15.2:d=0.8",
			"-c:a", "libmp3lame", "-b:a", "160k",
		},
	},
	{
		ID:       "bgm-horror-eerie",
		Name:     "Creepy Horror Ambience (Serem)",
		Kind:     "bgm",
		Category: "Horror",
		FileName: "builtin_bgm_horror_eerie.mp3",
		Duration: 16.0,
		Args: []string{
			"-f", "lavfi", "-i",
			"aevalsrc=exprs='(0.24*sin(2*PI*61.74*t)+0.22*sin(2*PI*65.41*t)+0.12*sin(2*PI*(440+8*sin(2*PI*0.3*t))*t))*(0.65+0.35*sin(2*PI*0.2*t))':s=48000:d=16",
			"-af", "lowpass=f=900,aecho=0.8:0.75:260:0.4,afade=t=in:st=0:d=0.6,afade=t=out:st=15.0:d=1.0",
			"-c:a", "libmp3lame", "-b:a", "160k",
		},
	},
	{
		ID:       "bgm-retro-arcade",
		Name:     "Retro 8-Bit Arcade Turbo",
		Kind:     "bgm",
		Category: "Retro",
		FileName: "builtin_bgm_retro_arcade.mp3",
		Duration: 16.0,
		Args: []string{
			"-f", "lavfi", "-i",
			"aevalsrc=exprs='0.22*sin(2*PI*(261.63+65*floor(mod(t*6,5)))*t)*exp(-7*mod(t,0.166))+0.20*sin(2*PI*130.81*t)*exp(-6*mod(t,0.333))':s=48000:d=16",
			"-af", "acrusher=bits=7:mode=lin:aa=1,lowpass=f=2200,afade=t=in:st=0:d=0.2,afade=t=out:st=15.5:d=0.5",
			"-c:a", "libmp3lame", "-b:a", "160k",
		},
	},
}

// Built-in 1-click Smart Moment Audio Presets (offsets are relative to selected playhead/peak second!)
var builtInPresets = []model.AudioPreset{
	{
		ID:          "preset-crash",
		Name:        "💥 Crash / Nabrak",
		MomentType:  "crash",
		Description: "Metal Pipe + Vine Boom + Oof Roblox tepat di momen nabrak!",
		BGMVolume:   0.14,
		BGMFadeIn:   0.3,
		BGMFadeOut:  0.8,
		BGMAutoDuck: true,
		SFXCues: []model.SFXCue{
			{AssetID: "sfx-metal-pipe", AtSec: 0.0, Volume: 1.05},
			{AssetID: "sfx-boom", AtSec: 0.15, Volume: 1.0},
			{AssetID: "sfx-oof-roblox", AtSec: 0.45, Volume: 0.95},
		},
		BuiltIn: true,
	},
	{
		ID:          "preset-fail",
		Name:        "💀 Fail / Wasted",
		MomentType:  "fail",
		Description: "Record Scratch + Sad Trombone + Bruh + Sad Piano BGM",
		BGMAssetID:  "bgm-fail-sad",
		BGMVolume:   0.15,
		BGMFadeIn:   0.4,
		BGMFadeOut:  0.9,
		BGMAutoDuck: true,
		SFXCues: []model.SFXCue{
			{AssetID: "sfx-record-scratch", AtSec: 0.0, Volume: 1.0},
			{AssetID: "sfx-sad-trombone", AtSec: 0.45, Volume: 0.95},
			{AssetID: "sfx-bruh", AtSec: 1.8, Volume: 0.95},
		},
		BuiltIn: true,
	},
	{
		ID:          "preset-hype",
		Name:        "🔥 Hype / Clutch",
		MomentType:  "hype",
		Description: "Tension Riser (-1.4s) → Bass Drop + Airhorn + Phonk BGM",
		BGMAssetID:  "bgm-hype-phonk",
		BGMVolume:   0.16,
		BGMFadeIn:   0.3,
		BGMFadeOut:  0.8,
		BGMAutoDuck: true,
		SFXCues: []model.SFXCue{
			{AssetID: "sfx-riser", AtSec: -1.4, Volume: 0.85},
			{AssetID: "sfx-bassdrop", AtSec: 0.0, Volume: 1.1},
			{AssetID: "sfx-airhorn", AtSec: 0.2, Volume: 0.8},
		},
		BuiltIn: true,
	},
	{
		ID:          "preset-kaget",
		Name:        "😱 Kaget / Plot Twist",
		MomentType:  "kaget",
		Description: "Dramatic Dun-Dun-DUUN + Alert Sting + Vine Boom",
		BGMVolume:   0.14,
		BGMFadeIn:   0.3,
		BGMFadeOut:  0.8,
		BGMAutoDuck: true,
		SFXCues: []model.SFXCue{
			{AssetID: "sfx-alert", AtSec: 0.0, Volume: 1.0},
			{AssetID: "sfx-dun-dun-dun", AtSec: 0.25, Volume: 1.0},
			{AssetID: "sfx-boom", AtSec: 1.15, Volume: 1.0},
		},
		BuiltIn: true,
	},
	{
		ID:          "preset-funny",
		Name:        "😂 Kocak / Meme Ngakak",
		MomentType:  "funny",
		Description: "Cartoon Bonk + Boing + Pop + Funny Bouncy BGM",
		BGMAssetID:  "bgm-funny-bouncy",
		BGMVolume:   0.14,
		BGMFadeIn:   0.3,
		BGMFadeOut:  0.8,
		BGMAutoDuck: true,
		SFXCues: []model.SFXCue{
			{AssetID: "sfx-bonk", AtSec: 0.0, Volume: 1.05},
			{AssetID: "sfx-boing", AtSec: 0.35, Volume: 0.9},
			{AssetID: "sfx-pop", AtSec: 1.1, Volume: 0.85},
		},
		BuiltIn: true,
	},
	{
		ID:          "preset-awkward",
		Name:        "🦗 Garing / Canggung",
		MomentType:  "awkward",
		Description: "Record Scratch + Jangkrik Krik-Krik + Windows Error",
		BGMVolume:   0.12,
		BGMFadeIn:   0.3,
		BGMFadeOut:  0.8,
		BGMAutoDuck: true,
		SFXCues: []model.SFXCue{
			{AssetID: "sfx-record-scratch", AtSec: 0.0, Volume: 0.95},
			{AssetID: "sfx-crickets", AtSec: 0.4, Volume: 0.95},
			{AssetID: "sfx-win-error", AtSec: 1.9, Volume: 0.9},
		},
		BuiltIn: true,
	},
}

func NewLibrary(dataDir string, d *deps.Manager) *Library {
	audioDir := filepath.Join(dataDir, "audio")
	_ = os.MkdirAll(audioDir, 0o755)
	lib := &Library{
		AudioDir:    audioDir,
		MetaPath:    filepath.Join(dataDir, "audio_library.json"),
		PresetsPath: filepath.Join(dataDir, "audio_presets.json"),
		Deps:        d,
		assets:      make(map[string]*model.AudioAsset),
		presets:     make(map[string]*model.AudioPreset),
	}
	lib.load()
	lib.loadPresets()
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

func (l *Library) loadPresets() {
	for _, p := range builtInPresets {
		cp := p
		l.presets[cp.ID] = &cp
	}
	if b, err := os.ReadFile(l.PresetsPath); err == nil {
		var list []*model.AudioPreset
		if json.Unmarshal(b, &list) == nil {
			for _, p := range list {
				if p.ID != "" && !p.BuiltIn {
					l.presets[p.ID] = p
				}
			}
		}
	}
}

func (l *Library) savePresetsLocked() {
	var custom []*model.AudioPreset
	for _, p := range l.presets {
		if !p.BuiltIn {
			custom = append(custom, p)
		}
	}
	sort.Slice(custom, func(i, j int) bool { return custom[i].Name < custom[j].Name })
	if b, err := json.MarshalIndent(custom, "", "  "); err == nil {
		tmp := l.PresetsPath + ".tmp"
		if os.WriteFile(tmp, b, 0o644) == nil {
			_ = os.Rename(tmp, l.PresetsPath)
		}
	}
}

func (l *Library) ListPresets() []*model.AudioPreset {
	l.mu.RLock()
	defer l.mu.RUnlock()
	list := make([]*model.AudioPreset, 0, len(l.presets))
	for _, p := range l.presets {
		cp := *p
		cp.SFXCues = append([]model.SFXCue{}, p.SFXCues...)
		list = append(list, &cp)
	}
	sort.Slice(list, func(i, j int) bool {
		if list[i].BuiltIn != list[j].BuiltIn {
			return !list[i].BuiltIn // User custom presets first!
		}
		return list[i].Name < list[j].Name
	})
	return list
}

func (l *Library) SavePreset(p model.AudioPreset) (*model.AudioPreset, error) {
	if strings.TrimSpace(p.Name) == "" {
		return nil, fmt.Errorf("nama preset wajib diisi")
	}
	if p.ID == "" {
		p.ID = fmt.Sprintf("preset-custom-%d", time.Now().UnixNano())
	}
	p.BuiltIn = false
	if p.MomentType == "" {
		p.MomentType = "custom"
	}
	l.mu.Lock()
	cp := p
	l.presets[cp.ID] = &cp
	l.savePresetsLocked()
	l.mu.Unlock()
	return &cp, nil
}

func (l *Library) DeletePreset(id string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	p, ok := l.presets[id]
	if !ok {
		return nil
	}
	if p.BuiltIn {
		return fmt.Errorf("preset bawaan tidak bisa dihapus")
	}
	delete(l.presets, id)
	l.savePresetsLocked()
	return nil
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
		tmp := l.MetaPath + ".tmp"
		if os.WriteFile(tmp, b, 0o644) == nil {
			_ = os.Rename(tmp, l.MetaPath)
		}
	}
}

// EnsureBuiltIns synthesizes any missing royalty-free SFX/BGM files using FFmpeg (bounded concurrency).
func (l *Library) EnsureBuiltIns() {
	ff := l.Deps.GetStatus().FFmpegPath
	if ff == "" {
		return
	}

	sem := make(chan struct{}, 4) // Max 4 parallel FFmpeg synths to stay light on CPU
	var wg sync.WaitGroup
	changed := false
	var mu sync.Mutex

	for _, spec := range builtInPack {
		dest := filepath.Join(l.AudioDir, spec.FileName)
		if fileExists(dest) {
			l.mu.Lock()
			if existing, ok := l.assets[spec.ID]; !ok || existing.Category != spec.Category || existing.Name != spec.Name {
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
		sem <- struct{}{}
		go func(sp synthSpec, outPath string) {
			defer wg.Done()
			defer func() { <-sem }()
			ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
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
		if list[i].Category != list[j].Category {
			return list[i].Category < list[j].Category
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
