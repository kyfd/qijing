package application

import (
	"context"
	"errors"

	"github.com/kyfd/qijing/internal/diskspace"
	"github.com/kyfd/qijing/internal/model"
	"github.com/kyfd/qijing/internal/scanbroker"
	"github.com/kyfd/qijing/internal/store"
)

// ErrLowDiskSpace reports that the scan was stopped because the volume
// holding the local index ran below the safety threshold. The previous
// complete snapshot is untouched; nothing was deleted.
var ErrLowDiskSpace = errors.New("scan stopped: disk space below the safety threshold")

// minFreeBytes keeps the local index from filling the system drive. The
// SQLite database, its journal and the OS all need headroom; 512 MiB is a
// conservative floor for a desktop machine.
const minFreeBytes = 512 << 20

// checkDiskEveryBatches bounds how often the free-space check runs: one
// cheap syscall per ten streamed batches (about five thousand entries).
const checkDiskEveryBatches = 10

// scanSink streams broker output into the store's staging snapshot with a
// low-disk guard. It runs in the main process only and is used by a single
// scan at a time.
type scanSink struct {
	db      *store.Store
	dataDir string
	writes  int
}

func newScanSink(db *store.Store, dataDir string) *scanSink {
	return &scanSink{db: db, dataDir: dataDir}
}

func (s *scanSink) BeginStaging(ctx context.Context, snapshotID string, roots []string) error {
	if err := s.checkDisk(); err != nil {
		return &scanbroker.SinkError{Code: "low_disk", Err: err}
	}
	if err := s.db.BeginStagingScan(ctx, snapshotID, roots, snapshotID); err != nil {
		return err
	}
	return nil
}

func (s *scanSink) WriteEntries(ctx context.Context, snapshotID string, entries []model.Entry) error {
	if err := s.checkDisk(); err != nil {
		return &scanbroker.SinkError{Code: "low_disk", Err: err}
	}
	return s.db.WriteEntryBatch(ctx, snapshotID, entries)
}

func (s *scanSink) Finalize(ctx context.Context, scan model.Scan) error {
	return s.db.FinalizeScan(ctx, scan)
}

func (s *scanSink) Abandon(ctx context.Context, snapshotID string, reason string) error {
	return s.db.AbandonScan(ctx, snapshotID, reason)
}

// checkDisk enforces the free-space floor: on open and then once per
// checkDiskEveryBatches batches. If the volume cannot answer, it fails
// closed — a scan is repeatable, a full disk is not recoverable afterwards.
func (s *scanSink) checkDisk() error {
	s.writes++
	if s.writes%checkDiskEveryBatches != 1 {
		return nil
	}
	free, err := diskspace.FreeBytes(s.dataDir)
	if err != nil {
		return ErrLowDiskSpace
	}
	if free < minFreeBytes {
		return ErrLowDiskSpace
	}
	return nil
}
