# rdev-client install & launch script for Windows
# Compatible with: PowerShell 2.0+ (Win7/8/8.1/10/11)
#
# Usage:
#   Download & run:
#     Invoke-WebRequest http://SERVER:PORT/install.ps1 -OutFile install.ps1
#     .\install.ps1 -Server ws://SERVER:PORT -Id my-pc -Password secret
#
#   One-liner (PS 3.0+):
#     $u='http://SERVER:PORT/install.ps1';iwr $u -OutFile $env:TEMP\rdev.ps1;& $env:TEMP\rdev.ps1 -Server ws://SERVER:PORT -Id my-pc
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
    [string]$Repo = 'icepie/rdev'
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
    $DL_URL = "$BaseURL/latest/download/$Binary"
} else {
    $DL_URL = "$BaseURL/download/$Tag/$Binary"
}

# ── Determine install path ─────────────────────────────────
if (-not $InstallDir) {
    $InstallDir = Join-Path $env:LOCALAPPDATA 'rdev'
}
if (-not (Test-Path $InstallDir)) {
    New-Item -ItemType Directory -Path $InstallDir -Force | Out-Null
}
$InstallPath = Join-Path $InstallDir 'rdev-client.exe'

# ── Download ────────────────────────────────────────────────
Write-Host "  Downloading rdev-client (windows/$Arch)..." -ForegroundColor Cyan

try {
    try {
        # PS 3.0+
        Invoke-WebRequest -Uri $DL_URL -OutFile $InstallPath -ErrorAction Stop
    } catch {
        # PS 2.0 fallback
        $wc = New-Object Net.WebClient
        $wc.DownloadFile($DL_URL, $InstallPath)
    }
} catch {
    Write-Error "Download failed: $_"
    exit 1
}

if (-not (Test-Path $InstallPath) -or (Get-Item $InstallPath).Length -eq 0) {
    Write-Error "Downloaded file is empty or missing"
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
$Args = @("-s", $Server)
if ($Id)       { $Args += @("-i", $Id) }
if ($Password)  { $Args += @("-p", $Password) }
if ($Shell)     { $Args += @("-S", $Shell) }
if ($SshPort)   { $Args += @("--ssh-port", $SshPort) }

# ── Launch ──────────────────────────────────────────────────
Write-Host ""
Write-Host "  Starting rdev-client..." -ForegroundColor Cyan
Write-Host "  $InstallPath $($Args -join ' ')" -ForegroundColor Gray
Write-Host ""

& $InstallPath @Args
