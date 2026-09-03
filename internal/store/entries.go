package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/kyfd/qijing/internal/model"
)

// ErrEntryNotFound reports that no entry with the requested id belongs to the
// snapshot. It is a normal outcome: snapshots are immutable, so an id from an
// older snapshot simply does not exist in this one.
var ErrEntryNotFound = errors.New("entry not found in snapshot")

// entryColumns is the single projection every entry read uses, so the row
// scanner below stays in sync with all of them.
const entryColumns = `id,root_id,path,relative_path,name,extension,kind,size,mod_time,sha256,classes,git_project,error`

// ScanStats is the whole-snapshot aggregate behind the status header. It is
// computed by SQLite rather than by walking entries in memory.
type ScanStats struct {
	Files int64
	Bytes int64
	// Flagged counts entries carrying at least one non-active class: the
	// population the tidy-up recommendations are drawn from.
	Flagged int64
}

// EntryStats aggregates the snapshot without materializing entries.
func (s *Store) EntryStats(ctx context.Context, scanID string) (ScanStats, error) {
	var out ScanStats
	if scanID == "" {
		return out, nil
	}
	var bytes sql.NullInt64
	if err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*),SUM(size) FROM entries WHERE scan_id=? AND kind=?`,
		scanID, model.KindFile).Scan(&out.Files, &bytes); err != nil {
		return out, err
	}
	out.Bytes = bytes.Int64
	if err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(DISTINCT entry_id) FROM entry_classes WHERE scan_id=? AND class<>?`,
		scanID, model.ClassActive).Scan(&out.Flagged); err != nil {
		return out, err
	}
	return out, nil
}

// Entry reads one entry of one snapshot.
func (s *Store) Entry(ctx context.Context, scanID, entryID string) (model.Entry, error) {
	if scanID == "" || entryID == "" {
		return model.Entry{}, ErrEntryNotFound
	}
	row := s.db.QueryRowContext(ctx, `SELECT `+entryColumns+` FROM entries WHERE scan_id=? AND id=?`, scanID, entryID)
	entry, err := scanEntryRow(row)
	if errors.Is(err, sql.ErrNoRows) {
		return model.Entry{}, ErrEntryNotFound
	}
	return entry, err
}

// EntriesByID reads a bounded, explicitly requested set of entries. Callers
// pass user-selected ids (the recycle preview caps its own batch), so the
// query is a keyed lookup, never a table walk.
func (s *Store) EntriesByID(ctx context.Context, scanID string, ids []string) (map[string]model.Entry, error) {
	out := make(map[string]model.Entry, len(ids))
	if scanID == "" || len(ids) == 0 {
		return out, nil
	}
	args := make([]any, 0, len(ids)+1)
	args = append(args, scanID)
	for _, id := range ids {
		args = append(args, id)
	}
	query := `SELECT ` + entryColumns + ` FROM entries WHERE scan_id=? AND id IN (?` +
		strings.Repeat(`,?`, len(ids)-1) + `)`
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		entry, err := scanEntryRow(rows)
		if err != nil {
			return nil, err
		}
		out[entry.ID] = entry
	}
	return out, rows.Err()
}

// LargestEntries returns the top-N files (and git projects) by size, which is
// exactly what the map renders. The count of all matching entries is returned
// alongside so the interface can state honestly how much it is omitting.
func (s *Store) LargestEntries(ctx context.Context, scanID string, limit int) ([]model.Entry, int, error) {
	if scanID == "" || limit <= 0 {
		return nil, 0, nil
	}
	const predicate = `scan_id=? AND (kind=? OR git_project=1)`
	var total int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM entries WHERE `+predicate, scanID, model.KindFile).Scan(&total); err != nil {
		return nil, 0, err
	}
	// id is the tiebreaker so an unchanged snapshot always renders the same
	// map: equal-sized files must not swap places between requests.
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+entryColumns+` FROM entries WHERE `+predicate+` ORDER BY size DESC, id LIMIT ?`,
		scanID, model.KindFile, limit)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := make([]model.Entry, 0, limit)
	for rows.Next() {
		entry, err := scanEntryRow(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, entry)
	}
	return out, total, rows.Err()
}

// FlaggedEntries returns entries carrying at least one non-active class, in
// stable id order, bounded by limit. It backs the recommendation list.
func (s *Store) FlaggedEntries(ctx context.Context, scanID string, kinds []model.Kind, limit int) ([]model.Entry, error) {
	if scanID == "" || limit <= 0 {
		return nil, nil
	}
	args := []any{scanID, model.ClassActive}
	query := `SELECT ` + entryColumns + ` FROM entries WHERE scan_id=? AND id IN (
		 SELECT entry_id FROM entry_classes WHERE scan_id=entries.scan_id AND class<>?)`
	if len(kinds) > 0 {
		placeholders := make([]string, len(kinds))
		for i, kind := range kinds {
			placeholders[i] = "?"
			args = append(args, kind)
		}
		query += ` AND kind IN (` + strings.Join(placeholders, ",") + `)`
	}
	query += ` ORDER BY id LIMIT ?`
	args = append(args, limit)
	return s.queryEntries(ctx, query, args...)
}

// EntriesWithClasses returns entries carrying any of the requested classes,
// largest first, as a page. The tidy-up candidate list uses it: it needs the
// specific disposable-artifact classes, not everything that was flagged.
func (s *Store) EntriesWithClasses(ctx context.Context, scanID string, classes []model.Class, kind model.Kind, limit, offset int) ([]model.Entry, error) {
	if scanID == "" || len(classes) == 0 || limit <= 0 {
		return nil, nil
	}
	args := []any{scanID}
	placeholders := make([]string, len(classes))
	for i, class := range classes {
		placeholders[i] = "?"
		args = append(args, class)
	}
	query := `SELECT ` + entryColumns + ` FROM entries WHERE scan_id=? AND id IN (
		 SELECT entry_id FROM entry_classes WHERE scan_id=entries.scan_id AND class IN (` +
		strings.Join(placeholders, ",") + `))`
	if kind != "" {
		query += ` AND kind=?`
		args = append(args, kind)
	}
	// id breaks size ties so paging cannot repeat or skip a row.
	query += ` ORDER BY size DESC, id LIMIT ? OFFSET ?`
	args = append(args, limit, offset)
	return s.queryEntries(ctx, query, args...)
}

// EntryCount reports how many entries the snapshot holds.
func (s *Store) EntryCount(ctx context.Context, scanID string) (int, error) {
	if scanID == "" {
		return 0, nil
	}
	var count int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM entries WHERE scan_id=?`, scanID).Scan(&count)
	return count, err
}

// EachEntry streams every entry of a snapshot past the callback in id order,
// one row at a time. Whole-snapshot consumers (the privacy view, the agent
// payload aggregation) use it so no caller has to hold the scan in memory.
// Returning an error from fn stops the walk and surfaces that error.
func (s *Store) EachEntry(ctx context.Context, scanID string, fn func(model.Entry) error) error {
	if scanID == "" {
		return nil
	}
	rows, err := s.db.QueryContext(ctx, `SELECT `+entryColumns+` FROM entries WHERE scan_id=? ORDER BY id`, scanID)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		entry, err := scanEntryRow(rows)
		if err != nil {
			return err
		}
		if err := fn(entry); err != nil {
			return err
		}
	}
	return rows.Err()
}

// rowScanner covers both *sql.Row and *sql.Rows.
type rowScanner interface{ Scan(dest ...any) error }

// queryEntries runs an entry query and materializes its rows. Every caller
// bounds its own query with LIMIT, so the returned slice is page-sized.
func (s *Store) queryEntries(ctx context.Context, query string, args ...any) ([]model.Entry, error) {
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.Entry
	for rows.Next() {
		entry, err := scanEntryRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, entry)
	}
	return out, rows.Err()
}

func scanEntryRow(row rowScanner) (model.Entry, error) {
	var entry model.Entry
	var mod, classes string
	if err := row.Scan(&entry.ID, &entry.RootID, &entry.Path, &entry.Relative, &entry.Name,
		&entry.Extension, &entry.Kind, &entry.Size, &mod, &entry.SHA256, &classes,
		&entry.GitProject, &entry.Error); err != nil {
		return model.Entry{}, err
	}
	entry.ModTime, _ = time.Parse(time.RFC3339Nano, mod)
	_ = json.Unmarshal([]byte(classes), &entry.Classes)
	return entry, nil
}

// execer covers both *sql.DB and *sql.Tx.
type execer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

// insertEntryClasses mirrors the entry's classes into the normalized table so
// class predicates are an index seek instead of a JSON scan. Duplicate classes
// on one entry are ignored rather than failing the write.
func insertEntryClasses(ctx context.Context, tx execer, scanID string, entry model.Entry) error {
	for _, class := range entry.Classes {
		if _, err := tx.ExecContext(ctx,
			`INSERT OR IGNORE INTO entry_classes(scan_id,entry_id,class) VALUES(?,?,?)`,
			scanID, entry.ID, class); err != nil {
			return fmt.Errorf("write entry classes: %w", err)
		}
	}
	return nil
}
