---
name: ffmpeg-pro-recipes
description: Vetted offline FFmpeg recipes used inside KlipForge for -14 LUFS mastering, broadcast vocal clarity, BGM sidechain ducking, gaming split-screen layouts, A/V-synced speed ramps, karaoke ASS captions, and hardware encoding.
---

# KlipForge FFmpeg Pro Recipes (Offline Reference)

All rendering in KlipForge executes locally via `ffmpeg`/`ffprobe` without paid cloud APIs. Use these vetted filter patterns when extending `internal/renderer/`:

## 1. Broadcast Audio Mastering (`-14 LUFS` + Vocal Enhance)
- Applied **strictly** to `[voice_raw]` on the main clip (NEVER on Intro or Outro):
```text
highpass=f=80,acompressor=threshold=-18dB:ratio=3:attack=10:release=120,equalizer=f=3000:t=q:w=1.2:g=2.5,loudnorm=I=-14:TP=-1.5:LRA=11
```
- BGM auto-ducking (`autoDuck: true`):
```text
[voice_raw]asplit=2[voice_mix][voice_sc];
[bgm_bed][voice_sc]sidechaincompress=threshold=0.03:ratio=6:attack=120:release=350:makeup=1[bgm_final]
```

## 2. A/V-Synced Speed Ramp (2-Segment Split)
- Split both video and audio at `AtSec` with matching `setpts`/`atempo` before `concat=n=2:v=1:a=1` so captions, SFX, and voice never drift.

## 3. Aspect & Split Framing (`1080x1920`, `fps=30`, `yuv420p`)
- `face_track`: smooth 4 Hz YuNet/Haar horizontal crop (`deadzone=0.035`, `emaAlpha=0.22`, capped at `<=45` keyframes).
- Gaming splits (`facecam_top_neon`, `facecam_top_float`, `gaming_cinema`): combine `GameplayBox` + auto-detected `FacecamBox` (`POST /api/projects/{id}/detect-facecam`).

## 4. Safe Outro Compliance
- Always concatenate Outro **after** color grading, watermark overlay (unless `OnOutro`), subtitle burning, BGM, and SFX mixing so brand outros remain 100% pure.
