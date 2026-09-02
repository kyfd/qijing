//go:build !windows

package appdir

// verifyUserPrivate is a Windows assertion; on other platforms the data
// directory permissions are enforced by MkdirAll's mode bits (0o700).
func verifyUserPrivate(path string) error { return nil }
