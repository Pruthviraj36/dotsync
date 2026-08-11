# DotSync installer for Windows (PowerShell)
#
# Usage:
#   irm https://<your-server>/install.ps1 | iex
#
# After install, point the CLI at your server:
#   $env:DOTSYNC_SERVER = "https://<your-server>"
#   dotsync login

$ErrorActionPreference = "Stop"

$Repo    = "Pruthviraj36/dotsync"
$BinName = "dotsync"
$InstallDir = if ($env:DOTSYNC_INSTALL_DIR) { $env:DOTSYNC_INSTALL_DIR } else { "$env:LOCALAPPDATA\dotsync" }

Write-Host "==> Checking latest release..."
$Release = Invoke-RestMethod "https://api.github.com/repos/$Repo/releases/latest"
$Tag = $Release.tag_name
if (-not $Tag) { throw "Could not determine latest release tag" }
Write-Host "==> Latest version: $Tag"

$Asset   = "$BinName-windows-amd64.zip"
$BaseUrl = "https://github.com/$Repo/releases/download/$Tag"
$TmpDir  = Join-Path $env:TEMP "dotsync-install-$([System.IO.Path]::GetRandomFileName())"
New-Item -ItemType Directory -Path $TmpDir | Out-Null

try {
    Write-Host "==> Downloading $Asset..."
    Invoke-WebRequest "$BaseUrl/$Asset"       -OutFile "$TmpDir\$Asset"
    Invoke-WebRequest "$BaseUrl/checksums.txt" -OutFile "$TmpDir\checksums.txt"

    Write-Host "==> Verifying checksum..."
    $Expected = (Select-String -Path "$TmpDir\checksums.txt" -Pattern " $Asset$").Line.Split(" ")[0]
    if (-not $Expected) { throw "No checksum entry for $Asset" }
    $Actual = (Get-FileHash "$TmpDir\$Asset" -Algorithm SHA256).Hash.ToLower()
    if ($Expected -ne $Actual) { throw "Checksum mismatch — download may be corrupted" }
    Write-Host "==> Checksum verified"

    Write-Host "==> Extracting..."
    Expand-Archive "$TmpDir\$Asset" -DestinationPath $TmpDir

    if (-not (Test-Path $InstallDir)) { New-Item -ItemType Directory -Path $InstallDir | Out-Null }
    Copy-Item "$TmpDir\$BinName.exe" "$InstallDir\$BinName.exe" -Force

    Write-Host ""
    Write-Host "dotsync $Tag installed to $InstallDir\$BinName.exe"
    Write-Host ""
    Write-Host "Add to PATH (run once as admin):"
    Write-Host "  [Environment]::SetEnvironmentVariable('Path', `$env:Path + ';$InstallDir', 'User')"
    Write-Host ""
    Write-Host "Then set your server and log in:"
    Write-Host "  `$env:DOTSYNC_SERVER = 'https://<your-server>'"
    Write-Host "  dotsync login"
} finally {
    Remove-Item -Recurse -Force $TmpDir -ErrorAction SilentlyContinue
}
