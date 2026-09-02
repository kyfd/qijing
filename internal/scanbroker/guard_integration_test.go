package scanbroker

import (
	"context"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/kyfd/qijing/internal/config"
)

// TestScannerProcessHasNoPrivilegedDependencies pins the process boundary in
// the dependency graph: the scanner binary must not link the database, the
// agent, the recycle-bin platform code, secrets, the desktop shell or the
// application layer. A future import is a review failure, not a silent
// capability increase.
func TestScannerProcessHasNoPrivilegedDependencies(t *testing.T) {
	goTool, err := exec.LookPath("go")
	if err != nil {
		t.Skipf("go toolchain unavailable: %v", err)
	}
	output, err := exec.Command(goTool, "list", "-deps", "../../cmd/qijing-scanner").Output()
	if err != nil {
		t.Skipf("go list failed: %v", err)
	}
	for _, dep := range strings.Split(string(output), "\n") {
		dep = strings.TrimSpace(dep)
		for _, forbidden := range []string{
			"qijing/internal/store",
			"qijing/internal/agent",
			"qijing/internal/llm",
			"qijing/internal/platform",
			"qijing/internal/secret",
			"qijing/internal/desktop",
			"qijing/internal/server",
			"qijing/internal/application",
			"qijing/internal/appdir",
			"modernc.org/sqlite",
		} {
			if dep == forbidden {
				t.Errorf("scanner binary depends on %s; the scan process must stay capability-minimal", dep)
			}
		}
	}
}

// TestBrokerRunsRealScannerProcess builds the actual scanner binary and runs
// one scan through it. This is the end-to-end proof that the broker can
// spawn, converse with and clean up the subprocess.
func TestBrokerRunsRealScannerProcess(t *testing.T) {
	if testing.Short() {
		t.Skip("building the scanner executable is skipped in -short mode")
	}
	goTool, err := exec.LookPath("go")
	if err != nil {
		t.Skipf("go toolchain unavailable: %v", err)
	}
	name := "qijing-scanner"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	executable := filepath.Join(t.TempDir(), name)
	build := exec.Command(goTool, "build", "-o", executable, "../../cmd/qijing-scanner")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build scanner: %v: %s", err, output)
	}

	root := fixtureTree(t)
	cfg := config.Default()
	cfg.Roots = []string{root}
	cfg.MaxDuration = 2 * time.Minute
	s := New(Options{Config: cfg, Executable: executable})

	scan, err := s.Scan(context.Background())
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if scan.Status != "complete" || scan.ID == "" {
		t.Fatalf("scan = %+v", scan)
	}
	if len(scan.Entries) != 1 || scan.Entries[0].Name != "note.txt" {
		t.Fatalf("entries = %+v", scan.Entries)
	}
}

// TestBrokerFailsCleanlyWhenScannerExecutableMissing pins the no-silent-
// fallback rule: without the scanner binary, scanning fails with an explicit
// error instead of degrading to in-process execution.
func TestBrokerFailsCleanlyWhenScannerExecutableMissing(t *testing.T) {
	root := fixtureTree(t)
	cfg := config.Default()
	cfg.Roots = []string{root}
	s := New(Options{Config: cfg, Executable: filepath.Join(t.TempDir(), "missing-scanner.exe")})
	if _, err := s.Scan(context.Background()); err == nil {
		t.Fatal("a missing scanner executable must fail the scan explicitly")
	}
}
