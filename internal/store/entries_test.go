package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/kyfd/qijing/internal/model"
)

// seedScan writes a snapshot through the streaming path, which is what
// production uses, so these tests exercise the same rows the application will
// query at runtime.
func seedScan(t *testing.T, s *Store, scanID string, entries []model.Entry) {
	t.Helper()
	ctx := context.Background()
	if err := s.BeginStagingScan(ctx, scanID, []string{"root-1"}, scanID); err != nil {
		t.Fatalf("begin staging: %v", err)
	}
	if err := s.WriteEntryBatch(ctx, scanID, entries); err != nil {
		t.Fatalf("write entries: %v", err)
	}
	if err := s.FinalizeScan(ctx, model.Scan{ID: scanID, EndedAt: time.Now(), Status: model.ScanStatusComplete}); err != nil {
		t.Fatalf("finalize: %v", err)
	}
}

func testEntry(id string, size int64, kind model.Kind, classes ...model.Class) model.Entry {
	return model.Entry{
		ID: id, RootID: "root-1", Path: `C:\x\` + id, Relative: id, Name: id,
		Extension: ".bin", Kind: kind, Size: size, ModTime: time.Now(), Classes: classes,
	}
}

func openTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "entries.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestLargestEntriesReturnsTopNAndHonestTotal(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	entries := []model.Entry{
		testEntry("a", 10, model.KindFile),
		testEntry("b", 50, model.KindFile),
		testEntry("c", 30, model.KindFile),
		// Directories are not map nodes unless they are git projects.
		testEntry("d", 90, model.KindDirectory),
	}
	gitProject := testEntry("e", 5, model.KindDirectory)
	gitProject.GitProject = true
	entries = append(entries, gitProject)
	seedScan(t, s, "scan-1", entries)

	got, total, err := s.LargestEntries(ctx, "scan-1", 2)
	if err != nil {
		t.Fatalf("largest entries: %v", err)
	}
	// Four candidates: three files plus the git project directory.
	if total != 4 {
		t.Fatalf("total = %d, want 4", total)
	}
	if len(got) != 2 || got[0].ID != "b" || got[1].ID != "c" {
		t.Fatalf("top entries = %+v", got)
	}
	if got[0].Size != 50 || got[0].Path == "" {
		t.Fatalf("entry not fully hydrated: %+v", got[0])
	}
}

// Equal-sized entries must not swap places between requests: an unchanged
// snapshot has to render the same map every time.
func TestLargestEntriesOrderIsStableForEqualSizes(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	var entries []model.Entry
	for i := 0; i < 20; i++ {
		entries = append(entries, testEntry(fmt.Sprintf("e%02d", i), 100, model.KindFile))
	}
	seedScan(t, s, "scan-1", entries)

	first, _, err := s.LargestEntries(ctx, "scan-1", 5)
	if err != nil {
		t.Fatal(err)
	}
	for attempt := 0; attempt < 3; attempt++ {
		again, _, err := s.LargestEntries(ctx, "scan-1", 5)
		if err != nil {
			t.Fatal(err)
		}
		for i := range first {
			if again[i].ID != first[i].ID {
				t.Fatalf("order changed between requests: %s != %s", again[i].ID, first[i].ID)
			}
		}
	}
}

func TestEntryStatsAggregatesFilesBytesAndFlagged(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	seedScan(t, s, "scan-1", []model.Entry{
		testEntry("a", 10, model.KindFile, model.ClassActive),
		testEntry("b", 25, model.KindFile, model.ClassRotten, model.ClassDormant),
		testEntry("c", 5, model.KindFile, model.ClassGiant),
		testEntry("d", 999, model.KindDirectory, model.ClassActive),
	})

	stats, err := s.EntryStats(ctx, "scan-1")
	if err != nil {
		t.Fatalf("entry stats: %v", err)
	}
	if stats.Files != 3 {
		t.Fatalf("files = %d, want 3 (directories excluded)", stats.Files)
	}
	if stats.Bytes != 40 {
		t.Fatalf("bytes = %d, want 40", stats.Bytes)
	}
	// b and c carry a non-active class; a and the directory do not.
	if stats.Flagged != 2 {
		t.Fatalf("flagged = %d, want 2", stats.Flagged)
	}
}

func TestEntryStatsOnEmptySnapshotIsZeroNotError(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	seedScan(t, s, "scan-1", nil)
	stats, err := s.EntryStats(ctx, "scan-1")
	if err != nil {
		t.Fatalf("empty snapshot must aggregate cleanly: %v", err)
	}
	if stats != (ScanStats{}) {
		t.Fatalf("stats = %+v, want zero", stats)
	}
	if _, err := s.EntryStats(ctx, ""); err != nil {
		t.Fatalf("missing snapshot id must not error: %v", err)
	}
}

func TestEntryLookupIsSnapshotScoped(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	seedScan(t, s, "scan-1", []model.Entry{testEntry("a", 10, model.KindFile)})
	seedScan(t, s, "scan-2", []model.Entry{testEntry("z", 10, model.KindFile)})

	if _, err := s.Entry(ctx, "scan-1", "a"); err != nil {
		t.Fatalf("entry of its own snapshot: %v", err)
	}
	// An id from another snapshot must not leak across the boundary.
	if _, err := s.Entry(ctx, "scan-1", "z"); !errors.Is(err, ErrEntryNotFound) {
		t.Fatalf("cross-snapshot lookup err = %v, want ErrEntryNotFound", err)
	}
	if _, err := s.Entry(ctx, "scan-1", "missing"); !errors.Is(err, ErrEntryNotFound) {
		t.Fatalf("missing entry err = %v, want ErrEntryNotFound", err)
	}
}

func TestEntriesByIDReturnsOnlyRequestedRows(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	seedScan(t, s, "scan-1", []model.Entry{
		testEntry("a", 1, model.KindFile),
		testEntry("b", 2, model.KindFile),
		testEntry("c", 3, model.KindFile),
	})
	seedScan(t, s, "scan-2", []model.Entry{testEntry("d", 4, model.KindFile)})

	got, err := s.EntriesByID(ctx, "scan-1", []string{"a", "c", "d", "nope"})
	if err != nil {
		t.Fatalf("entries by id: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d entries, want 2: %+v", len(got), got)
	}
	if _, ok := got["d"]; ok {
		t.Fatal("an id from another snapshot must not be returned")
	}
	if got["a"].Size != 1 || got["c"].Size != 3 {
		t.Fatalf("entries not hydrated: %+v", got)
	}
	if empty, err := s.EntriesByID(ctx, "scan-1", nil); err != nil || len(empty) != 0 {
		t.Fatalf("empty request = %+v, %v", empty, err)
	}
}

func TestFlaggedEntriesFiltersByClassAndKind(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	seedScan(t, s, "scan-1", []model.Entry{
		testEntry("a", 1, model.KindFile, model.ClassActive),
		testEntry("b", 2, model.KindFile, model.ClassRotten),
		testEntry("c", 3, model.KindDirectory, model.ClassDormant),
		testEntry("d", 4, model.KindFile, model.ClassActive, model.ClassGiant),
		testEntry("e", 5, model.KindFile),
	})

	all, err := s.FlaggedEntries(ctx, "scan-1", nil, 100)
	if err != nil {
		t.Fatalf("flagged entries: %v", err)
	}
	ids := map[string]bool{}
	for _, entry := range all {
		ids[entry.ID] = true
	}
	if len(all) != 3 || !ids["b"] || !ids["c"] || !ids["d"] {
		t.Fatalf("flagged = %v, want b, c and d", ids)
	}
	if ids["a"] || ids["e"] {
		t.Fatal("purely active or unclassified entries must not be flagged")
	}

	files, err := s.FlaggedEntries(ctx, "scan-1", []model.Kind{model.KindFile}, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 2 {
		t.Fatalf("file-only flagged = %d, want 2", len(files))
	}

	limited, err := s.FlaggedEntries(ctx, "scan-1", nil, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(limited) != 1 {
		t.Fatalf("limit not honoured: %d rows", len(limited))
	}
}

func TestEachEntryStreamsEverythingAndPropagatesErrors(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	var seeded []model.Entry
	for i := 0; i < 50; i++ {
		seeded = append(seeded, testEntry(fmt.Sprintf("e%02d", i), int64(i), model.KindFile))
	}
	seedScan(t, s, "scan-1", seeded)

	count := 0
	if err := s.EachEntry(ctx, "scan-1", func(model.Entry) error { count++; return nil }); err != nil {
		t.Fatalf("each entry: %v", err)
	}
	if count != len(seeded) {
		t.Fatalf("visited %d entries, want %d", count, len(seeded))
	}

	stop := errors.New("stop")
	visited := 0
	err := s.EachEntry(ctx, "scan-1", func(model.Entry) error {
		visited++
		return stop
	})
	if !errors.Is(err, stop) {
		t.Fatalf("callback error = %v, want stop", err)
	}
	if visited != 1 {
		t.Fatalf("walk continued after an error: %d visits", visited)
	}
}

// Abandoning a snapshot must not leave orphaned class rows behind, or a later
// snapshot reusing an id would inherit stale classifications.
func TestAbandonScanClearsEntryClasses(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	if err := s.BeginStagingScan(ctx, "scan-1", []string{"root-1"}, "job-1"); err != nil {
		t.Fatal(err)
	}
	if err := s.WriteEntryBatch(ctx, "scan-1", []model.Entry{testEntry("a", 1, model.KindFile, model.ClassRotten)}); err != nil {
		t.Fatal(err)
	}
	if err := s.AbandonScan(ctx, "scan-1", "cancelled"); err != nil {
		t.Fatalf("abandon: %v", err)
	}
	var remaining int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM entry_classes WHERE scan_id=?`, "scan-1").Scan(&remaining); err != nil {
		t.Fatal(err)
	}
	if remaining != 0 {
		t.Fatalf("%d class rows survived the abandon", remaining)
	}
}

// saveScanLegacy writes a snapshot using only pre-migration-8 tables, so an
// upgrade test seeds exactly what an older release would have left behind.
// SaveScan cannot be used here: it populates entry_classes, which is the very
// table the migration introduces.
func saveScanLegacy(t *testing.T, db *sql.DB, scan model.Scan) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO scans(id,started_at,ended_at,error_count,status,partial,truncated,truncation_reason) VALUES(?,?,?,?,?,0,0,'')`,
		scan.ID, formatTime(scan.StartedAt), formatTime(scan.EndedAt), scan.ErrorCount, scan.Status); err != nil {
		t.Fatalf("seed scan: %v", err)
	}
	for _, rootID := range scan.Roots {
		if _, err := db.Exec(`INSERT INTO scan_roots(scan_id,root_id) VALUES(?,?)`, scan.ID, rootID); err != nil {
			t.Fatalf("seed root: %v", err)
		}
	}
	for ordinal, message := range scan.Errors {
		if _, err := db.Exec(`INSERT INTO scan_errors(scan_id,ordinal,message) VALUES(?,?,?)`, scan.ID, ordinal, message); err != nil {
			t.Fatalf("seed error: %v", err)
		}
	}
	for _, entry := range scan.Entries {
		classes, _ := json.Marshal(entry.Classes)
		if _, err := db.Exec(`INSERT INTO entries(scan_id,id,root_id,path,relative_path,name,extension,kind,size,mod_time,sha256,classes,git_project,error) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			scan.ID, entry.ID, entry.RootID, entry.Path, entry.Relative, entry.Name, entry.Extension,
			entry.Kind, entry.Size, formatTime(entry.ModTime), entry.SHA256, string(classes), entry.GitProject, entry.Error); err != nil {
			t.Fatalf("seed entry: %v", err)
		}
	}
	for _, relation := range scan.Relations {
		if _, err := db.Exec(`INSERT INTO relations(scan_id,from_id,to_id,type) VALUES(?,?,?,?)`, scan.ID, relation.FromID, relation.ToID, relation.Type); err != nil {
			t.Fatalf("seed relation: %v", err)
		}
	}
}

// The migration that introduces entry_classes must backfill snapshots written
// by an older version, or upgrading would silently empty the map and the
// tidy-up candidates.
func TestMigrationBackfillsEntryClassesForExistingSnapshots(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "legacy.db")
	legacyDB, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	legacy := &Store{db: legacyDB}
	if err := legacy.applyMigrations(ctx, migrations[:len(migrations)-1]); err != nil {
		legacyDB.Close()
		t.Fatalf("apply legacy migrations: %v", err)
	}
	saveScanLegacy(t, legacyDB, model.Scan{
		ID: "legacy", StartedAt: time.Now(), EndedAt: time.Now(), Status: model.ScanStatusComplete,
		Roots: []string{"root-1"},
		Entries: []model.Entry{
			testEntry("a", 10, model.KindFile, model.ClassRotten, model.ClassDormant),
			testEntry("b", 20, model.KindFile, model.ClassActive),
		},
	})
	if err := legacyDB.Close(); err != nil {
		t.Fatal(err)
	}

	upgraded, err := Open(path)
	if err != nil {
		t.Fatalf("open legacy database: %v", err)
	}
	defer upgraded.Close()

	flagged, err := upgraded.FlaggedEntries(ctx, "legacy", nil, 100)
	if err != nil {
		t.Fatalf("flagged entries after upgrade: %v", err)
	}
	if len(flagged) != 1 || flagged[0].ID != "a" {
		t.Fatalf("backfill missed the legacy classes: %+v", flagged)
	}
	stats, err := upgraded.EntryStats(ctx, "legacy")
	if err != nil {
		t.Fatal(err)
	}
	if stats.Files != 2 || stats.Bytes != 30 || stats.Flagged != 1 {
		t.Fatalf("stats after upgrade = %+v", stats)
	}
}

// The tidy-up candidate list pages through the disposable classes largest
// first. Paging must not repeat or skip a row, and it must never return an
// entry whose only classes are outside the requested set.
func TestEntriesWithClassesPagesLargestFirstWithoutOverlap(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	entries := []model.Entry{
		testEntry("a", 500, model.KindFile, model.ClassRotten),
		testEntry("b", 400, model.KindFile, model.ClassOrphan, model.ClassDormant),
		testEntry("c", 300, model.KindFile, model.ClassGiant, model.ClassDormant),
		testEntry("d", 200, model.KindFile, model.ClassRotten),
		testEntry("e", 900, model.KindDirectory, model.ClassRotten),
	}
	seedScan(t, s, "scan-1", entries)

	want := []model.Class{model.ClassRotten, model.ClassOrphan}
	var got []string
	for offset := 0; ; offset += 2 {
		page, err := s.EntriesWithClasses(ctx, "scan-1", want, model.KindFile, 2, offset)
		if err != nil {
			t.Fatalf("page at offset %d: %v", offset, err)
		}
		if len(page) == 0 {
			break
		}
		for _, entry := range page {
			got = append(got, entry.ID)
		}
		if len(page) < 2 {
			break
		}
	}
	// c carries only non-disposable classes; e is a directory, not a file.
	if len(got) != 3 || got[0] != "a" || got[1] != "b" || got[2] != "d" {
		t.Fatalf("paged ids = %v, want [a b d]", got)
	}
}

// Without a kind filter the query must still be class-scoped, so a directory
// carrying a disposable class is visible to callers that want it.
func TestEntriesWithClassesWithoutKindFilterIncludesDirectories(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	seedScan(t, s, "scan-1", []model.Entry{
		testEntry("dir", 900, model.KindDirectory, model.ClassRotten),
		testEntry("file", 100, model.KindFile, model.ClassRotten),
		testEntry("keep", 800, model.KindFile, model.ClassActive),
	})

	got, err := s.EntriesWithClasses(ctx, "scan-1", []model.Class{model.ClassRotten}, "", 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].ID != "dir" || got[1].ID != "file" {
		t.Fatalf("entries = %+v, want dir then file", got)
	}
}

// An empty snapshot, an empty class set, or a class no entry carries must all
// return nothing rather than degrading into an unfiltered read.
func TestEntriesWithClassesRejectsEmptyInputs(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	seedScan(t, s, "scan-1", []model.Entry{testEntry("a", 10, model.KindFile, model.ClassRotten)})

	if got, err := s.EntriesWithClasses(ctx, "scan-1", nil, model.KindFile, 10, 0); err != nil || len(got) != 0 {
		t.Fatalf("no classes: got %d entries, err=%v", len(got), err)
	}
	if got, err := s.EntriesWithClasses(ctx, "", []model.Class{model.ClassRotten}, model.KindFile, 10, 0); err != nil || len(got) != 0 {
		t.Fatalf("no snapshot: got %d entries, err=%v", len(got), err)
	}
	if got, err := s.EntriesWithClasses(ctx, "scan-1", []model.Class{model.ClassGiant}, model.KindFile, 10, 0); err != nil || len(got) != 0 {
		t.Fatalf("unmatched class: got %d entries, err=%v", len(got), err)
	}
}
