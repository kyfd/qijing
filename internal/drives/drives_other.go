//go:build !windows

package drives

// List returns no drives on unsupported operating systems. This keeps desktop
// package tests portable while drive discovery remains Windows-specific.
func List() ([]Drive, error) {
	return []Drive{}, nil
}
