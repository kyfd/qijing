//go:build !windows

package pathsafe

// RejectUnsafeFile is a no-op on systems without Windows reparse points and
// cloud placeholder attributes.
func RejectUnsafeFile(string) error {
	return nil
}
