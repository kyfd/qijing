//go:build !windows

package platform

import "fmt"

func Reveal(string) error {
	return fmt.Errorf("revealing files is currently supported on Windows only")
}

func OpenBrowser(string) error {
	return fmt.Errorf("opening the browser is currently supported on Windows only")
}
