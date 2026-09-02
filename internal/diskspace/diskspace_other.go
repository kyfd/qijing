//go:build !windows

package diskspace

import "math"

// volumeKind is a non-Windows placeholder; the product ships on Windows.
func volumeKind(path string) string { return path }

// freeBytes reports "unlimited" on non-Windows development platforms: the
// resource guard then simply never trips.
func freeBytes(path string) (int64, error) {
	return math.MaxInt64 / 2, nil
}
