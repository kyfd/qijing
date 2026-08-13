// Package pathsafe enforces explicit-root containment and rejects symbolic links.
package pathsafe

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

var (
	ErrOutsideRoot = errors.New("path is outside allowlisted root")
	ErrSymlink     = errors.New("symbolic links are not allowed")
	ErrReparse     = errors.New("reparse points are not allowed")
	ErrCloudFile   = errors.New("cloud placeholders are not allowed")
)

// ValidateRoot validates an absolute, existing directory and every component
// without following symbolic links.
func ValidateRoot(root string) (string, error) {
	if !filepath.IsAbs(root) {
		return "", fmt.Errorf("root is not absolute: %q", root)
	}
	root = filepath.Clean(root)
	if err := RejectSymlinkComponents(root); err != nil {
		return "", err
	}
	info, err := os.Lstat(root)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", fmt.Errorf("root is not a directory: %q", root)
	}
	return root, nil
}

// Contained returns a cleaned path only if it is at or below root. It is a
// lexical check and intentionally does not resolve links.
func Contained(root, candidate string) (string, error) {
	root = filepath.Clean(root)
	candidate = filepath.Clean(candidate)
	rel, err := filepath.Rel(root, candidate)
	if err != nil {
		return "", err
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return "", ErrOutsideRoot
	}
	return candidate, nil
}

// IsWholeDriveRoot reports whether root is the top of its filesystem volume.
// On Unix this is "/"; on Windows it includes roots such as "C:\\".
func IsWholeDriveRoot(root string) bool {
	root = filepath.Clean(root)
	volume := filepath.VolumeName(root)
	if volume == "" {
		return root == string(filepath.Separator)
	}
	return root == filepath.Clean(volume+string(filepath.Separator))
}

// RejectSymlinkComponents checks each existing path component with Lstat.
func RejectSymlinkComponents(path string) error {
	path = filepath.Clean(path)
	volume := filepath.VolumeName(path)
	rest := strings.TrimPrefix(path, volume)
	current := volume
	if strings.HasPrefix(rest, string(filepath.Separator)) {
		current += string(filepath.Separator)
		rest = strings.TrimLeft(rest, string(filepath.Separator))
	}
	for _, component := range strings.Split(rest, string(filepath.Separator)) {
		if component == "" || component == "." {
			continue
		}
		current = filepath.Join(current, component)
		info, err := os.Lstat(current)
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("%w: %s", ErrSymlink, current)
		}
		if err := RejectUnsafeFile(current); err != nil {
			return err
		}
	}
	return nil
}
