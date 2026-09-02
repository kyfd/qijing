package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/kyfd/qijing/internal/model"
)

// Snapshot lifecycle statuses beyond the final ones defined in model.
// A staging snapshot exists while a scan is still streaming; an incomplete
// snapshot was abandoned by a crash, a shutdown, a cancellation or a
// resource guard. Neither is ever presented as a scan result.
const (
	SnapshotStatusStaging    = "staging"
	SnapshotStatusIncomplete = "incomplete"
)

var errSnapshotNotStaging = errors.New("snapshot is not in the staging state")

// BeginStagingScan opens a staging snapshot: the row exists before the
// first entry is written, so a crash at any later point is always
// recognisable and recoverable. The previous complete snapshot is untouched.
func (s *Store) BeginStagingScan(ctx context.Context, scanID string, roots []string, jobID string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := formatTime(time.Now())
	if _, err = tx.ExecContext(ctx, `INSERT INTO scans(id,started_at,ended_at,error_count,status,partial,truncated,truncation_reason) VALUES(?,?,'',0,?,?,0,'')`,
		scanID, now, SnapshotStatusStaging, false, false); err != nil {
		return fmt.Errorf("open staging snapshot: %w", err)
	}
	for _, rootID := range roots {
		if _, err = tx.ExecContext(ctx, `INSERT INTO scan_roots(scan_id,root_id) VALUES(?,?)`, scanID, rootID); err != nil {
			return err
		}
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO scan_jobs(id,snapshot_id,roots,state,created_at,updated_at) VALUES(?,?,?,?,?,?)`,
		jobID, scanID, "", "running", now, now); err != nil {
		return fmt.Errorf("record scan job: %w", err)
	}
	return tx.Commit()
}

// WriteEntryBatch persists one classified entry batch in its own
// transaction. Small transactions keep the write bounded and let a slow
// disk apply backpressure to the scanner instead of growing memory.
func (s *Store) WriteEntryBatch(ctx context.Context, scanID string, entries []model.Entry) error {
	if len(entries) == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, entry := range entries {
		classes, _ := json.Marshal(entry.Classes)
		if _, err = tx.ExecContext(ctx, `INSERT INTO entries(scan_id,id,root_id,path,relative_path,name,extension,kind,size,mod_time,sha256,classes,git_project,error) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			scanID, entry.ID, entry.RootID, entry.Path, entry.Relative, entry.Name, entry.Extension, entry.Kind, entry.Size, formatTime(entry.ModTime), entry.SHA256, string(classes), entry.GitProject, entry.Error); err != nil {
			return fmt.Errorf("write entry batch: %w", err)
		}
	}
	return tx.Commit()
}

// FinalizeScan completes a staging snapshot: relations, errors, counters and
// the final status land atomically. The snapshot becomes visible as a scan
// result only when this commits.
func (s *Store) FinalizeScan(ctx context.Context, scan model.Scan) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	status := scan.Status
	if status == "" {
		status = model.ScanStatusComplete
	}
	result, err := tx.ExecContext(ctx, `UPDATE scans SET ended_at=?,error_count=?,status=?,partial=?,truncated=?,truncation_reason=? WHERE id=? AND status=?`,
		formatTime(scan.EndedAt), scan.ErrorCount, status, scan.Partial, scan.Truncated, scan.TruncationReason, scan.ID, SnapshotStatusStaging)
	if err != nil {
		return err
	}
	if affected, err := result.RowsAffected(); err == nil && affected == 0 {
		return errSnapshotNotStaging
	}
	for ordinal, message := range scan.Errors {
		if _, err = tx.ExecContext(ctx, `INSERT INTO scan_errors(scan_id,ordinal,message) VALUES(?,?,?)`, scan.ID, ordinal, message); err != nil {
			return err
		}
	}
	for _, relation := range scan.Relations {
		if _, err = tx.ExecContext(ctx, `INSERT INTO relations(scan_id,from_id,to_id,type) VALUES(?,?,?,?)`, scan.ID, relation.FromID, relation.ToID, relation.Type); err != nil {
			return err
		}
	}
	if _, err = tx.ExecContext(ctx, `UPDATE scan_jobs SET state='finished',updated_at=? WHERE snapshot_id=?`, formatTime(time.Now()), scan.ID); err != nil {
		return err
	}
	return tx.Commit()
}

// AbandonScan marks a staging snapshot as never-completed. The streamed
// entries are deleted (they can be numerous); the row stays as a local
// diagnostic record and is never presented as a result.
func (s *Store) AbandonScan(ctx context.Context, scanID, reason string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `DELETE FROM entries WHERE scan_id=?`, scanID); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM relations WHERE scan_id=?`, scanID); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM scan_errors WHERE scan_id=?`, scanID); err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `UPDATE scans SET status=?,ended_at=?,truncation_reason=? WHERE id=? AND status=?`,
		SnapshotStatusIncomplete, formatTime(time.Now()), reason, scanID, SnapshotStatusStaging)
	if err != nil {
		return err
	}
	if affected, err := result.RowsAffected(); err == nil && affected == 0 {
		return errSnapshotNotStaging
	}
	if _, err = tx.ExecContext(ctx, `UPDATE scan_jobs SET state='abandoned',updated_at=? WHERE snapshot_id=?`, formatTime(time.Now()), scanID); err != nil {
		return err
	}
	return tx.Commit()
}

// PurgeStagingScans runs at startup: no scan survives a process restart, so
// every staging row is a leftover and its entries are garbage. The rows are
// downgraded to incomplete and emptied; the count is returned for the audit
// log. The most recent complete snapshot is never touched.
func (s *Store) PurgeStagingScans(ctx context.Context) (int, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id FROM scans WHERE status=?`, SnapshotStatusStaging)
	if err != nil {
		return 0, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return 0, err
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	purged := 0
	for _, id := range ids {
		if err := s.AbandonScan(ctx, id, "startup_cleanup"); err != nil && !errors.Is(err, errSnapshotNotStaging) {
			return purged, err
		}
		purged++
	}
	return purged, nil
}
