$ErrorActionPreference = "Stop"
$Root = Split-Path -Parent $PSScriptRoot
Set-Location $Root

$ExePath    = Join-Path $Root "klipforge.exe"
$CfExe      = Join-Path $Root "bin\cloudflared.exe"
$SecretFile = Join-Path $Root ".bridge-secret"
$TokenFile  = Join-Path $Root ".engine-token"
$LogFile    = Join-Path $Root ".cloudflared.log"
$EdgeUrl    = "https://klip.breakthematrix.id"
$EngineUrl  = "http://127.0.0.1:8787"

function Needs-Rebuild {
    if (-not (Test-Path $ExePath)) { return $true }
    $exeTime = (Get-Item $ExePath).LastWriteTimeUtc
    $newer = Get-ChildItem -Path $Root -Recurse -File -Include *.go,*.html,*.css,*.js -ErrorAction SilentlyContinue |
        Where-Object { $_.FullName -notmatch "\\cloudflare\\|\\data\\|\\outputs\\" -and $_.LastWriteTimeUtc -gt $exeTime } |
        Select-Object -First 1
    return [bool]$newer
}

function Send-NotifyWebhook($EventName, $Title, $Body) {
    $settingsPath = Join-Path $Root "data\settings.json"
    if (-not (Test-Path $settingsPath)) { return }
    try {
        $cfg = Get-Content $settingsPath -Raw | ConvertFrom-Json
        $wh = ($cfg.notifyWebhookUrl | Out-String).Trim()
        if (-not $wh -or ($wh -notmatch "^https?://")) { return }
        if ($wh -match "discord\.com/api/webhooks|discordapp\.com/api/webhooks") {
            $payload = @{ username = "KlipForge Watchdog"; content = "**$Title**`n$Body" } | ConvertTo-Json -Compress
        } else {
            $payload = @{ source = "klipforge-watchdog"; event = $EventName; title = $Title; message = $Body; timestamp = (Get-Date).ToString("o") } | ConvertTo-Json -Compress
        }
        $bytes = [System.Text.Encoding]::UTF8.GetBytes($payload)
        Invoke-RestMethod -Method Post -Uri $wh -Body $bytes -ContentType "application/json; charset=utf-8" -TimeoutSec 6 | Out-Null
    } catch {}
}

function Ensure-EngineToken {
    if (-not (Test-Path $TokenFile)) {
        $rng = [System.Security.Cryptography.RandomNumberGenerator]::Create()
        $bytes = New-Object byte[] 24
        $rng.GetBytes($bytes)
        $tok = ([BitConverter]::ToString($bytes) -replace "-", "").ToLower()
        [System.IO.File]::WriteAllText($TokenFile, $tok)
    }
    return (Get-Content $TokenFile -Raw).Trim()
}

function Test-EngineHealth($Token) {
    try {
        $headers = @{ "X-Engine-Token" = $Token }
        $res = Invoke-RestMethod -Uri "$EngineUrl/api/status" -Headers $headers -TimeoutSec 4
        return [bool]$res
    } catch {
        return $false
    }
}

function Start-GoEngine($Token) {
    Write-Host "[Engine] Menyalakan KlipForge Go Engine di $EngineUrl..." -ForegroundColor Yellow
    $p = Start-Process -FilePath $ExePath -ArgumentList "--port","8787","--no-open" -WorkingDirectory $Root -PassThru -WindowStyle Minimized
    for ($i = 0; $i -lt 30; $i++) {
        Start-Sleep -Milliseconds 500
        if (Test-EngineHealth $Token) {
            Write-Host "[Engine] Sehat & siap di $EngineUrl (PID $($p.Id))" -ForegroundColor Green
            return $p
        }
        if ($p.HasExited) { throw "klipforge.exe berhenti mendadak saat startup (exit $($p.ExitCode))" }
    }
    throw "Go Engine tidak merespons health check /api/status dalam 15 detik."
}

function Start-And-Register-Tunnel($BridgeSecret) {
    if (Test-Path $LogFile) { Remove-Item $LogFile -Force -ErrorAction SilentlyContinue }
    Write-Host "[Tunnel] Membuka Cloudflare Quick Tunnel..." -ForegroundColor Yellow
    $cf = Start-Process -FilePath $CfExe `
        -ArgumentList "tunnel","--no-autoupdate","--url",$EngineUrl,"--logfile",$LogFile `
        -WorkingDirectory $Root -PassThru -WindowStyle Hidden

    $tunnelUrl = ""
    for ($i = 0; $i -lt 90; $i++) {
        Start-Sleep -Milliseconds 500
        if ($cf.HasExited) { throw "cloudflared berhenti sebelum tunnel aktif." }
        if (Test-Path $LogFile) {
            $logTxt = Get-Content $LogFile -Raw -ErrorAction SilentlyContinue
            if ($logTxt -match 'https://[a-z0-9-]+\.trycloudflare\.com') {
                $tunnelUrl = $Matches[0]
                break
            }
        }
    }
    if (-not $tunnelUrl) {
        if (-not $cf.HasExited) { Stop-Process -Id $cf.Id -Force -ErrorAction SilentlyContinue }
        throw "Gagal mendapatkan URL Cloudflare Quick Tunnel dalam 45 detik."
    }

    Write-Host "[Bridge] Menghubungkan $EdgeUrl -> $tunnelUrl ..." -ForegroundColor Yellow
    $bodyFile = Join-Path $Root ".tmp_reg.json"
    [System.IO.File]::WriteAllText($bodyFile, (@{ origin = $tunnelUrl } | ConvertTo-Json -Compress))
    $regRaw = curl.exe -s -X POST "$EdgeUrl/edge-register" `
        -H "Content-Type: application/json" `
        -H "X-Bridge-Secret: $BridgeSecret" `
        --data "@$bodyFile"
    Remove-Item $bodyFile -Force -ErrorAction SilentlyContinue

    $regRes = $regRaw | ConvertFrom-Json
    if (-not $regRes.ok) {
        if (-not $cf.HasExited) { Stop-Process -Id $cf.Id -Force -ErrorAction SilentlyContinue }
        throw "Gagal registrasi bridge: $regRaw"
    }
    return @{ Proc = $cf; Url = $tunnelUrl }
}

# --- Main Startup ---
if (Needs-Rebuild) {
    Write-Host "[Build] Mendeteksi update source code — rebuild klipforge.exe..." -ForegroundColor Cyan
    Get-Process klipforge -ErrorAction SilentlyContinue | Stop-Process -Force -ErrorAction SilentlyContinue
    Start-Sleep -Milliseconds 400
    go build -o $ExePath .
    if ($LASTEXITCODE -ne 0) { throw "Gagal build klipforge.exe" }
}

if (-not (Test-Path $CfExe)) {
    Write-Host "[Setup] Mengunduh cloudflared.exe..." -ForegroundColor Cyan
    New-Item -ItemType Directory -Force -Path (Join-Path $Root "bin") | Out-Null
    curl.exe -sL "https://github.com/cloudflare/cloudflared/releases/latest/download/cloudflared-windows-amd64.exe" -o $CfExe
}

if (-not (Test-Path $SecretFile)) {
    throw "File .bridge-secret tidak ditemukan di $Root!"
}
$bridgeSecret = (Get-Content $SecretFile -Raw).Trim()
$engineToken  = Ensure-EngineToken

Write-Host "============================================================" -ForegroundColor Cyan
Write-Host "  KLIPFORGE HARDENED ONLINE WATCHDOG -> $EdgeUrl" -ForegroundColor Green
Write-Host "============================================================" -ForegroundColor Cyan

$engineProc = $null
$tunnelInfo = $null

try {
    if (Test-EngineHealth $engineToken) {
        Write-Host "[1/3] Go Engine sudah aktif & sehat di $EngineUrl." -ForegroundColor Green
    } else {
        Get-Process klipforge -ErrorAction SilentlyContinue | Stop-Process -Force -ErrorAction SilentlyContinue
        Start-Sleep -Milliseconds 300
        $engineProc = Start-GoEngine $engineToken
    }

    $tunnelInfo = Start-And-Register-Tunnel $bridgeSecret
    Write-Host ""
    Write-Host "  ONLINE & DILINDUNGI ACCESS KEY + ENGINE TOKEN!" -ForegroundColor Green
    Write-Host "  Dashboard Online : $EdgeUrl" -ForegroundColor Cyan
    Write-Host "  Watchdog         : Cek otomatis tiap 30 detik (auto-restart engine & tunnel)" -ForegroundColor Gray
    Write-Host "  Tekan Ctrl+C untuk berhenti." -ForegroundColor Gray
    Write-Host ""

    Send-NotifyWebhook "engine_online" "🟢 KlipForge Engine Online" "Dashboard aktif di $EdgeUrl (Watchdog 30s aktif)."
    Start-Process $EdgeUrl

    # 30-second self-healing watchdog loop!
    $failCount = 0
    while ($true) {
        Start-Sleep -Seconds 30

        # 1. Check Go Engine health
        if (-not (Test-EngineHealth $engineToken)) {
            $failCount++
            Write-Host "[Watchdog] $(Get-Date -Format 'HH:mm:ss') Engine tidak merespons ($failCount/2)..." -ForegroundColor Yellow
            if ($failCount -ge 2) {
                Write-Host "[Watchdog] Merestart KlipForge Go Engine..." -ForegroundColor Red
                Send-NotifyWebhook "engine_down" "⚠️ KlipForge Engine Crash — Auto-Restarting" "Engine tidak merespons health check, watchdog sedang menyalakan ulang."
                Get-Process klipforge -ErrorAction SilentlyContinue | Stop-Process -Force -ErrorAction SilentlyContinue
                Start-Sleep -Seconds 1
                try {
                    $engineProc = Start-GoEngine $engineToken
                    $failCount = 0
                    Send-NotifyWebhook "engine_recovered" "✅ KlipForge Engine Pulih" "Engine berhasil dinyalakan ulang otomatis."
                } catch {
                    Write-Host "[Watchdog] Restart engine gagal: $_" -ForegroundColor Red
                }
            }
        } else {
            $failCount = 0
        }

        # 2. Check Cloudflare Tunnel health
        if (-not $tunnelInfo -or -not $tunnelInfo.Proc -or $tunnelInfo.Proc.HasExited) {
            Write-Host "[Watchdog] $(Get-Date -Format 'HH:mm:ss') Tunnel mati — membuka ulang & registrasi ke $EdgeUrl..." -ForegroundColor Red
            Send-NotifyWebhook "tunnel_down" "⚠️ Cloudflare Tunnel Putus — Reconnecting" "Watchdog sedang membuka tunnel baru dan memperbarui bridge $EdgeUrl."
            try {
                $tunnelInfo = Start-And-Register-Tunnel $bridgeSecret
                Write-Host "[Watchdog] Tunnel baru terhubung: $($tunnelInfo.Url)" -ForegroundColor Green
                Send-NotifyWebhook "tunnel_recovered" "✅ Tunnel Terhubung Kembali" "Akses online $EdgeUrl kembali aktif."
            } catch {
                Write-Host "[Watchdog] Reconnect tunnel gagal: $_" -ForegroundColor Red
            }
        }
    }
} finally {
    Send-NotifyWebhook "engine_offline" "🔴 KlipForge Dimatikan" "Engine & tunnel dihentikan secara manual di PC."
    if ($tunnelInfo -and $tunnelInfo.Proc -and -not $tunnelInfo.Proc.HasExited) {
        Stop-Process -Id $tunnelInfo.Proc.Id -Force -ErrorAction SilentlyContinue
    }
    if ($engineProc -and -not $engineProc.HasExited) {
        Stop-Process -Id $engineProc.Id -Force -ErrorAction SilentlyContinue
    }
}
