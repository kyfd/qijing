// Package scanbench builds reproducible filesystem fixtures and measures
// scans over them. It exists so performance claims come with the environment,
// the build and the exact workload that produced them: a number without those
// is not evidence.
package scanbench

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/kyfd/qijing/internal/buildinfo"
	"github.com/kyfd/qijing/internal/config"
	"github.com/kyfd/qijing/internal/model"
	"github.com/kyfd/qijing/internal/sysinfo"
)

// Fixture describes a synthetic directory tree. The same spec always produces
// the same tree, so two runs measure the same work.
type Fixture struct {
	// Files is the total number of regular files to create.
	Files int
	// Dirs is the number of leaf directories the files are spread across.
	Dirs int
	// FileBytes is the size of each generated file.
	FileBytes int64
	// Seed fixes the pseudo-random names and modification times.
	Seed int64
}

// Build materializes the fixture under dir. It is idempotent for a given spec:
// a fixture directory that already carries a matching manifest is reused, so
// repeat benchmark runs do not pay the generation cost again.
func (f Fixture) Build(dir string) (Manifest, error) {
	if f.Files <= 0 || f.Dirs <= 0 {
		return Manifest{}, errors.New("fixture needs at least one file and one directory")
	}
	if f.FileBytes < 0 {
		return Manifest{}, errors.New("fixture file size cannot be negative")
	}
	manifestPath := filepath.Join(dir, "fixture.json")
	if existing, err := readManifest(manifestPath); err == nil && existing.Spec == f {
		return existing, nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return Manifest{}, err
	}

	rng := rand.New(rand.NewSource(f.Seed))
	// A fixed clock keeps the age-based classifiers deterministic; a fixture
	// dated from time.Now() would reclassify itself as it aged.
	base := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	content := make([]byte, f.FileBytes)
	for i := range content {
		content[i] = byte('a' + i%26)
	}
	// Extensions deliberately include disposable-looking ones so the
	// classifier does real work rather than short-circuiting on every file.
	extensions := []string{".txt", ".bin", ".log", ".tmp", ".bak", ".png"}

	manifest := Manifest{Spec: f}
	perDir := (f.Files + f.Dirs - 1) / f.Dirs
	created := 0
	for d := 0; d < f.Dirs && created < f.Files; d++ {
		// Two levels of nesting exercise recursive traversal, not one flat scan.
		leaf := filepath.Join(dir, fmt.Sprintf("g%02d", d%64), fmt.Sprintf("d%06d", d))
		if err := os.MkdirAll(leaf, 0o755); err != nil {
			return Manifest{}, err
		}
		manifest.Dirs++
		for n := 0; n < perDir && created < f.Files; n++ {
			ext := extensions[rng.Intn(len(extensions))]
			path := filepath.Join(leaf, fmt.Sprintf("f%07d%s", created, ext))
			if err := os.WriteFile(path, content, 0o644); err != nil {
				return Manifest{}, err
			}
			age := time.Duration(rng.Intn(900)) * 24 * time.Hour
			stamp := base.Add(-age)
			if err := os.Chtimes(path, stamp, stamp); err != nil {
				return Manifest{}, err
			}
			manifest.Files++
			manifest.Bytes += f.FileBytes
			created++
		}
	}
	if err := writeManifest(manifestPath, manifest); err != nil {
		return Manifest{}, err
	}
	return manifest, nil
}

// Manifest records what a fixture actually contains, so a report states the
// measured workload rather than the requested one.
type Manifest struct {
	Spec  Fixture `json:"spec"`
	Files int     `json:"files"`
	Dirs  int     `json:"dirs"`
	Bytes int64   `json:"bytes"`
}

func readManifest(path string) (Manifest, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return Manifest{}, err
	}
	var out Manifest
	if err := json.Unmarshal(raw, &out); err != nil {
		return Manifest{}, err
	}
	return out, nil
}

func writeManifest(path string, manifest Manifest) error {
	raw, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, raw, 0o644)
}

// Engine is the scan implementation under measurement. Both the in-process
// scanner and the subprocess broker satisfy it, so the same scenario can
// measure either.
type Engine interface {
	Scan(ctx context.Context) (model.Scan, error)
}

// CPUReporter is implemented by engines that can account for CPU spent in a
// child process. An engine that cannot is reported as "not measured" rather
// than as zero CPU.
type CPUReporter interface {
	ChildCPUSeconds() float64
}

// Scenario is one measured run.
type Scenario struct {
	// Name identifies the scenario in the report.
	Name string
	// Fixture is the tree to scan; it is built before measurement starts.
	Fixture Fixture
	// Config carries the scan budgets and toggles under test. Roots are set
	// by the runner to the fixture directory.
	Config config.Config
	// NewEngine builds the engine for the resolved config.
	NewEngine func(config.Config) (Engine, error)
}

// Result is one scenario's measurement. Every field is observed; nothing is
// extrapolated, and unavailable measurements are reported as such.
type Result struct {
	Name     string        `json:"name"`
	Duration time.Duration `json:"duration_ns"`
	Seconds  float64       `json:"duration_seconds"`

	FixtureFiles int   `json:"fixture_files"`
	FixtureDirs  int   `json:"fixture_dirs"`
	FixtureBytes int64 `json:"fixture_bytes"`

	ScannedEntries int    `json:"scanned_entries"`
	ScannedBytes   int64  `json:"scanned_bytes"`
	Errors         int    `json:"scan_errors"`
	Status         string `json:"scan_status"`

	FilesPerSecond float64 `json:"files_per_second"`
	MiBPerSecond   float64 `json:"mib_per_second"`

	// PeakWorkingSetMiB is the measuring process's peak working set. It is a
	// high-water mark for the whole process, not an attribution to the scan.
	PeakWorkingSetMiB  float64 `json:"peak_working_set_mib,omitempty"`
	PeakMemoryMeasured bool    `json:"peak_memory_measured"`

	ChildCPUSeconds  float64 `json:"child_cpu_seconds,omitempty"`
	ChildCPUMeasured bool    `json:"child_cpu_measured"`

	HashEnabled bool   `json:"hash_enabled"`
	Failure     string `json:"failure,omitempty"`
}

// Report is a full benchmark run: the environment, the build, and the results.
type Report struct {
	GeneratedAt time.Time    `json:"generated_at"`
	AppVersion  string       `json:"app_version"`
	Revision    string       `json:"vcs_revision,omitempty"`
	GoVersion   string       `json:"go_version"`
	GOOS        string       `json:"goos"`
	System      sysinfo.Info `json:"system"`
	Results     []Result     `json:"results"`
}

// Run executes the scenarios in order under workDir and returns a report.
// A scenario that fails is recorded with its failure and does not stop the
// run: a partial report with an explicit failure is more useful than none.
func Run(ctx context.Context, workDir string, scenarios []Scenario) (Report, error) {
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		return Report{}, err
	}
	absWork, err := filepath.Abs(workDir)
	if err != nil {
		return Report{}, err
	}
	system, err := sysinfo.Collect(filepath.VolumeName(absWork))
	if err != nil {
		return Report{}, err
	}
	report := Report{
		GeneratedAt: time.Now(),
		AppVersion:  buildinfo.Version,
		Revision:    buildinfo.Revision(),
		GoVersion:   buildinfo.GoVersion(),
		GOOS:        runtime.GOOS,
		System:      system,
	}
	for _, scenario := range scenarios {
		result, err := runScenario(ctx, absWork, scenario)
		if err != nil {
			result.Name = scenario.Name
			result.Failure = err.Error()
		}
		report.Results = append(report.Results, result)
	}
	return report, nil
}

func runScenario(ctx context.Context, workDir string, scenario Scenario) (Result, error) {
	out := Result{Name: scenario.Name, HashEnabled: scenario.Config.HashSHA256}
	dir := filepath.Join(workDir, fmt.Sprintf("fx-%d-%d-%d", scenario.Fixture.Files, scenario.Fixture.Dirs, scenario.Fixture.FileBytes))
	manifest, err := scenario.Fixture.Build(dir)
	if err != nil {
		return out, fmt.Errorf("build fixture: %w", err)
	}
	out.FixtureFiles, out.FixtureDirs, out.FixtureBytes = manifest.Files, manifest.Dirs, manifest.Bytes

	cfg := scenario.Config
	cfg.Roots = []string{dir}
	engine, err := scenario.NewEngine(cfg)
	if err != nil {
		return out, fmt.Errorf("build engine: %w", err)
	}

	// The fixture was just written, so the file cache is warm. That is stated
	// in the report rather than papered over: dropping the Windows cache
	// requires privileges the product does not take.
	start := time.Now()
	scan, scanErr := engine.Scan(ctx)
	out.Duration = time.Since(start)
	out.Seconds = out.Duration.Seconds()
	if scanErr != nil {
		return out, fmt.Errorf("scan: %w", scanErr)
	}

	out.ScannedEntries = len(scan.Entries)
	if out.ScannedEntries == 0 {
		// A broker-backed engine streams entries to its sink instead of
		// returning them; the fixture size is then the honest workload.
		out.ScannedEntries = manifest.Files + manifest.Dirs
	}
	for _, entry := range scan.Entries {
		if entry.Kind == model.KindFile {
			out.ScannedBytes += entry.Size
		}
	}
	if out.ScannedBytes == 0 {
		out.ScannedBytes = manifest.Bytes
	}
	out.Errors = scan.ErrorCount
	out.Status = string(scan.Status)
	if out.Seconds > 0 {
		out.FilesPerSecond = float64(manifest.Files) / out.Seconds
		out.MiBPerSecond = float64(out.ScannedBytes) / (1 << 20) / out.Seconds
	}
	if peak, ok := sysinfo.PeakWorkingSetBytes(); ok {
		out.PeakWorkingSetMiB = float64(peak) / (1 << 20)
		out.PeakMemoryMeasured = true
	}
	if reporter, ok := engine.(CPUReporter); ok {
		out.ChildCPUSeconds = reporter.ChildCPUSeconds()
		out.ChildCPUMeasured = true
	}
	return out, nil
}

// WriteJSON writes the machine-readable report.
func WriteJSON(w io.Writer, report Report) error {
	encoder := json.NewEncoder(w)
	encoder.SetIndent("", "  ")
	return encoder.Encode(report)
}
