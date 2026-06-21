package cmd

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
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
		RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Println("⏳ Checking for updates...")

			// 1. Fetch latest release from GitHub
			resp, err := http.Get("https://api.github.com/repos/Pruthviraj36/dotsync/releases/latest")
			if err != nil {
				return fmt.Errorf("failed to check for updates: %w", err)
			}
			defer resp.Body.Close()

			if resp.StatusCode != http.StatusOK {
				return fmt.Errorf("GitHub API returned status %d", resp.StatusCode)
			}

			var release githubRelease
			if err := json.NewDecoder(resp.Body).Decode(&release); err != nil {
				return fmt.Errorf("failed to parse release info: %w", err)
			}

			// 2. Check if we're already up to date
			if Version != "dev" && Version == release.TagName {
				fmt.Printf("✅ You are already using the latest version (%s)!\n", Version)
				return nil
			}

			fmt.Printf("📦 Found new version: %s (current: %s)\n", release.TagName, Version)
			fmt.Println("⏳ Downloading...")

			// 3. Find the correct asset for this OS/Arch
			ext := ".tar.gz"
			if runtime.GOOS == "windows" {
				ext = ".zip"
			}
			expectedAsset := fmt.Sprintf("dotsync-%s-%s%s", runtime.GOOS, runtime.GOARCH, ext)

			var downloadURL string
			for _, asset := range release.Assets {
				if asset.Name == expectedAsset {
					downloadURL = asset.BrowserDownloadURL
					break
				}
			}

			if downloadURL == "" {
				return fmt.Errorf("no suitable binary found for %s/%s in release %s", runtime.GOOS, runtime.GOARCH, release.TagName)
			}

			// 4. Download the asset
			assetResp, err := http.Get(downloadURL)
			if err != nil {
				return fmt.Errorf("failed to download update: %w", err)
			}
			defer assetResp.Body.Close()

			if assetResp.StatusCode != http.StatusOK {
				return fmt.Errorf("failed to download update (status %d)", assetResp.StatusCode)
			}

			bodyBytes, err := io.ReadAll(assetResp.Body)
			if err != nil {
				return fmt.Errorf("failed to read downloaded file: %w", err)
			}

			// 5. Extract the binary from the archive
			var binaryReader io.Reader

			if ext == ".zip" {
				zipReader, err := zip.NewReader(bytes.NewReader(bodyBytes), int64(len(bodyBytes)))
				if err != nil {
					return fmt.Errorf("failed to read zip archive: %w", err)
				}

				found := false
				for _, f := range zipReader.File {
					if f.Name == "dotsync.exe" || f.Name == "dotsync" {
						rc, err := f.Open()
						if err != nil {
							return fmt.Errorf("failed to open binary inside zip: %w", err)
						}
						defer rc.Close()

						binBytes, err := io.ReadAll(rc)
						if err != nil {
							return fmt.Errorf("failed to read binary from zip: %w", err)
						}
						binaryReader = bytes.NewReader(binBytes)
						found = true
						break
					}
				}
				if !found {
					return fmt.Errorf("executable not found inside the downloaded zip")
				}
			} else {
				// tar.gz extraction
				gzReader, err := gzip.NewReader(bytes.NewReader(bodyBytes))
				if err != nil {
					return fmt.Errorf("failed to read gzip archive: %w", err)
				}
				defer gzReader.Close()

				tarReader := tar.NewReader(gzReader)
				found := false
				for {
					header, err := tarReader.Next()
					if err == io.EOF {
						break
					}
					if err != nil {
						return fmt.Errorf("failed to read tar archive: %w", err)
					}

					base := filepath.Base(header.Name)
					if header.Typeflag == tar.TypeReg && (base == "dotsync" || base == "dotsync.exe") {
						binBytes, err := io.ReadAll(tarReader)
						if err != nil {
							return fmt.Errorf("failed to read binary from tar: %w", err)
						}
						binaryReader = bytes.NewReader(binBytes)
						found = true
						break
					}
				}
				if !found {
					return fmt.Errorf("executable not found inside the downloaded tar.gz")
				}
			}

			fmt.Println("⏳ Applying update...")

			// 6. Apply the self-update
			err = selfupdate.Apply(binaryReader, selfupdate.Options{})
			if err != nil {
				if strings.Contains(err.Error(), "permission denied") {
					return fmt.Errorf("permission denied. Try running with sudo/admin privileges")
				}
				return fmt.Errorf("failed to apply update: %w", err)
			}

			fmt.Printf("✅ Successfully updated to %s!\n", release.TagName)
			return nil
		},
	}
}
