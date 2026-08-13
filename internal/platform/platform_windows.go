//go:build windows

package platform

import (
	"fmt"
	"os/exec"
	"path/filepath"
)

// Reveal opens Explorer and selects path. It never changes the target file.
func Reveal(path string) error {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return fmt.Errorf("resolve path: %w", err)
	}
	cmd := exec.Command("explorer.exe", "/select,"+absolute)
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("open Explorer: %w", err)
	}
	return nil
}

// OpenBrowser opens a URL using the Windows shell.
func OpenBrowser(url string) error {
	cmd := exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("open browser: %w", err)
	}
	return nil
}
