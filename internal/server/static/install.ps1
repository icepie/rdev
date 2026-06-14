# rdev-client install & launch script for Windows
# Compatible with: PowerShell 2.0+ (Win7/8/8.1/10/11)
#
# Usage:
#   Download & run:
#     Invoke-WebRequest http://SERVER:PORT/install.ps1 -OutFile install.ps1
#     .\install.ps1 -Server ws://SERVER:PORT -Id my-pc -Password secret
#
#   One-liner (PS 3.0+):
#     $u='http://SERVER:PORT/install.ps1';iwr $u -OutFile $env:TEMP\rdev.ps1;& $env:TEMP\rdev.ps1 -Server ws://SERVER:PORT
#
#   One-liner (PS 2.0 / Win7):
#     $wc=New-Object Net.WebClient;$wc.DownloadFile('http://SERVER:PORT/install.ps1',"$env:TEMP\rdev.ps1");& "$env:TEMP\rdev.ps1" -Server ws://SERVER:PORT

param(
    [Parameter(Position=0)]
    [string]$Server = '',

    [string]$Id = '',
    [string]$Password = '',
    [string]$Shell = '',
    [string]$SshPort = '',
    [string]$Version = '',
    [string]$InstallDir = '',
    [string]$Repo = 'icepie/rdev',
    [string]$Mirror = 'auto'
)

# ── Compat: TLS 1.2 for GitHub ─────────────────────────────
try {
    [Net.ServicePointManager]::SecurityProtocol = `
        [Net.SecurityProtocolType]::Tls12 -bor `
        [Net.ServicePointManager]::SecurityProtocol
} catch {
    try {
        [Net.ServicePointManager]::SecurityProtocol = `
            [Net.SecurityProtocolType]::Tls11 -bor `
            [Net.ServicePointManager]::SecurityProtocol
    } catch {}
}

# ── If no Server, show help and exit ────────────────────────
if (-not $Server) {
    Write-Host "rdev-client installer for Windows" -ForegroundColor Cyan
    Write-Host ""
    Write-Host "Usage: .\install.ps1 -Server <ws://URL> [-Id ID] [-Password PW]" -ForegroundColor White
    Write-Host ""
    Write-Host "Options:" -ForegroundColor White
    Write-Host "  -Server    Server WebSocket URL (required, e.g. ws://1.2.3.4:8080)" -ForegroundColor Gray
    Write-Host "  -Id        Device ID (default: hostname)" -ForegroundColor Gray
    Write-Host "  -Password  Password for SSH auth" -ForegroundColor Gray
    Write-Host "  -Shell     Shell path" -ForegroundColor Gray
    Write-Host "  -SshPort   Server SSH port (for hint display)" -ForegroundColor Gray
    Write-Host "  -Version   Client version to download" -ForegroundColor Gray
    Write-Host "  -InstallDir Install directory" -ForegroundColor Gray
    Write-Host "  -Mirror    Download mirror (default: auto, try CN mirrors first)" -ForegroundColor Gray
    Write-Host "  -Mirror none  Skip mirrors, use github.com directly" -ForegroundColor Gray
    Write-Host ""
    Write-Host "Examples:" -ForegroundColor White
    Write-Host "  .\install.ps1 ws://192.168.1.100:8080 -Id my-pc -Password secret" -ForegroundColor Gray
    Write-Host "  .\install.ps1 -Server ws://10.0.0.1:8080 -SshPort 2222" -ForegroundColor Gray
    exit 0
}

# ── Detect architecture ─────────────────────────────────────
$Arch = 'amd64'
$procArch = $env:PROCESSOR_ARCHITECTURE
$procArchWow = $env:PROCESSOR_ARCHITEW6432
if ($procArch -eq 'ARM64' -or $procArchWow -eq 'ARM64') { $Arch = 'arm64' }
elseif ($procArch -eq 'AMD64' -or $procArch -eq 'x64' -or $procArchWow -eq 'AMD64' -or $procArchWow -eq 'x64') { $Arch = 'amd64' }

# ── Determine version & download URL ───────────────────────
$BaseURL = "https://github.com/$Repo/releases"
$Binary = "rdev-client-windows-$Arch.exe"

if ($Version) {
    $Tag = "v$Version"
} else {
    $Tag = 'latest'
    try {
        $apiURL = "https://api.github.com/repos/$Repo/releases/latest"
        try {
            # PS 3.0+
            $rel = Invoke-RestMethod -Uri $apiURL -ErrorAction Stop
            $Tag = $rel.tag_name
        } catch {
            # PS 2.0 fallback
            $wc = New-Object Net.WebClient
            $json = $wc.DownloadString($apiURL)
            if ($json -match '"tag_name"\s*:\s*"([^"]+)"') { $Tag = $Matches[1] }
        }
    } catch { $Tag = 'latest' }
}

if ($Tag -eq 'latest') {
    $GITHUB_URL = "$BaseURL/latest/download/$Binary"
} else {
    $GITHUB_URL = "$BaseURL/download/$Tag/$Binary"
}

# ── Determine install path ─────────────────────────────────
if (-not $InstallDir) {
    $InstallDir = Join-Path $env:LOCALAPPDATA 'rdev'
}
if (-not (Test-Path $InstallDir)) {
    New-Item -ItemType Directory -Path $InstallDir -Force | Out-Null
}
$InstallPath = Join-Path $InstallDir 'rdev-client.exe'

# ── Download helper ─────────────────────────────────────────
function Download-File([string]$Url, [string]$Out) {
    try {
        try {
            # PS 3.0+
            Invoke-WebRequest -Uri $Url -OutFile $Out -ErrorAction Stop
        } catch {
            # PS 2.0 fallback
            $wc = New-Object Net.WebClient
            $wc.DownloadFile($Url, $Out)
        }
        return $true
    } catch { return $false }
}

# ── Download (mirror → github fallback) ─────────────────────
$Mirrors = @('ghgo.xyz', 'gh-proxy.com', 'ghfast.top')
$DL_OK = $false

Write-Host "  Downloading rdev-client (windows/$Arch)..." -ForegroundColor Cyan

# Try mirrors first
if ($Mirror -eq 'auto') {
    foreach ($M in $Mirrors) {
        $mirrorUrl = "https://$M/$GITHUB_URL"
        Write-Host "  Trying mirror: $M..." -ForegroundColor DarkGray
        if (Download-File $mirrorUrl $InstallPath) {
            $f = Get-Item $InstallPath -ErrorAction SilentlyContinue
            if ($f -and $f.Length -gt 0) {
                $DL_OK = $true
                Write-Host "  OK via $M" -ForegroundColor Green
                break
            }
        }
    }
} elseif ($Mirror -ne 'none' -and $Mirror -ne '') {
    $mirrorUrl = "https://$Mirror/$GITHUB_URL"
    Write-Host "  Trying mirror: $Mirror..." -ForegroundColor DarkGray
    if (Download-File $mirrorUrl $InstallPath) {
        $f = Get-Item $InstallPath -ErrorAction SilentlyContinue
        if ($f -and $f.Length -gt 0) { $DL_OK = $true; Write-Host "  OK via $Mirror" -ForegroundColor Green }
    }
}

# Fallback to direct GitHub
if (-not $DL_OK) {
    Write-Host "  Trying github.com directly..." -ForegroundColor DarkGray
    if (Download-File $GITHUB_URL $InstallPath) {
        $f = Get-Item $InstallPath -ErrorAction SilentlyContinue
        if ($f -and $f.Length -gt 0) { $DL_OK = $true; Write-Host "  OK via github.com" -ForegroundColor Green }
    }
}

if (-not $DL_OK) {
    Write-Error "Download failed"
    exit 1
}

Write-Host "  Installed: $InstallPath" -ForegroundColor Green

# ── Add to PATH if needed ──────────────────────────────────
$pathDir = Split-Path $InstallPath
if ($env:PATH -notlike "*$pathDir*") {
    $env:PATH = "$pathDir;$env:PATH"
    Write-Host "  Added to PATH (current session)" -ForegroundColor Yellow
}

# ── Build launch arguments ─────────────────────────────────
$LaunchArgs = @("-s", $Server)
if ($Id)       { $LaunchArgs += @("-i", $Id) }
if ($Password)  { $LaunchArgs += @("-p", $Password) }
if ($Shell)     { $LaunchArgs += @("-S", $Shell) }
if ($SshPort)   { $LaunchArgs += @("--ssh-port", $SshPort) }

# ── Launch ──────────────────────────────────────────────────
Write-Host ""
Write-Host "  Starting rdev-client..." -ForegroundColor Cyan
Write-Host "  $InstallPath $($LaunchArgs -join ' ')" -ForegroundColor Gray
Write-Host ""

& $InstallPath @LaunchArgs
