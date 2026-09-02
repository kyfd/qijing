// Package appdir resolves the product's local-only storage root under
// %LocalAppData% and performs the one-time migration away from the legacy
// roaming location.
//
// 扫描索引包含真实路径与文件名，按隐私边界不得进入可能漫游的配置目录。
// 本包是唯一决定数据目录位置的地方：其他包通过 Ensure() 拿到布局，不自行
// 拼接路径。
package appdir

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// ProductName is the directory name under %LocalAppData%.
const ProductName = "Qijing"

const (
	legacyDirName    = "FileEcosystem"
	legacyNamespace  = "github.com/kyfd/qijing"
	markerName       = "migration.complete"
	stagingName      = "migration-staging"
	backupPrefix     = "pre-migration-"
	markerVersion    = 1
	secretsDirName   = "secrets"
	dpapiSuffix      = ".dpapi"
	maxMigratedEntry = 512 << 20 // refuse to copy absurdly large leftovers
	maxMigratedTotal = 2 << 30
)

// Layout is the resolved on-disk layout. Every directory is inside Root and
// therefore inside the current user's local profile.
type Layout struct {
	Root    string
	Data    string // SQLite index, recycle audit; the only dir the app writes business data into
	Logs    string
	Cache   string
	Backups string
	Updates string
	Secrets string // DPAPI-protected key material inside Data
}

// Options carries every external dependency of the migration so tests can
// run it against temporary directories.
type Options struct {
	// Root is the target root, e.g. %LocalAppData%\Qijing.
	Root string
	// LegacyData is the old roaming data dir; empty means "no legacy".
	LegacyData string
	// LegacySecrets is the old roaming DPAPI dir; empty means "no legacy".
	LegacySecrets string
	// CheckDB verifies a SQLite file's integrity. In production this is a
	// PRAGMA quick_check; tests inject failures.
	CheckDB func(path string) error
	// Now produces timestamps for backup directory names.
	Now func() time.Time
	// Logf receives sanitized progress notes. It must never receive secret
	// material; the inputs here are directory paths only.
	Logf func(format string, args ...any)
}

// DefaultRoot returns %LocalAppData%\Qijing. os.UserCacheDir maps to
// %LocalAppData% on Windows, which is local-only by definition.
func DefaultRoot() (string, error) {
	base, err := os.UserCacheDir()
	if err != nil {
		return "", fmt.Errorf("resolve local data root: %w", err)
	}
	return filepath.Join(base, ProductName), nil
}

// LegacyLocations returns the old roaming data and secret directories used
// before this ADR. Both may not exist.
func LegacyLocations() (dataDir, secretsDir string, err error) {
	base, err := os.UserConfigDir()
	if err != nil {
		return "", "", fmt.Errorf("resolve legacy location: %w", err)
	}
	return filepath.Join(base, legacyDirName), filepath.Join(base, legacyNamespace, secretsDirName), nil
}

// Ensure resolves the production layout, creates every directory, restricts
// the root to the current user, and runs the one-time migration from the
// legacy roaming locations. It never deletes or rewrites anything inside the
// legacy directories.
func Ensure() (Layout, error) {
	root, err := DefaultRoot()
	if err != nil {
		return Layout{}, err
	}
	legacyData, legacySecrets, err := LegacyLocations()
	if err != nil {
		return Layout{}, err
	}
	layout, err := EnsureWithOptions(Options{Root: root, LegacyData: legacyData, LegacySecrets: legacySecrets, CheckDB: sqliteQuickCheck, Now: time.Now, Logf: func(string, ...any) {}})
	if err != nil {
		return Layout{}, err
	}
	if err := restrictToCurrentUser(layout.Root); err != nil {
		// Volumes without ACL support cannot be restricted; the user
		// profile's own protection is the only guarantee there.
		if !aclSupported(layout.Root) {
			return layout, nil
		}
		return Layout{}, err
	}
	if err := verifyUserPrivate(layout.Root); err != nil {
		return Layout{}, fmt.Errorf("data directory is not user-private: %w", err)
	}
	return layout, nil
}

// EnsureWithOptions implements the migration contract documented in
// docs/adr/0001-localappdata-data-dir.md:
//
//	detect legacy → integrity check → backup → copy to staging →
//	re-check → atomic switch → failure recovery → completion marker
func EnsureWithOptions(opts Options) (Layout, error) {
	if opts.Root == "" {
		return Layout{}, errors.New("target root is required")
	}
	if opts.CheckDB == nil {
		opts.CheckDB = func(string) error { return nil }
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.Logf == nil {
		opts.Logf = func(string, ...any) {}
	}
	layout := Layout{
		Root:    filepath.Clean(opts.Root),
		Data:    filepath.Join(opts.Root, "data"),
		Logs:    filepath.Join(opts.Root, "logs"),
		Cache:   filepath.Join(opts.Root, "cache"),
		Backups: filepath.Join(opts.Root, "backups"),
		Updates: filepath.Join(opts.Root, "updates"),
		Secrets: filepath.Join(opts.Root, "data", secretsDirName),
	}
	for _, dir := range []string{layout.Root, layout.Data, layout.Logs, layout.Cache, layout.Backups, layout.Updates} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return Layout{}, fmt.Errorf("create %s: %w", relTo(layout.Root, dir), err)
		}
	}

	marker := layout.Root + string(filepath.Separator) + markerName
	if fileExists(marker) {
		// Already migrated (or freshly started) in a previous run. Secret
		// copying is idempotent, so it stays unconditional.
		if err := migrateSecrets(opts.LegacySecrets, layout.Secrets, opts.Logf); err != nil {
			return Layout{}, err
		}
		return layout, nil
	}

	source := ""
	note := "ok"
	switch {
	case !dirExists(opts.LegacyData):
		opts.Logf("no legacy data directory; starting fresh")
	case dataDirHasContent(layout.Data):
		// The new location already holds data (previous install or a prior
		// run). New wins; the legacy dir stays untouched as history.
		opts.Logf("target data directory already populated; keeping it")
		source = opts.LegacyData
	default:
		migrated, err := migrateData(opts, layout)
		if err != nil {
			return Layout{}, err
		}
		source, note = migrated.source, migrated.note
	}

	if err := migrateSecrets(opts.LegacySecrets, layout.Secrets, opts.Logf); err != nil {
		return Layout{}, err
	}
	if err := writeMarker(marker, markerData{Version: markerVersion, MigratedAt: opts.Now().UTC().Format(time.RFC3339), Source: source, Note: note}); err != nil {
		return Layout{}, fmt.Errorf("write migration marker: %w", err)
	}
	opts.Logf("local data root ready at %s", layout.Root)
	return layout, nil
}

type migrateResult struct {
	source string
	note   string
}

// migrateData moves the legacy index into layout.Data following the ADR
// sequence. On any error the staging directory is removed and the legacy
// directory is left exactly as it was.
func migrateData(opts Options, layout Layout) (migrateResult, error) {
	if err := os.MkdirAll(layout.Data, 0o700); err != nil {
		return migrateResult{}, fmt.Errorf("create data dir: %w", err)
	}
	legacyDB := filepath.Join(opts.LegacyData, "ecosystem.db")
	if err := opts.CheckDB(legacyDB); err != nil {
		if !errors.Is(err, errDBMissing) {
			// An unreadable index holds nothing recoverable, but it is still
			// the user's history: keep a copy as evidence, start fresh, and
			// say so in the marker instead of failing startup forever.
			kept := filepath.Join(layout.Backups, "legacy-unreadable-"+opts.Now().UTC().Format("20060102T150405"))
			if copyErr := copyTree(opts.LegacyData, kept); copyErr != nil {
				return migrateResult{}, fmt.Errorf("legacy database failed its integrity check (%v) and could not be archived: %w", err, copyErr)
			}
			opts.Logf("legacy database unreadable (%v); archived to %s and starting fresh", err, filepath.Base(kept))
			return migrateResult{source: opts.LegacyData, note: "legacy_db_unrecoverable"}, nil
		}
	}

	backup := filepath.Join(layout.Backups, backupPrefix+opts.Now().UTC().Format("20060102T150405"))
	if err := copyTree(opts.LegacyData, backup); err != nil {
		return migrateResult{}, fmt.Errorf("back up legacy data before migration: %w", err)
	}
	if err := opts.CheckDB(filepath.Join(backup, "ecosystem.db")); err != nil && !errors.Is(err, errDBMissing) {
		return migrateResult{}, fmt.Errorf("backup failed its integrity check: %w", err)
	}

	staging := filepath.Join(layout.Root, stagingName)
	_ = os.RemoveAll(staging)
	if err := copyTree(opts.LegacyData, staging); err != nil {
		_ = os.RemoveAll(staging)
		return migrateResult{}, fmt.Errorf("stage legacy data: %w", err)
	}
	if err := opts.CheckDB(filepath.Join(staging, "ecosystem.db")); err != nil && !errors.Is(err, errDBMissing) {
		_ = os.RemoveAll(staging)
		return migrateResult{}, fmt.Errorf("staged copy failed its integrity check: %w", err)
	}

	// layout.Data exists but is empty (checked by the caller); os.Remove
	// refuses to delete anything non-empty, which guards the atomic switch.
	if err := os.Remove(layout.Data); err != nil {
		_ = os.RemoveAll(staging)
		return migrateResult{}, fmt.Errorf("prepare atomic switch: %w", err)
	}
	if err := os.Rename(staging, layout.Data); err != nil {
		_ = os.RemoveAll(staging)
		_ = os.MkdirAll(layout.Data, 0o700)
		return migrateResult{}, fmt.Errorf("activate migrated data: %w", err)
	}
	opts.Logf("migrated legacy data from %s", opts.LegacyData)
	return migrateResult{source: opts.LegacyData, note: "ok"}, nil
}

// migrateSecrets copies DPAPI blobs from the legacy namespace directory. It
// is a pure copy: blobs are opaque, same-user decryption is unchanged, and
// the legacy directory remains as backup.
func migrateSecrets(legacyDir, targetDir string, logf func(string, ...any)) error {
	if !dirExists(legacyDir) {
		return nil
	}
	if err := os.MkdirAll(targetDir, 0o700); err != nil {
		return fmt.Errorf("create secrets dir: %w", err)
	}
	entries, err := os.ReadDir(legacyDir)
	if err != nil {
		return fmt.Errorf("read legacy secrets: %w", err)
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), dpapiSuffix) {
			continue
		}
		target := filepath.Join(targetDir, entry.Name())
		if fileExists(target) {
			continue
		}
		if err := copyFile(filepath.Join(legacyDir, entry.Name()), target); err != nil {
			return fmt.Errorf("copy secret material: %w", err)
		}
		logf("copied secret material into the local data root")
	}
	return nil
}

type markerData struct {
	Version    int    `json:"version"`
	MigratedAt string `json:"migrated_at"`
	Source     string `json:"source,omitempty"`
	Note       string `json:"note"`
}

func writeMarker(path string, data markerData) error {
	raw, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

var errDBMissing = errors.New("no sqlite database present")

// sqliteQuickCheck runs PRAGMA quick_check against one database file. A
// missing database is not a failure: a legacy directory may legitimately
// contain no scan history at all.
func sqliteQuickCheck(path string) error {
	if _, err := os.Stat(path); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return errDBMissing
		}
		return err
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return fmt.Errorf("open %s: %w", filepath.Base(path), err)
	}
	defer db.Close()
	var result string
	if err := db.QueryRow(`PRAGMA quick_check`).Scan(&result); err != nil {
		return fmt.Errorf("quick_check %s: %w", filepath.Base(path), err)
	}
	if result != "ok" {
		return fmt.Errorf("quick_check %s: %s", filepath.Base(path), result)
	}
	return nil
}

const copyBufferSize = 256 << 10

// copyTree copies a directory tree without following symlinks. Encountering
// a symlink is an error: the app's own data dir must never contain links,
// and copying one would silently widen what gets migrated. The total copy
// volume is bounded so a runaway legacy directory cannot exhaust the disk.
func copyTree(sourceDir, targetDir string) error {
	var copied int64
	return filepath.WalkDir(sourceDir, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.Type()&fs.ModeSymlink != 0 {
			return fmt.Errorf("refusing to copy symlink %s", relTo(sourceDir, path))
		}
		target := filepath.Join(targetDir, relTo(sourceDir, path))
		switch {
		case entry.IsDir():
			return os.MkdirAll(target, 0o700)
		case entry.Type().IsRegular():
			info, err := entry.Info()
			if err != nil {
				return err
			}
			if info.Size() > maxMigratedEntry {
				return fmt.Errorf("%s is too large to migrate", relTo(sourceDir, path))
			}
			copied += info.Size()
			if copied > maxMigratedTotal {
				return errors.New("legacy data exceeds the migration size budget")
			}
			return copyFile(path, target)
		default:
			return fmt.Errorf("refusing to copy irregular entry %s", relTo(sourceDir, path))
		}
	})
}

func copyFile(sourcePath, targetPath string) error {
	source, err := os.Open(sourcePath)
	if err != nil {
		return err
	}
	defer source.Close()
	info, err := source.Stat()
	if err != nil {
		return err
	}
	if info.Size() > maxMigratedEntry {
		return fmt.Errorf("%s is too large to migrate", filepath.Base(sourcePath))
	}
	tmp := targetPath + ".migrating"
	if err := func() error {
		target, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
		if err != nil {
			return err
		}
		if _, err := io.CopyBuffer(target, source, make([]byte, copyBufferSize)); err != nil {
			target.Close()
			return err
		}
		return target.Close()
	}(); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, targetPath)
}

func dirExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

// dataDirHasContent reports whether dir exists and holds at least one entry.
// A missing or empty directory is eligible to receive migrated data.
func dataDirHasContent(dir string) bool {
	entries, err := os.ReadDir(dir)
	return err == nil && len(entries) > 0
}

func relTo(base, path string) string {
	rel, err := filepath.Rel(base, path)
	if err != nil {
		return filepath.Base(path)
	}
	return filepath.ToSlash(rel)
}
