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

// Library manages built-in royalty-free CapCut-style SFX (36+) & BGM (15+) packs plus user-uploaded custom audio files.
type Library struct {
	mu               sync.RWMutex
	AudioDir         string
	MetaPath         string
	Deps             *deps.Manager
	assets           map[string]*model.AudioAsset
	momentMap        map[string]MomentEditRule
	momentMapModTime time.Time
	radarURLs        map[string]time.Time
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

// Built-in 100% DMCA-free synthesized CapCut-style starter pack:
// 36 Viral Clipper SFX (Meme, Impact, Transition, Fail, Alert, Crowd) + 15 Mood BGM loops (Funny, Hype, Chill, Suspense, Victory, Fail).
var builtInPack = []synthSpec{
	// ================= 1. MEME & VIRAL SFX (12) =================
	{
		ID:       "sfx-boom",
		Name:     "Deep Bass Boom (Vine Impact)",
		Kind:     "sfx",
		Category: "Meme",
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
		ID:       "sfx-record-scratch",
		Name:     "Record Scratch / Freeze Stop",
		Kind:     "sfx",
		Category: "Meme",
		FileName: "builtin_sfx_record_scratch.wav",
		Duration: 0.65,
		Args: []string{
			"-f", "lavfi", "-i",
			"aevalsrc=exprs='0.75*sin(2*PI*(1100*exp(-5.5*t)+120*sin(2*PI*18*t))*t)*exp(-2.2*t)':s=48000:d=0.65",
			"-af", "bandpass=f=900:w=700,afade=t=out:st=0.48:d=0.17",
		},
	},
	{
		ID:       "sfx-roblox-oof",
		Name:     "Oof / Ahh! Retro Hurt (Meme)",
		Kind:     "sfx",
		Category: "Meme",
		FileName: "builtin_sfx_roblox_oof.wav",
		Duration: 0.52,
		Args: []string{
			"-f", "lavfi", "-i",
			"aevalsrc=exprs='(0.65*sin(2*PI*(245*exp(-2.8*t)+115)*t)+0.38*sin(2*PI*(520*exp(-3.1*t)+220)*t)+0.22*sin(2*PI*(980*exp(-4*t)+340)*t))*exp(-5.2*t)':s=48000:d=0.52",
			"-af", "bandpass=f=480:w=520,bass=g=5:f=180,afade=t=in:st=0:d=0.015,afade=t=out:st=0.35:d=0.17",
		},
	},
	{
		ID:       "sfx-bruh",
		Name:     "Bruh Low Pitch Drop",
		Kind:     "sfx",
		Category: "Meme",
		FileName: "builtin_sfx_bruh.wav",
		Duration: 0.72,
		Args: []string{
			"-f", "lavfi", "-i",
			"aevalsrc=exprs='(0.70*sin(2*PI*(155*exp(-1.9*t)+78)*t)+0.35*sin(2*PI*(310*exp(-2.0*t)+150)*t)+0.18*sin(2*PI*(620*exp(-2.5*t)+260)*t))*exp(-2.9*t)':s=48000:d=0.72",
			"-af", "lowpass=f=900,bass=g=6:f=120,afade=t=out:st=0.5:d=0.22",
		},
	},
	{
		ID:       "sfx-bonk",
		Name:     "Wooden Bonk Meme",
		Kind:     "sfx",
		Category: "Meme",
		FileName: "builtin_sfx_bonk.wav",
		Duration: 0.38,
		Args: []string{
			"-f", "lavfi", "-i",
			"aevalsrc=exprs='(0.8*sin(2*PI*(420*exp(-12*t)+185)*t)+0.4*sin(2*PI*760*t)*exp(-22*t))*exp(-8.5*t)':s=48000:d=0.38",
			"-af", "bandpass=f=520:w=600,afade=t=out:st=0.25:d=0.13",
		},
	},
	{
		ID:       "sfx-boing",
		Name:     "Cartoon Spring Boing",
		Kind:     "sfx",
		Category: "Meme",
		FileName: "builtin_sfx_boing.wav",
		Duration: 0.75,
		Args: []string{
			"-f", "lavfi", "-i",
			"aevalsrc=exprs='0.72*sin(2*PI*(190+340*t+75*sin(2*PI*16*t))*t)*exp(-2.8*t)':s=48000:d=0.75",
			"-af", "afade=t=out:st=0.55:d=0.2",
		},
	},
	{
		ID:       "sfx-fart-reverb",
		Name:     "Bass Reverb Bruh/Fart Meme",
		Kind:     "sfx",
		Category: "Meme",
		FileName: "builtin_sfx_fart_reverb.wav",
		Duration: 1.15,
		Args: []string{
			"-f", "lavfi", "-i",
			"aevalsrc=exprs='0.82*sin(2*PI*(62+18*sin(2*PI*24*t))*t+2.2*sin(2*PI*95*t))*exp(-3.0*t)':s=48000:d=1.15",
			"-af", "lowpass=f=380,aecho=0.8:0.85:55|110:0.45|0.3,bass=g=7:f=80,afade=t=out:st=0.8:d=0.35",
		},
	},
	{
		ID:       "sfx-honk",
		Name:     "Clown Horn Honk (Badut)",
		Kind:     "sfx",
		Category: "Meme",
		FileName: "builtin_sfx_honk.wav",
		Duration: 0.55,
		Args: []string{
			"-f", "lavfi", "-i",
			"aevalsrc=exprs='(0.45*sin(2*PI*392*t)+0.42*sin(2*PI*784*t)+0.35*sin(2*PI*1176*t)+0.25*sin(2*PI*1568*t))*(lt(t,0.22)+gte(t,0.28)*lt(t,0.52))':s=48000:d=0.55",
			"-af", "bandpass=f=880:w=900,afade=t=out:st=0.45:d=0.1",
		},
	},
	{
		ID:       "sfx-quack",
		Name:     "Cartoon Quack Pop",
		Kind:     "sfx",
		Category: "Meme",
		FileName: "builtin_sfx_quack.wav",
		Duration: 0.32,
		Args: []string{
			"-f", "lavfi", "-i",
			"aevalsrc=exprs='(0.55*sin(2*PI*(540-260*t)*t)+0.45*sin(2*PI*(1080-520*t)*t)+0.3*sin(2*PI*1620*t))*exp(-7.5*t)':s=48000:d=0.32",
			"-af", "bandpass=f=950:w=700,afade=t=out:st=0.2:d=0.12",
		},
	},
	{
		ID:       "sfx-slide-up",
		Name:     "Slide Whistle Naik (Lucu)",
		Kind:     "sfx",
		Category: "Meme",
		FileName: "builtin_sfx_slide_up.wav",
		Duration: 0.55,
		Args: []string{
			"-f", "lavfi", "-i",
			"aevalsrc=exprs='0.65*sin(2*PI*(420+1600*t*t)*t)*exp(-1.2*t)':s=48000:d=0.55",
			"-af", "afade=t=in:st=0:d=0.04,afade=t=out:st=0.42:d=0.13",
		},
	},
	{
		ID:       "sfx-slide-down",
		Name:     "Slide Whistle Turun (Jatuh)",
		Kind:     "sfx",
		Category: "Meme",
		FileName: "builtin_sfx_slide_down.wav",
		Duration: 0.65,
		Args: []string{
			"-f", "lavfi", "-i",
			"aevalsrc=exprs='0.65*sin(2*PI*(1550*exp(-2.8*t)+220)*t)*exp(-1.5*t)':s=48000:d=0.65",
			"-af", "afade=t=in:st=0:d=0.03,afade=t=out:st=0.5:d=0.15",
		},
	},

	// ================= 2. IMPACT & HYPE SFX (7) =================
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
		ID:       "sfx-riser",
		Name:     "Hype Tension Riser",
		Kind:     "sfx",
		Category: "Impact",
		FileName: "builtin_sfx_riser.wav",
		Duration: 1.8,
		Args: []string{
			"-f", "lavfi", "-i",
			"aevalsrc=exprs='0.55*sin(2*PI*(180+620*t*t)*t)*(t/1.8)':s=48000:d=1.8",
			"-af", "highpass=f=140,afade=t=in:st=0:d=0.4,afade=t=out:st=1.65:d=0.15",
		},
	},
	{
		ID:       "sfx-cinematic-braam",
		Name:     "Cinematic Braam Horn (Epic)",
		Kind:     "sfx",
		Category: "Impact",
		FileName: "builtin_sfx_braam.wav",
		Duration: 1.9,
		Args: []string{
			"-f", "lavfi", "-i",
			"aevalsrc=exprs='(0.45*sin(2*PI*55*t)+0.35*sin(2*PI*110*t)+0.28*sin(2*PI*165*t)+0.22*sin(2*PI*220*t)+0.16*sin(2*PI*277*t))*exp(-1.2*t)':s=48000:d=1.9",
			"-af", "lowpass=f=750,bass=g=7:f=75,aecho=0.8:0.75:90:0.4,afade=t=in:st=0:d=0.06,afade=t=out:st=1.4:d=0.5",
		},
	},
	{
		ID:       "sfx-punch-hit",
		Name:     "Anime Punch Impact Hit",
		Kind:     "sfx",
		Category: "Impact",
		FileName: "builtin_sfx_punch.wav",
		Duration: 0.42,
		Args: []string{
			"-f", "lavfi", "-i",
			"aevalsrc=exprs='0.95*sin(2*PI*(240*exp(-18*t)+42)*t)*exp(-7.5*t)+0.35*(random(0)-0.5)*exp(-22*t)':s=48000:d=0.42",
			"-af", "lowpass=f=1200,bass=g=9:f=70,alimiter=limit=0.95",
		},
	},
	{
		ID:       "sfx-whip-crack",
		Name:     "Whip Crack Snap",
		Kind:     "sfx",
		Category: "Impact",
		FileName: "builtin_sfx_whip.wav",
		Duration: 0.45,
		Args: []string{
			"-f", "lavfi", "-i",
			"aevalsrc=exprs='0.35*sin(2*PI*(300+1800*t)*t)*lt(t,0.18)+0.95*(random(0)-0.5)*gte(t,0.18)*exp(-28*(t-0.18))':s=48000:d=0.45",
			"-af", "highpass=f=500,alimiter=limit=0.95",
		},
	},
	{
		ID:       "sfx-explosion-thud",
		Name:     "Explosion Sub Thud (Meledak!)",
		Kind:     "sfx",
		Category: "Impact",
		FileName: "builtin_sfx_explosion.wav",
		Duration: 1.5,
		Args: []string{
			"-f", "lavfi", "-i",
			"aevalsrc=exprs='(0.85*(random(0)-0.5)+0.75*sin(2*PI*(70*exp(-3*t)+28)*t))*exp(-2.3*t)':s=48000:d=1.5",
			"-af", "lowpass=f=420,bass=g=9:f=60,afade=t=out:st=1.0:d=0.5",
		},
	},
	{
		ID:       "sfx-powerup",
		Name:     "8-Bit Power-Up Level Up",
		Kind:     "sfx",
		Category: "Impact",
		FileName: "builtin_sfx_powerup.wav",
		Duration: 0.8,
		Args: []string{
			"-f", "lavfi", "-i",
			"aevalsrc=exprs='0.6*sin(2*PI*(523.25*lt(t,0.12)+659.25*gte(t,0.12)*lt(t,0.24)+783.99*gte(t,0.24)*lt(t,0.36)+1046.5*gte(t,0.36))*t)*exp(-2.8*max(0,t-0.36))':s=48000:d=0.8",
			"-af", "afade=t=out:st=0.62:d=0.18",
		},
	},

	// ================= 3. TRANSITION SFX (5) =================
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
		ID:       "sfx-fast-swoosh",
		Name:     "Fast Anime Swoosh",
		Kind:     "sfx",
		Category: "Transition",
		FileName: "builtin_sfx_fast_swoosh.wav",
		Duration: 0.38,
		Args: []string{
			"-f", "lavfi", "-i", "anoisesrc=c=white:r=48000:d=0.38:a=0.75",
			"-af", "bandpass=f=1600:w=1100,afade=t=in:st=0:d=0.09,afade=t=out:st=0.12:d=0.26",
		},
	},
	{
		ID:       "sfx-camera-shutter",
		Name:     "Camera Shutter Freeze Snap",
		Kind:     "sfx",
		Category: "Transition",
		FileName: "builtin_sfx_shutter.wav",
		Duration: 0.25,
		Args: []string{
			"-f", "lavfi", "-i",
			"aevalsrc=exprs='0.9*(random(0)-0.5)*(exp(-55*t)+gte(t,0.08)*exp(-65*(t-0.08)))':s=48000:d=0.25",
			"-af", "highpass=f=1800,lowpass=f=7500",
		},
	},
	{
		ID:       "sfx-tape-rewind",
		Name:     "Tape Rewind Zip Back",
		Kind:     "sfx",
		Category: "Transition",
		FileName: "builtin_sfx_rewind.wav",
		Duration: 0.55,
		Args: []string{
			"-f", "lavfi", "-i",
			"aevalsrc=exprs='0.6*sin(2*PI*(450+2200*t+200*sin(2*PI*38*t))*t)*exp(-1.5*t)':s=48000:d=0.55",
			"-af", "bandpass=f=1500:w=1200,afade=t=out:st=0.42:d=0.13",
		},
	},

	// ================= 4. FAIL & AWKWARD SFX (4) =================
	{
		ID:       "sfx-sad-trombone",
		Name:     "Sad Trombone Wah-Wah-Waaah",
		Kind:     "sfx",
		Category: "Fail",
		FileName: "builtin_sfx_sad_trombone.wav",
		Duration: 1.85,
		Args: []string{
			"-f", "lavfi", "-i",
			"aevalsrc=exprs='(0.45*sin(2*PI*(293.66*lt(t,0.36)+277.18*gte(t,0.38)*lt(t,0.74)+261.63*gte(t,0.76)*lt(t,1.12)+(246.94-18*(t-1.14)+4*sin(2*PI*6*t))*gte(t,1.14))*t)+0.22*sin(4*PI*(293.66*lt(t,0.36)+277.18*gte(t,0.38)*lt(t,0.74)+261.63*gte(t,0.76)*lt(t,1.12)+246.94*gte(t,1.14))*t))':s=48000:d=1.85",
			"-af", "lowpass=f=1100,afade=t=out:st=1.45:d=0.4",
		},
	},
	{
		ID:       "sfx-error-buzzer",
		Name:     "Wrong Answer Buzzer (Tetot!)",
		Kind:     "sfx",
		Category: "Fail",
		FileName: "builtin_sfx_buzzer.wav",
		Duration: 0.65,
		Args: []string{
			"-f", "lavfi", "-i",
			"aevalsrc=exprs='(0.42*sin(2*PI*140*t)+0.35*sin(2*PI*148*t)+0.28*sin(2*PI*285*t)+0.22*sin(2*PI*425*t))*exp(-1.6*t)':s=48000:d=0.65",
			"-af", "acrusher=bits=5:mode=lin:aa=1,lowpass=f=1400,afade=t=out:st=0.5:d=0.15",
		},
	},
	{
		ID:       "sfx-win-error",
		Name:     "PC Error Chord (Crash!)",
		Kind:     "sfx",
		Category: "Fail",
		FileName: "builtin_sfx_win_error.wav",
		Duration: 0.75,
		Args: []string{
			"-f", "lavfi", "-i",
			"aevalsrc=exprs='(0.45*sin(2*PI*440*t)+0.4*sin(2*PI*554.37*t)+0.38*sin(2*PI*659.25*t)+0.3*sin(2*PI*880*t))*exp(-5.0*t)':s=48000:d=0.75",
			"-af", "afade=t=out:st=0.55:d=0.2",
		},
	},
	{
		ID:       "sfx-cricket",
		Name:     "Jangkrik Krik-Krik (Garing)",
		Kind:     "sfx",
		Category: "Fail",
		FileName: "builtin_sfx_cricket.wav",
		Duration: 1.6,
		Args: []string{
			"-f", "lavfi", "-i",
			"aevalsrc=exprs='0.45*sin(2*PI*4250*t)*(0.5+0.5*sin(2*PI*28*t))*(lt(mod(t,0.75),0.30))':s=48000:d=1.6",
			"-af", "bandpass=f=4250:w=900,afade=t=out:st=1.35:d=0.25",
		},
	},

	// ================= 5. ALERT & POP SFX (5) =================
	{
		ID:       "sfx-alert",
		Name:     "Kaget / Dramatic Sting",
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
		ID:       "sfx-dun-dun-dun",
		Name:     "Dramatic Dun-Dun-DUUN!",
		Kind:     "sfx",
		Category: "Alert",
		FileName: "builtin_sfx_dundundun.wav",
		Duration: 1.75,
		Args: []string{
			"-f", "lavfi", "-i",
			"aevalsrc=exprs='(0.55*sin(2*PI*130.81*t)+0.35*sin(2*PI*261.63*t))*lt(t,0.28)*exp(-4*t)+(0.55*sin(2*PI*123.47*t)+0.35*sin(2*PI*246.94*t))*gte(t,0.34)*lt(t,0.62)*exp(-4*(t-0.34))+(0.75*sin(2*PI*98*t)+0.45*sin(2*PI*196*t)+0.3*sin(2*PI*146.83*t))*gte(t,0.70)*exp(-1.9*(t-0.70))':s=48000:d=1.75",
			"-af", "lowpass=f=1100,bass=g=6:f=95,aecho=0.75:0.7:65:0.35,afade=t=out:st=1.4:d=0.35",
		},
	},
	{
		ID:       "sfx-ding",
		Name:     "Pop Ding Highlight",
		Kind:     "sfx",
		Category: "Alert",
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
		Category: "Alert",
		FileName: "builtin_sfx_coin.wav",
		Duration: 0.75,
		Args: []string{
			"-f", "lavfi", "-i",
			"aevalsrc=exprs='(lt(t,0.12)*sin(2*PI*987.77*t)+gte(t,0.12)*sin(2*PI*1318.51*t))*exp(-4.5*max(0,t-0.12))':s=48000:d=0.75",
			"-af", "afade=t=out:st=0.55:d=0.2",
		},
	},
	{
		ID:       "sfx-heartbeat",
		Name:     "Deg-Degan Heartbeat (Tegang)",
		Kind:     "sfx",
		Category: "Alert",
		FileName: "builtin_sfx_heartbeat.wav",
		Duration: 1.4,
		Args: []string{
			"-f", "lavfi", "-i",
			"aevalsrc=exprs='0.9*sin(2*PI*58*t)*(exp(-18*mod(t,0.7))+0.7*gte(mod(t,0.7),0.18)*exp(-22*(mod(t,0.7)-0.18)))':s=48000:d=1.4",
			"-af", "lowpass=f=180,bass=g=8:f=55,afade=t=out:st=1.2:d=0.2",
		},
	},

	// ================= 6. CROWD & CELEBRATION SFX (3) =================
	{
		ID:       "sfx-airhorn",
		Name:     "MLG Airhorn Hype!",
		Kind:     "sfx",
		Category: "Crowd",
		FileName: "builtin_sfx_airhorn.wav",
		Duration: 1.25,
		Args: []string{
			"-f", "lavfi", "-i",
			"aevalsrc=exprs='(0.38*sin(2*PI*466.16*t)+0.34*sin(2*PI*470.5*t)+0.32*sin(2*PI*932.32*t)+0.25*sin(2*PI*1398*t)+0.18*sin(2*PI*1864*t))*(lt(t,0.16)+gte(t,0.20)*lt(t,0.36)+gte(t,0.40)*exp(-1.1*(t-0.40)))':s=48000:d=1.25",
			"-af", "bandpass=f=1100:w=1600,aecho=0.7:0.6:50:0.3,afade=t=out:st=1.0:d=0.25",
		},
	},
	{
		ID:       "sfx-wow-sparkle",
		Name:     "Sparkle Magic Wow Chime",
		Kind:     "sfx",
		Category: "Crowd",
		FileName: "builtin_sfx_sparkle.wav",
		Duration: 1.0,
		Args: []string{
			"-f", "lavfi", "-i",
			"aevalsrc=exprs='(0.4*sin(2*PI*(1046.5+523*floor(t*12))*t)+0.25*sin(2*PI*(2093+350*floor(t*12))*t))*exp(-2.6*t)':s=48000:d=1.0",
			"-af", "aecho=0.75:0.7:55:0.4,afade=t=out:st=0.75:d=0.25",
		},
	},
	{
		ID:       "sfx-applause-burst",
		Name:     "Crowd Cheer & Claps Burst",
		Kind:     "sfx",
		Category: "Crowd",
		FileName: "builtin_sfx_applause.wav",
		Duration: 1.5,
		Args: []string{
			"-f", "lavfi", "-i", "anoisesrc=c=pink:r=48000:d=1.5:a=0.65",
			"-af", "bandpass=f=1350:w=1500,tremolo=f=14:d=0.45,afade=t=in:st=0:d=0.18,afade=t=out:st=0.95:d=0.55",
		},
	},

	// ================= DMCA-FREE MOOD BGM LOOPS (15) =================
	// --- Hype / Gaming ---
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
		ID:       "bgm-phonk-drift",
		Name:     "Phonk Cowbell Drift (130 BPM)",
		Kind:     "bgm",
		Category: "Hype",
		FileName: "builtin_bgm_phonk_drift.mp3",
		Duration: 16.0,
		Args: []string{
			"-f", "lavfi", "-i",
			"aevalsrc=exprs='0.40*sin(2*PI*(55*exp(-12*mod(t,0.4615))+42)*t)*exp(-5*mod(t,0.4615))+0.18*(sin(2*PI*560*t)+sin(2*PI*845*t))*exp(-14*mod(t,0.2307))*gte(mod(t,0.923),0.23)':s=48000:d=16",
			"-af", "bass=g=6:f=60,lowpass=f=2400,afade=t=in:st=0:d=0.25,afade=t=out:st=15.5:d=0.5",
			"-c:a", "libmp3lame", "-b:a", "160k",
		},
	},
	{
		ID:       "bgm-boss-battle",
		Name:     "Epic Boss Chase (140 BPM)",
		Kind:     "bgm",
		Category: "Hype",
		FileName: "builtin_bgm_boss_battle.mp3",
		Duration: 16.0,
		Args: []string{
			"-f", "lavfi", "-i",
			"aevalsrc=exprs='0.30*sin(2*PI*(73.42*(lt(mod(t,4),2))+65.41*(gte(mod(t,4),2)))*t)*(0.35+0.65*exp(-10*mod(t,0.214)))+0.15*sin(2*PI*(293.66+73.42*floor(mod(t*4.66,4)))*t)*exp(-7*mod(t,0.214))':s=48000:d=16",
			"-af", "lowpass=f=2600,bass=g=5:f=70,afade=t=in:st=0:d=0.25,afade=t=out:st=15.5:d=0.5",
			"-c:a", "libmp3lame", "-b:a", "160k",
		},
	},
	{
		ID:       "bgm-arcade-rush",
		Name:     "Arcade 8-Bit Rush (Fast)",
		Kind:     "bgm",
		Category: "Hype",
		FileName: "builtin_bgm_arcade_rush.mp3",
		Duration: 16.0,
		Args: []string{
			"-f", "lavfi", "-i",
			"aevalsrc=exprs='0.22*sin(2*PI*(261.63+65.4*floor(mod(t*6,5)))*t)*exp(-6*mod(t,0.166))+0.25*sin(2*PI*(130.81*(lt(mod(t,4),2))+98*(gte(mod(t,4),2)))*t)*exp(-6*mod(t,0.333))':s=48000:d=16",
			"-af", "acrusher=bits=7:mode=lin:aa=1,lowpass=f=2800,afade=t=in:st=0:d=0.2,afade=t=out:st=15.5:d=0.5",
			"-c:a", "libmp3lame", "-b:a", "160k",
		},
	},
	// --- Funny / Meme ---
	{
		ID:       "bgm-funny-bouncy",
		Name:     "Funny Bouncy Comedy Pluck",
		Kind:     "bgm",
		Category: "Funny",
		FileName: "builtin_bgm_funny_bouncy.mp3",
		Duration: 16.0,
		Args: []string{
			"-f", "lavfi", "-i",
			"aevalsrc=exprs='0.28*sin(2*PI*(196*lt(mod(t,1),0.25)+246.94*gte(mod(t,1),0.25)*lt(mod(t,1),0.5)+293.66*gte(mod(t,1),0.5)*lt(mod(t,1),0.75)+392*gte(mod(t,1),0.75))*t)*exp(-10*mod(t,0.25))+0.18*sin(2*PI*130.81*t)*exp(-9*mod(t,0.5))':s=48000:d=16",
			"-af", "lowpass=f=1900,afade=t=in:st=0:d=0.25,afade=t=out:st=15.5:d=0.5",
			"-c:a", "libmp3lame", "-b:a", "160k",
		},
	},
	{
		ID:       "bgm-sneaky-troll",
		Name:     "Sneaky Troll Pizzicato (Ngeselin)",
		Kind:     "bgm",
		Category: "Funny",
		FileName: "builtin_bgm_sneaky_troll.mp3",
		Duration: 16.0,
		Args: []string{
			"-f", "lavfi", "-i",
			"aevalsrc=exprs='0.32*sin(2*PI*(220*lt(mod(t,2),0.5)+233.08*gte(mod(t,2),0.5)*lt(mod(t,2),1.0)+261.63*gte(mod(t,2),1.0)*lt(mod(t,2),1.5)+207.65*gte(mod(t,2),1.5))*t)*exp(-12*mod(t,0.5))':s=48000:d=16",
			"-af", "bandpass=f=620:w=900,aecho=0.6:0.5:110:0.2,afade=t=in:st=0:d=0.25,afade=t=out:st=15.5:d=0.5",
			"-c:a", "libmp3lame", "-b:a", "160k",
		},
	},
	{
		ID:       "bgm-circus-chaos",
		Name:     "Chaotic Panic Chase (Kacau)",
		Kind:     "bgm",
		Category: "Funny",
		FileName: "builtin_bgm_circus_chaos.mp3",
		Duration: 16.0,
		Args: []string{
			"-f", "lavfi", "-i",
			"aevalsrc=exprs='0.26*sin(2*PI*(329.63+55*mod(floor(t*8),4))*t)*exp(-8*mod(t,0.125))+0.24*sin(2*PI*(110+36*mod(floor(t*4),2))*t)*exp(-7*mod(t,0.25))':s=48000:d=16",
			"-af", "lowpass=f=2100,afade=t=in:st=0:d=0.2,afade=t=out:st=15.5:d=0.5",
			"-c:a", "libmp3lame", "-b:a", "160k",
		},
	},
	// --- Chill / Story / Retro ---
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
		ID:       "bgm-sunset-vice",
		Name:     "Vice City Synthwave Retro (GTA VI!)",
		Kind:     "bgm",
		Category: "Chill",
		FileName: "builtin_bgm_sunset_vice.mp3",
		Duration: 16.0,
		Args: []string{
			"-f", "lavfi", "-i",
			"aevalsrc=exprs='0.26*sin(2*PI*(82.41*lt(mod(t,8),4)+98*gte(mod(t,8),4))*t)*(0.5+0.5*exp(-7*mod(t,0.25)))+(0.12*sin(2*PI*329.63*t)+0.10*sin(2*PI*392*t)+0.10*sin(2*PI*493.88*t))*(0.7+0.3*sin(2*PI*0.35*t))':s=48000:d=16",
			"-af", "lowpass=f=1800,aecho=0.7:0.6:180:0.3,afade=t=in:st=0:d=0.4,afade=t=out:st=15.4:d=0.6",
			"-c:a", "libmp3lame", "-b:a", "160k",
		},
	},
	{
		ID:       "bgm-acoustic-chill",
		Name:     "Soft Warm Pluck (Santai)",
		Kind:     "bgm",
		Category: "Chill",
		FileName: "builtin_bgm_acoustic_chill.mp3",
		Duration: 16.0,
		Args: []string{
			"-f", "lavfi", "-i",
			"aevalsrc=exprs='(0.20*sin(2*PI*196*t)+0.16*sin(2*PI*293.66*t)+0.14*sin(2*PI*392*t))*exp(-3.2*mod(t,1.0))+0.14*sin(2*PI*493.88*t)*exp(-4.5*mod(t+0.5,1.0))':s=48000:d=16",
			"-af", "lowpass=f=1600,aecho=0.7:0.5:160:0.25,afade=t=in:st=0:d=0.4,afade=t=out:st=15.4:d=0.6",
			"-c:a", "libmp3lame", "-b:a", "160k",
		},
	},
	// --- Suspense / Horror / Heist ---
	{
		ID:       "bgm-suspense",
		Name:     "Suspense Tension Drone (Horror/GTA)",
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
		ID:       "bgm-horror-pulse",
		Name:     "Creepy Horror Heartbeat Sub",
		Kind:     "bgm",
		Category: "Suspense",
		FileName: "builtin_bgm_horror_pulse.mp3",
		Duration: 16.0,
		Args: []string{
			"-f", "lavfi", "-i",
			"aevalsrc=exprs='0.38*sin(2*PI*46*t)*exp(-12*mod(t,0.85))+0.14*sin(2*PI*(660+12*sin(2*PI*0.2*t))*t)*(0.5+0.5*sin(2*PI*0.18*t))':s=48000:d=16",
			"-af", "lowpass=f=950,aecho=0.8:0.75:220:0.4,afade=t=in:st=0:d=0.5,afade=t=out:st=15.3:d=0.7",
			"-c:a", "libmp3lame", "-b:a", "160k",
		},
	},
	{
		ID:       "bgm-heist-stealth",
		Name:     "GTA Heist Stealth Bass",
		Kind:     "bgm",
		Category: "Suspense",
		FileName: "builtin_bgm_heist_stealth.mp3",
		Duration: 16.0,
		Args: []string{
			"-f", "lavfi", "-i",
			"aevalsrc=exprs='0.34*sin(2*PI*(65.41*lt(mod(t,4),3)+61.74*gte(mod(t,4),3))*t)*exp(-7*mod(t,0.5))+0.10*sin(2*PI*523.25*t)*exp(-18*mod(t,0.25))':s=48000:d=16",
			"-af", "lowpass=f=1400,bass=g=5:f=70,afade=t=in:st=0:d=0.3,afade=t=out:st=15.4:d=0.6",
			"-c:a", "libmp3lame", "-b:a", "160k",
		},
	},
	// --- Victory & Fail ---
	{
		ID:       "bgm-victory-fanfare",
		Name:     "Victory Champion Synth (GG!)",
		Kind:     "bgm",
		Category: "Victory",
		FileName: "builtin_bgm_victory.mp3",
		Duration: 16.0,
		Args: []string{
			"-f", "lavfi", "-i",
			"aevalsrc=exprs='(0.22*sin(2*PI*261.63*t)+0.18*sin(2*PI*329.63*t)+0.16*sin(2*PI*392*t)+0.14*sin(2*PI*523.25*t))*(0.55+0.45*exp(-6*mod(t,0.5)))':s=48000:d=16",
			"-af", "lowpass=f=2400,aecho=0.7:0.6:140:0.3,afade=t=in:st=0:d=0.3,afade=t=out:st=15.4:d=0.6",
			"-c:a", "libmp3lame", "-b:a", "160k",
		},
	},
	{
		ID:       "bgm-sad-piano",
		Name:     "Sad Fail Slow Keys (Kasian)",
		Kind:     "bgm",
		Category: "Fail",
		FileName: "builtin_bgm_sad_piano.mp3",
		Duration: 16.0,
		Args: []string{
			"-f", "lavfi", "-i",
			"aevalsrc=exprs='(0.24*sin(2*PI*(220*lt(mod(t,8),4)+174.61*gte(mod(t,8),4))*t)+0.18*sin(2*PI*(261.63*lt(mod(t,8),4)+220*gte(mod(t,8),4))*t)+0.15*sin(2*PI*329.63*t))*exp(-1.2*mod(t,2.0))':s=48000:d=16",
			"-af", "lowpass=f=1100,aecho=0.75:0.7:240:0.35,afade=t=in:st=0:d=0.4,afade=t=out:st=15.2:d=0.8",
			"-c:a", "libmp3lame", "-b:a", "160k",
		},
	},
}

func NewLibrary(dataDir string, d *deps.Manager) *Library {
	audioDir := filepath.Join(dataDir, "audio")
	_ = os.MkdirAll(audioDir, 0o755)
	lib := &Library{
		AudioDir:  audioDir,
		MetaPath:  filepath.Join(dataDir, "audio_library.json"),
		Deps:      d,
		assets:    make(map[string]*model.AudioAsset),
		radarURLs: make(map[string]time.Time),
	}
	lib.load()
	lib.loadRadarURLs()
	// Generate missing built-in royalty-free assets asynchronously with bounded concurrency!
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

// EnsureBuiltIns synthesizes any missing royalty-free SFX/BGM files using FFmpeg (bounded 4-worker pool).
func (l *Library) EnsureBuiltIns() {
	ff := l.Deps.GetStatus().FFmpegPath
	if ff == "" {
		return
	}

	sem := make(chan struct{}, 4)
	var wg sync.WaitGroup
	changed := false
	var mu sync.Mutex

	for _, spec := range builtInPack {
		dest := filepath.Join(l.AudioDir, spec.FileName)
		if fileExists(dest) {
			l.mu.Lock()
			existing, ok := l.assets[spec.ID]
			if !ok || existing.Category != spec.Category || existing.Name != spec.Name {
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

// SetAttribution updates importer attribution & optional probed duration on an existing audio asset and persists metadata.
func (l *Library) SetAttribution(id, byID, byName, via string, duration float64) (*model.AudioAsset, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	a, ok := l.assets[id]
	if !ok {
		return nil, false
	}
	if byID != "" {
		a.ImportedByID = byID
	}
	if byName != "" {
		a.ImportedBy = byName
	}
	if via != "" {
		a.ImportedVia = via
	}
	if duration > 0 {
		a.Duration = duration
	}
	l.saveLocked()
	cp := *a
	return &cp, true
}

func fileExists(p string) bool {
	if p == "" {
		return false
	}
	fi, err := os.Stat(p)
	return err == nil && !fi.IsDir()
}
