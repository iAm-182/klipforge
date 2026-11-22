$ErrorActionPreference = "Stop"
Set-Location "E:\klipforge"

if (-not (Test-Path "E:\klipforge\klipforge.exe")) {
    Write-Host "[KlipForge] Building Go binary..." -ForegroundColor Cyan
    go build -o klipforge.exe .
    if ($LASTEXITCODE -ne 0) { throw "Gagal build klipforge.exe" }
}
if (-not (Test-Path "E:\klipforge\bin\cloudflared.exe")) {
    Write-Host "[KlipForge] Downloading cloudflared.exe..." -ForegroundColor Cyan
    New-Item -ItemType Directory -Force -Path "E:\klipforge\bin" | Out-Null
    curl.exe -sL "https://github.com/cloudflare/cloudflared/releases/latest/download/cloudflared-windows-amd64.exe" -o "E:\klipforge\bin\cloudflared.exe"
}

$secretPath = "E:\klipforge\.bridge-secret"
if (-not (Test-Path $secretPath)) {
    throw "File .bridge-secret tidak ditemukan! Jalankan setup bridge secret dulu."
}
$bridgeSecret = (Get-Content $secretPath -Raw).Trim()
$edgeUrl = "https://klip.breakthematrix.id"

Write-Host "============================================================" -ForegroundColor Cyan
Write-Host "  KLIPFORGE ONLINE MODE -> $edgeUrl" -ForegroundColor Green
Write-Host "============================================================" -ForegroundColor Cyan

# 1. Start Go Engine if port 8787 is not already listening
$engineProc = $null
$listening = Get-NetTCPConnection -LocalPort 8787 -State Listen -ErrorAction SilentlyContinue
if (-not $listening) {
    Write-Host "[1/3] Menyalakan Go Engine (FFmpeg + yt-dlp) di 127.0.0.1:8787..." -ForegroundColor Yellow
    $engineProc = Start-Process -FilePath "E:\klipforge\klipforge.exe" -ArgumentList "--port","8787","--no-open" -WorkingDirectory "E:\klipforge" -PassThru -WindowStyle Minimized
    Start-Sleep -Seconds 2
} else {
    Write-Host "[1/3] Go Engine sudah aktif di 127.0.0.1:8787." -ForegroundColor Green
}

# 2. Start Cloudflare Quick Tunnel and auto-register its HTTPS URL to klip.breakthematrix.id
Write-Host "[2/3] Membuka Cloudflare Tunnel (100% gratis, zero port-forward)..." -ForegroundColor Yellow
$logFile = "E:\klipforge\.cloudflared.log"
if (Test-Path $logFile) { Remove-Item $logFile -Force }

$cfProc = Start-Process -FilePath "E:\klipforge\bin\cloudflared.exe" `
    -ArgumentList "tunnel","--no-autoupdate","--url","http://127.0.0.1:8787","--logfile",$logFile `
    -WorkingDirectory "E:\klipforge" -PassThru -WindowStyle Hidden

try {
    $tunnelUrl = ""
    for ($i = 0; $i -lt 45; $i++) {
        Start-Sleep -Milliseconds 500
        if (Test-Path $logFile) {
            $logTxt = Get-Content $logFile -Raw -ErrorAction SilentlyContinue
            if ($logTxt -match 'https://[a-z0-9-]+\.trycloudflare\.com') {
                $tunnelUrl = $Matches[0]
                break
            }
        }
    }
    if (-not $tunnelUrl) {
        throw "Gagal mendapatkan URL Cloudflare Quick Tunnel dalam 22 detik."
    }

    Write-Host "[3/3] Menghubungkan $edgeUrl -> $tunnelUrl ..." -ForegroundColor Yellow
    $bodyFile = "E:\klipforge\.tmp_reg.json"
    [System.IO.File]::WriteAllText($bodyFile, (@{ origin = $tunnelUrl } | ConvertTo-Json -Compress))
    $regRes = curl.exe -s -X POST "$edgeUrl/edge-register" `
        -H "Content-Type: application/json" `
        -H "X-Bridge-Secret: $bridgeSecret" `
        --data "@$bodyFile" | ConvertFrom-Json
    Remove-Item $bodyFile -Force -ErrorAction SilentlyContinue

    if (-not $regRes.ok) {
        throw "Gagal registrasi bridge: $($regRes | ConvertTo-Json -Compress)"
    }

    Write-Host ""
    Write-Host "  ONLINE & TERHUBUNG!" -ForegroundColor Green
    Write-Host "  Buka dari HP / Browser mana saja : $edgeUrl" -ForegroundColor Cyan
    Write-Host "  URL Lokal PC                     : http://127.0.0.1:8787" -ForegroundColor Gray
    Write-Host ""
    Write-Host "  Biarkan jendela ini tetap terbuka selama memakai $edgeUrl." -ForegroundColor Yellow
    Write-Host "  Tekan Ctrl+C untuk berhenti." -ForegroundColor Gray

    Start-Process $edgeUrl
    Wait-Process -Id $cfProc.Id
} finally {
    if ($cfProc -and -not $cfProc.HasExited) { Stop-Process -Id $cfProc.Id -Force -ErrorAction SilentlyContinue }
    if ($engineProc -and -not $engineProc.HasExited) { Stop-Process -Id $engineProc.Id -Force -ErrorAction SilentlyContinue }
}
