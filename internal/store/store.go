// Package store persists scans and derived observations in SQLite.
package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"fileecosystem/internal/model"
	_ "modernc.org/sqlite"
)

type Store struct{ db *sql.DB }

func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(`PRAGMA foreign_keys=ON; PRAGMA journal_mode=WAL; PRAGMA busy_timeout=5000;`); err != nil {
		db.Close()
		return nil, err
	}
	s := &Store{db: db}
	if err := s.Migrate(context.Background()); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) Close() error { return s.db.Close() }

// LatestScan restores the most recently completed immutable snapshot.
func (s *Store) LatestScan(ctx context.Context) (model.Scan, error) {
	var id string
	if err := s.db.QueryRowContext(ctx, `SELECT id FROM scans WHERE status <> ? ORDER BY ended_at DESC, rowid DESC LIMIT 1`, model.ScanStatusCancelled).Scan(&id); err != nil {
		return model.Scan{}, err
	}
	return s.Scan(ctx, id)
}

// AuthorizedRoots returns the persisted filesystem allowlist.
func (s *Store) AuthorizedRoots(ctx context.Context) ([]string, error) {
	var raw string
	if err := s.db.QueryRowContext(ctx, `SELECT value FROM application_settings WHERE key='authorized_roots'`).Scan(&raw); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	var roots []string
	if err := json.Unmarshal([]byte(raw), &roots); err != nil {
		return nil, fmt.Errorf("decode authorized roots: %w", err)
	}
	return roots, nil
}

// SaveAuthorizedRoots atomically replaces the persisted filesystem allowlist.
func (s *Store) SaveAuthorizedRoots(ctx context.Context, roots []string) error {
	raw, err := json.Marshal(roots)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO application_settings(key,value) VALUES('authorized_roots',?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, string(raw))
	return err
}

var migrations = []string{
	`CREATE TABLE IF NOT EXISTS schema_migrations(version INTEGER PRIMARY KEY, applied_at TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS scans(id TEXT PRIMARY KEY, started_at TEXT NOT NULL, ended_at TEXT NOT NULL, error_count INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS scan_roots(scan_id TEXT NOT NULL REFERENCES scans(id) ON DELETE CASCADE, root_id TEXT NOT NULL, PRIMARY KEY(scan_id, root_id));
CREATE TABLE IF NOT EXISTS entries(
 scan_id TEXT NOT NULL REFERENCES scans(id) ON DELETE CASCADE, id TEXT NOT NULL, root_id TEXT NOT NULL,
 path TEXT NOT NULL, relative_path TEXT NOT NULL, name TEXT NOT NULL, extension TEXT NOT NULL, kind TEXT NOT NULL,
 size INTEGER NOT NULL, mod_time TEXT NOT NULL, sha256 TEXT NOT NULL, classes TEXT NOT NULL, git_project INTEGER NOT NULL, error TEXT NOT NULL,
 PRIMARY KEY(scan_id,id));
CREATE INDEX IF NOT EXISTS entries_scan_extension ON entries(scan_id,extension);
CREATE INDEX IF NOT EXISTS entries_scan_sha256 ON entries(scan_id,sha256);
CREATE TABLE IF NOT EXISTS relations(scan_id TEXT NOT NULL REFERENCES scans(id) ON DELETE CASCADE, from_id TEXT NOT NULL, to_id TEXT NOT NULL, type TEXT NOT NULL, PRIMARY KEY(scan_id,from_id,to_id,type));
CREATE TABLE IF NOT EXISTS audit_events(id INTEGER PRIMARY KEY AUTOINCREMENT, scan_id TEXT NOT NULL REFERENCES scans(id) ON DELETE CASCADE, at TEXT NOT NULL, level TEXT NOT NULL, code TEXT NOT NULL, message TEXT NOT NULL, entry_id TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS suggestions(id INTEGER PRIMARY KEY AUTOINCREMENT, scan_id TEXT NOT NULL REFERENCES scans(id) ON DELETE CASCADE, created_at TEXT NOT NULL, kind TEXT NOT NULL, entry_id TEXT NOT NULL, summary TEXT NOT NULL, details TEXT NOT NULL, resolved INTEGER NOT NULL DEFAULT 0);`,
	`CREATE TABLE IF NOT EXISTS scan_errors(scan_id TEXT NOT NULL REFERENCES scans(id) ON DELETE CASCADE, ordinal INTEGER NOT NULL, message TEXT NOT NULL, PRIMARY KEY(scan_id,ordinal));`,
	`CREATE TABLE IF NOT EXISTS application_settings(key TEXT PRIMARY KEY, value TEXT NOT NULL);`,
	`CREATE TABLE IF NOT EXISTS model_profiles(
		 id TEXT PRIMARY KEY, provider TEXT NOT NULL, base_url TEXT NOT NULL, model TEXT NOT NULL,

	 created_at TEXT NOT NULL, updated_at TEXT NOT NULL
	);
	CREATE TABLE IF NOT EXISTS network_consents(
	 profile_id TEXT PRIMARY KEY REFERENCES model_profiles(id) ON DELETE CASCADE,
	 enabled INTEGER NOT NULL, updated_at TEXT NOT NULL
	);
	CREATE TABLE IF NOT EXISTS agent_runs(
	 id TEXT PRIMARY KEY, scan_id TEXT NOT NULL, profile_id TEXT NOT NULL, target_origin TEXT NOT NULL,
	 model TEXT NOT NULL, status TEXT NOT NULL, payload_hash TEXT NOT NULL, payload_bytes INTEGER NOT NULL,
	 confirmed_at TEXT NOT NULL, started_at TEXT NOT NULL, ended_at TEXT NOT NULL DEFAULT '', error TEXT NOT NULL DEFAULT ''
	);
	CREATE INDEX IF NOT EXISTS agent_runs_started ON agent_runs(started_at DESC);
	CREATE TABLE IF NOT EXISTS agent_steps(
	 id INTEGER PRIMARY KEY AUTOINCREMENT, run_id TEXT NOT NULL REFERENCES agent_runs(id) ON DELETE CASCADE,
	 ordinal INTEGER NOT NULL, at TEXT NOT NULL, kind TEXT NOT NULL, name TEXT NOT NULL, detail TEXT NOT NULL,
	 UNIQUE(run_id, ordinal)
	);
	CREATE TABLE IF NOT EXISTS agent_payloads(
	 run_id TEXT PRIMARY KEY REFERENCES agent_runs(id) ON DELETE CASCADE,
	 schema_version TEXT NOT NULL, payload_json BLOB NOT NULL
	);
	CREATE TABLE IF NOT EXISTS agent_responses(
	 run_id TEXT PRIMARY KEY REFERENCES agent_runs(id) ON DELETE CASCADE,
	 response_json BLOB NOT NULL, http_status INTEGER NOT NULL DEFAULT 0, duration_ms INTEGER NOT NULL DEFAULT 0,
	 prompt_tokens INTEGER NOT NULL DEFAULT 0, completion_tokens INTEGER NOT NULL DEFAULT 0
	);
		CREATE TABLE IF NOT EXISTS ignored_recommendations(
		 recommendation_id TEXT PRIMARY KEY, scan_id TEXT NOT NULL, ignored_at TEXT NOT NULL
		);`,
	`ALTER TABLE scans ADD COLUMN status TEXT NOT NULL DEFAULT 'complete';
		ALTER TABLE scans ADD COLUMN partial INTEGER NOT NULL DEFAULT 0;
		ALTER TABLE scans ADD COLUMN truncated INTEGER NOT NULL DEFAULT 0;
		ALTER TABLE scans ADD COLUMN truncation_reason TEXT NOT NULL DEFAULT '';`,
	// recycled_items deliberately carries no foreign key: the record of what the
	// user recycled must outlive the snapshot it was observed in.
	`CREATE TABLE IF NOT EXISTS recycled_items(
		 id TEXT PRIMARY KEY, scan_id TEXT NOT NULL, entry_id TEXT NOT NULL, path TEXT NOT NULL,
		 name TEXT NOT NULL, kind TEXT NOT NULL, size INTEGER NOT NULL, root TEXT NOT NULL,
		 confirmed_at TEXT NOT NULL, recycled_at TEXT NOT NULL, outcome TEXT NOT NULL, error TEXT NOT NULL DEFAULT ''
		);
		CREATE INDEX IF NOT EXISTS recycled_items_recycled_at ON recycled_items(recycled_at DESC);`,
}

func (s *Store) Migrate(ctx context.Context) error {
	for index, migration := range migrations {
		version := index + 1
		var exists int
		err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM schema_migrations WHERE version=?`, version).Scan(&exists)
		if err != nil && !strings.Contains(err.Error(), "no such table") {
			return err
		}
		if exists != 0 {
			continue
		}
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, migration); err == nil {
			_, err = tx.ExecContext(ctx, `INSERT INTO schema_migrations(version,applied_at) VALUES(?,?)`, version, time.Now().UTC().Format(time.RFC3339Nano))
		}
		if err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("migration %d: %w", version, err)
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) SaveScan(ctx context.Context, scan model.Scan) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if scan.Status == "" {
		scan.Status = model.ScanStatusComplete
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO scans(id,started_at,ended_at,error_count,status,partial,truncated,truncation_reason) VALUES(?,?,?,?,?,?,?,?)`, scan.ID, formatTime(scan.StartedAt), formatTime(scan.EndedAt), scan.ErrorCount, scan.Status, scan.Partial, scan.Truncated, scan.TruncationReason); err != nil {
		return err
	}
	for _, rootID := range scan.Roots {
		if _, err = tx.ExecContext(ctx, `INSERT INTO scan_roots(scan_id,root_id) VALUES(?,?)`, scan.ID, rootID); err != nil {
			return err
		}
	}
	for ordinal, message := range scan.Errors {
		if _, err = tx.ExecContext(ctx, `INSERT INTO scan_errors(scan_id,ordinal,message) VALUES(?,?,?)`, scan.ID, ordinal, message); err != nil {
			return err
		}
	}
	for _, entry := range scan.Entries {
		classes, _ := json.Marshal(entry.Classes)
		_, err = tx.ExecContext(ctx, `INSERT INTO entries(scan_id,id,root_id,path,relative_path,name,extension,kind,size,mod_time,sha256,classes,git_project,error) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			scan.ID, entry.ID, entry.RootID, entry.Path, entry.Relative, entry.Name, entry.Extension, entry.Kind, entry.Size, formatTime(entry.ModTime), entry.SHA256, string(classes), entry.GitProject, entry.Error)
		if err != nil {
			return err
		}
	}
	for _, relation := range scan.Relations {
		if _, err = tx.ExecContext(ctx, `INSERT INTO relations(scan_id,from_id,to_id,type) VALUES(?,?,?,?)`, scan.ID, relation.FromID, relation.ToID, relation.Type); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) Scan(ctx context.Context, id string) (model.Scan, error) {
	var out model.Scan
	var started, ended string
	if err := s.db.QueryRowContext(ctx, `SELECT id,started_at,ended_at,error_count,status,partial,truncated,truncation_reason FROM scans WHERE id=?`, id).Scan(&out.ID, &started, &ended, &out.ErrorCount, &out.Status, &out.Partial, &out.Truncated, &out.TruncationReason); err != nil {
		return out, err
	}
	out.StartedAt, _ = time.Parse(time.RFC3339Nano, started)
	out.EndedAt, _ = time.Parse(time.RFC3339Nano, ended)
	rootRows, err := s.db.QueryContext(ctx, `SELECT root_id FROM scan_roots WHERE scan_id=? ORDER BY root_id`, id)
	if err != nil {
		return out, err
	}
	for rootRows.Next() {
		var rootID string
		if err := rootRows.Scan(&rootID); err != nil {
			rootRows.Close()
			return out, err
		}
		out.Roots = append(out.Roots, rootID)
	}
	if err := rootRows.Close(); err != nil {
		return out, err
	}
	errorRows, err := s.db.QueryContext(ctx, `SELECT message FROM scan_errors WHERE scan_id=? ORDER BY ordinal`, id)
	if err != nil {
		return out, err
	}
	for errorRows.Next() {
		var message string
		if err := errorRows.Scan(&message); err != nil {
			errorRows.Close()
			return out, err
		}
		out.Errors = append(out.Errors, message)
	}
	if err := errorRows.Close(); err != nil {
		return out, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id,root_id,path,relative_path,name,extension,kind,size,mod_time,sha256,classes,git_project,error FROM entries WHERE scan_id=? ORDER BY id`, id)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var e model.Entry
		var mod, classes string
		if err := rows.Scan(&e.ID, &e.RootID, &e.Path, &e.Relative, &e.Name, &e.Extension, &e.Kind, &e.Size, &mod, &e.SHA256, &classes, &e.GitProject, &e.Error); err != nil {
			return out, err
		}
		e.ModTime, _ = time.Parse(time.RFC3339Nano, mod)
		_ = json.Unmarshal([]byte(classes), &e.Classes)
		out.Entries = append(out.Entries, e)
	}
	if err := rows.Err(); err != nil {
		return out, err
	}
	rr, err := s.db.QueryContext(ctx, `SELECT from_id,to_id,type FROM relations WHERE scan_id=? ORDER BY type,from_id,to_id`, id)
	if err != nil {
		return out, err
	}
	defer rr.Close()
	for rr.Next() {
		var r model.Relation
		if err := rr.Scan(&r.FromID, &r.ToID, &r.Type); err != nil {
			return out, err
		}
		out.Relations = append(out.Relations, r)
	}
	return out, rr.Err()
}

// Map aggregates by root, extension and each class without exposing paths.
func (s *Store) Map(ctx context.Context, scanID string) ([]model.MapCell, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT root_id,extension,classes,size FROM entries WHERE scan_id=? AND kind='file'`, scanID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	type key struct {
		root, ext string
		class     model.Class
	}
	cells := map[key]*model.MapCell{}
	for rows.Next() {
		var root, ext, raw string
		var size int64
		if err := rows.Scan(&root, &ext, &raw, &size); err != nil {
			return nil, err
		}
		var classes []model.Class
		_ = json.Unmarshal([]byte(raw), &classes)
		for _, class := range classes {
			k := key{root, ext, class}
			if cells[k] == nil {
				cells[k] = &model.MapCell{RootID: root, Extension: ext, Class: class}
			}
			cells[k].Count++
			cells[k].TotalBytes += size
		}
	}
	out := make([]model.MapCell, 0, len(cells))
	for _, cell := range cells {
		out = append(out, *cell)
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.RootID != b.RootID {
			return a.RootID < b.RootID
		}
		if a.Extension != b.Extension {
			return a.Extension < b.Extension
		}
		return a.Class < b.Class
	})
	return out, rows.Err()
}

func (s *Store) AddAudit(ctx context.Context, event model.AuditEvent) (int64, error) {
	res, err := s.db.ExecContext(ctx, `INSERT INTO audit_events(scan_id,at,level,code,message,entry_id) VALUES(?,?,?,?,?,?)`, event.ScanID, formatTime(event.At), event.Level, event.Code, event.Message, event.EntryID)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}
func (s *Store) Audits(ctx context.Context, scanID string, limit int) ([]model.AuditEvent, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id,scan_id,at,level,code,message,entry_id FROM audit_events WHERE scan_id=? ORDER BY id DESC LIMIT ?`, scanID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.AuditEvent
	for rows.Next() {
		var e model.AuditEvent
		var at string
		if err := rows.Scan(&e.ID, &e.ScanID, &at, &e.Level, &e.Code, &e.Message, &e.EntryID); err != nil {
			return nil, err
		}
		e.At, _ = time.Parse(time.RFC3339Nano, at)
		out = append(out, e)
	}
	return out, rows.Err()
}
func (s *Store) AddSuggestion(ctx context.Context, v model.Suggestion) (int64, error) {
	res, err := s.db.ExecContext(ctx, `INSERT INTO suggestions(scan_id,created_at,kind,entry_id,summary,details,resolved) VALUES(?,?,?,?,?,?,?)`, v.ScanID, formatTime(v.CreatedAt), v.Kind, v.EntryID, v.Summary, v.Details, v.Resolved)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}
func (s *Store) Suggestions(ctx context.Context, scanID string, unresolvedOnly bool, limit int) ([]model.Suggestion, error) {
	if limit <= 0 {
		limit = 100
	}
	query := `SELECT id,scan_id,created_at,kind,entry_id,summary,details,resolved FROM suggestions WHERE scan_id=?`
	if unresolvedOnly {
		query += ` AND resolved=0`
	}
	query += ` ORDER BY id DESC LIMIT ?`
	rows, err := s.db.QueryContext(ctx, query, scanID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.Suggestion
	for rows.Next() {
		var v model.Suggestion
		var at string
		if err := rows.Scan(&v.ID, &v.ScanID, &at, &v.Kind, &v.EntryID, &v.Summary, &v.Details, &v.Resolved); err != nil {
			return nil, err
		}
		v.CreatedAt, _ = time.Parse(time.RFC3339Nano, at)
		out = append(out, v)
	}
	return out, rows.Err()
}

func formatTime(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }
