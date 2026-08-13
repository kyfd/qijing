package pathsafe

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestContainedRejectsEscape(t *testing.T) {
	root := t.TempDir()
	if _, err := Contained(root, filepath.Join(root, "..", "secret")); !errors.Is(err, ErrOutsideRoot) {
		t.Fatalf("expected ErrOutsideRoot, got %v", err)
	}
	if _, err := Contained(root, filepath.Join(root, "safe", "file")); err != nil {
		t.Fatalf("contained path rejected: %v", err)
	}
}

func TestValidateRootRejectsSymlink(t *testing.T) {
	target := t.TempDir()
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, err := ValidateRoot(link); !errors.Is(err, ErrSymlink) {
		t.Fatalf("expected symlink rejection, got %v", err)
	}
}

func TestIsWholeDriveRoot(t *testing.T) {
	if !IsWholeDriveRoot(string(filepath.Separator)) {
		t.Fatal("filesystem root was not detected")
	}
	if IsWholeDriveRoot(t.TempDir()) {
		t.Fatal("ordinary directory detected as whole drive")
	}
}
