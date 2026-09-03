// Package scanner performs policy-constrained, read-only filesystem scans.
package scanner

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/kyfd/qijing/internal/classify"
	"github.com/kyfd/qijing/internal/config"
	"github.com/kyfd/qijing/internal/model"
	"github.com/kyfd/qijing/internal/pathsafe"
)

var (
	ErrCancelled = errors.New("scan cancelled")
	errStopScan  = errors.New("scan budget exhausted")
)

var wholeDriveExcludedNames = []string{
	"$Extend", "$Recycle.Bin", "Config.Msi", "Documents and Settings",
	"hiberfil.sys", "pagefile.sys", "PerfLogs", "ProgramData", "Recovery",
	"swapfile.sys", "System Volume Information", "Windows", "Windows.old",
}

type ProgressPhase string

const (
	PhasePreparing   ProgressPhase = "preparing"
	PhaseTraversing  ProgressPhase = "traversing"
	PhaseClassifying ProgressPhase = "classifying"
	PhaseRelations   ProgressPhase = "relations"
	PhaseSaving      ProgressPhase = "saving"
)

// Progress is a compact immutable snapshot of work observed so far. It does
// not include a percentage because a recursive filesystem scan has no truthful
// total entry count before traversal. The JSON tags exist because progress
// travels verbatim over the scanner IPC protocol.
type Progress struct {
	Phase            ProgressPhase `json:"phase"`
	ObservedEntries  int64         `json:"observed_entries"`
	Files            int64         `json:"files"`
	Directories      int64         `json:"directories"`
	Bytes            int64         `json:"bytes"`
	RootsStarted     int           `json:"roots_started"`
	RootsCompleted   int           `json:"roots_completed"`
	RootsTotal       int           `json:"roots_total"`
	CurrentRootIndex int           `json:"current_root_index"`
	CurrentRootLabel string        `json:"current_root_label,omitempty"`
	Elapsed          time.Duration `json:"elapsed_ns"`
	EntryBudget      int           `json:"entry_budget"`
	ErrorBudget      int           `json:"error_budget"`
	DurationBudget   time.Duration `json:"duration_budget_ns"`
	Errors           int           `json:"errors"`
	Cancelling       bool          `json:"cancelling"`
	Paused           bool          `json:"paused"`
	BudgetTruncated  bool          `json:"budget_truncated"`
	TruncationReason string        `json:"truncation_reason,omitempty"`
}

type Scanner struct {
	Config config.Config
	Now    func() time.Time

	progress   func(Progress)
	batch      func([]model.Entry) error
	snapshotID string
}

// SetSnapshotID presets the scan's identifier. The broker owns the snapshot
// id because it opens the staging snapshot before the first entry arrives;
// when unset the scan generates one itself.
func (s *Scanner) SetSnapshotID(id string) { s.snapshotID = id }

// SetProgressCallback installs the scan's snapshot receiver. Scanner invokes
// it synchronously and never exposes the mutable Scan under construction.
func (s *Scanner) SetProgressCallback(callback func(Progress)) {
	s.progress = callback
}

// SetBatchCallback installs a streaming consumer for scan entries. Entries
// are already classified when delivered; the callback receives an immutable
// copy in bounded batches and may slow the scan by blocking (backpressure).
// A callback error aborts the scan with ErrOutputFailed.
func (s *Scanner) SetBatchCallback(callback func([]model.Entry) error) {
	s.batch = callback
}

// ErrOutputFailed reports that the batch consumer rejected output; the scan
// result is then incomplete and must not be presented as complete.
var ErrOutputFailed = errors.New("scan output failed")

type scanState struct {
	scanner      *Scanner
	result       *model.Scan
	deadline     time.Time
	progress     Progress
	lastProgress time.Time
	batchSent    int
}

func New(cfg config.Config) (*Scanner, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return &Scanner{Config: cfg, Now: time.Now}, nil
}

// Scan traverses only configured roots. It never follows links or writes to
// the filesystem. Individual inaccessible entries are recorded and skipped.
func (s *Scanner) Scan(ctx context.Context) (model.Scan, error) {
	now := s.Now()
	result := model.Scan{
		ID:        s.snapshotID,
		StartedAt: now,
		Status:    model.ScanStatusComplete,
	}
	if result.ID == "" {
		result.ID = opaqueID("scan", fmt.Sprint(now.UnixNano()))
	}
	state := scanState{
		scanner: s,
		result:  &result,
		progress: Progress{
			Phase:          PhasePreparing,
			RootsTotal:     len(s.Config.Roots),
			EntryBudget:    s.Config.MaxEntries,
			ErrorBudget:    s.Config.MaxErrors,
			DurationBudget: s.Config.MaxDuration,
		},
	}
	if s.Config.MaxDuration > 0 {
		state.deadline = now.Add(s.Config.MaxDuration)
	}
	state.publish(true)

	seenRoots := make(map[string]struct{})
	var cancelled error
	for rootIndex, configuredRoot := range s.Config.Roots {
		if stop := state.stopReason(ctx); stop != "" {
			state.truncate(stop)
			if stop == "cancelled" {
				cancelled = ctx.Err()
			}
			break
		}
		root, err := pathsafe.ValidateRoot(configuredRoot)
		if err != nil {
			if state.addError(fmt.Sprintf("root rejected: %v", err)) {
				break
			}
			continue
		}
		key := strings.ToLower(root)
		if _, duplicate := seenRoots[key]; duplicate {
			continue
		}
		seenRoots[key] = struct{}{}
		rootID := opaqueID("root", root)
		result.Roots = append(result.Roots, rootID)
		state.progress.Phase = PhaseTraversing
		state.progress.RootsStarted++
		state.progress.CurrentRootIndex = state.progress.RootsStarted
		state.progress.CurrentRootLabel = filepath.Base(filepath.Clean(root))
		if state.progress.CurrentRootLabel == "." || state.progress.CurrentRootLabel == string(filepath.Separator) {
			state.progress.CurrentRootLabel = fmt.Sprintf("root %d", rootIndex+1)
		}
		state.publish(true)
		if err := state.scanRoot(ctx, root, rootID); err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				state.truncate("cancelled")
				cancelled = err
			}
			if errors.Is(err, ErrOutputFailed) {
				return result, err
			}
			break
		}
		state.progress.RootsCompleted++
		state.publish(true)
	}

	if err := state.flushBatch(true); err != nil {
		return result, err
	}
	state.progress.Phase = PhaseClassifying
	state.publish(true)
	state.progress.Phase = PhaseRelations
	state.publish(true)
	result.Relations = deriveRelations(result.Entries)
	result.EndedAt = s.Now()
	state.publish(true)
	if cancelled != nil {
		return result, fmt.Errorf("%w: %v", ErrCancelled, cancelled)
	}
	return result, nil
}

func (state *scanState) scanRoot(ctx context.Context, root, rootID string) error {
	wholeDrive := pathsafe.IsWholeDriveRoot(root)
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
		if reason := state.stopReason(ctx); reason != "" {
			state.truncate(reason)
			if reason == "cancelled" {
				return ctx.Err()
			}
			return errStopScan
		}
		if walkErr != nil {
			if state.addError(fmt.Sprintf("unreadable entry: %v", walkErr)) {
				return errStopScan
			}
			if d != nil && d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if path == root {
			return nil
		}
		state.progress.ObservedEntries++
		state.publish(false)
		if _, err := pathsafe.Contained(root, path); err != nil {
			if state.addError(fmt.Sprintf("containment rejected: %v", err)) {
				return errStopScan
			}
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		info, err := os.Lstat(path)
		if err != nil {
			if state.addError(fmt.Sprintf("lstat failed: %v", err)) {
				return errStopScan
			}
			return nil
		}
		if info.Mode()&os.ModeSymlink != 0 {
			if state.addError("symlink rejected: " + safeRelative(root, path)) {
				return errStopScan
			}
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if err := pathsafe.RejectUnsafeFile(path); err != nil {
			if state.addError(fmt.Sprintf("unsafe entry skipped: %s: %v", safeRelative(root, path), err)) {
				return errStopScan
			}
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if info.IsDir() && (excluded(info.Name(), state.scanner.Config.ExcludedNames) || wholeDrive && excluded(info.Name(), wholeDriveExcludedNames)) {
			return filepath.SkipDir
		}
		if wholeDrive && !info.IsDir() && excluded(info.Name(), wholeDriveExcludedNames) {
			return nil
		}
		if state.scanner.Config.MaxEntries > 0 && len(state.result.Entries) >= state.scanner.Config.MaxEntries {
			state.truncate("entry_limit")
			return errStopScan
		}

		rel := safeRelative(root, path)
		entry := model.Entry{
			ID: opaqueID(rootID, filepath.ToSlash(rel)), RootID: rootID, Path: path,
			Relative: filepath.ToSlash(rel), Name: info.Name(), Size: info.Size(), ModTime: info.ModTime(),
		}
		if info.IsDir() {
			entry.Kind = model.KindDirectory
			state.progress.Directories++
			gitPath := filepath.Join(path, ".git")
			if gitInfo, err := os.Lstat(gitPath); err == nil && gitInfo.IsDir() && gitInfo.Mode()&os.ModeSymlink == 0 && pathsafe.RejectUnsafeFile(gitPath) == nil {
				entry.GitProject = true
				if gitInfo.ModTime().After(entry.ModTime) {
					entry.ModTime = gitInfo.ModTime()
				}
			}
		} else if info.Mode().IsRegular() {
			entry.Kind = model.KindFile
			state.progress.Files++
			if info.Size() > 0 {
				state.progress.Bytes += info.Size()
			}
			entry.Extension = strings.ToLower(filepath.Ext(info.Name()))
			hashAllowed := shouldHash(state.scanner.Config, wholeDrive)
			if hashAllowed && (state.scanner.Config.MaxHashBytes == 0 || info.Size() <= state.scanner.Config.MaxHashBytes) {
				var stop string
				entry.SHA256, entry.Error, stop = hashFile(path, func() string {
					state.publish(false)
					return state.stopReason(ctx)
				})

				if stop != "" {
					state.truncate(stop)
					if stop == "cancelled" {
						return ctx.Err()
					}
					return errStopScan
				}
			}
		} else {
			return nil
		}
		state.result.Entries = append(state.result.Entries, entry)
		classify.One(&state.result.Entries[len(state.result.Entries)-1], state.result.StartedAt, state.scanner.Config)
		if err := state.flushBatch(false); err != nil {
			return err
		}
		state.publish(false)
		return nil
	})
	if err != nil && !errors.Is(err, errStopScan) && !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) && !errors.Is(err, ErrOutputFailed) {
		state.addError(fmt.Sprintf("walk stopped: %v", err))
	}
	return err
}

// batchFlushSize bounds how many classified entries accumulate before they
// are handed to the streaming consumer. It also bounds the worst-case extra
// memory a slow consumer forces the scanner to hold.
const batchFlushSize = 512

// flushBatch delivers newly classified entries to the batch callback. With
// force=false it only fires once a full batch has accumulated; force=true
// flushes the tail at the end of the scan.
func (state *scanState) flushBatch(force bool) error {
	if state.scanner.batch == nil {
		return nil
	}
	pending := len(state.result.Entries) - state.batchSent
	if pending <= 0 {
		return nil
	}
	if !force && pending < batchFlushSize {
		return nil
	}
	batch := make([]model.Entry, pending)
	copy(batch, state.result.Entries[state.batchSent:])
	state.batchSent += pending
	if err := state.scanner.batch(batch); err != nil {
		return fmt.Errorf("%w: %v", ErrOutputFailed, err)
	}
	return nil
}

func (state *scanState) stopReason(ctx context.Context) string {
	if ctx.Err() != nil {
		return "cancelled"
	}
	if !state.deadline.IsZero() && !state.scanner.Now().Before(state.deadline) {
		return "duration_limit"
	}
	return ""
}

func (state *scanState) addError(message string) bool {
	state.result.ErrorCount++
	state.progress.Errors = state.result.ErrorCount
	state.publish(false)
	if state.scanner.Config.MaxErrors == 0 || len(state.result.Errors) < state.scanner.Config.MaxErrors {
		state.result.Errors = append(state.result.Errors, message)
	}
	if state.scanner.Config.MaxErrors > 0 && state.result.ErrorCount >= state.scanner.Config.MaxErrors {
		state.truncate("error_limit")
		return true
	}
	return false
}

func (state *scanState) truncate(reason string) {
	state.result.Partial = true
	state.result.Truncated = true
	state.result.TruncationReason = reason
	state.progress.TruncationReason = reason
	state.progress.Cancelling = reason == "cancelled"
	state.progress.BudgetTruncated = reason != "cancelled"
	if reason == "cancelled" {
		state.result.Status = model.ScanStatusCancelled
	} else {
		state.result.Status = model.ScanStatusPartial
	}
	state.publish(true)
}

func (state *scanState) publish(force bool) {
	if state.scanner.progress == nil {
		return
	}
	now := state.scanner.Now()
	if !force && !state.lastProgress.IsZero() && now.Sub(state.lastProgress) < 200*time.Millisecond {
		return
	}
	state.progress.Elapsed = now.Sub(state.result.StartedAt)
	if state.progress.Elapsed < 0 {
		state.progress.Elapsed = 0
	}
	state.lastProgress = now
	state.scanner.progress(state.progress)
}

func hashFile(path string, stopReason func() string) (string, string, string) {
	file, err := os.Open(path)
	if err != nil {
		return "", err.Error(), ""
	}
	defer file.Close()
	h := sha256.New()
	buffer := make([]byte, 128*1024)
	for {
		if stop := stopReason(); stop != "" {
			return "", "", stop
		}
		n, readErr := file.Read(buffer)
		if n > 0 {
			_, _ = h.Write(buffer[:n])
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return "", readErr.Error(), ""
		}
	}
	return hex.EncodeToString(h.Sum(nil)), "", ""
}

func deriveRelations(entries []model.Entry) []model.Relation {
	var relations []model.Relation
	byHash := make(map[string][]model.Entry)
	dirs := make(map[string]model.Entry)
	for _, entry := range entries {
		if entry.Kind == model.KindFile && entry.SHA256 != "" {
			byHash[entry.SHA256] = append(byHash[entry.SHA256], entry)
		}
		if entry.Kind == model.KindDirectory {
			dirs[strings.ToLower(filepath.Join(filepath.Dir(entry.Relative), entry.Name))] = entry
		}
	}
	for _, group := range byHash {
		if len(group) < 2 {
			continue
		}
		sort.Slice(group, func(i, j int) bool { return group[i].ID < group[j].ID })
		for i := 1; i < len(group); i++ {
			relations = append(relations, model.Relation{FromID: group[0].ID, ToID: group[i].ID, Type: model.RelationDuplicate})
		}
	}
	for _, entry := range entries {
		if entry.Kind != model.KindFile || entry.Extension != ".zip" {
			continue
		}
		stem := strings.TrimSuffix(entry.Name, filepath.Ext(entry.Name))
		key := strings.ToLower(filepath.Join(filepath.Dir(entry.Relative), stem))
		if dir, ok := dirs[key]; ok && dir.RootID == entry.RootID {
			relations = append(relations, model.Relation{FromID: entry.ID, ToID: dir.ID, Type: model.RelationZIPExtract})
		}
	}
	sort.Slice(relations, func(i, j int) bool {
		a, b := relations[i], relations[j]
		return a.Type+a.FromID+a.ToID < b.Type+b.FromID+b.ToID
	})
	return relations
}

func shouldHash(cfg config.Config, wholeDrive bool) bool {
	return cfg.HashSHA256 && (!wholeDrive || cfg.HashWholeDrive)
}

func excluded(name string, names []string) bool {
	for _, candidate := range names {
		if strings.EqualFold(name, candidate) {
			return true
		}
	}
	return false
}

func safeRelative(root, path string) string {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return "<invalid>"
	}
	return rel
}

func opaqueID(parts ...string) string {
	h := sha256.New()
	for _, part := range parts {
		_, _ = io.WriteString(h, part)
		_, _ = io.WriteString(h, "\x00")
	}
	return hex.EncodeToString(h.Sum(nil)[:16])
}

// RootID returns the stable entry-namespace id a scan assigns to one
// validated root. The broker uses it to check that returned entries only
// reference roots the scan actually authorized.
func RootID(validatedRoot string) string {
	return opaqueID("root", validatedRoot)
}
