package application

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/kyfd/qijing/internal/classify"
	"github.com/kyfd/qijing/internal/config"
	"github.com/kyfd/qijing/internal/model"
	"github.com/kyfd/qijing/internal/scanner"
	"github.com/kyfd/qijing/internal/store"
)

// classify.Apply sorts Entry.Classes alphabetically, so zoneFor must not let
// that ordering decide which class wins. "dormant" sorts before "giant",
// "orphan" and "rotten" and used to shadow all three.
// The map draws only the largest entries while stats cover the whole scan.
// That gap must be reported, otherwise the canvas silently implies it is
// showing everything.

// inProcessScanFactory keeps scan-logic tests on the in-process engine; the
// subprocess broker has its own test suite in internal/scanbroker.
func inProcessScanFactory(cfg config.Config) (ScanEngine, error) {
	return scanner.New(cfg)
}

func TestBuildNodesReportsTruncation(t *testing.T) {
	entries := make([]model.Entry, mapNodeLimit+25)
	for i := range entries {
		entries[i] = model.Entry{ID: fmt.Sprintf("e%d", i), Kind: model.KindFile, Size: int64(i + 1), ModTime: time.Now()}
	}
	scan := model.Scan{ID: "s", Entries: entries}

	nodes := buildNodes(scan, false)
	if len(nodes) != mapNodeLimit {
		t.Fatalf("nodes = %d, want %d", len(nodes), mapNodeLimit)
	}
	// Truncation must keep the largest entries, not an arbitrary slice.
	if nodes[0].Size != int64(len(entries)) {
		t.Fatalf("largest node size = %d, want %d", nodes[0].Size, len(entries))
	}
	if total := len(mapEntries(scan)); total != len(entries) {
		t.Fatalf("mapEntries = %d, want %d", total, len(entries))
	}

	// Stats must still describe every file, not just the drawn ones.
	if got := stats(scan).Files; got != int64(len(entries)) {
		t.Fatalf("stats.Files = %d, want %d", got, len(entries))
	}
}

func TestZoneForPrefersSpecificClassOverDormant(t *testing.T) {
	cfg := config.Default()
	now := time.Now()
	old := now.Add(-400 * 24 * time.Hour)
	ancient := now.Add(-800 * 24 * time.Hour)

	cases := []struct {
		name  string
		entry model.Entry
		zone  string
	}{
		{"large and long untouched belongs to giants", model.Entry{Kind: model.KindFile, Size: 2 << 30, ModTime: old}, "giants"},
		{"stale temp file belongs to decay", model.Entry{Kind: model.KindFile, Extension: ".tmp", Size: 1024, ModTime: ancient}, "decay"},
		{"orphan extension outranks dormancy", model.Entry{Kind: model.KindFile, Extension: ".bak", Size: 1024, ModTime: old}, "downloads"},
		{"plain old file stays dormant", model.Entry{Kind: model.KindFile, Size: 1024, ModTime: old}, "zombies"},
		{"recent file is active", model.Entry{Kind: model.KindFile, Size: 1024, ModTime: now.Add(-30 * 24 * time.Hour)}, "active"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			entries := []model.Entry{tc.entry}
			classify.Apply(entries, now, cfg)
			zone, _, _ := zoneFor(entries[0])
			if zone != tc.zone {
				t.Fatalf("zone = %q, want %q (classes %v)", zone, tc.zone, entries[0].Classes)
			}
		})
	}
}

func TestAuthorizeRootsIsAtomicAndReportsInvalidPaths(t *testing.T) {
	service, err := New(Options{DataDir: t.TempDir(), ScanFactory: inProcessScanFactory})
	if err != nil {
		t.Fatal(err)
	}
	defer closeService(t, service)
	existing := t.TempDir()
	if _, err := service.AddRoot(context.Background(), existing); err != nil {
		t.Fatal(err)
	}
	valid := t.TempDir()
	missing := filepath.Join(t.TempDir(), "missing")
	result, err := service.AuthorizeRoots(context.Background(), BatchRootsRequestDTO{Paths: []string{valid, missing}, StartScan: true})
	if err != nil {
		t.Fatal(err)
	}
	if result.AuthorizationSucceeded || result.Scan != nil {
		t.Fatalf("unexpected success: %#v", result)
	}
	if len(result.Results) != 2 || result.Results[0].Status != RootAuthorizationValidated || result.Results[1].Status != RootAuthorizationInvalid || result.Results[1].Error == "" {
		t.Fatalf("results=%#v", result.Results)
	}
	roots := service.Roots(context.Background())
	if len(roots.Roots) != 1 || roots.Roots[0].Path != existing {
		t.Fatalf("authorization changed after invalid batch: %#v", roots)
	}
}

func TestAuthorizeRootsCollapsesParentsDuplicatesAndStartsScan(t *testing.T) {
	service, err := New(Options{DataDir: t.TempDir(), ScanFactory: inProcessScanFactory})
	if err != nil {
		t.Fatal(err)
	}
	defer closeService(t, service)
	parent := t.TempDir()
	child := filepath.Join(parent, "child")
	if err := os.Mkdir(child, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(child, "note.txt"), []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := service.AuthorizeRoots(context.Background(), BatchRootsRequestDTO{Paths: []string{child, parent, parent + string(filepath.Separator)}, StartScan: true})
	if err != nil {
		t.Fatal(err)
	}
	if !result.AuthorizationSucceeded || result.Scan == nil || !result.Scan.Accepted || result.ScanError != "" {
		t.Fatalf("batch=%#v", result)
	}
	if len(result.Roots) != 1 || result.Roots[0].Path != filepath.Clean(parent) {
		t.Fatalf("roots=%#v", result.Roots)
	}
	if result.Results[0].Status != RootAuthorizationCovered || result.Results[1].Status != RootAuthorizationAuthorized || result.Results[2].Status != RootAuthorizationDuplicate {
		t.Fatalf("results=%#v", result.Results)
	}
	waitIdle(t, service)
	if got := service.Map(context.Background()).Stats.Files; got != 1 {
		t.Fatalf("scanned files=%d", got)
	}
}

func TestAuthorizeRootsExistingDescendantReplacedByParent(t *testing.T) {
	service, err := New(Options{DataDir: t.TempDir(), ScanFactory: inProcessScanFactory})
	if err != nil {
		t.Fatal(err)
	}
	defer closeService(t, service)
	parent := t.TempDir()
	child := filepath.Join(parent, "child")
	if err := os.Mkdir(child, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := service.AddRoot(context.Background(), child); err != nil {
		t.Fatal(err)
	}
	result, err := service.AuthorizeRoots(context.Background(), BatchRootsRequestDTO{Paths: []string{parent}})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Roots) != 1 || result.Roots[0].Path != parent || result.Results[0].Status != RootAuthorizationAuthorized {
		t.Fatalf("result=%#v", result)
	}
}

func TestServicePersistsRootsAndRestoresLatestScan(t *testing.T) {
	dataDir := t.TempDir()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "note.txt"), []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	service, err := New(Options{DataDir: dataDir, ScanFactory: inProcessScanFactory})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.AddRoot(context.Background(), root); err != nil {
		t.Fatal(err)
	}
	if _, err := service.StartScan(context.Background()); err != nil {
		t.Fatal(err)
	}
	waitIdle(t, service)
	first := service.Map(context.Background())
	if first.Stats.Files != 1 {
		t.Fatalf("files=%d", first.Stats.Files)
	}
	closeCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := service.Close(closeCtx); err != nil {
		t.Fatal(err)
	}

	restarted, err := New(Options{DataDir: dataDir, ScanFactory: inProcessScanFactory})
	if err != nil {
		t.Fatal(err)
	}
	defer closeService(t, restarted)
	roots := restarted.Roots(context.Background())
	if len(roots.Roots) != 1 || roots.Roots[0].Path != root {
		t.Fatalf("roots=%#v", roots)
	}
	if got := restarted.Map(context.Background()).Stats.Files; got != 1 {
		t.Fatalf("restored files=%d", got)
	}
}

func TestRootMutationIsRejectedWhileScanRuns(t *testing.T) {
	service, err := New(Options{DataDir: t.TempDir(), ScanFactory: inProcessScanFactory})
	if err != nil {
		t.Fatal(err)
	}
	defer closeService(t, service)
	root := t.TempDir()
	if _, err := service.AddRoot(context.Background(), root); err != nil {
		t.Fatal(err)
	}
	service.manager.factory = func(config.Config) (ScanEngine, error) {
		return &blockingEngine{started: make(chan struct{})}, nil
	}
	if _, err := service.StartScan(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := service.RemoveRoot(context.Background(), root); !errors.Is(err, ErrScanRunning) {
		t.Fatalf("RemoveRoot() error = %v, want ErrScanRunning", err)
	}
	if _, err := service.AddRoot(context.Background(), t.TempDir()); !errors.Is(err, ErrScanRunning) {
		t.Fatalf("AddRoot() error = %v, want ErrScanRunning", err)
	}
	if got := service.Roots(context.Background()).Roots; len(got) != 1 || got[0].Path != root {
		t.Fatalf("roots changed during scan: %#v", got)
	}
	if err := service.CancelScan(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestScanManagerDetachedSingleCancelAndFailureKeepsSnapshot(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	old := model.Scan{ID: "old", StartedAt: time.Now(), EndedAt: time.Now(), Entries: []model.Entry{{ID: "old-entry", Kind: model.KindFile, Size: 7}}}
	if err := db.SaveScan(context.Background(), old); err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{})
	engine := &blockingEngine{started: started}
	manager := newScanManager(context.Background(), db, old, func(config.Config) (ScanEngine, error) { return engine, nil })

	// ScanManager accepts only the application-owned context, so a caller's
	// cancelled request cannot be retained by Start.
	if _, err := manager.Start(config.Config{Roots: []string{t.TempDir()}}); err != nil {
		t.Fatalf("start with cancelled request context semantics: %v", err)
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("background scan did not start")
	}
	if _, err := manager.Start(config.Config{Roots: []string{t.TempDir()}}); !errors.Is(err, ErrScanRunning) {
		t.Fatalf("second start error=%v", err)
	}
	if err := manager.Cancel(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-engine.finished:
	case <-time.After(time.Second):
		t.Fatal("cancel did not reach scan")
	}
	for {
		scan, state, _, _, _, _, _ := manager.snapshot()
		if state == ScanIdle {
			if scan.ID != old.ID {
				t.Fatalf("cancelled scan replaced snapshot: %#v", scan)
			}
			break
		}
		time.Sleep(time.Millisecond)
	}
}

func TestScanManagerCloseIsBounded(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	engine := &stubbornEngine{release: make(chan struct{})}
	manager := newScanManager(context.Background(), db, model.Scan{}, func(config.Config) (ScanEngine, error) { return engine, nil })
	if _, err := manager.Start(config.Config{Roots: []string{t.TempDir()}}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := manager.Close(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("close error=%v", err)
	}
	close(engine.release)
	ctx2, cancel2 := context.WithTimeout(context.Background(), time.Second)
	defer cancel2()
	if err := manager.Close(ctx2); err != nil {
		t.Fatal(err)
	}
}

type reportingEngine struct {
	callback func(scanner.Progress)
	release  chan struct{}
}

func (e *reportingEngine) SetProgressCallback(callback func(scanner.Progress)) { e.callback = callback }
func (e *reportingEngine) Scan(context.Context) (model.Scan, error) {
	e.callback(scanner.Progress{Phase: scanner.PhaseTraversing, ObservedEntries: 12, Files: 7, RootsStarted: 1, RootsTotal: 2, EntryBudget: 100})
	<-e.release
	return model.Scan{ID: "new", StartedAt: time.Now(), EndedAt: time.Now(), Status: model.ScanStatusComplete}, nil
}

func TestScanManagerProgressIsTaskIDSafe(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	engine := &reportingEngine{release: make(chan struct{})}
	manager := newScanManager(context.Background(), db, model.Scan{ID: "old"}, func(config.Config) (ScanEngine, error) { return engine, nil })
	started, err := manager.Start(config.Config{Roots: []string{t.TempDir()}})
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for {
		_, state, _, _, _, progress, ok := manager.snapshot()
		if ok && progress.ObservedEntries == 12 {
			if state != ScanRunning || progress.Files != 7 || progress.RootsTotal != 2 {
				t.Fatalf("snapshot=%#v state=%s", progress, state)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("progress not received")
		}
		time.Sleep(time.Millisecond)
	}
	manager.updateProgress("stale-task", scanner.Progress{ObservedEntries: 999})
	_, _, taskID, _, _, progress, _ := manager.snapshot()
	if taskID != started.ScanID || progress.ObservedEntries != 12 {
		t.Fatalf("stale callback overwrote current task: task=%q progress=%#v", taskID, progress)
	}
	close(engine.release)
	for {
		_, state, _, _, _, _, _ := manager.snapshot()
		if state == ScanIdle {
			break
		}
		time.Sleep(time.Millisecond)
	}
}

type blockingEngine struct {
	started  chan struct{}
	finished chan struct{}
}

func (e *blockingEngine) Scan(ctx context.Context) (model.Scan, error) {
	e.finished = make(chan struct{})
	close(e.started)
	<-ctx.Done()
	close(e.finished)
	return model.Scan{}, ctx.Err()
}

type stubbornEngine struct{ release chan struct{} }

func (e *stubbornEngine) Scan(context.Context) (model.Scan, error) {
	<-e.release
	return model.Scan{}, errors.New("failed after release")
}

func waitIdle(t *testing.T, service *Service) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for service.Status(context.Background()).Scanning {
		if time.Now().After(deadline) {
			t.Fatal("scan did not finish")
		}
		time.Sleep(time.Millisecond)
	}
}

func closeService(t *testing.T, service *Service) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := service.Close(ctx); err != nil {
		t.Error(err)
	}
}
