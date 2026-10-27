package cmd

import (
	"strings"
	"testing"
)

// Real GoReleaser output uses sha256sum format: digest, two spaces, filename.
const goreleaserChecksums = `2536668617c8e0e62d7b6f9929be5ba8a68ecba4acb579eeb6b148014fc39580  dotsync-darwin-amd64.tar.gz
1b7f8d0ebdad7ca006bdc14230ced93d53350b9b82395a405b2d947cb3b44579  dotsync-linux-arm64.tar.gz
a02de1a926161382936443b0e6aaaa36a2223f557fd81be0ee5ecee2736274d8  dotsync-linux-amd64.tar.gz
e7a033d43e6d3f35957f8d5093c247837d49680b347abca2b8eb00dd43e790c6  dotsync-windows-amd64.zip
`

func TestParseChecksums(t *testing.T) {
	got, err := parseChecksums([]byte(goreleaserChecksums))
	if err != nil {
		t.Fatalf("parseChecksums: %v", err)
	}
	want := "a02de1a926161382936443b0e6aaaa36a2223f557fd81be0ee5ecee2736274d8"
	if got["dotsync-linux-amd64.tar.gz"] != want {
		t.Fatalf("linux/amd64 digest = %q, want %q", got["dotsync-linux-amd64.tar.gz"], want)
	}
	if len(got) != 4 {
		t.Fatalf("parsed %d entries, want 4", len(got))
	}
}

func TestParseChecksumsToleratesCRLFAndBinaryMarker(t *testing.T) {
	body := strings.ReplaceAll(goreleaserChecksums, "\n", "\r\n")
	body = strings.ReplaceAll(body, "  dotsync-linux-arm64.tar.gz", " *dotsync-linux-arm64.tar.gz")

	got, err := parseChecksums([]byte(body))
	if err != nil {
		t.Fatalf("parseChecksums: %v", err)
	}
	if _, ok := got["dotsync-linux-arm64.tar.gz"]; !ok {
		t.Fatalf("binary-mode entry not parsed: %#v", got)
	}
}

func TestParseChecksumsSkipsMalformedLines(t *testing.T) {
	body := "not-a-digest  dotsync-linux-amd64.tar.gz\n" +
		strings.Repeat("a", 63) + "  dotsync-linux-arm64.tar.gz\n" +
		strings.Repeat("z", 64) + "  dotsync-windows-amd64.zip\n" +
		"deadbeef  too  many  fields\n" +
		"\n"
	if _, err := parseChecksums([]byte(body)); err == nil {
		t.Fatal("expected an error when no valid entry remains")
	}
}

func TestSelectAsset(t *testing.T) {
	checksums := func(names ...string) map[string]string {
		m := make(map[string]string, len(names))
		for _, n := range names {
			m[n] = strings.Repeat("a", 64)
		}
		return m
	}
	assets := func(names ...string) []githubAsset {
		out := make([]githubAsset, 0, len(names))
		for _, n := range names {
			out = append(out, githubAsset{Name: n, BrowserDownloadURL: "https://example.test/" + n})
		}
		return out
	}

	t.Run("native asset wins", func(t *testing.T) {
		name, url, _, err := selectAsset("windows", "arm64",
			assets("dotsync-windows-amd64.zip", "dotsync-windows-arm64.zip"),
			checksums("dotsync-windows-amd64.zip", "dotsync-windows-arm64.zip"))
		if err != nil {
			t.Fatalf("selectAsset: %v", err)
		}
		if name != "dotsync-windows-arm64.zip" || url != "https://example.test/dotsync-windows-arm64.zip" {
			t.Fatalf("picked %s (%s), want the native arm64 asset", name, url)
		}
	})

	// v3.5.0 shipped windows/amd64 only; Windows on ARM must still install it.
	t.Run("windows/arm64 falls back to amd64", func(t *testing.T) {
		name, _, _, err := selectAsset("windows", "arm64",
			assets("dotsync-windows-amd64.zip"),
			checksums("dotsync-windows-amd64.zip"))
		if err != nil {
			t.Fatalf("selectAsset: %v", err)
		}
		if name != "dotsync-windows-amd64.zip" {
			t.Fatalf("picked %s, want dotsync-windows-amd64.zip", name)
		}
	})

	t.Run("asset without checksum is rejected", func(t *testing.T) {
		if _, _, _, err := selectAsset("linux", "amd64",
			assets("dotsync-linux-amd64.tar.gz"),
			checksums("dotsync-linux-arm64.tar.gz")); err == nil {
			t.Fatal("expected an error for an unverifiable asset")
		}
	})

	t.Run("checksum without asset is rejected", func(t *testing.T) {
		if _, _, _, err := selectAsset("linux", "amd64",
			assets("dotsync-linux-arm64.tar.gz"),
			checksums("dotsync-linux-amd64.tar.gz")); err == nil {
			t.Fatal("expected an error when the archive is not published")
		}
	})
}

func TestCompareSemver(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"v3.5.0", "3.5.0", 0},
		{"3.6.0", "3.5.9", 1},
		{"3.5.0", "3.6.0", -1},
		{"3.5.0", "3.5", 0},
		{"v3.5.0-rc1", "3.5.0", 0},
		{"dev", "3.5.0", 0},
	}
	for _, tc := range cases {
		if got := compareSemver(tc.a, tc.b); got != tc.want {
			t.Errorf("compareSemver(%q, %q) = %d, want %d", tc.a, tc.b, got, tc.want)
		}
	}
}
