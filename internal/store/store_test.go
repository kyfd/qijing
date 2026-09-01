package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/kyfd/qijing/internal/model"
)

func TestAuthorizedRootsReplaceIntendedSet(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "test.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SaveAuthorizedRoots(ctx, []string{`C:\\Users\\example`, `D:\\`}); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveAuthorizedRoots(ctx, []string{`C:\\`, `D:\\`}); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	roots, err := reopened.AuthorizedRoots(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(roots) != 2 || roots[0] != `C:\\` || roots[1] != `D:\\` {
		t.Fatalf("roots=%#v", roots)
	}
}

func TestMigrationAddsScanSnapshotColumnsToOldSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	for _, statement := range migrations[:4] {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	for version := 1; version <= 4; version++ {
		if _, err := db.Exec(`INSERT INTO schema_migrations(version,applied_at) VALUES(?,?)`, version, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	now := time.Now().UTC()
	scan := model.Scan{ID: "migrated", StartedAt: now, EndedAt: now, Status: model.ScanStatusPartial, Partial: true, Truncated: true, TruncationReason: "duration_limit", ErrorCount: 9}
	if err := s.SaveScan(context.Background(), scan); err != nil {
		t.Fatal(err)
	}
	got, err := s.Scan(context.Background(), scan.ID)
	if err != nil || got.Status != scan.Status || got.ErrorCount != 9 || got.TruncationReason != "duration_limit" {
		t.Fatalf("migrated round trip=%#v err=%v", got, err)
	}
}

func TestScanStatusAndTrueErrorCountRoundTrip(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "test.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	partial := model.Scan{ID: "partial", StartedAt: now, EndedAt: now, Status: model.ScanStatusPartial, Partial: true, Truncated: true, TruncationReason: "error_limit", ErrorCount: 17, Errors: []string{"retained one", "retained two"}}
	if err := s.SaveScan(ctx, partial); err != nil {
		t.Fatal(err)
	}
	got, err := s.Scan(ctx, partial.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != partial.Status || !got.Partial || !got.Truncated || got.TruncationReason != partial.TruncationReason || got.ErrorCount != 17 || len(got.Errors) != 2 {
		t.Fatalf("round trip=%#v", got)
	}
	cancelled := model.Scan{ID: "cancelled", StartedAt: now.Add(time.Second), EndedAt: now.Add(time.Second), Status: model.ScanStatusCancelled, Partial: true, Truncated: true, TruncationReason: "cancelled"}
	if err := s.SaveScan(ctx, cancelled); err != nil {
		t.Fatal(err)
	}
	latest, err := s.LatestScan(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if latest.ID != partial.ID {
		t.Fatalf("cancelled scan replaced latest: %#v", latest)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	got, err = reopened.Scan(ctx, partial.ID)
	if err != nil || got.ErrorCount != 17 || got.Status != model.ScanStatusPartial || got.TruncationReason != "error_limit" {
		t.Fatalf("reopened=%#v err=%v", got, err)
	}
}

func TestPersistenceMapAuditAndSuggestions(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	now := time.Now().UTC().Truncate(time.Microsecond)
	scan := model.Scan{ID: "scan-1", StartedAt: now, EndedAt: now, Roots: []string{"root-id"}, Entries: []model.Entry{{ID: "entry-1", RootID: "root-id", Path: "/private/a.log", Relative: "a.log", Name: "a.log", Extension: ".log", Kind: model.KindFile, Size: 42, ModTime: now, Classes: []model.Class{model.ClassDormant}}}}
	if err := s.SaveScan(ctx, scan); err != nil {
		t.Fatal(err)
	}
	got, err := s.Scan(ctx, scan.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Entries) != 1 || got.Entries[0].Path != scan.Entries[0].Path {
		t.Fatalf("bad round trip: %#v", got)
	}
	cells, err := s.Map(ctx, scan.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(cells) != 1 || cells[0].Count != 1 || cells[0].TotalBytes != 42 {
		t.Fatalf("bad map: %#v", cells)
	}
	if _, err = s.AddAudit(ctx, model.AuditEvent{ScanID: scan.ID, At: now, Level: "warn", Code: "symlink", Message: "rejected"}); err != nil {
		t.Fatal(err)
	}
	audits, err := s.Audits(ctx, scan.ID, 10)
	if err != nil || len(audits) != 1 {
		t.Fatalf("audits=%#v err=%v", audits, err)
	}
	if _, err = s.AddSuggestion(ctx, model.Suggestion{ScanID: scan.ID, CreatedAt: now, Kind: "cleanup", EntryID: "entry-1", Summary: "remove duplicate"}); err != nil {
		t.Fatal(err)
	}
	suggestions, err := s.Suggestions(ctx, scan.ID, true, 10)
	if err != nil || len(suggestions) != 1 {
		t.Fatalf("suggestions=%#v err=%v", suggestions, err)
	}
}
