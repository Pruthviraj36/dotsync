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
	"strconv"
	"strings"
	"time"

	"github.com/minio/selfupdate"
	"github.com/spf13/cobra"
)

const (
	maxUpdateBytes  = 128 << 20 // 128 MB archive ceiling
	maxChecksumBody = 1 << 20
	updateTimeout   = 2 * time.Minute
)

var updateHTTPClient = &http.Client{Timeout: updateTimeout}

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
	banner("DotSync Software Update")
	txRunning("update metadata check")
	txStep(1, 1, "Checking GitHub releases for latest version")

	release, err := fetchLatestRelease()
	if err != nil {
		return fmt.Errorf("failed to check for updates: %w", err)
	}

	currentNorm := strings.TrimPrefix(Version, "v")
	latestNorm := strings.TrimPrefix(release.TagName, "v")
	if Version != "dev" && currentNorm == latestNorm {
		blank()
		fmt.Println(ok(fmt.Sprintf("Installed version %s is already up to date.", Version)))
		complete("")
		return nil
	}
	// Refuse silent downgrades: a malicious/compromised release listing must not
	// be able to roll the CLI back to a version with known vulnerabilities.
	if Version != "dev" && compareSemver(latestNorm, currentNorm) < 0 {
		return fmt.Errorf("refusing to downgrade from %s to %s — pass a newer release or reinstall manually", Version, release.TagName)
	}

	blank()
	fmt.Printf("Upgrading package: dotsync (%s -> %s)\n\n", Version, release.TagName)

	txRunning("transaction")

	checksums, err := fetchChecksums(release)
	if err != nil {
		return fmt.Errorf("refusing to update: could not fetch checksums.txt: %w\n  DotSync will not install an unverifiable binary.", err)
	}
	txStep(1, 4, "Verifying release checksums.txt")

	ext := ".tar.gz"
	if runtime.GOOS == "windows" {
		ext = ".zip"
	}
	assetName := fmt.Sprintf("dotsync-%s-%s%s", runtime.GOOS, runtime.GOARCH, ext)

	expectedChecksum, ok2 := checksums[assetName]
	if !ok2 {
		return fmt.Errorf("refusing to update: no checksum for %s in checksums.txt", assetName)
	}

	var downloadURL string
	for _, asset := range release.Assets {
		if asset.Name == assetName {
			downloadURL = asset.BrowserDownloadURL
			break
		}
	}
	if downloadURL == "" {
		return fmt.Errorf("no binary found for %s/%s in release %s", runtime.GOOS, runtime.GOARCH, release.TagName)
	}

	txStep(2, 4, fmt.Sprintf("Downloading %s asset (%s/%s)", release.TagName, runtime.GOOS, runtime.GOARCH))
	archiveBytes, err := downloadAll(downloadURL)
	if err != nil {
		return fmt.Errorf("failed to download update: %w", err)
	}

	actualChecksum := sha256Hex(archiveBytes)
	if actualChecksum != expectedChecksum {
		return fmt.Errorf(
			"refusing to update: checksum mismatch for %s\n"+
				"  expected: %s\n"+
				"  got:      %s\n\n"+
				"  The download may be corrupted or tampered with.\n"+
				"  Nothing was changed. Report this if it persists.",
			assetName, expectedChecksum, actualChecksum)
	}
	txStep(3, 4, "Validating SHA-256 integrity")

	binaryReader, err := extractBinary(archiveBytes, ext)
	if err != nil {
		return err
	}

	txStep(4, 4, "Applying self-update binary to system")
	if err := selfupdate.Apply(binaryReader, selfupdate.Options{}); err != nil {
		if strings.Contains(err.Error(), "permission denied") {
			return fmt.Errorf("permission denied — try running with sudo: sudo dotsync update")
		}
		return fmt.Errorf("failed to apply update: %w", err)
	}

	transactionSummary("Update Summary", [][2]string{
		{"Package", "dotsync"},
		{"Platform", runtime.GOOS + "/" + runtime.GOARCH},
		{"Previous Version", Version},
		{"Installed Version", release.TagName},
	})

	complete(fmt.Sprintf("DotSync updated to %s.", release.TagName))
	return nil
}

func fetchLatestRelease() (*githubRelease, error) {
	resp, err := updateHTTPClient.Get("https://api.github.com/repos/Pruthviraj36/dotsync/releases/latest")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GitHub API returned status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxChecksumBody))
	if err != nil {
		return nil, fmt.Errorf("failed to read release info: %w", err)
	}

	var release githubRelease
	if err := json.Unmarshal(body, &release); err != nil {
		return nil, fmt.Errorf("failed to parse release info: %w", err)
	}
	if release.TagName == "" {
		return nil, fmt.Errorf("GitHub returned a release with no tag")
	}
	return &release, nil
}

// compareSemver compares dotted numeric versions ("1.4.2"). Non-numeric
// suffixes are ignored, and unparseable versions compare as equal so an odd tag
// never blocks an update.
func compareSemver(a, b string) int {
	parse := func(v string) []int {
		v = strings.TrimPrefix(v, "v")
		core := strings.SplitN(v, "-", 2)[0]
		parts := strings.Split(core, ".")
		out := make([]int, len(parts))
		for i, p := range parts {
			n, err := strconv.Atoi(p)
			if err != nil {
				return nil
			}
			out[i] = n
		}
		return out
	}
	av, bv := parse(a), parse(b)
	if av == nil || bv == nil {
		return 0
	}
	for i := 0; i < len(av) || i < len(bv); i++ {
		var x, y int
		if i < len(av) {
			x = av[i]
		}
		if i < len(bv) {
			y = bv[i]
		}
		if x < y {
			return -1
		}
		if x > y {
			return 1
		}
	}
	return 0
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

	resp, err := updateHTTPClient.Get(checksumsURL)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("failed to download checksums.txt (status %d)", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxChecksumBody))
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
		digest, filename := strings.ToLower(fields[0]), fields[1]
		if len(digest) != 64 {
			continue
		}
		if _, err := hex.DecodeString(digest); err != nil {
			continue
		}
		filename = strings.TrimPrefix(filename, "*") // sha256sum binary-mode marker
		result[filename] = digest
	}
	if len(result) == 0 {
		return nil, fmt.Errorf("checksums.txt was empty or unparseable")
	}
	return result, nil
}

func downloadAll(url string) ([]byte, error) {
	resp, err := updateHTTPClient.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("status %d", resp.StatusCode)
	}
	// Read one byte past the limit so we can distinguish "exactly max" from
	// "too large" without trusting Content-Length.
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxUpdateBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxUpdateBytes {
		return nil, fmt.Errorf("release archive exceeds %d MB", maxUpdateBytes>>20)
	}
	return data, nil
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
				binBytes, err := io.ReadAll(io.LimitReader(rc, maxUpdateBytes+1))
				if err != nil {
					return nil, fmt.Errorf("failed to read binary from zip: %w", err)
				}
				if len(binBytes) > maxUpdateBytes {
					return nil, fmt.Errorf("binary inside zip exceeds %d MB", maxUpdateBytes>>20)
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
			binBytes, err := io.ReadAll(io.LimitReader(tarReader, maxUpdateBytes+1))
			if err != nil {
				return nil, fmt.Errorf("failed to read binary from tar: %w", err)
			}
			if len(binBytes) > maxUpdateBytes {
				return nil, fmt.Errorf("binary inside tar exceeds %d MB", maxUpdateBytes>>20)
			}
			return bytes.NewReader(binBytes), nil
		}
	}
	return nil, fmt.Errorf("executable not found inside the downloaded tar.gz")
}
