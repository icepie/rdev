# rdev-client one-click runner for Windows
# Compatible with: PowerShell 2.0+ (Win7/8/8.1/10/11)
#
# One-liner:
#   powershell -Command "iwr -useb http://SERVER/run.ps1 | iex; RDev ws://SERVER"
#   powershell -Command "iwr -useb http://SERVER/run.ps1 | iex; RDev ws://SERVER -Id my-pc -Password secret"
#
# PS 2.0 (Win7/8):
#   powershell -Command "$wc=New-Object Net.WebClient; $wc.DownloadString('http://SERVER/run.ps1') | iex; RDev ws://SERVER"

# -- TLS compat ------------------------------------------------
try {
    # Win7/PowerShell 2.0 often defaults to SSL3/TLS1.0. Force TLS1.2 before HTTPS downloads.
    [Net.ServicePointManager]::SecurityProtocol = [Enum]::ToObject([Net.SecurityProtocolType], 3072)
} catch {
    try { [Net.ServicePointManager]::SecurityProtocol = [Enum]::ToObject([Net.SecurityProtocolType], 768) } catch {}
}

# ── Mirror list ─────────────────────────────────────────────
$script:Mirrors = @(
    'gh.idayer.com',
    'gh.ddlc.top',
    'gh-proxy.com',
    'ghfast.top',
    'ghproxy.net',
    'ghproxy.cc',
    'gh-proxy.net',
    'ghproxy.cfd',
    'github.moeyy.xyz',
    'hub.gitmirror.com',
    'ghproxy.1888866.xyz',
    'ghproxy.sakuramoe.dev'
)
if ($env:RDEV_MIRRORS) {
    $customMirrors = @()
    foreach ($m in ($env:RDEV_MIRRORS -split '[,\s]+')) { if ($m) { $customMirrors += $m } }
    if ($customMirrors.Count -gt 0) { $script:Mirrors = $customMirrors }
}
$script:Repo = 'icepie/rdev'

function Convert-RDevMirrorUrl([string]$Mirror, [string]$Url) {
    return "https://$Mirror/$Url"
}

function Get-RDevUrlText([string]$Url) {
    try {
        $w = New-Object Net.WebClient
        $w.Headers.Add('User-Agent', 'rdev-runner')
        return $w.DownloadString($Url)
    } catch { return '' }
}

function Get-RDevLatestTag {
    $Api = "https://api.github.com/repos/$script:Repo/releases/latest"
    foreach ($M in $script:Mirrors) {
        $j = Get-RDevUrlText (Convert-RDevMirrorUrl $M $Api)
        if ($j -match '"tag_name"\s*:\s*"([^"]+)"') { return $Matches[1] }
    }
    $direct = Get-RDevUrlText $Api
    if ($direct -match '"tag_name"\s*:\s*"([^"]+)"') { return $Matches[1] }
    return 'latest'
}

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
            if (Dl (Convert-RDevMirrorUrl $M $Base) $Tmp) { $f = Get-Item $Tmp -EA SilentlyContinue; if ($f -and $f.Length -gt 0) { $OK = $true; break } }
        }
    } elseif ($Mirror -ne 'none' -and $Mirror -ne '') {
        Write-Host "  Trying WinPTY via $Mirror..." -ForegroundColor DarkGray
        if (Dl (Convert-RDevMirrorUrl $Mirror $Base) $Tmp) { $f = Get-Item $Tmp -EA SilentlyContinue; if ($f -and $f.Length -gt 0) { $OK = $true } }
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

function Test-RDevAdministrator {
    try {
        $id = [Security.Principal.WindowsIdentity]::GetCurrent()
        $principal = New-Object Security.Principal.WindowsPrincipal($id)
        return $principal.IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)
    } catch { return $false }
}

function Wait-RDevElevationKey {
    try {
        Write-Host "  Not running as Administrator. Press any key within 3 seconds to run elevated; waiting continues normal mode... " -NoNewline -ForegroundColor Yellow
        for ($i = 0; $i -lt 30; $i++) {
            if ([Console]::KeyAvailable) {
                [void][Console]::ReadKey($true)
                Write-Host ""
                return $true
            }
            Start-Sleep -Milliseconds 100
        }
        Write-Host ""
    } catch {}
    return $false
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

    .PARAMETER Client
    Client flavor: go compatible client or rs performance client (default: go)

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
        [ValidateSet('go','rs')]
        [string]$Client = 'go',
        [string]$Mirror = 'auto'
    )

    $Elevate = $false
    if (-not (Test-RDevAdministrator)) {
        if (Wait-RDevElevationKey) {
            $Elevate = $true
            Write-Host "  Elevation requested; will start rdev-client with UAC after download." -ForegroundColor Cyan
        } else {
            Write-Host "  Continuing in normal user mode." -ForegroundColor DarkGray
        }
    }

    # ── Detect arch ─────────────────────────────────────────
    $Arch = 'amd64'
    if ($env:PROCESSOR_ARCHITECTURE -eq 'ARM64' -or $env:PROCESSOR_ARCHITEW6432 -eq 'ARM64') { $Arch = 'arm64' }

    # ── Resolve version & URL ───────────────────────────────
    $WindowsMajor = Get-WindowsMajorVersion
    if ($Version) { if ($Version -like 'v*') { $Tag = $Version } else { $Tag = "v$Version" } } else {
        $Tag = Get-RDevLatestTag
    }

    $Base = "https://github.com/$script:Repo/releases"
    if ($Client -eq 'rs') {
        if ($WindowsMajor -gt 0 -and $WindowsMajor -lt 10) {
            $Asset = 'rdev-client-gpu-windows-win7-amd64.zip'
        } elseif ($Arch -eq 'arm64') {
            $Asset = 'rdev-client-gpu-windows-arm64.zip'
        } else {
            $Asset = 'rdev-client-gpu-windows-amd64.zip'
        }
        $PackageKind = 'zip'
    } else {
        $Asset = "rdev-client-windows-$Arch.exe"
        $PackageKind = 'exe'
    }
    $GH_URL = if ($Tag -eq 'latest') { "$Base/latest/download/$Asset" } else { "$Base/download/$Tag/$Asset" }

    # ── Download helper ──────────────────────────────────────
    function Dl([string]$Url, [string]$Out) {
        try {
            $w = New-Object Net.WebClient
            $w.Headers.Add('User-Agent', 'rdev-runner')
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
    $OutPath = if ($PackageKind -eq 'zip') { Join-Path $env:TEMP "rdev-client-gpu-$SafeTag-windows-amd64.zip" } else { Join-Path $env:TEMP "rdev-client-$SafeTag-windows-$Arch.exe" }
    $OK = $false

    $ClientName = if ($Client -eq 'rs') { 'rdev-client-gpu' } else { 'rdev-client' }
    Write-Host "  Downloading $ClientName package (windows/$Arch)..." -ForegroundColor Cyan

    if ($Mirror -eq 'auto' -and $Tag -ne 'latest') {
        foreach ($M in $script:Mirrors) {
            Write-Host "  Trying $M..." -ForegroundColor DarkGray
            if (Dl (Convert-RDevMirrorUrl $M $GH_URL) $OutPath) {
                $f = Get-Item $OutPath -EA SilentlyContinue
                if ($f -and $f.Length -gt 0) { $OK = $true; Write-Host "  OK via $M" -ForegroundColor Green; break }
            }
        }
    } elseif ($Mirror -ne 'none' -and $Mirror -ne '' -and $Tag -ne 'latest') {
        Write-Host "  Trying $Mirror..." -ForegroundColor DarkGray
        if (Dl (Convert-RDevMirrorUrl $Mirror $GH_URL) $OutPath) {
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

    if ($PackageKind -eq 'zip') {
        $ExtractDir = Join-Path $env:TEMP "rdev-client-gpu-$SafeTag-windows-amd64"
        if (-not (Expand-ZipLegacy $OutPath $ExtractDir)) { Write-Error "Package unzip failed"; return }
        $Exe = Get-ChildItem -Path $ExtractDir -Recurse -Filter 'rdev-client-gpu.exe' -EA SilentlyContinue | Select-Object -First 1
        if (-not $Exe) { Write-Error "rdev-client-gpu.exe not found in package"; return }
        $RunPath = $Exe.FullName
    } else {
        $RunPath = $OutPath
    }

    $WinPTYDir = Install-WinPTYIfLegacy $Arch $Mirror

    # ── Run ──────────────────────────────────────────────────
    $A = @("-s", $Server)
    if ($Id)       { $A += @("-i", $Id) }
    if ($Password)  { $A += @("-p", $Password) }
    if ($Shell)     { if ($Client -eq 'rs') { $A += @("--shell", $Shell) } else { $A += @("-S", $Shell) } }
    if ($SshPort -and $Client -ne 'rs')   { $A += @("--ssh-port", $SshPort) }

    if ($WinPTYDir) { $env:RDEV_WINPTY_DIR = $WinPTYDir }

    Write-Host ""
    Write-Host "  Starting $ClientName..." -ForegroundColor Cyan
    Write-Host "  $RunPath $($A -join ' ')" -ForegroundColor Gray
    Write-Host ""

    if ($Elevate) {
        try {
            Start-Process -FilePath $RunPath -ArgumentList $A -Verb RunAs | Out-Null
            return
        } catch {
            Write-Warning "Elevation failed; continuing in normal user mode: $($_.Exception.Message)"
        }
    }

    & $RunPath @A
}

# ── Banner: show usage hint when piped via iex ──────────────
Write-Host ""
Write-Host "  RDev client ready!" -ForegroundColor Green
Write-Host "  Usage: " -NoNewline; Write-Host "RDev <server-url> [options]" -ForegroundColor Cyan
Write-Host "  Example: " -NoNewline; Write-Host "RDev wss://rdev.example.com -Password your-password" -ForegroundColor Gray
Write-Host ""
