# Run: pwsh -NoProfile -File scripts/test-runner-release.ps1
# Live metadata + real Windows CGO download: add -Live (does not launch a client).
param([switch]$Live, [string]$Server = 'https://r.geofun.cn')
$ErrorActionPreference = 'Stop'
$OldTemp = $env:TEMP
$OldArch = $env:PROCESSOR_ARCHITECTURE
$OldNativeArch = $env:PROCESSOR_ARCHITEW6432
$TempRoot = Join-Path ([IO.Path]::GetTempPath()) ('rdev-release-smoke-' + [Guid]::NewGuid().ToString('N'))
New-Item -ItemType Directory -Path $TempRoot | Out-Null
$env:TEMP = $TempRoot
try {
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
        $script:DownloadUrl = $Url
        [IO.File]::WriteAllBytes($Out, [byte[]](77,90,1,2,3))
    }
    # Stop after real installer cache publication, before any executable launch.
    function Install-WinPTYIfRequired { throw 'SMOKE_STOP_BEFORE_LAUNCH' }
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
        if (-not (Test-Path (Join-Path $Cache '.complete'))) { throw "Missing pinned cache: $Cache" }
    }
    Write-Host 'PASS PowerShell release regression: asset-aware metadata, old server fallback, missing asset failure, pinned CGO download/cache, explicit version'
} finally {
    $env:TEMP = $OldTemp
    $env:PROCESSOR_ARCHITECTURE = $OldArch
    $env:PROCESSOR_ARCHITEW6432 = $OldNativeArch
    Remove-Item -Recurse -Force $TempRoot
}
