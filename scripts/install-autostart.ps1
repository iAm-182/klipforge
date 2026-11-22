$ErrorActionPreference = "Stop"
$Root = Split-Path -Parent $PSScriptRoot
$BatPath = Join-Path $Root "start-online.bat"
$TaskName = "KlipForgeOnlineAutoStart"

if (-not (Test-Path $BatPath)) {
    throw "start-online.bat tidak ditemukan di $Root"
}

$action = New-ScheduledTaskAction -Execute "cmd.exe" -Argument "/c `"$BatPath`"" -WorkingDirectory $Root
$trigger = New-ScheduledTaskTrigger -AtLogOn
$settings = New-ScheduledTaskSettingsSet -AllowStartIfOnBatteries -DontStopIfGoingOnBatteries -StartWhenAvailable

Register-ScheduledTask -TaskName $TaskName -Action $action -Trigger $trigger -Settings $settings -Force | Out-Null
Write-Host "[OK] Auto-Start berhasil dipasang ($TaskName)!" -ForegroundColor Green
Write-Host "     KlipForge akan otomatis menyala + online + Autopilot auto-catchup setiap kali kamu login Windows." -ForegroundColor Cyan
Write-Host "     Untuk menghapusnya nanti: Unregister-ScheduledTask -TaskName '$TaskName' -Confirm:`$false" -ForegroundColor Gray
