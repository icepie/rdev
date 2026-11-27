# Sign Windows executables and libraries with the shared nicecode-tools
# Authenticode service, then prove every signature is valid and timestamped.
#
#   sign-windows.ps1 <path>...
#
# A path may be a .exe/.dll or a directory; directories are searched recursively
# so a packaged client (exe + bundled DLLs) is never published half-signed.
# Requires EVSIGN_CLIENT (downloaded client) and EVSIGN_LICENSE_KEY.
param(
  [Parameter(Mandatory = $true, Position = 0)][string[]] $Path,
  [string] $Provider = 'digicert'
)

$ErrorActionPreference = 'Stop'
if (-not $env:EVSIGN_CLIENT -or -not $env:EVSIGN_LICENSE_KEY) {
  throw 'EVSign client and signing credentials are required.'
}
if (-not (Test-Path -LiteralPath $env:EVSIGN_CLIENT -PathType Leaf)) {
  throw "EVSign client not found: $($env:EVSIGN_CLIENT)"
}

function Test-PeFile([string] $File) {
  $stream = [IO.File]::OpenRead($File)
  try {
    $reader = [IO.BinaryReader]::new($stream)
    if ($stream.Length -lt 64 -or $reader.ReadUInt16() -ne 0x5A4D) { return $false }
    $stream.Position = 0x3c
    $offset = $reader.ReadUInt32()
    if ($offset -gt $stream.Length - 4) { return $false }
    $stream.Position = $offset
    return $reader.ReadUInt32() -eq 0x4550
  } finally {
    $stream.Dispose()
  }
}

$candidates = foreach ($item in $Path) {
  if (-not (Test-Path -LiteralPath $item)) { throw "Signing input not found: $item" }
  if (Test-Path -LiteralPath $item -PathType Container) {
    Get-ChildItem -LiteralPath $item -Recurse -File | Where-Object { $_.Extension -in '.exe', '.dll' }
  } else {
    Get-Item -LiteralPath $item
  }
}
$targets = @($candidates | Where-Object { Test-PeFile $_.FullName })
if ($targets.Count -eq 0) { throw 'No PE executable or library found to sign.' }

foreach ($target in $targets) {
  $stage = Join-Path ([IO.Path]::GetTempPath()) ('rdev-sign-' + [Guid]::NewGuid().ToString('N'))
  New-Item -ItemType Directory -Path $stage | Out-Null
  try {
    $file = Join-Path $stage $target.Name
    Copy-Item -LiteralPath $target.FullName -Destination $file
    Push-Location $stage
    try {
      # The provider's diagnostic output may contain its command line or key.
      $null = & $env:EVSIGN_CLIENT $target.Name -key $env:EVSIGN_LICENSE_KEY -sha256 -t $Provider -cdn 2>&1
      $code = $LASTEXITCODE
    } finally {
      Pop-Location
    }
    if ($code -ne 0) { throw "EVSign failed for $($target.Name) (exit $code)." }
    $signature = Get-AuthenticodeSignature -LiteralPath $file
    if ($signature.Status -ne 'Valid' -or -not $signature.TimeStamperCertificate) {
      throw "Signing did not produce a valid timestamped signature for $($target.Name): $($signature.Status)"
    }
    if ($signature.SignerCertificate.Thumbprint -eq 'C461B34B991665F8941FB9AC541048BA367ACEAA') {
      throw "Legacy self-signed certificate is not accepted for $($target.Name)."
    }
    Copy-Item -LiteralPath $file -Destination $target.FullName -Force
    Write-Host "Verified Authenticode: $($target.Name) [$($signature.SignerCertificate.Subject)]"
  } finally {
    Remove-Item -LiteralPath $stage -Recurse -Force -ErrorAction SilentlyContinue
  }
}

Write-Host "Windows signing complete: $($targets.Count) file(s)"
