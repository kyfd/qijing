// Package scanbroker runs scans in the independent qijing-scanner process
// and mediates between it and the application: it spawns the process, owns
// the IPC conversation, re-validates every returned entry against the
// authorized roots, and guarantees process cleanup on cancel, crash or exit.
package scanbroker

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
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

// Options configures one subprocess scan.
type Options struct {
	// Executable is the qijing-scanner binary to spawn.
	Executable string
	// ExtraArgs are appended for tests only.
	ExtraArgs []string

	Config config.Config
	// Progress receives scanner progress snapshots.
	Progress func(scanner.Progress)

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
type Scan struct {
	opts Options
}

func New(opts Options) *Scan {
	opts.defaults()
	return &Scan{opts: opts}
}

// Scan spawns qijing-scanner, drives the conversation and assembles the
// model.Scan. On any failure the subprocess is terminated and the error is
// returned; an already-stored snapshot is never touched on this path.
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

	name, err := ipcpipe.RandomName()
	if err != nil {
		return model.Scan{}, err
	}
	args := append([]string{
		"--pipe", name,
		"--protocol", fmt.Sprint(scanproto.Version),
	}, s.opts.ExtraArgs...)
	cmd := exec.Command(s.opts.Executable, args...)
	if err := cmd.Start(); err != nil {
		return model.Scan{}, fmt.Errorf("start scanner process: %w", err)
	}
	stopJob, err := assignJobObject(cmd)
	if err != nil {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
		return model.Scan{}, fmt.Errorf("contain scanner process: %w", err)
	}
	// stopJob closes the kill-on-close job handle; combined with the
	// terminate below it guarantees the subprocess cannot outlive us,
	// including when this process is killed outright.
	defer func() { _ = stopJob() }()
	defer s.terminate(cmd)

	conn, err := ipcpipe.Dial(name, s.opts.ConnectTimeout)
	if err != nil {
		return model.Scan{}, err
	}
	return s.converse(ctx, conn, roots, func() { s.terminate(cmd) })
}

// terminate kills and reaps the subprocess if it is still running.
func (s *Scan) terminate(cmd *exec.Cmd) {
	if cmd.Process == nil {
		return
	}
	_ = cmd.Process.Kill()
	_, _ = cmd.Process.Wait()
}

func (s *Scan) converse(ctx context.Context, conn ipcpipe.Conn, roots []string, kill func()) (model.Scan, error) {
	stream := scanproto.NewConn(conn)
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
		scan, err := s.talk(stream, jobID, roots, &lastFrame)
		doneCh <- outcome{scan, err}
	}()

	select {
	case result := <-doneCh:
		return result.scan, result.err
	case <-ctx.Done():
		// Let the watchdog run the cancel sequence, then collect whatever
		// partial state the scanner reported before termination.
		select {
		case result := <-doneCh:
			return result.scan, result.err
		case <-time.After(s.opts.CancelGrace + 5*time.Second):
			kill()
			return model.Scan{}, fmt.Errorf("scanner did not acknowledge cancellation: %w", ctx.Err())
		}
	}
}

// talk performs the handshake and consumes messages until Done, a fatal
// error or the connection breaks.
func (s *Scan) talk(stream *scanproto.Conn, jobID string, roots []string, lastFrame *atomic.Int64) (model.Scan, error) {
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
		JobID:   jobID,
		Roots:   append([]string(nil), roots...),
		Options: scanproto.OptionsFromConfig(s.opts.Config),
	}}); err != nil {
		return model.Scan{}, err
	}

	out := model.Scan{}
	rootIDs := make(map[string]bool, len(roots))
	for _, root := range roots {
		rootIDs[scanner.RootID(root)] = true
	}
	entryBudget := s.opts.Config.MaxEntries
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
				if entryBudget > 0 && len(out.Entries) >= entryBudget {
					return model.Scan{}, errViolation
				}
				out.Entries = append(out.Entries, entry)
			}
		case scanproto.TypeRelations:
			out.Relations = append(out.Relations, message.Relations.Relations...)
		case scanproto.TypeFatal:
			return model.Scan{}, fmt.Errorf("scanner aborted: %s: %s", message.Fatal.Code, message.Fatal.Detail)
		case scanproto.TypeDone:
			return s.finish(out, message.Done, roots)
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
