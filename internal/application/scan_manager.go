package application

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"fileecosystem/internal/config"
	"fileecosystem/internal/model"
	"fileecosystem/internal/scanner"
	"fileecosystem/internal/store"
)

var (
	ErrScanRunning = errors.New("scan already running")
	ErrNoRoots     = errors.New("authorize at least one root")
	ErrNoScan      = errors.New("no completed scan")
	ErrNotRunning  = errors.New("no scan running")
)

type scanEngine interface {
	Scan(context.Context) (model.Scan, error)
}

type progressEngine interface {
	SetProgressCallback(func(scanner.Progress))
}

type scannerFactory func(config.Config) (scanEngine, error)

// ScanManager owns scan task lifetime independently of callers and windows.
type ScanManager struct {
	mu          sync.RWMutex
	appCtx      context.Context
	appCancel   context.CancelFunc
	store       *store.Store
	factory     scannerFactory
	latest      model.Scan
	state       ScanState
	taskID      string
	lastErr     string
	taskResult  string
	progress    scanner.Progress
	hasProgress bool
	taskCancel  context.CancelFunc
	done        chan struct{}
	closeOnce   sync.Once
}

func newScanManager(parent context.Context, db *store.Store, latest model.Scan, factory scannerFactory) *ScanManager {
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithCancel(parent)
	return &ScanManager{appCtx: ctx, appCancel: cancel, store: db, factory: factory, latest: latest, state: ScanIdle}
}

// Start starts one background scan and returns before filesystem traversal begins.
// The supplied request context is deliberately not retained.
func (m *ScanManager) Start(cfg config.Config) (StartScanDTO, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.state != ScanIdle {
		return StartScanDTO{}, ErrScanRunning
	}
	if len(cfg.Roots) == 0 {
		return StartScanDTO{}, ErrNoRoots
	}
	engine, err := m.factory(cfg)
	if err != nil {
		return StartScanDTO{}, err
	}
	select {
	case <-m.appCtx.Done():
		return StartScanDTO{}, errors.New("application is closing")
	default:
	}
	taskCtx, cancel := context.WithCancel(m.appCtx)
	m.state = ScanRunning
	m.taskID = fmt.Sprintf("task-%d", time.Now().UnixNano())
	m.lastErr = ""
	m.taskResult = ""
	m.progress = scanner.Progress{}
	m.hasProgress = false
	m.taskCancel = cancel
	m.done = make(chan struct{})
	id := m.taskID
	if reporting, ok := engine.(progressEngine); ok {
		reporting.SetProgressCallback(func(progress scanner.Progress) { m.updateProgress(id, progress) })
	}
	go m.run(taskCtx, id, engine)
	return StartScanDTO{Accepted: true, ScanID: id}, nil
}

func (m *ScanManager) run(ctx context.Context, taskID string, engine scanEngine) {
	result, err := engine.Scan(ctx)
	cancelled := errors.Is(err, context.Canceled) || errors.Is(err, scanner.ErrCancelled) || result.Status == model.ScanStatusCancelled
	if err == nil && !cancelled {
		m.updateProgress(taskID, scanner.Progress{Phase: scanner.PhaseSaving})
		err = m.store.SaveScan(ctx, result)
		cancelled = errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || ctx.Err() != nil
	}
	m.mu.Lock()
	if taskID != m.taskID {
		m.mu.Unlock()
		return
	}
	if err == nil && !cancelled {
		m.latest = result
		m.lastErr = ""
	} else if !cancelled {
		m.lastErr = err.Error()
	}
	if cancelled {
		m.taskResult = model.ScanStatusCancelled
	} else if err != nil {
		m.taskResult = "failed"
	} else {
		m.taskResult = result.Status
	}
	m.state = ScanIdle
	m.taskCancel = nil
	done := m.done
	m.done = nil
	m.mu.Unlock()
	if done != nil {
		close(done)
	}
}

func (m *ScanManager) updateProgress(taskID string, progress scanner.Progress) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if taskID != m.taskID || m.state == ScanIdle {
		return
	}
	if progress.Phase == scanner.PhaseSaving && m.hasProgress {
		progress.ObservedEntries = m.progress.ObservedEntries
		progress.Files = m.progress.Files
		progress.Directories = m.progress.Directories
		progress.Bytes = m.progress.Bytes
		progress.RootsStarted = m.progress.RootsStarted
		progress.RootsCompleted = m.progress.RootsCompleted
		progress.RootsTotal = m.progress.RootsTotal
		progress.EntryBudget = m.progress.EntryBudget
		progress.ErrorBudget = m.progress.ErrorBudget
		progress.DurationBudget = m.progress.DurationBudget
		progress.Errors = m.progress.Errors
		progress.Elapsed = m.progress.Elapsed
	}
	if m.state == ScanCancelling {
		progress.Cancelling = true
	}
	m.progress = progress
	m.hasProgress = true
}

func (m *ScanManager) Cancel() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.state == ScanIdle || m.taskCancel == nil {
		return ErrNotRunning
	}
	m.state = ScanCancelling
	m.progress.Cancelling = true
	m.hasProgress = true
	m.taskCancel()
	return nil
}

func (m *ScanManager) snapshot() (model.Scan, ScanState, string, string, string, scanner.Progress, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.latest, m.state, m.taskID, m.taskResult, m.lastErr, m.progress, m.hasProgress
}

// Close requests cancellation and waits only until the supplied context expires.
func (m *ScanManager) Close(ctx context.Context) error {
	m.closeOnce.Do(m.appCancel)
	m.mu.RLock()
	done := m.done
	m.mu.RUnlock()
	if done == nil {
		return nil
	}
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
