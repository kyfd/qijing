package store

import (
	"context"
	"time"
)

// RecycledItem is the permanent local record of one user-confirmed move into
// the Windows Recycle Bin. It is never deleted when a scan snapshot is
// discarded, so the user can always audit what the application touched.
type RecycledItem struct {
	ID          string    `json:"id"`
	ScanID      string    `json:"scan_id"`
	EntryID     string    `json:"entry_id"`
	Path        string    `json:"path"`
	Name        string    `json:"name"`
	Kind        string    `json:"kind"`
	Size        int64     `json:"size"`
	Root        string    `json:"root"`
	ConfirmedAt time.Time `json:"confirmed_at"`
	RecycledAt  time.Time `json:"recycled_at"`
	Outcome     string    `json:"outcome"`
	Error       string    `json:"error,omitempty"`
}

const (
	RecycleOutcomeRecycled = "recycled"
	RecycleOutcomeRefused  = "refused"
	RecycleOutcomeFailed   = "failed"
)

func (s *Store) AddRecycledItem(ctx context.Context, item RecycledItem) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO recycled_items(id,scan_id,entry_id,path,name,kind,size,root,confirmed_at,recycled_at,outcome,error) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`,
		item.ID, item.ScanID, item.EntryID, item.Path, item.Name, item.Kind, item.Size, item.Root,
		formatTime(item.ConfirmedAt), formatTime(item.RecycledAt), item.Outcome, item.Error)
	return err
}

func (s *Store) RecycledItems(ctx context.Context, limit int) ([]RecycledItem, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id,scan_id,entry_id,path,name,kind,size,root,confirmed_at,recycled_at,outcome,error FROM recycled_items ORDER BY recycled_at DESC, rowid DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []RecycledItem
	for rows.Next() {
		var item RecycledItem
		var confirmed, recycled string
		if err := rows.Scan(&item.ID, &item.ScanID, &item.EntryID, &item.Path, &item.Name, &item.Kind, &item.Size, &item.Root, &confirmed, &recycled, &item.Outcome, &item.Error); err != nil {
			return nil, err
		}
		item.ConfirmedAt, _ = time.Parse(time.RFC3339Nano, confirmed)
		item.RecycledAt, _ = time.Parse(time.RFC3339Nano, recycled)
		out = append(out, item)
	}
	return out, rows.Err()
}
