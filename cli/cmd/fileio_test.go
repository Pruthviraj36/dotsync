package cmd

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWriteFileAtomicRefusesSymlink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target")
	link := filepath.Join(dir, "secret")
	if err := os.WriteFile(target, []byte("old"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if err := writeFileAtomic(link, []byte("new"), 0600); err == nil {
		t.Fatal("expected symlink write to be rejected")
	}
	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "old" {
		t.Fatalf("target changed to %q", data)
	}
}

func TestWriteFileAtomicReplacesFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "secrets")
	if err := writeFileAtomic(path, []byte("new"), 0600); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "new" {
		t.Fatalf("got %q", data)
	}
}
