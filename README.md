# ⚡ KlipForge — Ultra-Fast Go Auto-Clipper & MotionKlip Studio

Tools auto-clipper YouTube berbasis **Golang + FFmpeg + yt-dlp** lengkap dengan Web Dashboard interaktif, dirancang khusus untuk mempercepat workflow clipper **MotionKlip** (dan campaign kreator lainnya).

---

## 🔍 Konsep ATM (Amati, Tiru, Modifikasi) dari GitHub

KlipForge menggabungkan fitur terbaik dari beberapa project open-source populer di GitHub lalu ditulis ulang dengan arsitektur **Go Concurrent Engine** + tambahan khusus MotionKlip:

1. **`naufaljct48/youtube-heatmap-clipper` & `0xACAB666/yt-heatmap-clipper`**
   - *Ditiru:* Ekstraksi YouTube **Most Replayed Heatmap** tanpa API Key, mode split facecam + gameplay, dan partial download via `yt-dlp`.
   - *Dimodifikasi:* Ditulis di Go dengan **intro bias dampening** (tidak tertipu spike `0:00`), ditambah **Visual Drag-and-Drop Crop Box** pada frame asli video, dan simulasi live output 9:16 + watermark.
2. **`utkualsir/sharkyclips` (ClipForge)**
   - *Ditiru:* Deteksi momen viral dari **ledakan Live Chat Replay** + **puncak loudness audio**.
   - *Dimodifikasi:* Streaming parser Live Chat berkecepatan tinggi di Go dengan kamus **hype keyword Indonesia & gaming** (`wkwk`, `njir`, `gila`, `ngakak`, `kaget`, `gg`, `peak`, SuperChat, ALL-CAPS) lalu digabungkan secara otomatis dengan Heatmap + Audio (Multi-Signal Fusion).
3. **`NaufalRizqullah/opensource-clipping`**
   - *Ditiru:* Burn-in viral subtitle bergaya Hormozi/TikTok dan manajemen output terstruktur.
   - *Dimodifikasi:* Ditambah **MotionKlip Campaign Studio** (manajemen multi-campaign, upload Watermark PNG/GIF/MOV, **Outro 3 detik wajib di akhir tanpa filter/efek**, urutan hashtag wajib 1-klik copy, dan compliance pre-flight check).

---

## ✨ Fitur Utama

- **Multi-Signal Viral Detector (Super Akurat):**
  - 🔥 **YouTube Most Replayed Heatmap** (`yt-dlp` JSON + HTML fallback).
  - 💬 **Live Chat Replay Explosion** (tetap akurat mendeteksi momen kocak/epic walau VOD baru selesai & belum punya heatmap!).
  - 🔊 **Audio Excitement Peaks** (mendeteksi teriakan, tawa keras, dan jumpscare lewat FFmpeg RMS energy).
  - 📝 **Auto-Captions Preview & Burn-in ASS Subtitles** (langsung ambil caption `id`/`en` YouTube, tidak butuh GPU mahal!).
- **Dual Mode Download:**
  - **Quick Scan (Instan):** Scan grafik hits + preview tanpa harus download VOD 4 jam! Saat render, Go hanya download potongan detik yang dipilih secara paralel.
  - **Full MP4 Mode:** Download full video sekali untuk scrub lokal + deteksi puncak audio + render super cepat.
- **Framing & Layout Studio (Gameplay + Facecam):**
  - Drag langsung kotak **Gameplay (Biru)** dan **Facecam (Hijau)** di atas frame asli video, lihat hasil simulasi **9:16 + Watermark + Subtitle** secara live!
  - Mendukung `Split Custom`, `Split Kanan/Kiri Bawah/Atas`, `Blur Background 9:16`, `Center Crop`, dan `Original 16:9`.
- **MotionKlip Campaign Manager (Fleksibel untuk Semua Kreator):**
  - Sudah pre-loaded preset **Windah Basudara — GTA VI (MotionKlip)** lengkap dengan urutan hashtag wajib `#windahbasudara #GTAVI #windahGTAVI #motionklip #ezklip`, mention `@windahbasudara @rockstargames`, reminder Discord Tag `MKLP`, dan checklist rules.
  - Upload **Watermark** sekali -> otomatis ditempel sesuai posisi/ukuran/opacity.
  - Upload **Video Template Outro 3 Detik** sekali -> otomatis disambung di **akhir video**, resolusi & audio dinormalisasi (aman walau outro tanpa audio), dan **100% bersih tanpa filter/efek/watermark**.

---

## 🚀 Cara Menjalankan

### 1. Mode Lokal (Tercepat — 100% Offline di PC)
Klik dua kali **`start.bat`** atau jalankan dari PowerShell:

```powershell
cd E:\klipforge
.\klipforge.exe
```

Dashboard terbuka di **`http://127.0.0.1:8787`**.

### 2. Mode Online di Domain Sendiri (`https://klip.breakthematrix.id`)
Arsitektur **Hybrid Cloudflare Edge + Go Engine** (100% Gratis di Cloudflare Free Plan):
- **Dashboard Web UI** di-deploy via **Wrangler** (`cloudflare/wrangler.toml`) ke **`https://klip.breakthematrix.id`** (online 24/7 dari Cloudflare Global Edge).
- **Go Engine (FFmpeg + yt-dlp)** berjalan di PC kamu (IP residensial → tidak mudah diblokir YouTube!) atau VPS Docker, terhubung aman lewat **Cloudflare Tunnel** (`engine-klip.breakthematrix.id`).

Untuk menyalakan mode online dari PC:
- Klik dua kali **`start-online.bat`** → langsung akses **`https://klip.breakthematrix.id`** dari HP atau browser mana saja!

Untuk re-deploy update dashboard ke Cloudflare via Wrangler:
```powershell
Copy-Item web\index.html, web\app.css, web\app.js cloudflare\public\ -Force
cd cloudflare
wrangler deploy
```

Untuk pasang password pelindung akses publik (opsional, direkomendasikan):
```powershell
cd cloudflare
wrangler secret put ACCESS_KEY
```

### 3. Deploy Engine 24/7 di VPS (Opsional via Docker)
```bash
# Buat file .env berisi CF_TUNNEL_TOKEN=<token-tunnel-kamu>
docker compose --profile tunnel up -d --build
```

### Workflow Cepat Campaign Windah Basudara — GTA VI:
1. Buka tab **🏆 Campaign Rules** → pilih **Windah Basudara — GTA VI**.
2. Download asset **Watermark** dan **Template Video 3 Detik** dari Google Drive MotionKlip, lalu upload keduanya di halaman Campaign Rules → klik **Simpan Campaign**.
3. Pastikan akun Discord kamu sudah memasang Server Tag **`MKLP`**.
4. Buka tab **🎬 Studio Clipper** → paste link YouTube VOD Windah Basudara main GTA VI → klik **Scan & Deteksi Hits**.
5. Lihat grafik puncak paling ramai, atur kotak **Gameplay + Facecam**, edit **Hook Text** bila perlu, centang klip terbaik, lalu klik **⚡ AUTO-CLIP TERPILIH**.
6. Buka tab **📦 Queue & Clips** → preview hasil MP4 (sudah ada gameplay+facecam, subtitle, watermark, dan outro 3 detik bersih di akhir!), klik **📋 Copy Caption + Hashtags** (urutan sudah persis sesuai rules), lalu upload ke TikTok/Reels/Shorts!
