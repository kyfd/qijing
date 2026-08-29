//go:build !windows

package platform

import (
	"errors"
	"fmt"
)

func Reveal(string) error {
	return fmt.Errorf("revealing files is currently supported on Windows only")
}

func OpenBrowser(string) error {
	return fmt.Errorf("opening the browser is currently supported on Windows only")
}

// ErrRecycleAborted keeps the Windows error value referenceable on other
// platforms so callers compile without build tags.
var ErrRecycleAborted = errors.New("the item was not moved to the Recycle Bin")

func MoveToRecycleBin(string) error {
	return fmt.Errorf("the Recycle Bin is currently supported on Windows only")
}
