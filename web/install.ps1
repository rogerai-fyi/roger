#Requires -Version 5.1
<#
.SYNOPSIS
  RogerAI installer for Windows - a two-way radio for GPUs.

.DESCRIPTION
  Downloads the right rogerai.exe for your CPU architecture from the latest
  GitHub release, verifies its SHA-256 against checksums.txt, installs it to
  %LOCALAPPDATA%\Programs\rogerai, and adds that folder to your *user* PATH.

  One-liner (PowerShell):
      irm https://rogerai.fyi/install.ps1 | iex

  Idempotent: re-running updates the binary in place and never duplicates the
  PATH entry. Touches only the current user - no admin rights required.

.PARAMETER Version
  Release tag to install (e.g. v0.2.0). Defaults to the latest release.
  Override via env:  $env:ROGERAI_VERSION = 'v0.2.0'

.PARAMETER InstallDir
  Target directory. Defaults to %LOCALAPPDATA%\Programs\rogerai.
  Override via env:  $env:ROGERAI_INSTALL_DIR = 'C:\tools\rogerai'
#>
[CmdletBinding()]
param(
    [string]$Version    = $env:ROGERAI_VERSION,
    [string]$InstallDir = $env:ROGERAI_INSTALL_DIR
)

$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest

$Repo = 'bownux/rogerai'
$Bin  = 'rogerai'

# ---- pretty output --------------------------------------------------
function Write-Info($m) { Write-Host "• $m"   -ForegroundColor Blue }
function Write-Ok($m)   { Write-Host "✓ $m"   -ForegroundColor Green }
function Write-Warn($m) { Write-Host "  note: $m" -ForegroundColor DarkGray }
function Die($m)        { Write-Host "✗ $m"   -ForegroundColor Red; exit 1 }

Write-Host ''
Write-Host '  RogerAI' -ForegroundColor Blue -NoNewline
Write-Host ' - a two-way radio for GPUs' -ForegroundColor DarkGray
Write-Host ''

# Modern TLS - older Windows PowerShell defaults can refuse GitHub's endpoints.
try { [Net.ServicePointManager]::SecurityProtocol = [Net.SecurityProtocolType]::Tls12 } catch {}

# ---- detect arch ----------------------------------------------------
# PROCESSOR_ARCHITECTURE reports the *process* arch; under WOW64 an x64 box can
# report differently, so consult PROCESSOR_ARCHITEW6432 too. ARM64 reports ARM64.
$rawArch = $env:PROCESSOR_ARCHITEW6432
if ([string]::IsNullOrEmpty($rawArch)) { $rawArch = $env:PROCESSOR_ARCHITECTURE }
switch ($rawArch) {
    'AMD64' { $Arch = 'amd64' }
    'ARM64' { $Arch = 'arm64' }
    'x86'   { Die 'RogerAI needs 64-bit Windows (x64 or ARM64); 32-bit x86 is not supported.' }
    default { Die "unsupported architecture: $rawArch. See https://github.com/$Repo/releases" }
}
$Asset = "$Bin-windows-$Arch.exe"
Write-Info "platform: windows/$Arch"

# ---- resolve version ------------------------------------------------
if ([string]::IsNullOrEmpty($Version)) {
    Write-Info 'resolving latest release…'
    try {
        $rel = Invoke-RestMethod -Headers @{ 'User-Agent' = 'rogerai-install' } `
            -Uri "https://api.github.com/repos/$Repo/releases/latest"
        $Tag = $rel.tag_name
    } catch {
        $Tag = $null
    }
} else {
    $Tag = $Version
}

if ([string]::IsNullOrEmpty($Tag)) {
    Write-Host ''
    Write-Host "  No published release found for $Repo yet." -ForegroundColor Red
    Write-Host ''
    Write-Host '  RogerAI is brand new - releases are on their way. In the meantime,'
    Write-Host '  build from source (needs Go):'
    Write-Host "    git clone https://github.com/$Repo; cd rogerai" -ForegroundColor DarkGray
    Write-Host '    go build -o rogerai.exe ./cmd/rogerai'           -ForegroundColor DarkGray
    Write-Host ''
    Write-Host "  Watch https://github.com/$Repo/releases for prebuilt binaries." -ForegroundColor Blue
    Write-Host ''
    exit 1
}
Write-Info "version:  $Tag"

$Base = "https://github.com/$Repo/releases/download/$Tag"

# ---- download to a temp dir -----------------------------------------
$Tmp = Join-Path ([System.IO.Path]::GetTempPath()) ("rogerai-" + [guid]::NewGuid().ToString('N'))
New-Item -ItemType Directory -Path $Tmp -Force | Out-Null
$OutFile = Join-Path $Tmp $Asset

try {
    Write-Info "downloading $Asset…"
    try {
        Invoke-WebRequest -Headers @{ 'User-Agent' = 'rogerai-install' } `
            -Uri "$Base/$Asset" -OutFile $OutFile
    } catch {
        Write-Host ''
        Write-Host "  Couldn't download $Asset for $Tag." -ForegroundColor Red
        Write-Host '  That build may not exist for your platform yet.'
        Write-Host "  Browse what's available: https://github.com/$Repo/releases/tag/$Tag" -ForegroundColor Blue
        Write-Host ''
        exit 1
    }
    if (-not (Test-Path $OutFile) -or (Get-Item $OutFile).Length -eq 0) {
        Die 'downloaded file is empty - aborting.'
    }

    # ---- verify checksum (if the release ships checksums.txt) --------
    $SumsFile = Join-Path $Tmp 'checksums.txt'
    $verified = $false
    try {
        Invoke-WebRequest -Headers @{ 'User-Agent' = 'rogerai-install' } `
            -Uri "$Base/checksums.txt" -OutFile $SumsFile -ErrorAction Stop
    } catch {
        $SumsFile = $null
    }
    if ($SumsFile -and (Test-Path $SumsFile)) {
        # checksums.txt is "<sha256>  <filename>" (filename may have a '*' prefix
        # for binary mode, as sha256sum writes). Match our asset, case-insensitive.
        $want = $null
        foreach ($line in (Get-Content $SumsFile)) {
            $parts = ($line -replace '\s+', ' ').Trim().Split(' ')
            if ($parts.Count -ge 2) {
                $name = $parts[1].TrimStart('*')
                if ($name -ieq $Asset) { $want = $parts[0].ToLower(); break }
            }
        }
        if ($want) {
            $got = (Get-FileHash -Algorithm SHA256 -Path $OutFile).Hash.ToLower()
            if ($got -eq $want) {
                Write-Ok 'checksum verified'
                $verified = $true
            } else {
                Die "checksum mismatch for $Asset - refusing to install. expected $want, got $got."
            }
        } else {
            Write-Warn "$Asset not listed in checksums.txt - skipping verification."
        }
    } else {
        Write-Warn 'no checksums.txt in this release - skipping verification.'
    }
    if (-not $verified) { Write-Warn 'binary is still served over HTTPS.' }

    # ---- install ----------------------------------------------------
    if ([string]::IsNullOrEmpty($InstallDir)) {
        $InstallDir = Join-Path $env:LOCALAPPDATA "Programs\$Bin"
    }
    try {
        New-Item -ItemType Directory -Path $InstallDir -Force | Out-Null
    } catch {
        Die "can't create $InstallDir. Set `$env:ROGERAI_INSTALL_DIR to a writable directory."
    }
    $Dest = Join-Path $InstallDir "$Bin.exe"

    # A running rogerai.exe holds a lock on the file; move it aside so the copy
    # succeeds, then best-effort clean up the stale copy.
    if (Test-Path $Dest) {
        $old = "$Dest.old"
        try { Remove-Item $old -Force -ErrorAction SilentlyContinue } catch {}
        try { Move-Item -Path $Dest -Destination $old -Force } catch {}
    }
    try {
        Copy-Item -Path $OutFile -Destination $Dest -Force
    } catch {
        Die "failed to install to $Dest (is rogerai.exe currently running?)."
    }
    try { Remove-Item "$Dest.old" -Force -ErrorAction SilentlyContinue } catch {}
    Write-Ok "installed $Bin → $Dest"

    # ---- add to user PATH (idempotent) ------------------------------
    # Edit the persisted *user* PATH via the registry-backed environment so it
    # survives reboots; setx truncates to 1024 chars, so we use the .NET API.
    $userPath = [Environment]::GetEnvironmentVariable('Path', 'User')
    if ($null -eq $userPath) { $userPath = '' }
    $onPath = $userPath.Split(';') | Where-Object { $_.TrimEnd('\') -ieq $InstallDir.TrimEnd('\') }
    if (-not $onPath) {
        $newPath = if ([string]::IsNullOrEmpty($userPath)) { $InstallDir } else { "$userPath;$InstallDir" }
        [Environment]::SetEnvironmentVariable('Path', $newPath, 'User')
        # Update *this* session too, so `rogerai` works without reopening a shell.
        $env:Path = "$env:Path;$InstallDir"
        Write-Info "added $InstallDir to your PATH"
        Write-Warn 'open a new terminal for the PATH change to take effect everywhere.'
    } else {
        if (($env:Path -split ';') -notcontains $InstallDir) { $env:Path = "$env:Path;$InstallDir" }
    }

    Write-Host ''
    Write-Ok "roger that. run $Bin to go on air or tune in."
    Write-Host ''
} finally {
    try { Remove-Item -Recurse -Force $Tmp -ErrorAction SilentlyContinue } catch {}
}
