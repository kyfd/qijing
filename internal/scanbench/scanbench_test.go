package scanbench

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kyfd/qijing/internal/config"
	"github.com/kyfd/qijing/internal/model"
	"github.com/kyfd/qijing/internal/scanner"
)

func benchConfig() config.Config {
	cfg := config.Default()
	cfg.MaxEntries = 0
	cfg.MaxErrors = 0
	cfg.MaxDuration = 0
	cfg.ExcludedNames = nil
	return cfg
}

// A benchmark is only repeatable if the same spec produces the same tree.
func TestFixtureIsDeterministicAndReusedOnRebuild(t *testing.T) {
	spec := Fixture{Files: 40, Dirs: 4, FileBytes: 16, Seed: 7}

	first := filepath.Join(t.TempDir(), "fx")
	manifestA, err := spec.Build(first)
	if err != nil {
		t.Fatal(err)
	}
	second := filepath.Join(t.TempDir(), "fx")
	manifestB, err := spec.Build(second)
	if err != nil {
		t.Fatal(err)
	}
	if manifestA != manifestB {
		t.Fatalf("manifests differ: %+v vs %+v", manifestA, manifestB)
	}
	if manifestA.Files != 40 || manifestA.Bytes != 40*16 {
		t.Fatalf("manifest = %+v", manifestA)
	}
	if namesA, namesB := walkNames(t, first), walkNames(t, second); namesA != namesB {
		t.Fatalf("fixture trees differ:\n%s\n---\n%s", namesA, namesB)
	}

	// A rebuild of an identical spec must reuse the tree rather than rewrite
	// it, so repeated benchmark runs measure the same on-disk layout.
	marker := filepath.Join(first, "g00", "d000000", "marker")
	if err := os.WriteFile(marker, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := spec.Build(first); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("rebuild regenerated an existing fixture: %v", err)
	}
}

// A changed spec must not silently reuse the old tree, or the report would
// describe a workload that was never scanned.
func TestFixtureRebuildsWhenSpecChanges(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "fx")
	if _, err := (Fixture{Files: 10, Dirs: 2, FileBytes: 8, Seed: 1}).Build(dir); err != nil {
		t.Fatal(err)
	}
	manifest, err := (Fixture{Files: 20, Dirs: 2, FileBytes: 8, Seed: 1}).Build(dir)
	if err != nil {
		t.Fatal(err)
	}
	if manifest.Files != 20 {
		t.Fatalf("manifest.Files = %d, want 20", manifest.Files)
	}
}

func TestFixtureRejectsEmptySpecs(t *testing.T) {
	dir := t.TempDir()
	for _, spec := range []Fixture{{}, {Files: 1}, {Dirs: 1}, {Files: 1, Dirs: 1, FileBytes: -1}} {
		if _, err := spec.Build(filepath.Join(dir, "x")); err == nil {
			t.Fatalf("spec %+v was accepted", spec)
		}
	}
}

// The report must describe the workload that was actually scanned, and must
// carry the build and environment that produced the numbers.
func TestRunMeasuresTheFixtureAndRecordsProvenance(t *testing.T) {
	work := t.TempDir()
	report, err := Run(context.Background(), work, []Scenario{{
		Name:      "smoke",
		Fixture:   Fixture{Files: 60, Dirs: 3, FileBytes: 32, Seed: 5},
		Config:    benchConfig(),
		NewEngine: func(cfg config.Config) (Engine, error) { return scanner.New(cfg) },
	}})
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Results) != 1 {
		t.Fatalf("results = %d", len(report.Results))
	}
	result := report.Results[0]
	if result.Failure != "" {
		t.Fatalf("scenario failed: %s", result.Failure)
	}
	if result.FixtureFiles != 60 || result.FixtureBytes != 60*32 {
		t.Fatalf("result describes the wrong workload: %+v", result)
	}
	// The scan sees the files plus the directories that hold them.
	if result.ScannedEntries < 60 {
		t.Fatalf("scanned entries = %d, want at least the 60 files", result.ScannedEntries)
	}
	if result.Seconds <= 0 || result.FilesPerSecond <= 0 {
		t.Fatalf("timing not measured: %+v", result)
	}
	if report.AppVersion == "" || report.GoVersion == "" {
		t.Fatalf("report is missing build provenance: %+v", report)
	}
	if report.System.LogicalCPUs <= 0 {
		t.Fatalf("report is missing environment: %+v", report.System)
	}
	// An in-process engine has no child process; claiming 0 CPU would read as
	// a measurement, so it must be marked unmeasured.
	if result.ChildCPUMeasured {
		t.Fatalf("in-process engine reported child CPU: %+v", result)
	}
}

// A failing scenario must be recorded, not swallowed, and must not abort the
// scenarios that follow it.
func TestRunRecordsScenarioFailureAndContinues(t *testing.T) {
	work := t.TempDir()
	report, err := Run(context.Background(), work, []Scenario{
		{
			Name:      "broken",
			Fixture:   Fixture{Files: 5, Dirs: 1, FileBytes: 4, Seed: 1},
			Config:    benchConfig(),
			NewEngine: func(config.Config) (Engine, error) { return nil, errors.New("engine unavailable") },
		},
		{
			Name:      "healthy",
			Fixture:   Fixture{Files: 5, Dirs: 1, FileBytes: 4, Seed: 1},
			Config:    benchConfig(),
			NewEngine: func(cfg config.Config) (Engine, error) { return scanner.New(cfg) },
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Results) != 2 {
		t.Fatalf("results = %d, want 2", len(report.Results))
	}
	if !strings.Contains(report.Results[0].Failure, "engine unavailable") {
		t.Fatalf("failure not recorded: %+v", report.Results[0])
	}
	if report.Results[1].Failure != "" {
		t.Fatalf("healthy scenario was affected: %+v", report.Results[1])
	}
}

// A scan whose entries were streamed elsewhere still has a known workload;
// the report must state it rather than reporting zero work.
func TestResultFallsBackToFixtureCountsForStreamingEngines(t *testing.T) {
	work := t.TempDir()
	report, err := Run(context.Background(), work, []Scenario{{
		Name:      "streaming",
		Fixture:   Fixture{Files: 12, Dirs: 2, FileBytes: 64, Seed: 9},
		Config:    benchConfig(),
		NewEngine: func(config.Config) (Engine, error) { return streamingEngine{}, nil },
	}})
	if err != nil {
		t.Fatal(err)
	}
	result := report.Results[0]
	if result.ScannedEntries != 12+2 || result.ScannedBytes != 12*64 {
		t.Fatalf("streaming fallback = %+v", result)
	}
	if !result.ChildCPUMeasured || result.ChildCPUSeconds != 1.5 {
		t.Fatalf("child CPU not taken from the engine: %+v", result)
	}
}

// streamingEngine returns no entries, mirroring the broker with a sink.
type streamingEngine struct{}

func (streamingEngine) Scan(context.Context) (model.Scan, error) {
	return model.Scan{Status: model.ScanStatusComplete, StartedAt: time.Now(), EndedAt: time.Now()}, nil
}
func (streamingEngine) ChildCPUSeconds() float64 { return 1.5 }

// The rendered report must never print an unmeasured value as a number.
func TestMarkdownMarksUnmeasuredValues(t *testing.T) {
	var out strings.Builder
	err := WriteMarkdown(&out, Report{Results: []Result{{
		Name: "n", FixtureFiles: 1, Seconds: 1,
		PeakMemoryMeasured: false, ChildCPUMeasured: false,
	}}})
	if err != nil {
		t.Fatal(err)
	}
	text := out.String()
	if !strings.Contains(text, "| 未测量 | 未测量 |") {
		t.Fatalf("unmeasured values were rendered as numbers:\n%s", text)
	}
	if !strings.Contains(text, "缓存是热的") {
		t.Fatalf("report omits the warm-cache caveat:\n%s", text)
	}
}

func walkNames(t *testing.T, root string) string {
	t.Helper()
	var names []string
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if rel == "fixture.json" {
			return nil
		}
		names = append(names, rel)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return strings.Join(names, "\n")
}
