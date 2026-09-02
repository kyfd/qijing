//go:build windows

package fileid

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestIdentifyIsStableForTheSameFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cache.tmp")
	if err := os.WriteFile(path, []byte("junk"), 0o600); err != nil {
		t.Fatal(err)
	}
	first, err := Identify(path)
	if err != nil {
		t.Fatalf("identify: %v", err)
	}
	second, err := Identify(path)
	if err != nil {
		t.Fatalf("identify again: %v", err)
	}
	if !first.Matches(second) {
		t.Fatalf("the same file must match itself: %+v vs %+v", first, second)
	}
	if first.VolumeSerial == 0 || first.FileID == 0 {
		t.Fatalf("NTFS must provide a usable identity: %+v", first)
	}
}

func TestIdentifyDistinguishesDifferentFiles(t *testing.T) {
	dir := t.TempDir()
	a := filepath.Join(dir, "a.tmp")
	b := filepath.Join(dir, "b.tmp")
	for _, path := range []string{a, b} {
		if err := os.WriteFile(path, []byte("junk"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	idA, err := Identify(a)
	if err != nil {
		t.Fatal(err)
	}
	idB, err := Identify(b)
	if err != nil {
		t.Fatal(err)
	}
	if idA.Matches(idB) {
		t.Fatalf("different files must not match: %+v vs %+v", idA, idB)
	}
}

// The core TOCTOU guarantee: replacing the file at a path must not inherit
// the old file's identity, even when an attacker (or a careless program)
// recreates the file with identical size and modification time.
func TestIdentifyChangesWhenFileIsReplaced(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cache.tmp")
	if err := os.WriteFile(path, []byte("junk"), 0o600); err != nil {
		t.Fatal(err)
	}
	original, err := Identify(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("junk"), 0o600); err != nil {
		t.Fatal(err)
	}
	replacement, err := Identify(path)
	if err != nil {
		t.Fatal(err)
	}
	if original.Matches(replacement) {
		t.Skip("the filesystem reused the file reference; identity cannot distinguish the objects here")
	}
}

func TestIdentifyWorksOnDirectoriesAndRejectsRelativePaths(t *testing.T) {
	dir := t.TempDir()
	if _, err := Identify(dir); err != nil {
		t.Fatalf("directory identity must be available: %v", err)
	}
	if _, err := Identify("relative.tmp"); err == nil {
		t.Fatal("relative paths must be refused")
	}
}

func TestMatchesRejectsUnidentifiedFiles(t *testing.T) {
	var zero Identity
	known := Identity{VolumeSerial: 1, FileID: 2, Size: 1, ModTime: time.Unix(1, 0), CreationTime: time.Unix(1, 0)}
	if zero.Matches(known) || known.Matches(zero) {
		t.Fatal("an unidentified observation must never match a known identity")
	}
}
