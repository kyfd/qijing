//go:build windows

package pathsafe

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestJunctionEscapeRejected(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("no"), 0o600); err != nil {
		t.Fatal(err)
	}
	junction := filepath.Join(root, "escape")
	out, err := exec.Command("cmd", "/c", "mklink", "/J", junction, outside).CombinedOutput()
	if err != nil {
		t.Skipf("junctions unavailable: %v\n%s", err, out)
	}
	t.Cleanup(func() { _ = os.Remove(junction) })

	if err := RejectUnsafeFile(junction); !errors.Is(err, ErrReparse) {
		t.Fatalf("junction must be rejected as a reparse point, got %v", err)
	}
	if _, err := ValidateRoot(junction); !errors.Is(err, ErrReparse) && !errors.Is(err, ErrSymlink) {
		t.Fatalf("junction root must be rejected, got %v", err)
	}
}
