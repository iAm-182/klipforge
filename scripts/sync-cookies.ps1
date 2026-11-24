param(
    [ValidateSet("auto", "firefox", "edge", "chrome", "file")]
    [string]$Browser = "auto",
    [string]$EdgeURL = "https://klip.breakthematrix.id",
    [switch]$Force,
    [switch]$DryRun
)

$ErrorActionPreference = "Stop"
$Root = Split-Path -Parent $PSScriptRoot
$DataDir = Join-Path $Root "data"
$CookiesDest = Join-Path $DataDir "cookies.txt"
$HashFile = Join-Path $DataDir ".cookies-sync.hash"
$LogFile = Join-Path $DataDir "cookie-sync.log"
$AccessKeyFile = Join-Path $Root ".access-key"

New-Item -ItemType Directory -Force -Path $DataDir | Out-Null

function Write-SyncLog {
    param([string]$Message, [string]$Level = "INFO")
    $ts = (Get-Date).ToString("yyyy-MM-dd HH:mm:ss")
    $line = "[$ts] [$Level] $Message"
    Add-Content -Path $LogFile -Value $line -Encoding UTF8
    Write-Host $line
}

function Find-YtDlp {
    $local = Join-Path $Root "bin\yt-dlp.exe"
    if (Test-Path $local) { return $local }
    $cmd = Get-Command yt-dlp -ErrorAction SilentlyContinue
    if ($cmd) { return $cmd.Source }
    return ""
}

# Filter strictly to YouTube-domain Netscape lines so cookies from unrelated sites never leave this PC.
function Filter-YouTubeNetscapeLines {
    param([string[]]$RawLines)
    $out = New-Object System.Collections.Generic.List[string]
    $out.Add("# Netscape HTTP Cookie File")
    $out.Add("# Filtered strictly to YouTube domains by KlipForge sync-cookies.ps1")
    foreach ($ln in $RawLines) {
        if ([string]::IsNullOrWhiteSpace($ln)) { continue }
        $t = $ln.Trim()
        if ($t.StartsWith("#") -and -not $t.StartsWith("#HttpOnly_")) { continue }
        $cols = $t -split "`t"
        if ($cols.Count -lt 7) { continue }
        $dom = $cols[0] -replace '^#HttpOnly_', ''
        if ($dom -match '(^|\.)(youtube\.com|youtu\.be|googlevideo\.com)$') {
            $out.Add($t)
        }
    }
    return $out.ToArray()
}

function Test-HasYouTubeAuthCookies {
    param([string[]]$Lines)
    $authNames = @("LOGIN_INFO", "SID", "__Secure-1PSID", "__Secure-3PSID", "SAPISID")
    $hits = 0
    foreach ($ln in $Lines) {
        $cols = $ln -split "`t"
        if ($cols.Count -ge 7 -and ($authNames -contains $cols[5]) -and -not [string]::IsNullOrWhiteSpace($cols[6])) {
            $hits++
        }
    }
    return ($hits -ge 2)
}

# Compute SHA-256 over stable auth cookie values only (ignores expiry/noise so InstaBox doesn't wake needlessly).
function Get-AuthCookieFingerprint {
    param([string[]]$Lines)
    $stableKeys = @(
        "LOGIN_INFO", "SID", "HSID", "SSID", "APISID", "SAPISID",
        "__Secure-1PSID", "__Secure-3PSID", "__Secure-1PAPISID", "__Secure-3PAPISID",
        "__Secure-1PSIDTS", "__Secure-3PSIDTS", "SIDCC", "__Secure-1PSIDCC", "__Secure-3PSIDCC"
    )
    $pairs = New-Object System.Collections.Generic.List[string]
    foreach ($ln in $Lines) {
        $cols = $ln -split "`t"
        if ($cols.Count -ge 7 -and ($stableKeys -contains $cols[5])) {
            $pairs.Add("$($cols[5])=$($cols[6])")
        }
    }
    $sorted = ($pairs | Sort-Object -Unique) -join "`n"
    if ([string]::IsNullOrWhiteSpace($sorted)) {
        $sorted = ($Lines | Where-Object { -not $_.StartsWith("# ") } | Sort-Object) -join "`n"
    }
    $sha = [System.Security.Cryptography.SHA256]::Create()
    try {
        $bytes = [System.Text.Encoding]::UTF8.GetBytes($sorted)
        $hashBytes = $sha.ComputeHash($bytes)
        return ([BitConverter]::ToString($hashBytes) -replace '-', '').ToLowerInvariant()
    } finally {
        $sha.Dispose()
    }
}

function Try-ExportFromBrowser {
    param([string]$BrowserName, [string]$YtDlpPath)
    if (-not $YtDlpPath) { return $null }

    $tmpFile = Join-Path $env:TEMP ("kf_cookie_export_" + [guid]::NewGuid().ToString("N") + ".txt")
    try {
        $prevErr = $ErrorActionPreference
        $ErrorActionPreference = "Continue"
        $null = & $YtDlpPath --cookies-from-browser $BrowserName --cookies $tmpFile --skip-download --no-playlist --quiet "https://www.youtube.com/watch?v=jNQXAC9IVRw" 2>&1
        $exitCode = $LASTEXITCODE
        $ErrorActionPreference = $prevErr

        if ($exitCode -ne 0 -or -not (Test-Path $tmpFile)) {
            return $null
        }
        $raw = Get-Content $tmpFile -Encoding UTF8
        $filtered = Filter-YouTubeNetscapeLines -RawLines $raw
        if (-not (Test-HasYouTubeAuthCookies -Lines $filtered)) {
            return $null
        }
        return @{
            Source = "browser:$BrowserName"
            Lines  = $filtered
            Raw    = ($filtered -join "`n") + "`n"
        }
    } catch {
        return $null
    } finally {
        Remove-Item $tmpFile -Force -ErrorAction SilentlyContinue
    }
}

function Convert-JsonCookiesToNetscapeLines {
    param([string]$RawText)
    $trimmed = $RawText.Trim()
    if (-not $trimmed.StartsWith("[")) { return $null }
    try {
        $arr = $trimmed | ConvertFrom-Json
        if (-not $arr) { return $null }
        $lines = New-Object System.Collections.Generic.List[string]
        foreach ($c in $arr) {
            if (-not $c.domain -or -not $c.name) { continue }
            $dom = [string]$c.domain
            $includeSub = if ($dom.StartsWith(".")) { "TRUE" } else { "FALSE" }
            $path = if ($c.path) { [string]$c.path } else { "/" }
            $secure = if ($c.secure) { "TRUE" } else { "FALSE" }
            $exp = if ($c.expirationDate) { [long][Math]::Floor([double]$c.expirationDate) } else { 0 }
            $prefix = if ($c.httpOnly) { "#HttpOnly_" } else { "" }
            $lines.Add("$prefix$dom`t$includeSub`t$path`t$secure`t$exp`t$($c.name)`t$($c.value)")
        }
        return (Filter-YouTubeNetscapeLines -RawLines $lines.ToArray())
    } catch {
        return $null
    }
}

function Try-LoadFromLocalFiles {
    $candidates = New-Object System.Collections.Generic.List[System.IO.FileInfo]
    $dlDir = Join-Path $env:USERPROFILE "Downloads"
    if (Test-Path $dlDir) {
        $dlMatches = Get-ChildItem -Path $dlDir -File -ErrorAction SilentlyContinue | Where-Object {
            $_.Name -match '^(youtube\.com_cookies|www\.youtube\.com_cookies|cookies).*(\.txt|\.json)$'
        }
        foreach ($f in $dlMatches) { $candidates.Add($f) }
    }
    if (Test-Path $CookiesDest) {
        $candidates.Add((Get-Item $CookiesDest))
    }
    $ordered = $candidates | Sort-Object LastWriteTimeUtc -Descending
    foreach ($f in $ordered) {
        try {
            $rawText = Get-Content $f.FullName -Raw -Encoding UTF8
            if ([string]::IsNullOrWhiteSpace($rawText)) { continue }
            $filtered = Convert-JsonCookiesToNetscapeLines -RawText $rawText
            if (-not $filtered) {
                $filtered = Filter-YouTubeNetscapeLines -RawLines ($rawText -split "`r?`n")
            }
            if (Test-HasYouTubeAuthCookies -Lines $filtered) {
                return @{
                    Source = "file:$($f.Name)"
                    Lines  = $filtered
                    Raw    = ($filtered -join "`n") + "`n"
                }
            }
        } catch {}
    }
    return $null
}

# --- Main flow ---
$ytdlp = Find-YtDlp
$result = $null

if ($Browser -eq "auto") {
    $order = @()
    if (Test-Path "$env:APPDATA\Mozilla\Firefox\Profiles") { $order += "firefox" }
    # Only attempt Chromium browsers when they are NOT actively locking their SQLite DB
    if ((Test-Path "$env:LOCALAPPDATA\Microsoft\Edge\User Data") -and -not (Get-Process msedge -ErrorAction SilentlyContinue)) { $order += "edge" }
    if ((Test-Path "$env:LOCALAPPDATA\Google\Chrome\User Data") -and -not (Get-Process chrome -ErrorAction SilentlyContinue)) { $order += "chrome" }

    foreach ($b in $order) {
        $result = Try-ExportFromBrowser -BrowserName $b -YtDlpPath $ytdlp
        if ($result) { break }
    }
    if (-not $result) {
        $result = Try-LoadFromLocalFiles
    }
} elseif ($Browser -eq "file") {
    $result = Try-LoadFromLocalFiles
} else {
    $result = Try-ExportFromBrowser -BrowserName $Browser -YtDlpPath $ytdlp
}

if (-not $result) {
    Write-SyncLog "Tidak ada sumber cookie YouTube yang bisa dibaca (pastikan Firefox login YouTube atau export cookies.txt ke Downloads)." "WARN"
    exit 0
}

$newHash = Get-AuthCookieFingerprint -Lines $result.Lines
$oldHash = if (Test-Path $HashFile) { (Get-Content $HashFile -Raw).Trim() } else { "" }

# Always keep local backup updated if file content differs
$utf8NoBom = New-Object System.Text.UTF8Encoding($false)
[System.IO.File]::WriteAllText($CookiesDest, $result.Raw, $utf8NoBom)

if (-not $Force -and $oldHash -and ($oldHash -eq $newHash)) {
    Write-SyncLog "Cookie tidak berubah (source=$($result.Source), hash=$($newHash.Substring(0,12))...) -> skip upload agar InstaBox tetap tidur." "INFO"
    exit 0
}

if ($DryRun) {
    Write-SyncLog "[DryRun] Cookie berubah (source=$($result.Source), lines=$($result.Lines.Count), hash=$($newHash.Substring(0,12))...) -> siap upload ke $EdgeURL." "INFO"
    exit 0
}

if (-not (Test-Path $AccessKeyFile)) {
    Write-SyncLog "File .access-key tidak ditemukan di $Root — tidak bisa upload ke $EdgeURL." "ERROR"
    exit 1
}
$accessKey = (Get-Content $AccessKeyFile | Select-Object -First 1).Trim()
if (-not $accessKey) {
    Write-SyncLog "File .access-key kosong." "ERROR"
    exit 1
}

$uploadUrl = ($EdgeURL.TrimEnd("/")) + "/api/settings/cookies"
$resp = & curl.exe -s -w "`nHTTP=%{http_code}" -X POST `
    -H "Authorization: Bearer $accessKey" `
    -F "file=@$CookiesDest" `
    $uploadUrl

$httpMatch = [regex]::Match($resp, 'HTTP=(\d{3})\s*$')
$httpCode = if ($httpMatch.Success) { [int]$httpMatch.Groups[1].Value } else { 0 }

if ($httpCode -eq 200) {
    [System.IO.File]::WriteAllText($HashFile, $newHash, $utf8NoBom)
    Write-SyncLog "Upload cookies.txt BERHASIL ke $EdgeURL (source=$($result.Source), lines=$($result.Lines.Count), hash=$($newHash.Substring(0,12))...)." "OK"
    exit 0
}

Write-SyncLog "Upload gagal (HTTP $httpCode, source=$($result.Source)). Akan dicoba lagi pada jadwal berikutnya." "ERROR"
exit 1
