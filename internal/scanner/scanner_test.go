package scanner

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"fileecosystem/internal/config"
	"fileecosystem/internal/model"
)

func TestScanInvariantsRelationsAndExclusions(t *testing.T) {
	root := t.TempDir()
	mustWrite := func(rel, value string) {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(value), 0600); err != nil {
			t.Fatal(err)
		}
	}
	mustWrite("a.txt", "same")
	mustWrite("b.txt", "same")
	mustWrite("bundle.zip", "zip")
	mustWrite("bundle/content.txt", "x")
	mustWrite(".ssh/secret", "never")
	outside := t.TempDir()
	mustWriteOutside := filepath.Join(outside, "outside.txt")
	if err := os.WriteFile(mustWriteOutside, []byte("outside"), 0600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "escape")
	symlinkOK := os.Symlink(outside, link) == nil
	cfg := config.Default()
	cfg.Roots = []string{root}
	cfg.HashSHA256 = true
	s, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	result, err := s.Scan(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var duplicate, zip bool
	for _, r := range result.Relations {
		duplicate = duplicate || r.Type == model.RelationDuplicate
		zip = zip || r.Type == model.RelationZIPExtract
	}
	if !duplicate || !zip {
		t.Fatalf("relations missing duplicate=%v zip=%v: %#v", duplicate, zip, result.Relations)
	}
	for _, e := range result.Entries {
		if strings.Contains(e.Relative, ".ssh") || strings.Contains(e.Path, outside) {
			t.Fatalf("excluded/outside entry leaked: %#v", e)
		}
		if e.Path == "" || !strings.HasPrefix(filepath.Clean(e.Path), filepath.Clean(root)) {
			t.Fatalf("containment invariant failed: %#v", e)
		}
	}
	if symlinkOK {
		found := false
		for _, message := range result.Errors {
			found = found || strings.Contains(message, "symlink rejected")
		}
		if !found {
			t.Fatal("symlink was not audited as rejected")
		}
	}
}

func TestScanRequiresExplicitAllowlist(t *testing.T) {
	cfg := config.Default()
	if _, err := New(cfg); err == nil {
		t.Fatal("scanner accepted empty allowlist")
	}
}

func TestScanEntryBudgetReturnsPartialMetadata(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"a", "b", "c"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(name), 0600); err != nil {
			t.Fatal(err)
		}
	}
	cfg := config.Default()
	cfg.Roots = []string{root}
	cfg.MaxEntries = 2
	s, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	result, err := s.Scan(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Entries) != 2 || !result.Partial || !result.Truncated || result.Status != model.ScanStatusPartial || result.TruncationReason != "entry_limit" {
		t.Fatalf("unexpected budget result: %#v", result)
	}
}

func TestScanDurationBudgetReturnsPartialMetadata(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "file"), []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.Roots = []string{root}
	cfg.MaxDuration = time.Second
	s, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	start := time.Unix(1_000, 0)
	calls := 0
	s.Now = func() time.Time {
		calls++
		if calls == 1 {
			return start
		}
		return start.Add(2 * time.Second)
	}
	result, err := s.Scan(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !result.Truncated || result.TruncationReason != "duration_limit" || result.Status != model.ScanStatusPartial {
		t.Fatalf("unexpected duration result: %#v", result)
	}
}

func TestScanCancellationReturnsSafePartialResult(t *testing.T) {
	root := t.TempDir()
	cfg := config.Default()
	cfg.Roots = []string{root}
	s, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	result, err := s.Scan(ctx)
	if !errors.Is(err, ErrCancelled) {
		t.Fatalf("expected ErrCancelled, got %v", err)
	}
	if !result.Partial || !result.Truncated || result.Status != model.ScanStatusCancelled || result.TruncationReason != "cancelled" || result.EndedAt.IsZero() {
		t.Fatalf("unexpected cancellation result: %#v", result)
	}
}

func TestOrdinaryRootDoesNotApplyWholeDriveExclusions(t *testing.T) {
	root := t.TempDir()
	windowsDir := filepath.Join(root, "Windows")
	if err := os.Mkdir(windowsDir, 0700); err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.Roots = []string{root}
	s, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	result, err := s.Scan(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, entry := range result.Entries {
		found = found || entry.Name == "Windows"
	}
	if !found {
		t.Fatal("whole-drive exclusions changed ordinary-root behavior")
	}
}

func TestWholeDriveHashRequiresExplicitOptIn(t *testing.T) {
	cfg := config.Default()
	cfg.HashSHA256 = true
	if shouldHash(cfg, true) {
		t.Fatal("whole-drive scan enabled hashing by default")
	}
	cfg.HashWholeDrive = true
	if !shouldHash(cfg, true) || !shouldHash(cfg, false) {
		t.Fatal("explicit hashing policy was not honored")
	}
}

func TestProgressSnapshotsReportTruthfulCountersRootsAndPhases(t *testing.T) {
	rootA := t.TempDir()
	rootB := t.TempDir()
	if err := os.Mkdir(filepath.Join(rootA, "dir"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(rootA, "dir", "a"), []byte("abc"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(rootB, "b"), []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.Roots = []string{rootA, rootB}
	s, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	var snapshots []Progress
	s.SetProgressCallback(func(progress Progress) { snapshots = append(snapshots, progress) })
	result, err := s.Scan(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshots) < 6 {
		t.Fatalf("too few snapshots: %#v", snapshots)
	}
	var last Progress
	seen := map[ProgressPhase]bool{}
	for i, progress := range snapshots {
		seen[progress.Phase] = true
		if i > 0 && (progress.ObservedEntries < last.ObservedEntries || progress.Files < last.Files || progress.Directories < last.Directories || progress.Bytes < last.Bytes || progress.RootsCompleted < last.RootsCompleted) {
			t.Fatalf("non-monotonic progress: previous=%#v current=%#v", last, progress)
		}
		last = progress
	}
	if !seen[PhasePreparing] || !seen[PhaseTraversing] || !seen[PhaseClassifying] || !seen[PhaseRelations] {
		t.Fatalf("missing phases: %#v", snapshots)
	}
	if last.ObservedEntries != 3 || last.Files != 2 || last.Directories != 1 || last.Bytes != 8 || last.RootsStarted != 2 || last.RootsCompleted != 2 || last.RootsTotal != 2 {
		t.Fatalf("final progress=%#v result entries=%d", last, len(result.Entries))
	}
}

func TestProgressMarksBudgetAndCancellation(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"a", "b"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(name), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	cfg := config.Default()
	cfg.Roots = []string{root}
	cfg.MaxEntries = 1
	s, _ := New(cfg)
	var budget Progress
	s.SetProgressCallback(func(progress Progress) { budget = progress })
	if _, err := s.Scan(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !budget.BudgetTruncated || budget.TruncationReason != "entry_limit" || budget.Cancelling {
		t.Fatalf("budget progress=%#v", budget)
	}

	cfg.MaxEntries = 0
	s, _ = New(cfg)
	var cancelled Progress
	s.SetProgressCallback(func(progress Progress) { cancelled = progress })
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.Scan(ctx); !errors.Is(err, ErrCancelled) {
		t.Fatalf("cancel error=%v", err)
	}
	if !cancelled.Cancelling || cancelled.BudgetTruncated || cancelled.TruncationReason != "cancelled" {
		t.Fatalf("cancel progress=%#v", cancelled)
	}
}

func TestErrorBudgetCountsAndTruncates(t *testing.T) {
	cfg := config.Default()
	cfg.Roots = []string{t.TempDir()}
	cfg.MaxErrors = 2
	s, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	result := model.Scan{Status: model.ScanStatusComplete}
	state := scanState{scanner: s, result: &result}
	if state.addError("one") {
		t.Fatal("stopped before error limit")
	}
	if !state.addError("two") {
		t.Fatal("did not stop at error limit")
	}
	state.addError("three")
	if result.ErrorCount != 3 || len(result.Errors) != 2 || result.TruncationReason != "error_limit" {
		t.Fatalf("unexpected error budget result: %#v", result)
	}
}
