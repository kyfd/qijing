// Package scanbroker runs scans in the independent qijing-scanner process
// and mediates between it and the application: it spawns the process, owns
// the IPC conversation, re-validates every returned entry against the
// authorized roots, and guarantees process cleanup on cancel, crash or exit.
package scanbroker

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/kyfd/qijing/internal/config"
	"github.com/kyfd/qijing/internal/ipcpipe"
	"github.com/kyfd/qijing/internal/model"
	"github.com/kyfd/qijing/internal/pathsafe"
	"github.com/kyfd/qijing/internal/scanner"
	"github.com/kyfd/qijing/internal/scanproto"
)

const (
	// DefaultConnectTimeout bounds the wait for the scanner to create and
	// accept its pipe.
	DefaultConnectTimeout = 30 * time.Second
	// DefaultHeartbeatTimeout is the silence threshold after which the
	// subprocess is presumed dead or stuck and gets killed.
	DefaultHeartbeatTimeout = 45 * time.Second
	// DefaultCancelGrace is how long a cancel message may take effect
	// before the process is terminated.
	DefaultCancelGrace = 10 * time.Second
)

// errViolation marks results the broker refuses to accept: a path outside
// the authorized roots, an unknown root id, or a budget breach. The message
// deliberately carries no path material.
var errViolation = errors.New("scanner returned data outside the authorized roots")

// SinkError marks a sink failure with a sanitized code for the audit trail.
// Codes travel into the snapshot record; they never contain path material.
type SinkError struct {
	Code string
	Err  error
}

func (e *SinkError) Error() string { return e.Err.Error() }
func (e *SinkError) Unwrap() error { return e.Err }

// Sink receives streamed scan output. The broker calls it from its
// conversation goroutine, so a slow implementation applies backpressure all
// the way to the scanner process instead of growing memory.
type Sink interface {
	// BeginStaging opens the staging snapshot before the first entry is
	// written; the previous complete snapshot is untouched.
	BeginStaging(ctx context.Context, snapshotID string, roots []string) error
	// WriteEntries persists one classified batch.
	WriteEntries(ctx context.Context, snapshotID string, entries []model.Entry) error
	// Finalize commits the final status, counters, relations and errors;
	// the snapshot becomes a scan result only when this succeeds.
	Finalize(ctx context.Context, scan model.Scan) error
	// Abandon downgrades the staging snapshot to an incomplete record:
	// streamed entries are discarded, the previous complete snapshot is
	// untouched.
	Abandon(ctx context.Context, snapshotID string, reason string) error
}

// Options configures one subprocess scan.
type Options struct {
	// Executable is the qijing-scanner binary to spawn.
	Executable string
	// ExtraArgs are appended for tests only.
	ExtraArgs []string

	Config config.Config
	// Progress receives scanner progress snapshots.
	Progress func(scanner.Progress)
	// Sink receives the streamed scan output. When nil, the result is only
	// returned in memory and the caller persists it (tests use this; the
	// application wires the store-backed sink).
	Sink Sink

	ConnectTimeout   time.Duration
	HeartbeatTimeout time.Duration
	CancelGrace      time.Duration
}

func (o *Options) defaults() {
	if o.ConnectTimeout <= 0 {
		o.ConnectTimeout = DefaultConnectTimeout
	}
	if o.HeartbeatTimeout <= 0 {
		o.HeartbeatTimeout = DefaultHeartbeatTimeout
	}
	if o.CancelGrace <= 0 {
		o.CancelGrace = DefaultCancelGrace
	}
}

// Scan is one spawn-to-completion scan. It satisfies the application layer's
// scanEngine contract: constructed with configuration, run with a context.
// It is pausable while the conversation is live.
type Scan struct {
	opts Options

	streamMu sync.Mutex
	stream   *scanproto.Conn
	paused   bool

	childCPUSeconds float64
}

// ChildCPUSeconds reports the scanner subprocess's total user+kernel CPU
// time for the last scan (job accounting; zero when unavailable). The
// benchmark suite records it.
func (s *Scan) ChildCPUSeconds() float64 { return s.childCPUSeconds }

// Pause asks the scanner to suspend producing output. Pausing is
// idempotent; a paused scan keeps its heartbeat alive and can be resumed
// or cancelled.
func (s *Scan) Pause() error {
	return s.togglePause(true)
}

// Resume continues a paused scan. Resuming a running scan is a no-op.
func (s *Scan) Resume() error {
	return s.togglePause(false)
}

func (s *Scan) togglePause(paused bool) error {
	s.streamMu.Lock()
	stream, unchanged := s.stream, s.paused == paused
	s.paused = paused
	s.streamMu.Unlock()
	if stream == nil {
		return ErrNotConnected
	}
	if unchanged {
		return nil
	}
	message := scanproto.Message{Type: scanproto.TypePause, Pause: &scanproto.Pause{}}
	if !paused {
		message = scanproto.Message{Type: scanproto.TypeResume, Resume: &scanproto.Resume{}}
	}
	return stream.Send(message)
}

// ErrNotConnected reports that the scan is not currently talking to a
// scanner process.
var ErrNotConnected = errors.New("scan is not connected to a scanner process")

func New(opts Options) *Scan {
	opts.defaults()
	return &Scan{opts: opts}
}

// PersistsOwnResults reports that, when a Sink is wired, the scan output is
// persisted by the broker itself and the caller must not save it again.
func (s *Scan) PersistsOwnResults() bool { return s.opts.Sink != nil }

// Scan spawns qijing-scanner, drives the conversation and assembles the
// model.Scan. Output streams into the Sink: a staging snapshot is opened
// before the first entry and finalized only on success; on any failure it
// is downgraded to an incomplete record. An already-stored snapshot is
// never touched on failure paths.
func (s *Scan) Scan(ctx context.Context) (model.Scan, error) {
	// Re-validate roots on the broker side before anything is spawned. The
	// scanner re-validates too; neither side trusts the other.
	roots := make([]string, 0, len(s.opts.Config.Roots))
	for _, root := range s.opts.Config.Roots {
		validated, err := pathsafe.ValidateRoot(root)
		if err != nil {
			return model.Scan{}, fmt.Errorf("root rejected: %w", err)
		}
		roots = append(roots, validated)
	}

	// The broker owns the snapshot id: the staging row must exist before
	// the first entry arrives.
	snapshotID, err := randomSnapshotID()
	if err != nil {
		return model.Scan{}, err
	}
	return s.spawnAndConverse(ctx, snapshotID, roots)
}

// sinkReason maps a failure to a sanitized audit code.
func sinkReason(err error) string {
	var sinkErr *SinkError
	if errors.As(err, &sinkErr) {
		return sinkErr.Code
	}
	return "error"
}

func randomSnapshotID() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("generate snapshot id: %w", err)
	}
	return hex.EncodeToString(raw[:]), nil
}

func (s *Scan) spawnAndConverse(ctx context.Context, snapshotID string, roots []string) (model.Scan, error) {
	name, err := ipcpipe.RandomName()
	if err != nil {
		return model.Scan{}, err
	}
	args := append([]string{
		"--pipe", name,
		"--protocol", fmt.Sprint(scanproto.Version),
	}, s.opts.ExtraArgs...)
	cmd := exec.Command(s.opts.Executable, args...)
	stderr := &stderrTail{limit: scanproto.MaxStderrTailBytes}
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		return model.Scan{}, fmt.Errorf("start scanner process: %w", err)
	}
	stopJob, queryChildCPU, err := assignJobObject(cmd)
	if err != nil {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
		return model.Scan{}, fmt.Errorf("contain scanner process: %w", err)
	}
	// Sample child CPU before the job handle closes. The kill-on-close
	// job plus terminate below still guarantee the subprocess cannot
	// outlive us, including when this process is killed outright.
	defer func() {
		if queryChildCPU != nil {
			s.childCPUSeconds = queryChildCPU()
		}
		_ = stopJob()
	}()
	defer s.terminate(cmd)

	// A scanner that dies before listening (a protocol or usage error) only
	// shows up as a dial timeout unless its last words are attached, so the
	// sanitized tail annotates this failure too.
	conn, err := ipcpipe.Dial(name, s.opts.ConnectTimeout)
	if err != nil {
		s.terminate(cmd)
		return model.Scan{}, withChildStderr(fmt.Errorf("connect scanner: %w", err), stderr.Text())
	}
	scan, err := s.converse(ctx, conn, roots, snapshotID, func() { s.terminate(cmd) })
	return scan, withChildStderr(err, stderr.Text())
}

// terminate kills and reaps the subprocess if it is still running.
func (s *Scan) terminate(cmd *exec.Cmd) {
	if cmd.Process == nil {
		return
	}
	_ = cmd.Process.Kill()
	_, _ = cmd.Process.Wait()
}

func (s *Scan) converse(ctx context.Context, conn ipcpipe.Conn, roots []string, snapshotID string, kill func()) (model.Scan, error) {
	if s.opts.Sink != nil {
		if err := s.opts.Sink.BeginStaging(ctx, snapshotID, roots); err != nil {
			return model.Scan{}, fmt.Errorf("open staging snapshot: %w", err)
		}
	}
	stream := scanproto.NewConn(conn)
	s.streamMu.Lock()
	s.stream = stream
	s.paused = false
	s.streamMu.Unlock()
	defer func() {
		s.streamMu.Lock()
		s.stream = nil
		s.paused = false
		s.streamMu.Unlock()
	}()
	jobID := fmt.Sprintf("scan-%d", time.Now().UnixNano())

	type outcome struct {
		scan model.Scan
		err  error
	}
	doneCh := make(chan outcome, 1)

	var lastFrame atomic.Int64
	lastFrame.Store(time.Now().UnixNano())

	watchdogStop := make(chan struct{})
	defer close(watchdogStop)
	go func() {
		// Watchdog: enforces heartbeat liveness and carries out the
		// application cancel sequence. Closing the connection and killing
		// the process unblocks the conversation goroutine in every case.
		ticker := time.NewTicker(250 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				if elapsed := time.Since(time.Unix(0, lastFrame.Load())); elapsed > s.opts.HeartbeatTimeout {
					_ = conn.Close()
					kill()
					return
				}
			case <-ctx.Done():
				_ = stream.Send(scanproto.Message{Type: scanproto.TypeCancel, Cancel: &scanproto.Cancel{JobID: jobID}})
				time.Sleep(s.opts.CancelGrace)
				_ = conn.Close()
				kill()
				return
			case <-watchdogStop:
				return
			}
		}
	}()

	go func() {
		scan, err := s.talk(ctx, stream, jobID, snapshotID, roots, &lastFrame)
		doneCh <- outcome{scan, err}
	}()

	select {
	case result := <-doneCh:
		if result.err != nil {
			if s.opts.Sink != nil {
				_ = s.opts.Sink.Abandon(ctx, snapshotID, sinkReason(result.err))
			}
			return result.scan, result.err
		}
		if result.scan.Status == model.ScanStatusCancelled && s.opts.Sink != nil {
			_ = s.opts.Sink.Abandon(ctx, snapshotID, "cancelled")
		}
		return result.scan, result.err
	case <-ctx.Done():
		// Let the watchdog run the cancel sequence, then collect whatever
		// partial state the scanner reported before termination.
		select {
		case result := <-doneCh:
			if result.err != nil {
				if s.opts.Sink != nil {
					_ = s.opts.Sink.Abandon(ctx, snapshotID, sinkReason(result.err))
				}
				return result.scan, result.err
			}
			if result.scan.Status == model.ScanStatusCancelled && s.opts.Sink != nil {
				_ = s.opts.Sink.Abandon(ctx, snapshotID, "cancelled")
			}
			return result.scan, result.err
		case <-time.After(s.opts.CancelGrace + 5*time.Second):
			kill()
			err := fmt.Errorf("scanner did not acknowledge cancellation: %w", ctx.Err())
			if s.opts.Sink != nil {
				_ = s.opts.Sink.Abandon(ctx, snapshotID, sinkReason(err))
			}
			return model.Scan{}, err
		}
	}
}

// talk performs the handshake and consumes messages until Done, a fatal
// error or the connection breaks.
func (s *Scan) talk(ctx context.Context, stream *scanproto.Conn, jobID, snapshotID string, roots []string, lastFrame *atomic.Int64) (model.Scan, error) {
	if err := stream.Send(scanproto.Message{Type: scanproto.TypeHello, Hello: &scanproto.Hello{Version: scanproto.Version, JobID: jobID}}); err != nil {
		return model.Scan{}, fmt.Errorf("scanner handshake: %w", err)
	}
	ack, err := stream.Receive()
	if err != nil {
		return model.Scan{}, fmt.Errorf("scanner handshake: %w", err)
	}
	if ack.Type != scanproto.TypeHelloAck || ack.HelloAck == nil || ack.HelloAck.Version != scanproto.Version {
		return model.Scan{}, fmt.Errorf("%w: scanner protocol version mismatch", scanproto.ErrProtocol)
	}
	if err := stream.Send(scanproto.Message{Type: scanproto.TypeScan, Scan: &scanproto.ScanRequest{
		JobID:      jobID,
		SnapshotID: snapshotID,
		Roots:      append([]string(nil), roots...),
		Options:    scanproto.OptionsFromConfig(s.opts.Config),
	}}); err != nil {
		return model.Scan{}, err
	}

	out := model.Scan{}
	rootIDs := make(map[string]bool, len(roots))
	for _, root := range roots {
		rootIDs[scanner.RootID(root)] = true
	}
	entryBudget := s.opts.Config.MaxEntries
	streamed := 0
	for {
		message, err := stream.Receive()
		if err != nil {
			return model.Scan{}, fmt.Errorf("scanner connection: %w", err)
		}
		lastFrame.Store(time.Now().UnixNano())
		switch message.Type {
		case scanproto.TypeProgress:
			if s.opts.Progress != nil && message.Progress != nil {
				s.opts.Progress(*message.Progress)
			}
		case scanproto.TypeHeartbeat:
		case scanproto.TypeEntries:
			for _, entry := range message.Entries.Entries {
				if err := validateEntry(entry, roots, rootIDs); err != nil {
					return model.Scan{}, err
				}
			}
			// streamed counts everything the scanner produced, so the budget
			// bounds the scanner's output independently of what is retained.
			// out.ErrorCount stays owned by the Done message.
			streamed += len(message.Entries.Entries)
			if entryBudget > 0 && streamed > entryBudget {
				return model.Scan{}, errViolation
			}
			// With a sink the batch is already persisted; keeping a copy
			// would double the memory a scan of any size costs. Without one
			// (tests, in-memory callers) the result carries the entries.
			if s.opts.Sink == nil {
				out.Entries = append(out.Entries, message.Entries.Entries...)
			}
			// The validated batch also streams straight to the sink; a slow
			// sink blocks this loop, which blocks the IPC, which slows the
			// scanner: honest backpressure instead of an unbounded queue.
			if s.opts.Sink != nil {
				if err := s.opts.Sink.WriteEntries(ctx, snapshotID, message.Entries.Entries); err != nil {
					return model.Scan{}, fmt.Errorf("persist scan output: %w", err)
				}
			}
		case scanproto.TypeRelations:
			out.Relations = append(out.Relations, message.Relations.Relations...)
		case scanproto.TypeFatal:
			return model.Scan{}, fmt.Errorf("scanner aborted: %s: %s", message.Fatal.Code, message.Fatal.Detail)
		case scanproto.TypeDone:
			scan, err := s.finish(out, message.Done, roots)
			if err != nil {
				return model.Scan{}, err
			}
			if s.opts.Sink != nil {
				if err := s.opts.Sink.Finalize(ctx, scan); err != nil {
					return model.Scan{}, fmt.Errorf("persist scan output: %w", err)
				}
			}
			return scan, nil
		default:
			return model.Scan{}, fmt.Errorf("%w: unexpected message %q", scanproto.ErrProtocol, message.Type)
		}
	}
}

func (s *Scan) finish(out model.Scan, done *scanproto.Done, roots []string) (model.Scan, error) {
	// The done message may only echo root ids the broker authorized. The
	// scanner reports ids, not paths, exactly like the stored scan format.
	authorized := make(map[string]bool, len(roots))
	for _, root := range roots {
		authorized[scanner.RootID(root)] = true
	}
	for _, rootID := range done.Roots {
		if !authorized[rootID] {
			return model.Scan{}, errViolation
		}
	}
	out.ID = done.ScanID
	out.Status = done.Status
	out.Partial = done.Partial
	out.Truncated = done.Truncated
	out.TruncationReason = done.TruncationReason
	out.StartedAt = done.StartedAt
	out.EndedAt = done.EndedAt
	out.Roots = append([]string(nil), done.Roots...)
	out.ErrorCount = done.ErrorCount
	out.Errors = append([]string(nil), done.Errors...)
	return out, nil
}

// validateEntry re-checks every returned entry before it may enter the
// index: the root id must belong to a root this scan authorized, and the
// path must still sit inside one of those roots.
func validateEntry(entry model.Entry, roots []string, rootIDs map[string]bool) error {
	if !rootIDs[entry.RootID] {
		return errViolation
	}
	if !filepath.IsAbs(entry.Path) {
		return errViolation
	}
	for _, root := range roots {
		if _, err := pathsafe.Contained(root, entry.Path); err == nil {
			return nil
		}
	}
	return errViolation
}

// stderrTail keeps a bounded rolling copy of the child's stderr. Only the
// sanitized form of Text() may leave this type: the raw bytes stay here.
// Writes come from the exec package's copy goroutine and can still be in
// flight when the conversation returns, so every access takes the mutex.
type stderrTail struct {
	mu    sync.Mutex
	limit int
	buf   []byte
}

func (t *stderrTail) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.limit <= 0 {
		t.limit = scanproto.MaxStderrTailBytes
	}
	if len(p) >= t.limit {
		t.buf = append([]byte(nil), p[len(p)-t.limit:]...)
		return len(p), nil
	}
	need := len(t.buf) + len(p) - t.limit
	if need > 0 {
		t.buf = t.buf[need:]
	}
	t.buf = append(t.buf, p...)
	return len(p), nil
}

func (t *stderrTail) Text() string {
	if t == nil {
		return ""
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return scanproto.SanitizeDiagnostic(t.buf)
}

// withChildStderr annotates a connection-level failure with the sanitized
// child diagnostic. Empty tails and nil errors are left unchanged so a
// successful scan never grows an error string.
func withChildStderr(err error, diagnostic string) error {
	if err == nil || diagnostic == "" {
		return err
	}
	return fmt.Errorf("%w (scanner: %s)", err, diagnostic)
}
