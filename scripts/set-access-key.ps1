param(
    [string]$NewKey = ""
)
$ErrorActionPreference = "Stop"
$Root = Split-Path -Parent $PSScriptRoot
$KeyFile = Join-Path $Root ".access-key"

if (-not $NewKey) {
    $NewKey = Read-Host "Masukkan ACCESS_KEY baru untuk login ke https://klip.breakthematrix.id (min 8 karakter)"
}
$NewKey = $NewKey.Trim()
if ($NewKey.Length -lt 8) {
    throw "ACCESS_KEY minimal 8 karakter agar aman!"
}

[System.IO.File]::WriteAllText($KeyFile, $NewKey)
Write-Host "[OK] Disimpan lokal ke .access-key (gitignored)." -ForegroundColor Green

Set-Location (Join-Path $Root "cloudflare")
$NewKey | wrangler secret put ACCESS_KEY
if ($LASTEXITCODE -ne 0) {
    throw "Gagal set secret ACCESS_KEY di Cloudflare Worker (pastikan CLOUDFLARE_API_TOKEN / wrangler login aktif)."
}
Write-Host "[DONE] ACCESS_KEY aktif di https://klip.breakthematrix.id!" -ForegroundColor Cyan
