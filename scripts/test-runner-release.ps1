# Run: pwsh -NoProfile -File scripts/test-runner-release.ps1
# Live metadata + real Windows CGO download: add -Live (does not launch a client).
param([switch]$Live, [string]$Server = 'https://r.geofun.cn')
$ErrorActionPreference = 'Stop'
$OldTemp = $env:TEMP
$OldArch = $env:PROCESSOR_ARCHITECTURE
$OldNativeArch = $env:PROCESSOR_ARCHITEW6432
$OldPath = $env:PATH
$TempRoot = Join-Path ([IO.Path]::GetTempPath()) ('rdev-release-smoke-' + [Guid]::NewGuid().ToString('N'))
New-Item -ItemType Directory -Path $TempRoot | Out-Null
$env:TEMP = $TempRoot
try {
    $env:PATH = ''
    if (Get-Command openssl -CommandType Application -ErrorAction SilentlyContinue) { throw 'OpenSSL remains discoverable by the installer' }
    . (Join-Path (Split-Path $PSScriptRoot -Parent) 'web/public/run.ps1')
    $Asset = 'rdev-client-cgo-windows-amd64.exe'
    if ($Live) {
        $Tag = Get-RDevLatestTag $Server $Asset
        $Url = "https://github.com/$script:Repo/releases/download/$Tag/$Asset"
        $Out = Join-Path $TempRoot $Asset
        Save-RDevDownload $Url $Out
        $Stream = [IO.File]::OpenRead($Out)
        try {
            if ($Stream.ReadByte() -ne 77 -or $Stream.ReadByte() -ne 90) { throw 'Downloaded asset lacks Windows PE MZ header' }
        } finally { $Stream.Dispose() }
        Write-Host "PASS live CGO resolution/download: $Tag $Asset $((Get-Item $Out).Length) bytes"
        return
    }

    $Fixture = Join-Path $TempRoot 'fixture.exe'
    [IO.File]::WriteAllBytes($Fixture, [byte[]](77,90,1,2,3))
    $script:Fixture = $Fixture
    $script:DownloadMode = 'valid'
    $script:Requests = @()
    $script:MetadataMode = 'old-server'
    function Get-RDevText([string]$Url) {
        $script:Requests += $Url
        if ($Url -like '*/api/release/latest*') {
            if ($script:MetadataMode -eq 'new-server') { return '{"tag":"v0.2.134","asset":"rdev-client-cgo-windows-amd64.exe","fresh":true}' }
            return '{"tag":"v0.2.136","fresh":true}'
        }
        if ($script:MetadataMode -eq 'missing') { return '[]' }
        return '[{"tag_name":"v0.2.137","draft":true,"assets":[{"name":"rdev-client-cgo-windows-amd64.exe"}]},{"tag_name":"v0.2.137-rc1","prerelease":true,"assets":[{"name":"rdev-client-cgo-windows-amd64.exe"}]},{"tag_name":"v0.2.136","assets":[{"name":"rdev-client-gpu-windows-amd64.zip"}]},{"tag_name":"v0.2.134","assets":[{"name":"rdev-client-cgo-windows-amd64.exe"}]}]'
    }
    if ((Get-RDevLatestTag $Server $Asset) -ne 'v0.2.134') { throw 'Old server must fall back to exact-asset GitHub metadata' }
    $script:MetadataMode = 'new-server'
    $script:Requests = @()
    if ((Get-RDevLatestTag $Server $Asset) -ne 'v0.2.134' -or $script:Requests.Count -ne 1) { throw 'Asset-aware server response was not used' }
    $script:MetadataMode = 'missing'
    $MissingFailed = $false
    try { Get-RDevLatestTag $Server $Asset | Out-Null } catch { $MissingFailed = $true }
    if (-not $MissingFailed) { throw 'Missing CGO asset must fail rather than substitute a client' }

    $script:MetadataMode = 'old-server'
    $env:PROCESSOR_ARCHITECTURE = 'AMD64'
    $env:PROCESSOR_ARCHITEW6432 = ''
    function Test-RDevAdministrator { return $true }
    function Get-WindowsVersion { return @{ Major = 10; Build = 19045 } }
    function Save-RDevDownload([string]$Url, [string]$Out) {
        if ($Url -match '\.sig(?:&|$)') { throw 'Unexpected signature download' }
        if ($script:DownloadMode -eq 'offline') { throw 'fixture offline' }
        $script:DownloadUrl = $Url
        if ($script:DownloadMode -eq 'html') { [IO.File]::WriteAllText($Out, '<html>error</html>'); return }
        if ($script:DownloadMode -eq 'empty') { [IO.File]::WriteAllBytes($Out, [byte[]]@()); return }
        Copy-Item $script:Fixture $Out -Force
    }
    # Inspect freshly extracted runtime before any executable launch.
    function Install-WinPTYIfRequired {
        if ($Client -eq 'rs' -and -not (Test-Path (Join-Path (Split-Path $RunPath -Parent) 'libfixture.dll'))) { throw 'Lost runtime sidecar' }
        $script:ReachedLaunch = $true
        throw 'SMOKE_STOP_BEFORE_LAUNCH'
    }
    foreach ($Version in @('', '0.2.133')) {
        $script:DownloadUrl = ''
        try { RDev -Server $Server -Client cgo -Mirror none -Version $Version } catch {
            if ($_.Exception.Message -ne 'SMOKE_STOP_BEFORE_LAUNCH') { throw }
        }
        $ExpectedTag = if ($Version) { 'v0.2.133' } else { 'v0.2.134' }
        if ($script:DownloadUrl -notlike "*/download-release?asset=$Asset&tag=$ExpectedTag") {
            throw "Installer did not pin exact CGO download: $script:DownloadUrl"
        }
        $Cache = Join-Path $TempRoot "rdev-cache/cgo-$ExpectedTag-windows-amd64-$Asset"
        if (-not (Test-Path (Join-Path $Cache 'payload'))) { throw "Missing pinned cache: $Cache" }
    }
    foreach ($Mode in @('html', 'empty')) {
        $script:DownloadMode = $Mode
        $script:ReachedLaunch = $false
        $Failed = $false
        try { RDev -Server $Server -Client cgo -Mirror none -Version "2.0.$($Mode.Length)" } catch { $Failed = $true }
        if (-not $Failed -or $script:ReachedLaunch) { throw "Accepted $Mode release" }
    }
    $script:DownloadMode = 'offline'
    $Cache = Join-Path $TempRoot "rdev-cache/cgo-v0.2.134-windows-amd64-$Asset"
    $script:ReachedLaunch = $false
    try { RDev -Server $Server -Client cgo -Mirror none } catch {
        if ($_.Exception.Message -ne 'SMOKE_STOP_BEFORE_LAUNCH') { throw }
    }
    if (-not $script:ReachedLaunch) { throw 'Cached package was not used offline without a signature' }
    foreach ($CacheMode in @('invalid', 'legacy')) {
        if ($CacheMode -eq 'invalid') { [IO.File]::WriteAllText((Join-Path $Cache 'payload'), 'invalid') }
        else {
            Remove-Item (Join-Path $Cache 'payload') -Force
            Copy-Item $Fixture (Join-Path $Cache $Asset)
            New-Item -ItemType File (Join-Path $Cache '.runtime-complete') | Out-Null
        }
        $script:ReachedLaunch = $false
        $Failed = $false
        try { RDev -Server $Server -Client cgo -Mirror none } catch { $Failed = $true }
        if (-not $Failed -or $script:ReachedLaunch) { throw "Accepted $CacheMode cache" }
    }
    # ZIP payloads are kept and extracted anew on cache hits, including sidecars.
    Add-Type -AssemblyName System.IO.Compression.FileSystem
    $Runtime = Join-Path $TempRoot 'runtime'
    New-Item -ItemType Directory $Runtime | Out-Null
    Copy-Item $Fixture (Join-Path $Runtime 'rdev-client-gpu.exe')
    [IO.File]::WriteAllText((Join-Path $Runtime 'libfixture.dll'), 'runtime sidecar')
    $script:Fixture = Join-Path $TempRoot 'fixture.zip'
    [IO.Compression.ZipFile]::CreateFromDirectory($Runtime, $script:Fixture)
    foreach ($Mode in @('valid', 'offline')) {
        $script:DownloadMode = $Mode
        $script:ReachedLaunch = $false
        try { RDev -Server $Server -Client rs -Mirror none -Version '3.0.0' } catch {
            if ($_.Exception.Message -ne 'SMOKE_STOP_BEFORE_LAUNCH') { throw }
        }
        if (-not $script:ReachedLaunch) { throw 'ZIP runtime was not prepared' }
    }
    if (Test-WinPTYArchive $script:Fixture) { throw 'Accepted unpinned WinPTY archive' }
    # Exercise the real launch argument builder without starting a fixture EXE.
    function Install-WinPTYIfRequired { return '' }
    function Test-RDevAdministrator { return $false }
    function Wait-RDevElevationKey { return $true }
    function Start-Process([string]$FilePath, [string]$ArgumentList, [string]$Verb, [switch]$Wait) {
        if (-not $Wait -or $Verb -ne 'RunAs') { throw 'Elevated runtime must remain present until process exit' }
        if (-not (Test-Path (Join-Path (Split-Path $FilePath -Parent) 'libfixture.dll'))) { throw 'Missing runtime at launch' }
        $script:LaunchArguments = $ArgumentList
    }
    $script:LaunchArguments = ''
    RDev -Server 'ws://fixture.test:8080' -Client rs -Mirror none -Version '3.0.0' -Id 'device with spaces' -Password 'semi;quote"' -Shell 'C:\a shell\'
    $ExpectedArguments = '"-s" "ws://fixture.test:8080" "-i" "device with spaces" "-p" "semi;quote\"" "--shell" "C:\a shell\\"'
    if ($script:LaunchArguments -ne $ExpectedArguments) { throw "Incorrect elevated argv: $script:LaunchArguments" }
    Write-Host 'PASS PowerShell release regression without OpenSSL or signatures: metadata fallback/pinning, raw/ZIP package cache, preserved sidecars, invalid-package rejection and elevated argument quoting'
} finally {
    $env:PATH = $OldPath
    $env:TEMP = $OldTemp
    $env:PROCESSOR_ARCHITECTURE = $OldArch
    $env:PROCESSOR_ARCHITEW6432 = $OldNativeArch
    Remove-Item -Recurse -Force $TempRoot
}
