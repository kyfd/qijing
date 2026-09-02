package appdir

import (
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func fixedNow() time.Time { return time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC) }

func testOptions(t *testing.T) Options {
	t.Helper()
	root := t.TempDir()
	return Options{
		Root:    root,
		Now:     fixedNow,
		CheckDB: sqliteQuickCheck,
		Logf:    func(string, ...any) {},
	}
}

func makeLegacyDB(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", filepath.Join(dir, "ecosystem.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE scans(id TEXT PRIMARY KEY); INSERT INTO scans(id) VALUES('legacy-scan')`); err != nil {
		t.Fatal(err)
	}
}

func readMarker(t *testing.T, root string) markerData {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(root, markerName))
	if err != nil {
		t.Fatalf("read marker: %v", err)
	}
	var data markerData
	if err := json.Unmarshal(raw, &data); err != nil {
		t.Fatalf("parse marker: %v", err)
	}
	return data
}

func TestFreshInstallCreatesLayoutWithoutLegacy(t *testing.T) {
	opts := testOptions(t)
	layout, err := EnsureWithOptions(opts)
	if err != nil {
		t.Fatalf("EnsureWithOptions: %v", err)
	}
	for _, dir := range []string{layout.Root, layout.Data, layout.Logs, layout.Cache, layout.Backups, layout.Updates} {
		info, err := os.Stat(dir)
		if err != nil || !info.IsDir() {
			t.Fatalf("expected directory %s", dir)
		}
	}
	marker := readMarker(t, layout.Root)
	if marker.Version != markerVersion || marker.Source != "" || marker.Note != "ok" {
		t.Fatalf("unexpected marker: %+v", marker)
	}
}

func TestNormalMigrationMovesDataAndKeepsLegacy(t *testing.T) {
	opts := testOptions(t)
	opts.LegacyData = filepath.Join(t.TempDir(), "legacy data 目录")
	makeLegacyDB(t, opts.LegacyData)

	layout, err := EnsureWithOptions(opts)
	if err != nil {
		t.Fatalf("EnsureWithOptions: %v", err)
	}
	if got, want := layout.Data, filepath.Join(opts.Root, "data"); got != want {
		t.Fatalf("unexpected data dir %q", got)
	}

	// The migrated index is intact.
	db, err := sql.Open("sqlite", filepath.Join(layout.Data, "ecosystem.db"))
	if err != nil {
		t.Fatal(err)
	}
	var id string
	if err := db.QueryRow(`SELECT id FROM scans`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	db.Close()
	if id != "legacy-scan" {
		t.Fatalf("migrated db lost content: %q", id)
	}

	// The legacy directory survives untouched as the backup of record.
	if _, err := os.Stat(filepath.Join(opts.LegacyData, "ecosystem.db")); err != nil {
		t.Fatalf("legacy dir must remain: %v", err)
	}
	backup := filepath.Join(layout.Backups, backupPrefix+"20260902T120000")
	if info, err := os.Stat(filepath.Join(backup, "ecosystem.db")); err != nil || info.IsDir() {
		t.Fatalf("expected pre-migration backup: %v", err)
	}

	marker := readMarker(t, layout.Root)
	if marker.Source != opts.LegacyData || marker.Note != "ok" {
		t.Fatalf("unexpected marker: %+v", marker)
	}

	// No staging leftovers.
	if _, err := os.Stat(filepath.Join(layout.Root, stagingName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("staging dir should be gone: %v", err)
	}
}

func TestMigrationIsIdempotentAfterMarker(t *testing.T) {
	opts := testOptions(t)
	opts.LegacyData = filepath.Join(t.TempDir(), "legacy")
	makeLegacyDB(t, opts.LegacyData)
	if _, err := EnsureWithOptions(opts); err != nil {
		t.Fatal(err)
	}

	checks := 0
	opts.CheckDB = func(path string) error { checks++; return nil }
	second, err := EnsureWithOptions(opts)
	if err != nil {
		t.Fatal(err)
	}
	if checks != 0 {
		t.Fatalf("marker must short-circuit migration, got %d integrity checks", checks)
	}
	if _, err := os.Stat(filepath.Join(second.Data, "ecosystem.db")); err != nil {
		t.Fatalf("migrated data must persist: %v", err)
	}
}

func TestTargetDataWinsOverLegacy(t *testing.T) {
	opts := testOptions(t)
	opts.LegacyData = filepath.Join(t.TempDir(), "legacy")
	makeLegacyDB(t, opts.LegacyData)
	layout, err := EnsureWithOptions(opts)
	if err != nil {
		t.Fatal(err)
	}
	// Simulate a later run that already produced new data.
	fresh := filepath.Join(layout.Data, "fresh.marker")
	if err := os.WriteFile(fresh, []byte("new"), 0o600); err != nil {
		t.Fatal(err)
	}

	opts.CheckDB = func(path string) error {
		t.Fatalf("integrity check must not run when the target already has data")
		return nil
	}
	if _, err := EnsureWithOptions(opts); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Fatalf("existing new data must not be replaced: %v", err)
	}
	if _, err := os.Stat(filepath.Join(opts.LegacyData, "ecosystem.db")); err != nil {
		t.Fatalf("legacy data must remain: %v", err)
	}
}

func TestUnreadableLegacyDBArchivesAndStartsFresh(t *testing.T) {
	opts := testOptions(t)
	opts.LegacyData = filepath.Join(t.TempDir(), "legacy")
	if err := os.MkdirAll(opts.LegacyData, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(opts.LegacyData, "ecosystem.db"), []byte("not a database at all"), 0o600); err != nil {
		t.Fatal(err)
	}

	layout, err := EnsureWithOptions(opts)
	if err != nil {
		t.Fatalf("unreadable legacy must not block startup: %v", err)
	}
	marker := readMarker(t, layout.Root)
	if marker.Note != "legacy_db_unrecoverable" {
		t.Fatalf("marker must record the skip: %+v", marker)
	}
	if entries, err := os.ReadDir(layout.Data); err != nil || len(entries) != 0 {
		t.Fatalf("fresh data dir expected, got %v (%v)", entries, err)
	}
	found := false
	backupEntries, err := os.ReadDir(layout.Backups)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range backupEntries {
		if strings.HasPrefix(entry.Name(), "legacy-unreadable-") {
			found = true
		}
	}
	if !found {
		t.Fatalf("corrupt legacy dir must be archived, saw %v", backupEntries)
	}
}

func TestBackupFailureLeavesNoPartialState(t *testing.T) {
	opts := testOptions(t)
	opts.LegacyData = filepath.Join(t.TempDir(), "legacy")
	makeLegacyDB(t, opts.LegacyData)

	calls := 0
	opts.CheckDB = func(path string) error {
		calls++
		if calls == 2 { // legacy ok, backup check fails
			return errors.New("simulated corruption")
		}
		return nil
	}
	_, err := EnsureWithOptions(opts)
	if err == nil {
		t.Fatal("expected the backup integrity failure to abort migration")
	}
	if _, err := os.Stat(filepath.Join(opts.LegacyData, "ecosystem.db")); err != nil {
		t.Fatalf("legacy dir must be untouched: %v", err)
	}
	if _, err := os.Stat(filepath.Join(opts.Root, markerName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("failed migration must not write the marker")
	}
}

func TestStagingFailureRemovesStaging(t *testing.T) {
	opts := testOptions(t)
	opts.LegacyData = filepath.Join(t.TempDir(), "legacy")
	makeLegacyDB(t, opts.LegacyData)

	calls := 0
	opts.CheckDB = func(path string) error {
		calls++ // 1: legacy, 2: backup, 3: staging
		if calls == 3 {
			return errors.New("staged copy damaged")
		}
		return nil
	}
	_, err := EnsureWithOptions(opts)
	if err == nil {
		t.Fatal("expected the staging failure to abort migration")
	}
	if _, err := os.Stat(filepath.Join(opts.Root, stagingName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("failed staging must be cleaned up")
	}
	if _, err := os.Stat(filepath.Join(opts.Root, markerName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("failed migration must not write the marker")
	}
}

func TestSecretsMigrationCopiesOnlyBlobs(t *testing.T) {
	opts := testOptions(t)
	layout, err := EnsureWithOptions(opts)
	if err != nil {
		t.Fatal(err)
	}
	legacy := filepath.Join(t.TempDir(), "old secrets")
	if err := os.MkdirAll(filepath.Join(legacy, "nested"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(legacy, "aaa.dpapi"), []byte("blob-1"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(legacy, "bbb.dpapi"), []byte("blob-2"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(legacy, "notes.txt"), []byte("not a secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(legacy, "nested", "ccc.dpapi"), []byte("subdir blob"), 0o600); err != nil {
		t.Fatal(err)
	}
	opts.LegacySecrets = legacy

	if _, err := EnsureWithOptions(opts); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"aaa.dpapi", "bbb.dpapi"} {
		data, err := os.ReadFile(filepath.Join(layout.Secrets, name))
		if err != nil {
			t.Fatalf("secret %s not migrated: %v", name, err)
		}
		if string(data) == "" {
			t.Fatalf("secret %s copied empty", name)
		}
	}
	if _, err := os.Stat(filepath.Join(layout.Secrets, "notes.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("non-blob files must not be copied")
	}
	if _, err := os.Stat(filepath.Join(layout.Secrets, "ccc.dpapi")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("subdirectories must not be flattened into the secrets dir")
	}
}

func TestSecretsMigrationDoesNotOverwriteExisting(t *testing.T) {
	opts := testOptions(t)
	layout, err := EnsureWithOptions(opts)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(layout.Secrets, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(layout.Secrets, "key.dpapi"), []byte("current"), 0o600); err != nil {
		t.Fatal(err)
	}
	legacy := filepath.Join(t.TempDir(), "old secrets")
	if err := os.MkdirAll(legacy, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(legacy, "key.dpapi"), []byte("older"), 0o600); err != nil {
		t.Fatal(err)
	}
	opts.LegacySecrets = legacy
	if _, err := EnsureWithOptions(opts); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(layout.Secrets, "key.dpapi"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "current" {
		t.Fatalf("existing secret overwritten with %q", data)
	}
}

func TestMigrationHandlesUnicodeAndSpacePaths(t *testing.T) {
	opts := testOptions(t)
	opts.Root = filepath.Join(t.TempDir(), "栖境 数据 Root")
	opts.LegacyData = filepath.Join(t.TempDir(), "旧 数 据")
	makeLegacyDB(t, opts.LegacyData)

	layout, err := EnsureWithOptions(opts)
	if err != nil {
		t.Fatalf("unicode paths must work: %v", err)
	}
	if _, err := os.Stat(filepath.Join(layout.Data, "ecosystem.db")); err != nil {
		t.Fatalf("migrated db missing: %v", err)
	}
}

func TestSymlinkInLegacyRefusesMigration(t *testing.T) {
	opts := testOptions(t)
	opts.LegacyData = filepath.Join(t.TempDir(), "legacy")
	makeLegacyDB(t, opts.LegacyData)
	link := filepath.Join(opts.LegacyData, "stray")
	if err := os.Symlink(filepath.Join(opts.LegacyData, "ecosystem.db"), link); err != nil {
		t.Skipf("cannot create symlinks here: %v", err)
	}
	_ = link

	if _, err := EnsureWithOptions(opts); err == nil {
		t.Fatal("symlinks in the legacy tree must abort the migration")
	}
	if _, err := os.Stat(filepath.Join(opts.Root, markerName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("refused migration must not write the marker")
	}
}

// A legacy tree too large to move must never block startup: the migration
// is skipped, the legacy directory stays untouched, and the marker records
// the skip.
func TestOversizedLegacySkipsMigrationAndStartsFresh(t *testing.T) {
	opts := testOptions(t)
	opts.LegacyData = filepath.Join(t.TempDir(), "legacy")
	makeLegacyDB(t, opts.LegacyData)
	opts.LegacySize = func(dir string) (int64, error) { return maxMigratedTotal + 1, nil }

	layout, err := EnsureWithOptions(opts)
	if err != nil {
		t.Fatalf("oversized legacy must not block startup: %v", err)
	}
	if marker := readMarker(t, layout.Root); marker.Note != "legacy_skipped_too_large" {
		t.Fatalf("marker = %+v", marker)
	}
	if _, err := os.Stat(filepath.Join(opts.LegacyData, "ecosystem.db")); err != nil {
		t.Fatalf("legacy dir must be untouched: %v", err)
	}
}

// Without enough free space for backup plus staging, the migration skips
// instead of half-filling the disk and failing.
func TestInsufficientSpaceSkipsMigration(t *testing.T) {
	opts := testOptions(t)
	opts.LegacyData = filepath.Join(t.TempDir(), "legacy")
	makeLegacyDB(t, opts.LegacyData)
	opts.LegacySize = func(dir string) (int64, error) { return 1 << 30, nil }
	opts.FreeBytes = func(path string) (int64, error) { return 1 << 20, nil }

	layout, err := EnsureWithOptions(opts)
	if err != nil {
		t.Fatalf("insufficient space must not block startup: %v", err)
	}
	if marker := readMarker(t, layout.Root); marker.Note != "legacy_skipped_insufficient_space" {
		t.Fatalf("marker = %+v", marker)
	}
	if entries, err := os.ReadDir(layout.Data); err != nil || len(entries) != 0 {
		t.Fatalf("no data may be copied when space is insufficient: %v (%v)", entries, err)
	}
}

func TestSqliteQuickCheck(t *testing.T) {
	dir := t.TempDir()
	makeLegacyDB(t, dir)
	if err := sqliteQuickCheck(filepath.Join(dir, "ecosystem.db")); err != nil {
		t.Fatalf("healthy db must pass: %v", err)
	}
	if err := sqliteQuickCheck(filepath.Join(dir, "absent.db")); !errors.Is(err, errDBMissing) {
		t.Fatalf("missing db must be reported as such, got %v", err)
	}
	bad := filepath.Join(dir, "bad.db")
	if err := os.WriteFile(bad, []byte(strings.Repeat("garbage", 100)), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := sqliteQuickCheck(bad); err == nil {
		t.Fatal("garbage file must fail the integrity check")
	}
}

func TestCopyTreeRefusesOversizedEntry(t *testing.T) {
	source := t.TempDir()
	target := t.TempDir()
	big := filepath.Join(source, "huge.bin")
	if err := os.WriteFile(big, make([]byte, maxMigratedEntry+1), 0o600); err != nil {
		t.Skipf("cannot allocate oversized fixture: %v", err)
	}
	if err := copyTree(source, target); err == nil {
		t.Fatal("oversized entries must abort the copy")
	}
}
