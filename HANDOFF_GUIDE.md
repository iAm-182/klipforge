# KlipForge — Panduan Lengkap, Arsitektur & Agent Handoff (`HANDOFF_GUIDE.md`)

Dokumen ini ditulis agar **pengguna maupun AI agent lain** dapat langsung memahami, menjalankan, memperbaiki, atau mengotomatisasi **KlipForge** (Go Auto-Clipper + MotionKlip Campaign Studio + Autonomous HITL Scheduler + n8n Integration).

---

## 1. Informasi Sistem & Lokasi File

| Komponen | Lokasi / URL |
|---|---|
| **Workspace Lokal** | `E:\klipforge` |
| **GitHub Repository** | `https://github.com/iAm-182/klipforge` |
| **URL Publik (Cloudflare Edge)** | `https://klip.breakthematrix.id` |
| **URL Lokal (Go Engine)** | `http://127.0.0.1:8787` |
| **Binary Go Engine** | `E:\klipforge\klipforge.exe` |
| **Launcher Mode Lokal** | `E:\klipforge\start.bat` |
| **Launcher Mode Online (Domain)** | `E:\klipforge\start-online.bat` (memanggil `scripts/online.ps1`) |
| **Bundled Binaries (Gitignored)** | `E:\klipforge\bin\yt-dlp.exe`, `E:\klipforge\bin\cloudflared.exe` |
| **FFmpeg & FFprobe** | Auto-detect via PATH / WinGet (`Gyan.FFmpeg`) |
| **Data Persisten (Gitignored)** | `E:\klipforge\data\` (`campaigns.json`, `projects.json`, `jobs.json`, `autopilot.json`, `audio_library.json`, `assets/`, `audio/`, `projects/`) |
| **Output Clip MP4 (Gitignored)** | `E:\klipforge\outputs\<Nama_Campaign>\` |
| **Template Workflow n8n** | `E:\klipforge\n8n\klipforge-hitl-workflow.json` |

> **Catatan Keamanan:** File `.bridge-secret`, `.env`, `data/`, `outputs/`, dan binary `.exe` selalu di-ignore di `.gitignore`. Jangan pernah commit secret/token ke GitHub.

---

## 2. Arsitektur Hybrid (Cloudflare Edge + Go Engine)

```text
Browser / HP / n8n
   │  https://klip.breakthematrix.id
   ▼
Cloudflare Worker ("klipforge-edge", deploy via `cd cloudflare && wrangler deploy`)
   ├── Static UI Assets (HTML/CSS/JS) → Online 24/7 di Cloudflare Edge
   ├── Workers KV (`STATE`)           → Menyimpan URL tunnel aktif (di-update otomatis oleh `start-online.bat` via POST /edge-register)
   └── Proxy `/api/*` (REST, SSE, Video Range)
        │
        ▼  Cloudflare Quick Tunnel (`cloudflared`)
Go Engine (`klipforge.exe`, port 8787) — berjalan di PC (IP residensial, aman dari blokir YouTube) atau VPS Docker
   ├── yt-dlp (+ `--js-runtimes node`) → Metadata, Heatmap, Transkrip `id`/`en`, Download per-segmen cepat
   ├── Multi-Signal Viral Detector     → Gabungan Heatmap + Instant Transcript Hype + Audio Peak + Background Live Chat
   ├── FFmpeg 9.0 Renderer             → Split Gameplay+Facecam, ASS Viral Subtitles, Hook Banner, Video FX, BGM + Auto-Ducking, Auto-SFX, Watermark, Clean 3s Outro
   └── Autonomous HITL Scheduler       → Watch channel terjadwal → Render Top N → Masuk Review Inbox → Webhook Notifikasi (n8n/Discord)
```

---

## 3. Langkah Pakai Manual (Step-by-Step untuk Manusia)

### Persiapan Awal MotionKlip (Windah Basudara — GTA VI)
1. Pasang **Discord Server Tag `MKLP`** di profil Discord (*Edit Profile → Server Tag → MKLP*).
2. Download dari Google Drive campaign MotionKlip:
   - File **Watermark** (PNG/GIF/MOV)
   - File **Video Template Outro 3 Detik** (MP4/MOV)

### Step 1: Jalankan KlipForge
- **Mode Online (bisa buka dari HP / `https://klip.breakthematrix.id`):** double-click `start-online.bat` (biarkan jendela terminal terbuka).
- **Mode Lokal (tercepat di PC):** double-click `start.bat` → buka `http://127.0.0.1:8787`.

### Step 2: Konfigurasi Campaign (Sekali per Campaign)
1. Buka tab **🏆 Campaign Rules** → pilih **Windah Basudara — GTA VI**.
2. Upload **Watermark** → atur posisi/ukuran/opacity (biarkan *"Tampilkan watermark di atas Outro"* tetap **OFF**).
3. Upload **Video Template Outro 3 Detik** → pastikan **Aktif** (KlipForge menjamin 0 filter, 0 watermark, 0 bocoran BGM/SFX di outro!).
4. Verifikasi urutan hashtag wajib: `#windahbasudara #GTAVI #windahGTAVI #motionklip #ezklip`.
5. Klik **💾 Simpan Campaign**.
6. *(Opsional)* Di tab **🎵 BGM & SFX Pack**, dengarkan 8 SFX + 3 BGM DMCA-free bawaan atau upload SFX meme favorit (Vine Boom, Bruh, Phonk, dll.).

### Step 3: Scan Video / Livestream YouTube (~10–15 Detik!)
1. Buka tab **🎬 Studio Clipper**, pilih campaign aktif, tempel link YouTube.
2. Klik **🔍 Scan & Deteksi Hits**.
   - **Fase 1 (Cepat ~10–15 detik):** Mengambil metadata, Most Replayed Heatmap (jika ada), preview frame HD, dan transkrip ucapan `id`/`en` → **langsung tampil grafik & 15 momen hits!**
   - **Fase 2 (Background):** Untuk stream < 90 menit, Live Chat Replay diproses di background tanpa memblokir UI. *(Untuk VOD raksasa 3–4 jam jutaan views seperti Windah GTA VI, Live Chat otomatis dilewati agar tidak hang 20 menit — deteksi mengandalkan Heatmap + Instant Transcript Hype + opsional Audio Peak).*

### Step 4: Atur Frame Gameplay + Facecam & FX
1. Klik **📸 Ambil Frame Untuk Crop** agar gambar asli stream muncul.
2. Pilih mode **`Split Custom`**, klik **🟦 Gameplay** lalu drag kotak biru di area game, klik **🟩 Facecam** lalu drag kotak hijau di wajah kreator.
3. Atur **BGM + Auto-Ducking**, **Preset Auto-SFX** (`Meme Punch` / `Hype Epic` / `Kaget Alert`), **Color Grading** (`Vivid Gaming`), dan **Motion FX** (`Peak Punch Zoom + Shake` / `Jedag-Jedug`).
4. Klik **💾 Simpan ke Campaign** agar dipakai otomatis oleh Autopilot juga!

### Step 5: Auto-Clip & Upload
1. Centang kandidat terbaik (atau **Top 5**), edit Hook Text jika perlu → klik **⚡ AUTO-CLIP TERPILIH**.
2. Buka tab **📦 Queue & Clips** → preview MP4 → cek badge compliance hijau semua → klik **📋 Copy Caption + Hashtags** → download MP4 → upload ke TikTok, YT Shorts, dan FB Reels → submit link ke MotionKlip.

---

## 4. Mode Autonomous / Terjadwal + Human-in-the-Loop (Seperti n8n)

KlipForge punya **2 cara** menjalankan sistem autonomous yang tetap butuh **analisis & approval akhir dari manusia**:

### Cara A: Built-In Autopilot Scheduler (Tanpa Install n8n!)
1. Buka tab **🤖 Autopilot & Review**.
2. Pilih rule **Windah Basudara — GTA VI Auto-Watcher** (atau klik **+ Rule Baru** untuk channel lain):
   - **Channel URL:** `https://www.youtube.com/@WindahBasudara/streams`
   - **Filter Keyword Judul:** `GTA VI, GTA 6, Grand Theft Auto` (hanya proses VOD yang cocok!)
   - **Interval Cek:** misal `60` menit
   - **Top Klip per VOD:** misal `5`
   - **Notify Webhook URL (Opsional):** isi **Discord Webhook URL** atau **n8n Webhook URL** untuk menerima notif tiap klip baru selesai dirender.
   - **Approve Webhook URL (Opsional):** isi **n8n Webhook URL** yang otomatis terpicu saat lo klik **✅ Approve**!
3. Nyalakan toggle **Aktifkan Jadwal Otomatis** → klik **💾 Simpan Rule** (atau klik **▶ Jalankan Sekarang** untuk tes langsung).
4. Saat ditinggal tidur/kerja, KlipForge otomatis scan VOD baru → deteksi momen hits → render Top 5 klip dengan layout/watermark/outro/FX campaign → masukkan semuanya ke **🕵️ Review Inbox (Menunggu Approval)**.
5. Saat lo sempat, buka **`https://klip.breakthematrix.id`** (dari PC atau HP) → masuk tab **🤖 Autopilot & Review** → tonton tiap klip → edit caption jika perlu → klik **✅ Approve (Siap Upload)** atau **❌ Reject & Hapus**.

### Cara B: Integrasi Eksternal dengan n8n
Import file **`E:\klipforge\n8n\klipforge-hitl-workflow.json`** ke instance n8n kamu, lalu atur Base URL ke `https://klip.breakthematrix.id` (atau `http://127.0.0.1:8787` jika n8n di mesin lokal yang sama).

---

## 5. REST API Reference (Untuk AI Agent & n8n)

Base URL: `http://127.0.0.1:8787` (lokal) atau `https://klip.breakthematrix.id` (online).

| Method & Endpoint | Fungsi |
|---|---|
| `GET /api/status` | Cek kesehatan FFmpeg, yt-dlp, encoder, jumlah campaign/project/job |
| `GET /api/campaigns` | Daftar semua campaign rules |
| `POST /api/campaigns` / `PUT /api/campaigns/{id}` | Buat / update campaign |
| `POST /api/campaigns/{id}/upload/{watermark\|outro\|intro}` | Upload asset multipart (`file`) |
| `GET /api/audio` | Daftar BGM & SFX (8 SFX + 3 BGM bawaan + custom) |
| `POST /api/audio/upload/{bgm\|sfx}` | Upload BGM/SFX custom (`file`, `name`, `category`) |
| `POST /api/projects/analyze` | Mulai scan YouTube `{url, campaignId, downloadFull, includeChat, includeAudio}` |
| `GET /api/projects/{id}` | Ambil metadata, timeline & kandidat momen hits |
| `POST /api/projects/{id}/reanalyze` | Hitung ulang kandidat instan dengan bobot deteksi baru |
| `POST /api/jobs/render` / `POST /api/jobs/batch` | Antre render 1 klip atau batch banyak klip |
| `GET /api/jobs` | Daftar semua job (cek `status`, `reviewStatus`, `postCaption`, `compliance`) |
| `POST /api/jobs/{id}/review` | Human Approval `{reviewStatus: "approved"\|"rejected", reviewNote, postCaption, deleteFileOnReject}` |
| `GET /api/jobs/{id}/video?download=1` | Stream / download hasil MP4 |
| `GET /api/autopilot` | Daftar semua jadwal Autopilot |
| `POST /api/autopilot` / `PUT /api/autopilot/{id}` | Buat / edit jadwal Autopilot |
| `POST /api/autopilot/{id}/run` | Picu 1 siklus Autopilot secara langsung (cocok dipanggil n8n Cron!) |
| `GET /api/events` | Real-time Server-Sent Events (SSE) progress & logs |

---

## 6. Perintah Build, Test & Deploy (Untuk Agent Maintainer)

```powershell
# Test & Build Go binary
cd E:\klipforge
go vet ./...
go test -v ./...
go build -o klipforge.exe .

# Sync Web Dashboard & Re-deploy Cloudflare Worker ke klip.breakthematrix.id
Copy-Item E:\klipforge\web\index.html, E:\klipforge\web\app.css, E:\klipforge\web\app.js E:\klipforge\cloudflare\public\ -Force
cd E:\klipforge\cloudflare
wrangler deploy

# Opsional: Tambah password pelindung untuk domain publik
wrangler secret put ACCESS_KEY
```
