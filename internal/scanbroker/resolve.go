package scanbroker

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// EnvExecutable lets development and test environments point at a scanner
// binary outside the install layout.
const EnvExecutable = "QIJING_SCANNER_EXE"

// ResolveScannerExecutable finds the scanner binary: first the explicit
// environment override, then the application's own directory. There is no
// silent fallback to in-process scanning — a missing scanner executable is
// an installation error and must surface as one.
func ResolveScannerExecutable() (string, error) {
	if override := strings.TrimSpace(os.Getenv(EnvExecutable)); override != "" {
		info, err := os.Stat(override)
		if err != nil {
			return "", fmt.Errorf("%s: %w", EnvExecutable, err)
		}
		if info.IsDir() {
			return "", fmt.Errorf("%s points at a directory", EnvExecutable)
		}
		return override, nil
	}
	exe, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("resolve application directory: %w", err)
	}
	name := "qijing-scanner"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	candidate := filepath.Join(filepath.Dir(exe), name)
	if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
		return candidate, nil
	}
	return "", errors.New("qijing-scanner was not found next to the application; build it into the same directory or set " + EnvExecutable)
}
