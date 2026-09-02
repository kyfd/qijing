package application

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/kyfd/qijing/internal/fileid"
	"github.com/kyfd/qijing/internal/model"
	"github.com/kyfd/qijing/internal/pathsafe"
	"github.com/kyfd/qijing/internal/platform"
	"github.com/kyfd/qijing/internal/store"
)

var (
	// ErrRecycleConfirmation mirrors the agent flow: a recycle confirmation is
	// bound to one preview, one snapshot and one exact set of files.
	ErrRecycleConfirmation = errors.New("recycle confirmation is missing, expired, or stale")
	ErrRecycleEmpty        = errors.New("select at least one item to recycle")
	ErrRecycleChanged      = errors.New("the file changed since it was previewed")
)

// recycleTokenLifetime matches the agent preview window: long enough to read
// every row, short enough that a stale desk cannot act on old evidence.
const recycleTokenLifetime = 10 * time.Minute

// maxRecycleBatch keeps one confirmation legible. Recycling is deliberately a
// reviewed, item-by-item act rather than a bulk sweep.
const maxRecycleBatch = 50

// RecycleCandidateDTO is one file the user may choose to send to the Recycle
// Bin. It always carries the real path: the user cannot consent to something
// they cannot see.
type RecycleCandidateDTO struct {
	EntryID  string `json:"entry_id"`
	Name     string `json:"name"`
	Path     string `json:"path"`
	Size     int64  `json:"size"`
	Kind     string `json:"kind"`
	Modified string `json:"modified"`
	Zone     string `json:"zone"`
	Reason   string `json:"reason"`
	Eligible bool   `json:"eligible"`
	Blocker  string `json:"blocker,omitempty"`
}

type RecycleCandidatesDTO struct {
	SnapshotID string                `json:"snapshot_id"`
	Candidates []RecycleCandidateDTO `json:"candidates"`
}

type RecyclePreviewRequestDTO struct {
	EntryIDs []string `json:"entry_ids"`
}

type RecyclePreviewDTO struct {
	SnapshotID        string                `json:"snapshot_id"`
	Items             []RecycleCandidateDTO `json:"items"`
	TotalBytes        int64                 `json:"total_bytes"`
	SelectionHash     string                `json:"selection_hash"`
	ConfirmationToken string                `json:"confirmation_token"`
	ExpiresInSeconds  int                   `json:"expires_in_seconds"`
}

type RecycleConfirmRequestDTO struct {
	SelectionHash     string `json:"selection_hash"`
	ConfirmationToken string `json:"confirmation_token"`
}

type RecycleResultItemDTO struct {
	EntryID string `json:"entry_id"`
	Path    string `json:"path"`
	Name    string `json:"name"`
	Outcome string `json:"outcome"`
	Error   string `json:"error,omitempty"`
}

type RecycleResultDTO struct {
	Recycled     int                    `json:"recycled"`
	Failed       int                    `json:"failed"`
	BytesFreed   int64                  `json:"bytes_freed"`
	Items        []RecycleResultItemDTO `json:"items"`
	Restorable   bool                   `json:"restorable"`
	AuditEntries int                    `json:"audit_entries"`
}

type RecycleHistoryDTO struct {
	Items []store.RecycledItem `json:"items"`
}

// recycleTarget is the resolved, validated file behind one candidate. The
// full Windows identity (volume serial, file reference number, size,
// timestamps) is captured at preview time so confirmation can prove the
// file is still the same object the user looked at — not merely another
// file that happens to share the path and stat data.
type recycleTarget struct {
	entryID  string
	name     string
	path     string
	root     string
	kind     string
	size     int64
	modTime  time.Time
	identity fileid.Identity
	zone     string
	reason   string
	modified string
}

type recycleConfirmation struct {
	hash       string
	snapshotID string
	targets    []recycleTarget
	expires    time.Time
}

// RecycleManager holds pending recycle confirmations. Like the agent manager it
// keeps them in memory only: an application restart voids every confirmation.
type RecycleManager struct {
	mu            sync.Mutex
	confirmations map[string]recycleConfirmation
}

func newRecycleManager() *RecycleManager {
	return &RecycleManager{confirmations: map[string]recycleConfirmation{}}
}

// RecycleCandidates lists observed files the heuristics flagged, annotated with
// whether they can currently be recycled. Ineligible rows are still returned so
// the reason is visible rather than silently hidden.
func (s *Service) RecycleCandidates(ctx context.Context) RecycleCandidatesDTO {
	scan, _, _, _, _, _, _ := s.manager.snapshot()
	ignored, _ := s.db.IgnoredRecommendations(ctx, scan.ID)
	out := RecycleCandidatesDTO{SnapshotID: scan.ID, Candidates: []RecycleCandidateDTO{}}
	for _, entry := range scan.Entries {
		if ignored[entry.ID] || entry.Kind != model.KindFile || !recyclable(entry) {
			continue
		}
		zone, _, reason := zoneFor(entry)
		candidate := RecycleCandidateDTO{
			EntryID: entry.ID, Name: entry.Name, Path: entry.Path, Size: entry.Size,
			Kind: string(entry.Kind), Modified: entry.ModTime.Format("2006-01-02"),
			Zone: zone, Reason: reason, Eligible: true,
		}
		if _, err := s.validateRecyclePath(entry.Path); err != nil {
			// A file that is already gone (recycled, or removed elsewhere) is
			// noise rather than information; every other blocker is shown.
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			candidate.Eligible = false
			candidate.Blocker = recycleBlockerMessage(err)
		}
		out.Candidates = append(out.Candidates, candidate)
		if len(out.Candidates) >= 200 {
			break
		}
	}
	sort.SliceStable(out.Candidates, func(i, j int) bool { return out.Candidates[i].Size > out.Candidates[j].Size })
	return out
}

// PreviewRecycle validates an explicit selection and issues a one-time
// confirmation token bound to the snapshot and to the exact files on disk.
func (s *Service) PreviewRecycle(ctx context.Context, request RecyclePreviewRequestDTO) (RecyclePreviewDTO, error) {
	scan, _, _, _, _, _, _ := s.manager.snapshot()
	if scan.ID == "" {
		return RecyclePreviewDTO{}, ErrNoScan
	}
	if len(request.EntryIDs) == 0 {
		return RecyclePreviewDTO{}, ErrRecycleEmpty
	}
	if len(request.EntryIDs) > maxRecycleBatch {
		return RecyclePreviewDTO{}, fmt.Errorf("select at most %d items per confirmation", maxRecycleBatch)
	}
	entries := map[string]model.Entry{}
	for _, entry := range scan.Entries {
		entries[entry.ID] = entry
	}
	seen := map[string]bool{}
	targets := make([]recycleTarget, 0, len(request.EntryIDs))
	items := make([]RecycleCandidateDTO, 0, len(request.EntryIDs))
	var total int64
	for _, id := range request.EntryIDs {
		if seen[id] {
			continue
		}
		seen[id] = true
		entry, ok := entries[id]
		if !ok {
			return RecyclePreviewDTO{}, ErrNodeNotFound
		}
		if entry.Kind != model.KindFile {
			return RecyclePreviewDTO{}, fmt.Errorf("%s: only files can be recycled", entry.Name)
		}
		root, err := s.validateRecyclePath(entry.Path)
		if err != nil {
			return RecyclePreviewDTO{}, err
		}
		info, err := os.Lstat(entry.Path)
		if err != nil {
			return RecyclePreviewDTO{}, err
		}
		identity, err := fileid.Identify(entry.Path)
		if err != nil {
			return RecyclePreviewDTO{}, fmt.Errorf("capture file identity: %w", err)
		}
		zone, _, reason := zoneFor(entry)
		modified := info.ModTime().Format("2006-01-02")
		targets = append(targets, recycleTarget{
			entryID: entry.ID, name: entry.Name, path: entry.Path, root: root, kind: string(entry.Kind),
			size: info.Size(), modTime: info.ModTime(), identity: identity, zone: zone, reason: reason, modified: modified,
		})
		items = append(items, RecycleCandidateDTO{
			EntryID: entry.ID, Name: entry.Name, Path: entry.Path, Size: info.Size(),
			Kind: string(entry.Kind), Modified: modified, Zone: zone, Reason: reason, Eligible: true,
		})
		total += info.Size()
	}
	hash := selectionHash(scan.ID, targets)
	token := randomID(32)
	s.recycle.mu.Lock()
	s.recycle.confirmations[token] = recycleConfirmation{hash: hash, snapshotID: scan.ID, targets: targets, expires: time.Now().Add(recycleTokenLifetime)}
	s.recycle.mu.Unlock()
	return RecyclePreviewDTO{
		SnapshotID: scan.ID, Items: items, TotalBytes: total, SelectionHash: hash,
		ConfirmationToken: token, ExpiresInSeconds: int(recycleTokenLifetime.Seconds()),
	}, nil
}

// ConfirmRecycle consumes a preview token and moves each still-unchanged file
// into the Windows Recycle Bin, recording every outcome locally.
func (s *Service) ConfirmRecycle(ctx context.Context, request RecycleConfirmRequestDTO) (RecycleResultDTO, error) {
	scan, _, _, _, _, _, _ := s.manager.snapshot()
	s.recycle.mu.Lock()
	confirmation, ok := s.recycle.confirmations[request.ConfirmationToken]
	delete(s.recycle.confirmations, request.ConfirmationToken)
	s.recycle.mu.Unlock()
	if !ok || time.Now().After(confirmation.expires) || confirmation.snapshotID != scan.ID ||
		subtle.ConstantTimeCompare([]byte(confirmation.hash), []byte(request.SelectionHash)) != 1 {
		return RecycleResultDTO{}, ErrRecycleConfirmation
	}
	confirmedAt := time.Now()
	result := RecycleResultDTO{Items: make([]RecycleResultItemDTO, 0, len(confirmation.targets)), Restorable: true}
	for _, target := range confirmation.targets {
		item := RecycleResultItemDTO{EntryID: target.entryID, Path: target.path, Name: target.name}
		err := s.recycleOne(target)
		outcome := store.RecycleOutcomeRecycled
		switch {
		case err == nil:
			result.Recycled++
			result.BytesFreed += target.size
		case errors.Is(err, platform.ErrRecycleAborted):
			outcome = store.RecycleOutcomeRefused
			result.Failed++
			item.Error = "Windows 未将其移入回收站，文件保持原样"
		default:
			outcome = store.RecycleOutcomeFailed
			result.Failed++
			item.Error = err.Error()
		}
		item.Outcome = outcome
		result.Items = append(result.Items, item)
		record := store.RecycledItem{
			ID: randomID(16), ScanID: confirmation.snapshotID, EntryID: target.entryID, Path: target.path,
			Name: target.name, Kind: target.kind, Size: target.size, Root: target.root,
			ConfirmedAt: confirmedAt, RecycledAt: time.Now(), Outcome: outcome, Error: item.Error,
		}
		if err := s.db.AddRecycledItem(ctx, record); err == nil {
			result.AuditEntries++
		}
	}
	return result, nil
}

// RecycleHistory returns the local audit trail of every recycle attempt.
func (s *Service) RecycleHistory(ctx context.Context, limit int) (RecycleHistoryDTO, error) {
	items, err := s.db.RecycledItems(ctx, limit)
	if err != nil {
		return RecycleHistoryDTO{}, err
	}
	if items == nil {
		items = []store.RecycledItem{}
	}
	return RecycleHistoryDTO{Items: items}, nil
}

// recycleOne re-validates immediately before acting. The preview proved
// intent; this proves the file on disk is still the very object that intent
// referred to: same volume serial and file reference number, same size,
// same timestamps. A path that now resolves to a different file object —
// even one with byte-for-byte identical stat data — is refused.
func (s *Service) recycleOne(target recycleTarget) error {
	if _, err := s.validateRecyclePath(target.path); err != nil {
		return err
	}
	identity, err := fileid.Identify(target.path)
	if err != nil {
		return err
	}
	if !identity.Matches(target.identity) {
		return ErrRecycleChanged
	}
	return platform.MoveToRecycleBin(target.path)
}

// validateRecyclePath enforces every existing filesystem boundary before a
// destructive-looking operation: the path must be a regular file inside a
// currently authorized root, reached without traversing any symlink, junction,
// reparse point or cloud placeholder, and must not be a root itself.
func (s *Service) validateRecyclePath(path string) (string, error) {
	if !filepath.IsAbs(path) {
		return "", fmt.Errorf("path is not absolute: %q", path)
	}
	clean := filepath.Clean(path)
	s.mu.RLock()
	roots := append([]string(nil), s.cfg.Roots...)
	s.mu.RUnlock()
	root := ""
	for _, candidate := range roots {
		if _, err := pathsafe.Contained(candidate, clean); err == nil {
			root = candidate
			break
		}
	}
	if root == "" {
		return "", ErrUnauthorized
	}
	if strings.EqualFold(clean, filepath.Clean(root)) || pathsafe.IsWholeDriveRoot(clean) {
		return "", errors.New("an authorized root cannot be recycled")
	}
	// Contained is purely lexical, so the link and attribute checks below are
	// what actually prevent escaping the root through a reparse point.
	if err := pathsafe.RejectSymlinkComponents(clean); err != nil {
		return "", err
	}
	info, err := os.Lstat(clean)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", errors.New("only regular files can be recycled")
	}
	return root, nil
}

// recyclable limits candidates to the heuristics that describe disposable
// artifacts. Dormant or merely large files are never offered.
func recyclable(entry model.Entry) bool {
	for _, class := range entry.Classes {
		if class == model.ClassRotten || class == model.ClassOrphan {
			return true
		}
	}
	return false
}

func recycleBlockerMessage(err error) string {
	switch {
	case errors.Is(err, ErrUnauthorized):
		return "已不在授权目录内"
	case errors.Is(err, pathsafe.ErrSymlink), errors.Is(err, pathsafe.ErrReparse):
		return "路径经过链接或重解析点"
	case errors.Is(err, pathsafe.ErrCloudFile):
		return "云端占位文件"
	case errors.Is(err, os.ErrNotExist):
		return "文件已不存在"
	default:
		return err.Error()
	}
}

// selectionHash binds a confirmation to the snapshot and to each file's
// full Windows identity at preview time.
func selectionHash(snapshotID string, targets []recycleTarget) string {
	ordered := append([]recycleTarget(nil), targets...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].path < ordered[j].path })
	sum := sha256.New()
	fmt.Fprintf(sum, "%s\x00", snapshotID)
	for _, target := range ordered {
		fmt.Fprintf(sum, "%s\x00%d\x00%s\x00", target.path, target.size, target.modTime.UTC().Format(time.RFC3339Nano))
		fmt.Fprintf(sum, "%d\x00%d\x00%s\x00", target.identity.VolumeSerial, target.identity.FileID,
			target.identity.CreationTime.UTC().Format(time.RFC3339Nano))
	}
	return hex.EncodeToString(sum.Sum(nil))
}
