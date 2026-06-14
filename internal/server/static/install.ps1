# rdev-client one-click launcher for Windows
# Compatible with: PowerShell 2.0+ (Win7/8/8.1/10/11)
#
# One-liner:
#   powershell -Command "iwr -useb http://SERVER/install.ps1 | iex; RDev ws://SERVER"
#   powershell -Command "iwr -useb http://SERVER/install.ps1 | iex; RDev ws://SERVER -Id my-pc -Password secret"
#
# PS 2.0 (Win7/8):
#   powershell -Command "$wc=New-Object Net.WebClient; $wc.DownloadString('http://SERVER/install.ps1') | iex; RDev ws://SERVER"

# -- TLS compat ------------------------------------------------
try {
    # Win7/PowerShell 2.0 often defaults to SSL3/TLS1.0. Force TLS1.2 before HTTPS downloads.
    [Net.ServicePointManager]::SecurityProtocol = [Enum]::ToObject([Net.SecurityProtocolType], 3072)
} catch {
    try { [Net.ServicePointManager]::SecurityProtocol = [Enum]::ToObject([Net.SecurityProtocolType], 768) } catch {}
}

# ── Mirror list ─────────────────────────────────────────────
$script:Mirrors = @(
    'gh.llkk.cc',
    'gh.idayer.com',
    'gh.ddlc.top',
    'gh-proxy.com',
    'ghfast.top',
    'ghproxy.net'
)
$script:Repo = 'icepie/rdev'

function global:RDev {
    <#
    .SYNOPSIS
    Download and run rdev-client (no install needed)

    .PARAMETER Server
    Server WebSocket URL (e.g. ws://1.2.3.4:8080 or wss://example.com)

    .PARAMETER Id
    Device ID (default: hostname)

    .PARAMETER Password
    Password for SSH auth

    .PARAMETER Shell
    Shell path

    .PARAMETER SshPort
    Server SSH port (for hint display)

    .PARAMETER Version
    Client version to download (default: latest)

    .PARAMETER Mirror
    Download mirror: auto|none|host (default: auto)
    #>
    [CmdletBinding()]
    param(
        [Parameter(Position=0, Mandatory=$true)]
        [string]$Server,

        [string]$Id = '',
        [string]$Password = '',
        [string]$Shell = '',
        [string]$SshPort = '',
        [string]$Version = '',
        [string]$Mirror = 'auto'
    )

    # ── Detect arch ─────────────────────────────────────────
    $Arch = 'amd64'
    if ($env:PROCESSOR_ARCHITECTURE -eq 'ARM64' -or $env:PROCESSOR_ARCHITEW6432 -eq 'ARM64') { $Arch = 'arm64' }

    # ── Resolve version & URL ───────────────────────────────
    $Binary = "rdev-client-windows-$Arch.exe"
    if ($Version) { $Tag = "v$Version" } else {
        $Tag = 'latest'
        try {
            try { $rel = Invoke-RestMethod -Uri "https://api.github.com/repos/$script:Repo/releases/latest" -ErrorAction Stop; $Tag = $rel.tag_name }
            catch { $wc = New-Object Net.WebClient; $j = $wc.DownloadString("https://api.github.com/repos/$script:Repo/releases/latest"); if ($j -match '"tag_name"\s*:\s*"([^"]+)"') { $Tag = $Matches[1] } }
        } catch { $Tag = 'latest' }
    }

    $Base = "https://github.com/$script:Repo/releases"
    $GH_URL = if ($Tag -eq 'latest') { "$Base/latest/download/$Binary" } else { "$Base/download/$Tag/$Binary" }

    # ── Download helper ──────────────────────────────────────
    function Dl([string]$Url, [string]$Out) {
        try { try { Invoke-WebRequest -Uri $Url -OutFile $Out -ErrorAction Stop } catch { $w = New-Object Net.WebClient; $w.DownloadFile($Url, $Out) }; return $true } catch { return $false }
    }

    # ── Download (mirror → github) ────────────────────────────
    $OutPath = Join-Path $env:TEMP "rdev-client.exe"
    $OK = $false

    Write-Host "  Downloading rdev-client (windows/$Arch)..." -ForegroundColor Cyan

    if ($Mirror -eq 'auto') {
        foreach ($M in $script:Mirrors) {
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

    if (-not $OK) { Write-Error "Download failed"; return }

    # ── Run ──────────────────────────────────────────────────
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
}

# ── Banner: show usage hint when piped via iex ──────────────
Write-Host ""
Write-Host "  RDev client ready!" -ForegroundColor Green
Write-Host "  Usage: " -NoNewline; Write-Host "RDev <server-url> [options]" -ForegroundColor Cyan
Write-Host "  Example: " -NoNewline; Write-Host "RDev wss://rdev.example.com -Password <your-password>" -ForegroundColor Gray
Write-Host ""
