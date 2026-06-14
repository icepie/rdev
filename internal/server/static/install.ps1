# rdev-client one-click launcher for Windows
# Download to %TEMP% and run, no install needed
# Compatible with: PowerShell 2.0+ (Win7/8/8.1/10/11)
#
# Usage:
#   PS3+: $u='http://SERVER/install.ps1';iwr $u -OutFile $env:TEMP\rdev.ps1;& $env:TEMP\rdev.ps1 ws://SERVER
#   PS2:  $wc=New-Object Net.WebClient;$wc.DownloadFile('http://SERVER/install.ps1',"$env:TEMP\rdev.ps1");& "$env:TEMP\rdev.ps1" ws://SERVER

param(
    [Parameter(Position=0)]
    [string]$Server = '',

    [string]$Id = '',
    [string]$Password = '',
    [string]$Shell = '',
    [string]$SshPort = '',
    [string]$Version = '',
    [string]$Repo = 'icepie/rdev',
    [string]$Mirror = 'auto'
)

# ── TLS compat ──────────────────────────────────────────────
try { [Net.ServicePointManager]::SecurityProtocol = [Net.SecurityProtocolType]::Tls12 -bor [Net.ServicePointManager]::SecurityProtocol } catch {
    try { [Net.ServicePointManager]::SecurityProtocol = [Net.SecurityProtocolType]::Tls11 -bor [Net.ServicePointManager]::SecurityProtocol } catch {}
}

if (-not $Server) {
    Write-Host "rdev-client launcher (no install, run from %TEMP%)" -ForegroundColor Cyan
    Write-Host ""
    Write-Host "Usage: .\install.ps1 ws://SERVER:PORT [-Id ID] [-Password PW]" -ForegroundColor White
    Write-Host ""
    Write-Host "  -Server    WebSocket URL (required)" -ForegroundColor Gray
    Write-Host "  -Id        Device ID (default: hostname)" -ForegroundColor Gray
    Write-Host "  -Password  SSH auth password" -ForegroundColor Gray
    Write-Host "  -Shell     Shell path" -ForegroundColor Gray
    Write-Host "  -SshPort   Server SSH port (hint display)" -ForegroundColor Gray
    Write-Host "  -Version   Client version (default: latest)" -ForegroundColor Gray
    Write-Host "  -Mirror    auto|none|HOST (default: auto, try CN mirrors)" -ForegroundColor Gray
    Write-Host ""
    Write-Host "Examples:" -ForegroundColor White
    Write-Host "  .\install.ps1 ws://1.2.3.4:8080 -Id my-pc -Password secret" -ForegroundColor Gray
    exit 0
}

# ── Detect arch ─────────────────────────────────────────────
$Arch = 'amd64'
if ($env:PROCESSOR_ARCHITECTURE -eq 'ARM64' -or $env:PROCESSOR_ARCHITEW6432 -eq 'ARM64') { $Arch = 'arm64' }

# ── Version & URL ──────────────────────────────────────────
$Binary = "rdev-client-windows-$Arch.exe"
if ($Version) { $Tag = "v$Version" } else {
    $Tag = 'latest'
    try {
        try { $rel = Invoke-RestMethod -Uri "https://api.github.com/repos/$Repo/releases/latest" -ErrorAction Stop; $Tag = $rel.tag_name }
        catch { $wc = New-Object Net.WebClient; $json = $wc.DownloadString("https://api.github.com/repos/$Repo/releases/latest"); if ($json -match '"tag_name"\s*:\s*"([^"]+)"') { $Tag = $Matches[1] } }
    } catch { $Tag = 'latest' }
}

if ($Tag -eq 'latest') { $GH_URL = "https://github.com/$Repo/releases/latest/download/$Binary" }
else { $GH_URL = "https://github.com/$Repo/releases/download/$Tag/$Binary" }

# ── Download helper ─────────────────────────────────────────
function Dl([string]$Url, [string]$Out) {
    try { try { Invoke-WebRequest -Uri $Url -OutFile $Out -ErrorAction Stop } catch { $wc = New-Object Net.WebClient; $wc.DownloadFile($Url, $Out) }; return $true } catch { return $false }
}

# ── Download (mirror → github) ─────────────────────────────
$OutPath = Join-Path $env:TEMP "rdev-client.exe"
$Mirrors = @('ghgo.xyz', 'gh-proxy.com', 'ghfast.top')
$OK = $false

Write-Host "  Downloading rdev-client (windows/$Arch)..." -ForegroundColor Cyan

if ($Mirror -eq 'auto') {
    foreach ($M in $Mirrors) {
        Write-Host "  Trying $M..." -ForegroundColor DarkGray
        if (Dl "https://$M/$GH_URL" $OutPath) {
            $f = Get-Item $OutPath -EA SilentlyContinue
            if ($f -and $f.Length -gt 0) { $OK = $true; Write-Host "  OK via $M" -ForegroundColor Green; break }
        }
    }
} elseif ($Mirror -ne 'none' -and $Mirror -ne '') {
    Write-Host "  Trying $Mirror..." -ForegroundColor DarkGray
    if (Dl "https://$Mirror/$GH_URL" $OutPath) {
        $f = Get-Item $OutPath -EA SilentlyContinue
        if ($f -and $f.Length -gt 0) { $OK = $true; Write-Host "  OK via $Mirror" -ForegroundColor Green }
    }
}

if (-not $OK) {
    Write-Host "  Trying github.com..." -ForegroundColor DarkGray
    if (Dl $GH_URL $OutPath) {
        $f = Get-Item $OutPath -EA SilentlyContinue
        if ($f -and $f.Length -gt 0) { $OK = $true; Write-Host "  OK via github.com" -ForegroundColor Green }
    }
}

if (-not $OK) { Write-Error "Download failed"; exit 1 }

# ── Run ─────────────────────────────────────────────────────
$A = @("-s", $Server)
if ($Id)       { $A += @("-i", $Id) }
if ($Password)  { $A += @("-p", $Password) }
if ($Shell)     { $A += @("-S", $Shell) }
if ($SshPort)   { $A += @("--ssh-port", $SshPort) }

Write-Host ""
Write-Host "  Starting rdev-client..." -ForegroundColor Cyan
Write-Host "  $OutPath $($A -join ' ')" -ForegroundColor Gray
Write-Host ""

& $OutPath @A
