//go:build !windows

package scanbroker

import "os/exec"

// assignJobObject is a Windows hardening; other platforms rely on explicit
// termination and are development-only. Child CPU accounting is unavailable.
func assignJobObject(cmd *exec.Cmd) (stop func() error, queryChildCPU func() float64, err error) {
	return func() error { return nil }, nil, nil
}
