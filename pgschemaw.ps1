$ErrorActionPreference = "Stop"

$ScriptDir = Split-Path -Parent $MyInvocation.MyCommand.Path
$RepoRoot = $ScriptDir
$PropertiesFile = Join-Path $RepoRoot ".pgschema-wrapper.properties"

if (-not (Test-Path $PropertiesFile)) {
  Write-Error "pgschema wrapper config not found: $PropertiesFile`nRun ./scripts/wrapper/generate.sh --version <x.y.z> first."
}

function Read-Property {
  param(
    [Parameter(Mandatory = $true)][string]$Path,
    [Parameter(Mandatory = $true)][string]$Key
  )
  foreach ($line in Get-Content -Path $Path) {
    if ($line -match '^\s*#') { continue }
    $parts = $line -split '=', 2
    if ($parts.Count -lt 2) { continue }
    if ($parts[0].Trim() -eq $Key) {
      return $parts[1].Trim()
    }
  }
  return ""
}

$Version = if ($env:PGSCHEMAW_VERSION) { $env:PGSCHEMAW_VERSION } else { Read-Property -Path $PropertiesFile -Key "pgschema.version" }
$BaseUrl = if ($env:PGSCHEMAW_BASE_URL) { $env:PGSCHEMAW_BASE_URL } else { Read-Property -Path $PropertiesFile -Key "pgschema.baseUrl" }
$CacheDir = if ($env:PGSCHEMAW_CACHE_DIR) { $env:PGSCHEMAW_CACHE_DIR } else { Read-Property -Path $PropertiesFile -Key "pgschema.cacheDir" }
$Source = if ($env:PGSCHEMAW_SOURCE) { $env:PGSCHEMAW_SOURCE } else { Read-Property -Path $PropertiesFile -Key "pgschema.source" }
$LocalRebuild = if ($env:PGSCHEMAW_LOCAL_REBUILD) { $env:PGSCHEMAW_LOCAL_REBUILD } else { "false" }

if (-not $Version) { throw "Missing pgschema.version in $PropertiesFile" }
if (-not $BaseUrl) { $BaseUrl = "https://github.com/pgplex/pgschema/releases/download" }
if (-not $CacheDir) { $CacheDir = Join-Path $RepoRoot ".pgschema/wrapper/bin" }
if (-not $Source) { $Source = "remote" }
if ($Source -ne "remote" -and $Source -ne "local") {
  throw "Invalid pgschema.source: $Source (expected: remote|local)"
}

$Version = $Version.TrimStart('v')

function Detect-Os {
  if ($IsLinux) { return "linux" }
  if ($IsMacOS) { return "darwin" }
  if ($IsWindows) {
    throw "Windows native binaries are not supported by pgschema. Use WSL for execution."
  }
  throw "Unsupported OS"
}

function Detect-Arch {
  $arch = [System.Runtime.InteropServices.RuntimeInformation]::OSArchitecture.ToString().ToLowerInvariant()
  switch ($arch) {
    "x64" { return "amd64" }
    "arm64" { return "arm64" }
    default { throw "Unsupported architecture: $arch" }
  }
}

function Get-FileSha256 {
  param([Parameter(Mandatory = $true)][string]$Path)
  if (Get-Command Get-FileHash -ErrorAction SilentlyContinue) {
    return (Get-FileHash -Path $Path -Algorithm SHA256).Hash.ToLowerInvariant()
  }
  throw "Get-FileHash not found to validate checksum."
}

function Build-LocalBinary {
  param(
    [Parameter(Mandatory = $true)][string]$Output,
    [Parameter(Mandatory = $true)][string]$Os,
    [Parameter(Mandatory = $true)][string]$Arch
  )
  if (-not (Get-Command go -ErrorAction SilentlyContinue)) {
    throw "Go is required for local wrapper source mode."
  }
  $tmpFile = "$Output.tmp"
  Write-Host "Building pgschema locally for $Os/$Arch"
  Push-Location $RepoRoot
  try {
    $env:CGO_ENABLED = "0"
    $env:GOOS = $Os
    $env:GOARCH = $Arch
    & go build -o $tmpFile .
  } finally {
    Pop-Location
  }
  Move-Item -Force $tmpFile $Output
}

$Os = Detect-Os
$Arch = Detect-Arch
$AssetName = "pgschema-$Version-$Os-$Arch"
$TargetDir = Join-Path $CacheDir "$Version/$Os-$Arch"
$TargetBin = Join-Path $TargetDir "pgschema"
$DownloadUrl = "$BaseUrl/v$Version/$AssetName"

New-Item -ItemType Directory -Force -Path $TargetDir | Out-Null

if ($Source -eq "local") {
  if (-not (Test-Path $TargetBin) -or $LocalRebuild -eq "true") {
    Build-LocalBinary -Output $TargetBin -Os $Os -Arch $Arch
  }
} else {
  if (-not (Test-Path $TargetBin)) {
    $tmpFile = "$TargetBin.tmp"
    Write-Host "Downloading $DownloadUrl"
    Invoke-WebRequest -Uri $DownloadUrl -OutFile $tmpFile
    Move-Item -Force $tmpFile $TargetBin
    try {
      & chmod +x $TargetBin | Out-Null
    } catch {
      # Ignore when chmod is unavailable
    }
  }
}

if ($Source -eq "remote") {
  $checksumKey = "pgschema.sha256.$Os-$Arch"
  $expectedChecksum = Read-Property -Path $PropertiesFile -Key $checksumKey
  if ($expectedChecksum) {
    $actualChecksum = Get-FileSha256 -Path $TargetBin
    if ($actualChecksum -ne $expectedChecksum.ToLowerInvariant()) {
      throw "Checksum mismatch for $TargetBin. Expected $expectedChecksum but got $actualChecksum"
    }
  }
}

& $TargetBin @args
exit $LASTEXITCODE
