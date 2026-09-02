//go:build !windows

package scanbroker

import "os/exec"

// assignJobObject is a Windows hardening; other platforms rely on explicit
// termination and are development-only.
func assignJobObject(cmd *exec.Cmd) (func() error, error) {
	return func() error { return nil }, nil
}
