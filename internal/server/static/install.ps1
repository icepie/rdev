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

# -- WinPTY fallback for legacy Windows -----------------------
$script:WinPTYVersion = '0.4.3'
$script:WinPTYAsset = "winpty-$script:WinPTYVersion-msvc2015.zip"
$script:WinPTYRepo = 'rprichard/winpty'
$script:WinPTYDir = Join-Path $env:TEMP 'rdev-winpty'

function Get-WindowsMajorVersion {
    try { return [int]([Environment]::OSVersion.Version.Major) } catch { return 0 }
}

function Expand-ZipLegacy([string]$Zip, [string]$Dest) {
    if (Test-Path $Dest) { Remove-Item -Recurse -Force $Dest -EA SilentlyContinue }
    New-Item -ItemType Directory -Force -Path $Dest | Out-Null
    try {
        Add-Type -AssemblyName System.IO.Compression.FileSystem -ErrorAction Stop
        [System.IO.Compression.ZipFile]::ExtractToDirectory($Zip, $Dest)
        return $true
    } catch {
        try {
            $sh = New-Object -ComObject Shell.Application
            $zipNs = $sh.NameSpace($Zip)
            $dstNs = $sh.NameSpace($Dest)
            if ($zipNs -and $dstNs) { $dstNs.CopyHere($zipNs.Items(), 0x14); Start-Sleep -Seconds 2; return $true }
        } catch {}
    }
    return $false
}

function Install-WinPTYIfLegacy([string]$Arch, [string]$Mirror) {
    if ((Get-WindowsMajorVersion) -ge 10) { return '' }
    $Dll = Join-Path $script:WinPTYDir 'winpty.dll'
    $Agent = Join-Path $script:WinPTYDir 'winpty-agent.exe'
    if ((Test-Path $Dll) -and (Test-Path $Agent)) { return $script:WinPTYDir }

    Write-Host "  Legacy Windows detected, preparing WinPTY..." -ForegroundColor Cyan
    $Tmp = Join-Path $env:TEMP $script:WinPTYAsset
    $Base = "https://github.com/$script:WinPTYRepo/releases/download/$script:WinPTYVersion/$script:WinPTYAsset"
    $OK = $false
    if ($Mirror -eq 'auto') {
        foreach ($M in $script:Mirrors) {
            Write-Host "  Trying WinPTY via $M..." -ForegroundColor DarkGray
            if (Dl "https://$M/$Base" $Tmp) { $f = Get-Item $Tmp -EA SilentlyContinue; if ($f -and $f.Length -gt 0) { $OK = $true; break } }
        }
    } elseif ($Mirror -ne 'none' -and $Mirror -ne '') {
        Write-Host "  Trying WinPTY via $Mirror..." -ForegroundColor DarkGray
        if (Dl "https://$Mirror/$Base" $Tmp) { $f = Get-Item $Tmp -EA SilentlyContinue; if ($f -and $f.Length -gt 0) { $OK = $true } }
    }
    if (-not $OK) {
        Write-Host "  Trying WinPTY via github.com..." -ForegroundColor DarkGray
        if (Dl $Base $Tmp) { $f = Get-Item $Tmp -EA SilentlyContinue; if ($f -and $f.Length -gt 0) { $OK = $true } }
    }
    if (-not $OK) { Write-Warning "WinPTY download failed; falling back to pipe shell"; return '' }

    $Extract = Join-Path $env:TEMP 'rdev-winpty-extract'
    if (-not (Expand-ZipLegacy $Tmp $Extract)) { Write-Warning "WinPTY unzip failed; falling back to pipe shell"; return '' }
    $SrcArch = if ($Arch -eq 'arm64') { 'x64' } elseif ($Arch -eq 'amd64') { 'x64' } else { 'ia32' }
    $Src = Join-Path $Extract (Join-Path $SrcArch 'bin')
    New-Item -ItemType Directory -Force -Path $script:WinPTYDir | Out-Null
    Copy-Item (Join-Path $Src 'winpty.dll') $script:WinPTYDir -Force
    Copy-Item (Join-Path $Src 'winpty-agent.exe') $script:WinPTYDir -Force
    Write-Host "  OK WinPTY ready" -ForegroundColor Green
    return $script:WinPTYDir
}

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
    if ($Version) { if ($Version -like 'v*') { $Tag = $Version } else { $Tag = "v$Version" } } else {
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
        try {
            $w = New-Object Net.WebClient
            $w.Headers.Add('User-Agent', 'rdev-installer')
            $w.DownloadFile($Url, $Out)
            return $true
        } catch {
            Write-Host "  Download error: $($_.Exception.Message)" -ForegroundColor DarkGray
            try { if ($_.Exception.InnerException) { Write-Host "  Inner error: $($_.Exception.InnerException.Message)" -ForegroundColor DarkGray } } catch {}
            return $false
        }
    }

    # ── Download (mirror → github) ────────────────────────────
    $SafeTag = $Tag -replace '[^A-Za-z0-9_.-]', '-'
    $OutPath = Join-Path $env:TEMP "rdev-client-$SafeTag-windows-$Arch.exe"
    $OK = $false

    Write-Host "  Downloading rdev-client (windows/$Arch)..." -ForegroundColor Cyan

    if ($Mirror -eq 'auto' -and $Tag -ne 'latest') {
        foreach ($M in $script:Mirrors) {
            Write-Host "  Trying $M..." -ForegroundColor DarkGray
            if (Dl "https://$M/$GH_URL" $OutPath) {
                $f = Get-Item $OutPath -EA SilentlyContinue
                if ($f -and $f.Length -gt 0) { $OK = $true; Write-Host "  OK via $M" -ForegroundColor Green; break }
            }
        }
    } elseif ($Mirror -ne 'none' -and $Mirror -ne '' -and $Tag -ne 'latest') {
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

    $WinPTYDir = Install-WinPTYIfLegacy $Arch $Mirror

    # ── Run ──────────────────────────────────────────────────
    $A = @("-s", $Server)
    if ($Id)       { $A += @("-i", $Id) }
    if ($Password)  { $A += @("-p", $Password) }
    if ($Shell)     { $A += @("-S", $Shell) }
    if ($SshPort)   { $A += @("--ssh-port", $SshPort) }

    if ($WinPTYDir) { $env:RDEV_WINPTY_DIR = $WinPTYDir }

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
Write-Host "  Example: " -NoNewline; Write-Host "RDev wss://rdev.example.com -Password your-password" -ForegroundColor Gray
Write-Host ""
