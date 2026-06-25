package cmd

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/minio/selfupdate"
	"github.com/spf13/cobra"
)

type githubRelease struct {
	TagName string        `json:"tag_name"`
	Assets  []githubAsset `json:"assets"`
}

type githubAsset struct {
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
}

func updateCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "update",
		Short: "Update DotSync to the latest version",
		Long: `Downloads and installs the latest DotSync release.

Every downloaded binary is verified against a SHA-256 checksum published
alongside the release (checksums.txt) before it is applied. If the
checksum is missing or doesn't match, the update is refused — DotSync
will never silently install an unverified binary.`,
		RunE: runUpdate,
	}
}

func runUpdate(cmd *cobra.Command, args []string) error {
	fmt.Println("⏳ Checking for updates...")

	release, err := fetchLatestRelease()
	if err != nil {
		return fmt.Errorf("failed to check for updates: %w", err)
	}

	// Normalize both sides: strip leading "v" so "1.33.10" == "v1.33.10"
	currentNorm := strings.TrimPrefix(Version, "v")
	latestNorm := strings.TrimPrefix(release.TagName, "v")
	if Version != "dev" && currentNorm == latestNorm {
		fmt.Printf("✅ You are already using the latest version (%s)!\n", Version)
		return nil
	}

	fmt.Printf("📦 Found new version: %s (current: %s)\n", release.TagName, Version)

	// ── Fetch checksums.txt FIRST — refuse to proceed at all if it's missing.
	// This is the actual security boundary: we never download or apply a
	// binary we have no way to verify. ──────────────────────────────────────
	fmt.Println("⏳ Fetching checksums...")
	checksums, err := fetchChecksums(release)
	if err != nil {
		return fmt.Errorf(
			"refusing to update: could not fetch checksums.txt for verification: %w\n"+
				"This is a safety check — DotSync will not install an unverifiable binary.", err)
	}

	ext := ".tar.gz"
	if runtime.GOOS == "windows" {
		ext = ".zip"
	}
	assetName := fmt.Sprintf("dotsync-%s-%s%s", runtime.GOOS, runtime.GOARCH, ext)

	expectedChecksum, ok := checksums[assetName]
	if !ok {
		return fmt.Errorf("refusing to update: no checksum entry found for %s in checksums.txt", assetName)
	}

	var downloadURL string
	for _, asset := range release.Assets {
		if asset.Name == assetName {
			downloadURL = asset.BrowserDownloadURL
			break
		}
	}
	if downloadURL == "" {
		return fmt.Errorf("no suitable binary found for %s/%s in release %s", runtime.GOOS, runtime.GOARCH, release.TagName)
	}

	fmt.Println("⏳ Downloading...")
	archiveBytes, err := downloadAll(downloadURL)
	if err != nil {
		return fmt.Errorf("failed to download update: %w", err)
	}

	// ── Verify the downloaded ARCHIVE against checksums.txt before we even
	// look inside it. This is the actual cryptographic check. ───────────────
	actualChecksum := sha256Hex(archiveBytes)
	if actualChecksum != expectedChecksum {
		return fmt.Errorf(
			"refusing to update: checksum mismatch for %s\n"+
				"  expected: %s\n"+
				"  got:      %s\n"+
				"This could mean the download was corrupted, or — far more seriously —\n"+
				"tampered with in transit. The update has been aborted and nothing was\n"+
				"changed on your system. Please try again, and if this persists, report\n"+
				"it: https://github.com/Pruthviraj36/dotsync/issues",
			assetName, expectedChecksum, actualChecksum)
	}
	fmt.Println("✅ Checksum verified")

	binaryReader, err := extractBinary(archiveBytes, ext)
	if err != nil {
		return err
	}

	fmt.Println("⏳ Applying update...")
	if err := selfupdate.Apply(binaryReader, selfupdate.Options{}); err != nil {
		if strings.Contains(err.Error(), "permission denied") {
			return fmt.Errorf("permission denied — try running with sudo/admin privileges")
		}
		return fmt.Errorf("failed to apply update: %w", err)
	}

	fmt.Printf("✅ Successfully updated to %s!\n", release.TagName)
	return nil
}

func fetchLatestRelease() (*githubRelease, error) {
	resp, err := http.Get("https://api.github.com/repos/Pruthviraj36/dotsync/releases/latest")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GitHub API returned status %d", resp.StatusCode)
	}

	var release githubRelease
	if err := json.NewDecoder(resp.Body).Decode(&release); err != nil {
		return nil, fmt.Errorf("failed to parse release info: %w", err)
	}
	return &release, nil
}

// fetchChecksums downloads and parses checksums.txt from the release,
// in the standard `sha256sum` output format: "<hex-digest>  <filename>".
func fetchChecksums(release *githubRelease) (map[string]string, error) {
	var checksumsURL string
	for _, asset := range release.Assets {
		if asset.Name == "checksums.txt" {
			checksumsURL = asset.BrowserDownloadURL
			break
		}
	}
	if checksumsURL == "" {
		return nil, fmt.Errorf("release %s has no checksums.txt asset", release.TagName)
	}

	resp, err := http.Get(checksumsURL)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("failed to download checksums.txt (status %d)", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	result := make(map[string]string)
	for _, line := range strings.Split(string(body), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) != 2 {
			continue
		}
		digest, filename := fields[0], fields[1]
		filename = strings.TrimPrefix(filename, "*") // sha256sum binary-mode marker
		result[filename] = digest
	}
	if len(result) == 0 {
		return nil, fmt.Errorf("checksums.txt was empty or unparseable")
	}
	return result, nil
}

func downloadAll(url string) ([]byte, error) {
	resp, err := http.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("status %d", resp.StatusCode)
	}
	return io.ReadAll(resp.Body)
}

func sha256Hex(data []byte) string {
	h := sha256.New()
	_, _ = h.Write(data)
	return hex.EncodeToString(h.Sum(nil))
}

func extractBinary(archiveBytes []byte, ext string) (io.Reader, error) {
	if ext == ".zip" {
		zipReader, err := zip.NewReader(bytes.NewReader(archiveBytes), int64(len(archiveBytes)))
		if err != nil {
			return nil, fmt.Errorf("failed to read zip archive: %w", err)
		}
		for _, f := range zipReader.File {
			if f.Name == "dotsync.exe" || f.Name == "dotsync" {
				rc, err := f.Open()
				if err != nil {
					return nil, fmt.Errorf("failed to open binary inside zip: %w", err)
				}
				defer rc.Close()
				binBytes, err := io.ReadAll(rc)
				if err != nil {
					return nil, fmt.Errorf("failed to read binary from zip: %w", err)
				}
				return bytes.NewReader(binBytes), nil
			}
		}
		return nil, fmt.Errorf("executable not found inside the downloaded zip")
	}

	gzReader, err := gzip.NewReader(bytes.NewReader(archiveBytes))
	if err != nil {
		return nil, fmt.Errorf("failed to read gzip archive: %w", err)
	}
	defer gzReader.Close()

	tarReader := tar.NewReader(gzReader)
	for {
		header, err := tarReader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("failed to read tar archive: %w", err)
		}
		base := filepath.Base(header.Name)
		if header.Typeflag == tar.TypeReg && (base == "dotsync" || base == "dotsync.exe") {
			binBytes, err := io.ReadAll(tarReader)
			if err != nil {
				return nil, fmt.Errorf("failed to read binary from tar: %w", err)
			}
			return bytes.NewReader(binBytes), nil
		}
	}
	return nil, fmt.Errorf("executable not found inside the downloaded tar.gz")
}
