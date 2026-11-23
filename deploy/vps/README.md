# Deploy KlipForge Go Engine ke VPS Linux (Tim 5 Orang + 5 Muse 24/7)

Panduan cepat memindahkan Go Engine (FFmpeg + yt-dlp) dari PC lokal ke VPS Linux agar online **24/7**, bisa render paralel tanpa bikin PC lemot, dan bebas putus `trycloudflare.com`.

## 1. Spesifikasi VPS Rekomendasi (Tim 5 Orang + 5 Muse)
- **CPU**: 4–8 vCPU (x86_64) — cukup buat `MaxWorkers = 4` render FFmpeg paralel
- **RAM**: 8 GB (minimal 4 GB)
- **Disk**: 80–160 GB NVMe/SSD (VOD Windah 4 jam ~1.5 GB + hasil klip MP4)
- **OS**: Ubuntu 22.04 / 24.04 LTS atau Debian 12
- **Contoh provider**: Hetzner CX32/CPX31, Contabo VPS 2, Biznet GIO Neo Lite, atau IDCloudHost (~Rp 150rb–300rb/bln)

## 2. Clone & Siapkan `.env` di VPS
```bash
git clone https://github.com/iAm-182/klipforge.git
cd klipforge

cat > .env << 'EOF'
# Samakan dengan file .engine-token, .agent-key, dan .bridge-secret dari PC kamu
KLIPFORGE_ENGINE_TOKEN=ISI_ENGINE_TOKEN_KAMU
KLIPFORGE_AGENT_KEY=ISI_AGENT_KEY_KAMU
KLIPFORGE_BRIDGE_SECRET=ISI_BRIDGE_SECRET_KAMU
KLIPFORGE_EDGE_URL=https://klip.breakthematrix.id
ENGINE_DOMAIN=engine.breakthematrix.id
EOF
chmod 600 .env
```

*(Opsional: copy folder `data/` dari PC kamu ke VPS pakai `scp -r data/ user@vps:~/klipforge/` agar Campaign Windah, Watermark, Outro 3s, dan daftar Tim langsung ikut pindah!)*

## 3. Jalankan dengan Docker Compose + Auto-HTTPS Caddy
1. Arahkan DNS **A Record** `engine.breakthematrix.id` -> **IP publik VPS kamu** (DNS Only / abu-abu di Cloudflare agar Caddy terbitin SSL otomatis, atau Proxied).
2. Jalankan:
```bash
docker compose --profile caddy up -d --build
```
3. Daftarkan origin VPS permanen ke Cloudflare Edge Worker (`klip.breakthematrix.id`):
```bash
curl -X POST "https://klip.breakthematrix.id/edge-register" \
  -H "Content-Type: application/json" \
  -H "X-Bridge-Secret: $KLIPFORGE_BRIDGE_SECRET" \
  -d '{"origin":"https://engine.breakthematrix.id"}'
```
Selesai! Sekarang **`https://klip.breakthematrix.id`** langsung tembak VPS kamu **24/7 tanpa perlu PC nyala**!

## 4. Penting untuk VPS: Upload `cookies.txt` (Anti-Bot YouTube)
IP datacenter VPS (Hetzner/Contabo/dll.) sering kena challenge *"Sign in to confirm you're not a bot"* dari YouTube saat `yt-dlp` jalan.
Solusinya super gampang (sudah built-in di KlipForge):
1. Di Chrome laptop kamu (pakai akun YouTube cadangan), export cookies memakai ekstensi **"Get cookies.txt LOCALLY"**.
2. Buka `https://klip.breakthematrix.id` sebagai **Owner** -> tab **⚙️ Settings** -> klik **⬆️ Upload `cookies.txt`**.
3. Otomatis aktif untuk semua scan & download seluruh tim + Muse!
4. Tip hemat kuota & anti-403: setelah scan VOD baru Windah, klik **⬇️ Download Full MP4** sekali agar 5 clipper + 5 Muse memotong dari file lokal VPS secara instan!
