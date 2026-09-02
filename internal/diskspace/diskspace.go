// Package diskspace reports free disk space for the resource guards.
package diskspace

import "fmt"

// FreeBytes reports the bytes available to the current user on the volume
// holding path.
func FreeBytes(path string) (int64, error) {
	free, err := freeBytes(path)
	if err != nil {
		return 0, fmt.Errorf("query free disk space for %s: %w", volumeKind(path), err)
	}
	return free, nil
}
