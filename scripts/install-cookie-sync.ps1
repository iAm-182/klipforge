param(
    [int]$IntervalMinutes = 60,
    [ValidateSet("auto", "firefox", "edge", "chrome", "file")]
    [string]$Browser = "auto",
    [switch]$Uninstall
)

$ErrorActionPreference = "Stop"
$Root = Split-Path -Parent $PSScriptRoot
$ScriptPath = Join-Path $PSScriptRoot "sync-cookies.ps1"
$TaskName = "KlipForgeCookieAutoSync"

if ($Uninstall) {
    Unregister-ScheduledTask -TaskName $TaskName -Confirm:$false -ErrorAction SilentlyContinue
    Write-Host "[OK] Scheduled task '$TaskName' dihapus." -ForegroundColor Yellow
    exit 0
}

if (-not (Test-Path $ScriptPath)) {
    throw "sync-cookies.ps1 tidak ditemukan di $PSScriptRoot"
}
if ($IntervalMinutes -lt 15) {
    throw "Interval minimal 15 menit agar tidak spam InstaBox Auto-Wake (rekomendasi: 60 menit)."
}

$arg = "-NoProfile -ExecutionPolicy Bypass -WindowStyle Hidden -File `"$ScriptPath`" -Browser $Browser"
$action = New-ScheduledTaskAction -Execute "powershell.exe" -Argument $arg -WorkingDirectory $Root

# Repeat every $IntervalMinutes for current user (no Admin UAC required!)
$startAt = (Get-Date).AddMinutes(2)
$trigger = New-ScheduledTaskTrigger -Once -At $startAt -RepetitionInterval (New-TimeSpan -Minutes $IntervalMinutes)
$principal = New-ScheduledTaskPrincipal -UserId "$env:USERDOMAIN\$env:USERNAME" -LogonType Interactive -RunLevel Limited

$settings = New-ScheduledTaskSettingsSet `
    -AllowStartIfOnBatteries `
    -DontStopIfGoingOnBatteries `
    -StartWhenAvailable `
    -ExecutionTimeLimit (New-TimeSpan -Minutes 5) `
    -MultipleInstances IgnoreNew

try {
    Register-ScheduledTask `
        -TaskName $TaskName `
        -Action $action `
        -Trigger $trigger `
        -Principal $principal `
        -Settings $settings `
        -Description "KlipForge smart YouTube cookie auto-sync (hash-gated; hanya upload ke InstaBox jika berubah)" `
        -Force | Out-Null
} catch {
    # Clean fallback via standard non-elevated schtasks.exe
    $trCmd = "powershell.exe $arg"
    & schtasks.exe /Create /F /SC MINUTE /MO $IntervalMinutes /TN $TaskName /TR $trCmd | Out-Null
    if ($LASTEXITCODE -ne 0) {
        throw "Gagal mendaftarkan Task Scheduler '$TaskName': $($_.Exception.Message)"
    }
}

Write-Host "[OK] Auto-sync cookie aktif ($TaskName) setiap $IntervalMinutes menit (mode: $Browser)!" -ForegroundColor Green
Write-Host "     - Hash-gated: jika cookie tidak berubah, TIDAK ada request ke InstaBox (server tetap bisa tidur)." -ForegroundColor Cyan
Write-Host "     - Log lokal:  E:\klipforge\data\cookie-sync.log" -ForegroundColor Gray
Write-Host "     - Hapus task: .\scripts\install-cookie-sync.ps1 -Uninstall" -ForegroundColor Gray
