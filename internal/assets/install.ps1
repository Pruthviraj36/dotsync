# DotSync installer for Windows (PowerShell)
#
# Usage:
#   irm https://<your-server>/install.ps1 | iex
#
# Installs dotsync.exe to %LOCALAPPDATA%\dotsync and adds that folder to your
# user PATH, so `dotsync` works in this terminal right away and in every new
# terminal you open later. Override the location with DOTSYNC_INSTALL_DIR.
#
# After install, point the CLI at your server:
#   $env:DOTSYNC_SERVER = "https://<your-server>"
#   dotsync login
#
# NOTE: keep this file pure ASCII. Windows PowerShell 5.1 reads BOM-less files
# with the ANSI code page, which mangles non-ASCII punctuation and can turn a
# comment or string into a parse error.

# Windows PowerShell 5.1 may still default to TLS 1.0; GitHub requires 1.2+.
try {
    [Net.ServicePointManager]::SecurityProtocol = [Net.ServicePointManager]::SecurityProtocol -bor [Net.SecurityProtocolType]::Tls12
} catch { }

$ErrorActionPreference = "Stop"

$Repo       = "Pruthviraj36/dotsync"
$BinName    = "dotsync"
$InstallDir = if ($env:DOTSYNC_INSTALL_DIR) { $env:DOTSYNC_INSTALL_DIR } else { Join-Path $env:LOCALAPPDATA "dotsync" }
$InstallDir = $InstallDir.TrimEnd('\')

# ---- Architecture -----------------------------------------------------------
# PROCESSOR_ARCHITEW6432 is set when 32-bit PowerShell runs on 64-bit Windows.
$archRaw = if ($env:PROCESSOR_ARCHITEW6432) { $env:PROCESSOR_ARCHITEW6432 } else { $env:PROCESSOR_ARCHITECTURE }
switch ($archRaw.ToUpperInvariant()) {
    "AMD64" { $goarch = "amd64" }
    "ARM64" { $goarch = "arm64" }
    "X86" {
        throw "DotSync does not publish a 32-bit Windows build. Install 64-bit Windows, or download a binary manually from https://github.com/$Repo/releases"
    }
    default {
        Write-Host "==> Unknown architecture '$archRaw', assuming amd64"
        $goarch = "amd64"
    }
}

# ---- PATH helpers -----------------------------------------------------------
# Only the *user* PATH is ever read and written. Reading the merged $env:Path
# and writing it back to the user scope would copy every machine-wide entry
# into HKCU and slowly bloat PATH past its limit.
function Add-ToUserPath {
    param([string]$Dir)

    $dir = $Dir.TrimEnd('\')
    $persisted = $false

    $userPath = [Environment]::GetEnvironmentVariable("Path", "User")
    $entries = @()
    if ($userPath) { $entries = @($userPath -split ';' | Where-Object { $_ -ne '' }) }

    $present = $false
    foreach ($e in $entries) {
        if ([string]::Equals($e.TrimEnd('\'), $dir, [StringComparison]::OrdinalIgnoreCase)) {
            $present = $true
            break
        }
    }

    if (-not $present) {
        $entries += $dir
        [Environment]::SetEnvironmentVariable("Path", ($entries -join ';'), "User")
        $persisted = $true
    }

    # Make `dotsync` usable in the shell that ran the installer. Environment
    # variables are process-wide, so this also applies outside the function.
    $procEntries = @($env:Path -split ';' | Where-Object { $_ -ne '' })
    $inProcess = $false
    foreach ($e in $procEntries) {
        if ([string]::Equals($e.TrimEnd('\'), $dir, [StringComparison]::OrdinalIgnoreCase)) {
            $inProcess = $true
            break
        }
    }
    if (-not $inProcess) {
        $env:Path = (($procEntries + $dir) -join ';')
    }

    return [pscustomobject]@{ Persisted = $persisted; InProcess = $true }
}

# Tell Explorer (and anything it launches later) that the environment changed,
# so newly opened terminals see the updated PATH without a sign-out.
function Send-EnvironmentChange {
    try {
        if (-not ("DotSyncNative" -as [type])) {
            Add-Type -TypeDefinition @'
using System;
using System.Runtime.InteropServices;
public static class DotSyncNative {
    [DllImport("user32.dll", SetLastError = true, CharSet = CharSet.Auto)]
    public static extern IntPtr SendMessageTimeout(IntPtr hWnd, uint Msg, UIntPtr wParam,
        string lParam, uint fuFlags, uint uTimeout, out UIntPtr lpdwResult);
}
'@
        }
        $result = [UIntPtr]::Zero
        [DotSyncNative]::SendMessageTimeout([IntPtr]0xffff, 0x1A, [UIntPtr]::Zero, "Environment", 2, 5000, [ref]$result) | Out-Null
    } catch {
        # Cosmetic only - a new terminal still picks PATH up from the registry.
    }
}

# ---- Download latest release ------------------------------------------------
Write-Host "==> Checking latest release..."
try {
    $Release = Invoke-RestMethod -Headers @{ "User-Agent" = "dotsync-installer" } "https://api.github.com/repos/$Repo/releases/latest"
} catch {
    throw "Could not reach GitHub to find the latest release: $($_.Exception.Message). If this is an API rate limit, wait a few minutes or download from https://github.com/$Repo/releases"
}
$Tag = $Release.tag_name
if (-not $Tag) { throw "Could not determine latest release tag" }
Write-Host "==> Latest version: $Tag"

# Pick the matching asset. Some releases only ship windows/amd64, and Windows
# on ARM runs x64 binaries under emulation, so fall back instead of failing.
$wanted = @("$BinName-windows-$goarch.zip")
if ($goarch -eq "arm64") { $wanted += "$BinName-windows-amd64.zip" }

$available = @()
if ($Release.assets) { $available = @($Release.assets | ForEach-Object { $_.name }) }

$Asset = $null
foreach ($candidate in $wanted) {
    if ($available.Count -eq 0 -or $available -contains $candidate) {
        $Asset = $candidate
        break
    }
}
if (-not $Asset) {
    throw "Release $Tag has no Windows build ($($wanted -join ', ')). See https://github.com/$Repo/releases"
}
if ($Asset -ne $wanted[0]) {
    Write-Host "==> $($wanted[0]) is not published for $Tag; using $Asset (runs under x64 emulation)"
}

$BaseUrl = "https://github.com/$Repo/releases/download/$Tag"
$TmpDir  = Join-Path $env:TEMP "dotsync-install-$([System.IO.Path]::GetRandomFileName())"
New-Item -ItemType Directory -Path $TmpDir | Out-Null

try {
    Write-Host "==> Downloading $Asset..."
    Invoke-WebRequest "$BaseUrl/$Asset" -OutFile "$TmpDir\$Asset"
    Invoke-WebRequest "$BaseUrl/checksums.txt" -OutFile "$TmpDir\checksums.txt"

    Write-Host "==> Verifying checksum..."
    $Expected = $null
    foreach ($line in Get-Content "$TmpDir\checksums.txt") {
        # sha256sum format: "<hash>  <file>" (a leading "*" marks binary mode).
        $parts = $line.Trim() -split '\s+', 2
        if ($parts.Count -eq 2 -and $parts[1].TrimStart('*') -eq $Asset) {
            $Expected = $parts[0].ToLowerInvariant()
            break
        }
    }
    if (-not $Expected) { throw "No checksum entry for $Asset" }

    $Actual = (Get-FileHash "$TmpDir\$Asset" -Algorithm SHA256).Hash.ToLowerInvariant()
    if ($Expected -ne $Actual) { throw "Checksum mismatch - download may be corrupted or tampered with" }
    Write-Host "==> Checksum verified"

    Write-Host "==> Extracting..."
    Expand-Archive "$TmpDir\$Asset" -DestinationPath $TmpDir -Force
    $Extracted = Join-Path $TmpDir "$BinName.exe"
    if (-not (Test-Path $Extracted)) { throw "Archive did not contain $BinName.exe" }

    if (-not (Test-Path $InstallDir)) { New-Item -ItemType Directory -Path $InstallDir | Out-Null }

    $target = Join-Path $InstallDir "$BinName.exe"
    try {
        Copy-Item $Extracted $target -Force
    } catch {
        throw "Could not replace $target - close any running dotsync process and run the installer again. ($($_.Exception.Message))"
    }

    # Best-effort: strip the "downloaded from the internet" mark so SmartScreen
    # does not hassle the user on first run.
    try { Unblock-File -Path $target -ErrorAction SilentlyContinue } catch { }

    $pathResult = Add-ToUserPath $InstallDir
    if ($pathResult.Persisted) { Send-EnvironmentChange }

    $runs = $false
    try {
        # Older releases do not implement --version; `help` exists in every build
        # and only proves the binary can actually execute (antivirus sometimes
        # quarantines freshly downloaded Go binaries).
        & $target help *> $null
        $runs = ($LASTEXITCODE -eq 0)
    } catch { }

    Write-Host ""
    Write-Host "dotsync $Tag installed to $target"
    if (-not $runs) {
        Write-Host "  WARNING: the installed binary did not run." -ForegroundColor Yellow
        Write-Host "  Windows Defender or SmartScreen may have blocked it. Allow" -ForegroundColor Yellow
        Write-Host "  $target in your antivirus settings, then run: dotsync help" -ForegroundColor Yellow
    }
    Write-Host ""

    if (-not $pathResult.Persisted) {
        Write-Host "$InstallDir is already on your user PATH."
    } else {
        Write-Host "Added $InstallDir to your user PATH."
        Write-Host "It is ready in this terminal and in every terminal you open from now on."
    }
    Write-Host "If some other app still cannot find it, log out and back in once."

    Write-Host ""
    Write-Host "Next: set your server and log in:"
    Write-Host "  `$env:DOTSYNC_SERVER = 'https://<your-server>'"
    Write-Host "  dotsync login"
} finally {
    Remove-Item -Recurse -Force $TmpDir -ErrorAction SilentlyContinue
}
