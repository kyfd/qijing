package store

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/kyfd/qijing/internal/model"
)

// The upgrade test: a database created at schema version N-1 must migrate
// cleanly, keeping every stored scan readable. Release CI relies on this
// pattern for real old-version databases.
func TestMigrationFromPreviousSchemaPreservesScans(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "legacy.db")

	// Build the previous schema version by applying every migration but the
	// newest one.
	legacyDB, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	legacy := &Store{db: legacyDB}
	if err := legacy.applyMigrations(ctx, migrations[:len(migrations)-1]); err != nil {
		t.Fatalf("apply legacy migrations: %v", err)
	}
	stored := model.Scan{
		ID: "legacy-scan", StartedAt: time.Now().Add(-time.Minute), EndedAt: time.Now(),
		Status: model.ScanStatusComplete, ErrorCount: 1, Errors: []string{"one"},
		Roots: []string{"root-1"},
		Entries: []model.Entry{{ID: "e1", RootID: "root-1", Path: `C:\x\a.txt`, Relative: "a.txt",
			Name: "a.txt", Extension: ".txt", Kind: model.KindFile, Size: 3, ModTime: time.Now()}},
		Relations: []model.Relation{{FromID: "e1", ToID: "e2", Type: model.RelationDuplicate}},
	}
	if err := legacy.SaveScan(ctx, stored); err != nil {
		t.Fatalf("seed legacy data: %v", err)
	}
	if err := legacyDB.Close(); err != nil {
		t.Fatal(err)
	}

	// Opening the legacy database runs the full migration chain.
	upgraded, err := Open(path)
	if err != nil {
		t.Fatalf("open legacy database: %v", err)
	}
	defer upgraded.Close()
	var version int
	if err := upgraded.db.QueryRow(`SELECT MAX(version) FROM schema_migrations`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != len(migrations) {
		t.Fatalf("schema version = %d, want %d", version, len(migrations))
	}
	got, err := upgraded.Scan(ctx, stored.ID)
	if err != nil {
		t.Fatalf("legacy scan must survive the upgrade: %v", err)
	}
	if got.Status != model.ScanStatusComplete || len(got.Entries) != 1 || len(got.Relations) != 1 || got.ErrorCount != 1 {
		t.Fatalf("upgraded scan = %+v", got)
	}
}

func TestStagingLifecycleStreamsAndFinalizes(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "staging.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	if err := s.BeginStagingScan(ctx, "snap-1", []string{"root-1"}, "job-1"); err != nil {
		t.Fatalf("begin staging: %v", err)
	}
	// A staging snapshot is not a result yet.
	if _, err := s.LatestScan(ctx); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("latest during staging must be empty, got %v", err)
	}
	if err := s.WriteEntryBatch(ctx, "snap-1", []model.Entry{
		{ID: "e1", RootID: "root-1", Path: `C:\x\a.txt`, Relative: "a.txt", Name: "a.txt", Kind: model.KindFile, Size: 1, ModTime: time.Now()},
		{ID: "e2", RootID: "root-1", Path: `C:\x\b.txt`, Relative: "b.txt", Name: "b.txt", Kind: model.KindFile, Size: 2, ModTime: time.Now()},
	}); err != nil {
		t.Fatalf("write batch: %v", err)
	}
	final := model.Scan{ID: "snap-1", Status: model.ScanStatusComplete, EndedAt: time.Now(),
		Roots: []string{"root-1"}, Relations: []model.Relation{{FromID: "e1", ToID: "e2", Type: model.RelationDuplicate}}}
	if err := s.FinalizeScan(ctx, final); err != nil {
		t.Fatalf("finalize: %v", err)
	}
	latest, err := s.LatestScan(ctx)
	if err != nil {
		t.Fatalf("latest after finalize: %v", err)
	}
	if latest.ID != "snap-1" || len(latest.Entries) != 2 || len(latest.Relations) != 1 {
		t.Fatalf("latest = %+v", latest)
	}
	// Finalizing twice must fail instead of silently mutating history.
	if err := s.FinalizeScan(ctx, final); err == nil {
		t.Fatal("a finalized snapshot must not be finalizable again")
	}
}

func TestStagingLifecycleAbandonHidesEntries(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "abandon.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	if err := s.BeginStagingScan(ctx, "snap-1", []string{"root-1"}, "job-1"); err != nil {
		t.Fatal(err)
	}
	if err := s.WriteEntryBatch(ctx, "snap-1", []model.Entry{
		{ID: "e1", RootID: "root-1", Path: `C:\x\a.txt`, Relative: "a.txt", Name: "a.txt", Kind: model.KindFile, Size: 1, ModTime: time.Now()},
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.AbandonScan(ctx, "snap-1", "low_disk"); err != nil {
		t.Fatalf("abandon: %v", err)
	}
	if _, err := s.LatestScan(ctx); err == nil {
		t.Fatal("an abandoned snapshot must never be the latest scan")
	}
	var count int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM entries WHERE scan_id='snap-1'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("abandoned entries must be deleted, got %d", count)
	}
	// Abandoning twice is a no-op error, not silent history rewriting.
	if err := s.AbandonScan(ctx, "snap-1", "low_disk"); err == nil {
		t.Fatal("a non-staging snapshot must not be abandonable")
	}
}

func TestPurgeStagingScansCleansStartupLeftovers(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "purge.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	// A finished snapshot from a previous run.
	old := model.Scan{ID: "complete-1", StartedAt: time.Now(), EndedAt: time.Now(), Status: model.ScanStatusComplete}
	if err := s.SaveScan(ctx, old); err != nil {
		t.Fatal(err)
	}
	// Two leftover staging snapshots from crashed runs.
	for _, id := range []string{"stale-1", "stale-2"} {
		if err := s.BeginStagingScan(ctx, id, []string{"root-1"}, "job-"+id); err != nil {
			t.Fatal(err)
		}
		if err := s.WriteEntryBatch(ctx, id, []model.Entry{
			{ID: "e-" + id, RootID: "root-1", Path: `C:\x\a.txt`, Relative: "a.txt", Name: "a.txt", Kind: model.KindFile, Size: 1, ModTime: time.Now()},
		}); err != nil {
			t.Fatal(err)
		}
	}
	purged, err := s.PurgeStagingScans(ctx)
	if err != nil {
		t.Fatalf("purge: %v", err)
	}
	if purged != 2 {
		t.Fatalf("purged = %d, want 2", purged)
	}
	latest, err := s.LatestScan(ctx)
	if err != nil || latest.ID != "complete-1" {
		t.Fatalf("the previous complete snapshot must survive: %#v err=%v", latest, err)
	}
	var count int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM entries WHERE scan_id LIKE 'stale-%'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("leftover entries must be gone, got %d", count)
	}
}
