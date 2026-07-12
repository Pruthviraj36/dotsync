# dotsync installer (Windows / PowerShell)
#
# Downloads the prebuilt release binary, verifies it against the published
# SHA-256 checksum, and installs it to a directory added to your user PATH
# — no admin rights needed. Mirrors what install.sh does for /usr/local/bin
# on Linux/macOS: land somewhere that's usable immediately, no extra steps.
#
# Usage:
#   irm https://dotsync.onrender.com/install.ps1 | iex
#
# Override the install location:
#   $env:DOTSYNC_INSTALL_DIR = "D:\tools\dotsync"
#   irm https://dotsync.onrender.com/install.ps1 | iex
#
# Note: this deliberately never calls `exit` — a script run via `iex` shares
# your current shell process, so `exit` here would close your whole
# terminal, not just stop the install. Failures throw instead, which just
# unwinds back out of the iex call.

$ErrorActionPreference = "Stop"

function Install-DotSync {
    $Repo = "Pruthviraj36/dotsync"
    $BinName = "dotsync.exe"
    $InstallDir = if ($env:DOTSYNC_INSTALL_DIR) { $env:DOTSYNC_INSTALL_DIR } else { "$env:LOCALAPPDATA\dotsync\bin" }

    # Only windows/amd64 builds are published today (see .goreleaser.yaml) —
    # no Windows arm64 release yet.
    if (-not [System.Environment]::Is64BitOperatingSystem) {
        throw "dotsync requires a 64-bit Windows system"
    }
    if ($env:PROCESSOR_ARCHITECTURE -eq "ARM64") {
        throw "no Windows arm64 build is published yet — see https://github.com/$Repo/issues"
    }

    $Asset = "dotsync-windows-amd64.zip"

    Write-Host "==> Checking latest release..."
    try {
        $Release = Invoke-RestMethod -Uri "https://api.github.com/repos/$Repo/releases/latest"
    } catch {
        throw "failed to reach GitHub — check your network connection"
    }
    $Tag = $Release.tag_name
    if (-not $Tag) { throw "could not determine the latest release tag" }
    Write-Host "==> Latest version: $Tag"

    $BaseUrl = "https://github.com/$Repo/releases/download/$Tag"
    $TempDir = Join-Path $env:TEMP "dotsync-install-$([System.Guid]::NewGuid().ToString('N'))"
    New-Item -ItemType Directory -Path $TempDir | Out-Null

    try {
        Write-Host "==> Downloading $Asset..."
        $AssetPath = Join-Path $TempDir $Asset
        try {
            Invoke-WebRequest -Uri "$BaseUrl/$Asset" -OutFile $AssetPath -UseBasicParsing
        } catch {
            throw "failed to download $Asset — does a release exist for windows/amd64?"
        }

        Write-Host "==> Downloading checksums.txt..."
        $ChecksumsPath = Join-Path $TempDir "checksums.txt"
        try {
            Invoke-WebRequest -Uri "$BaseUrl/checksums.txt" -OutFile $ChecksumsPath -UseBasicParsing
        } catch {
            throw "failed to download checksums.txt"
        }

        Write-Host "==> Verifying checksum..."
        $ChecksumLine = Select-String -Path $ChecksumsPath -Pattern ([regex]::Escape($Asset)) | Select-Object -First 1
        if (-not $ChecksumLine) {
            throw "no checksum entry for $Asset — refusing to install an unverifiable binary"
        }
        $Expected = ($ChecksumLine.Line -split '\s+')[0].ToLower()
        $Actual = (Get-FileHash -Path $AssetPath -Algorithm SHA256).Hash.ToLower()
        if ($Expected -ne $Actual) {
            throw "checksum mismatch for $Asset`n  expected: $Expected`n  got:      $Actual`nThis could mean the download was corrupted or tampered with in transit. Nothing has been installed."
        }
        Write-Host "==> Checksum verified"

        Write-Host "==> Extracting..."
        Expand-Archive -Path $AssetPath -DestinationPath $TempDir -Force
        $ExtractedBin = Join-Path $TempDir $BinName
        if (-not (Test-Path $ExtractedBin)) {
            throw "binary not found inside $Asset"
        }

        Write-Host "==> Installing to $InstallDir\$BinName"
        New-Item -ItemType Directory -Path $InstallDir -Force | Out-Null
        Copy-Item -Path $ExtractedBin -Destination (Join-Path $InstallDir $BinName) -Force

        # Add to the current user's PATH permanently — no admin rights
        # required, unlike modifying the system-wide PATH.
        $UserPath = [Environment]::GetEnvironmentVariable("PATH", "User")
        if ($UserPath -notlike "*$InstallDir*") {
            $NewPath = if ([string]::IsNullOrEmpty($UserPath)) { $InstallDir } else { "$UserPath;$InstallDir" }
            [Environment]::SetEnvironmentVariable("PATH", $NewPath, "User")
            Write-Host ""
            Write-Host "Added $InstallDir to your PATH." -ForegroundColor Yellow
            Write-Host "Restart your terminal (or sign out/in) for it to take effect." -ForegroundColor Yellow
        }

        Write-Host ""
        Write-Host "✅ dotsync $Tag installed to $InstallDir\$BinName" -ForegroundColor Green
        & (Join-Path $InstallDir $BinName) --version 2>$null
        Write-Host ""
        Write-Host "Run 'dotsync login' to get started."
    }
    finally {
        Remove-Item -Path $TempDir -Recurse -Force -ErrorAction SilentlyContinue
    }
}

try {
    Install-DotSync
} catch {
    Write-Host "error: $($_.Exception.Message)" -ForegroundColor Red
}
