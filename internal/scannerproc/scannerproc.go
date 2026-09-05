// Package scannerproc implements the qijing-scanner subprocess side of the
// scan IPC: handshake, one scan request, streamed progress and entries,
// heartbeats and cancel handling.
//
// 本包只依赖扫描与协议层；进程内不得出现数据库、Agent、回收站或设置代码
// （由 go list -deps 守护测试钉住）。
package scannerproc

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/kyfd/qijing/internal/config"
	"github.com/kyfd/qijing/internal/ipcpipe"
	"github.com/kyfd/qijing/internal/model"
	"github.com/kyfd/qijing/internal/scanner"
	"github.com/kyfd/qijing/internal/scanproto"
)

// Engine is the capability surface the serve loop needs from a scanner.
type Engine interface {
	// Run executes one scan identified by snapshotID. Progress snapshots
	// and classified entry batches stream through the callbacks; the
	// returned Scan additionally carries roots, errors and relations for
	// the final Done message.
	Run(ctx context.Context, cfg config.Config, snapshotID string, progress func(scanner.Progress), batch func([]model.Entry) error) (model.Scan, error)
}

// ScannerEngine adapts the in-process scanner to the Engine interface. It
// is the only production implementation.
type ScannerEngine struct{}

func (ScannerEngine) Run(ctx context.Context, cfg config.Config, snapshotID string, progress func(scanner.Progress), batch func([]model.Entry) error) (model.Scan, error) {
	engine, err := scanner.New(cfg)
	if err != nil {
		return model.Scan{}, err
	}
	engine.SetSnapshotID(snapshotID)
	engine.SetProgressCallback(progress)
	engine.SetBatchCallback(batch)
	return engine.Scan(ctx)
}

const DefaultHeartbeatEvery = 10 * time.Second

// Serve drives one connection: version handshake, a single scan request,
// streaming output, then Done. It returns when the scan completes or the
// connection breaks; the connection is always closed.
func Serve(conn ipcpipe.Conn, engine Engine, scannerVersion string, heartbeatEvery time.Duration) error {
	defer conn.Close()
	stream := scanproto.NewConn(conn)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// The reader goroutine owns every Receive for the whole connection
	// lifetime. It forwards handshake frames to the serve flow, turns
	// cancels into context cancellation, records violations, and — after
	// the last frame is sent — observes the broker's disconnect so the
	// scanner never closes first (Windows would discard unread data).
	var reader sync.WaitGroup
	reader.Add(1)
	violations := make(chan error, 1)
	requests := make(chan scanproto.Message, 4)
	scanned := false
	gate := newPauseGate()
	var progressMu sync.Mutex
	var lastProgress *scanner.Progress
	notifyPaused := func(paused bool) {
		progressMu.Lock()
		snapshot := scanner.Progress{Paused: paused}
		if lastProgress != nil {
			snapshot = *lastProgress
			snapshot.Paused = paused
		}
		progressMu.Unlock()
		_ = stream.Send(scanproto.Message{Type: scanproto.TypeProgress, Progress: &snapshot})
	}
	go func() {
		defer reader.Done()
		for {
			message, err := stream.Receive()
			if err != nil {
				close(requests)
				return
			}
			switch message.Type {
			case scanproto.TypeHello, scanproto.TypeScan:
				if scanned {
					violations <- fmt.Errorf("%w: unexpected second handshake frame", scanproto.ErrProtocol)
					cancel()
					return
				}
				if message.Type == scanproto.TypeScan {
					scanned = true
				}
				requests <- message
			case scanproto.TypeCancel:
				cancel()
			case scanproto.TypePause:
				gate.setPaused(true)
				notifyPaused(true)
			case scanproto.TypeResume:
				gate.setPaused(false)
				notifyPaused(false)
			default:
				violations <- fmt.Errorf("%w: unexpected client message %q", scanproto.ErrProtocol, message.Type)
				cancel()
				return
			}
		}
	}()

	hello, err := nextRequest(requests)
	if err != nil {
		return err
	}
	if hello.Type != scanproto.TypeHello || hello.Hello == nil || hello.Hello.Version != scanproto.Version {
		_ = stream.Send(fatal("protocol_version", "unsupported protocol version"))
		holdOpen(&reader, closeGrace)
		return fmt.Errorf("%w: expected hello v%d", scanproto.ErrProtocol, scanproto.Version)
	}
	jobID := hello.Hello.JobID
	if err := stream.Send(scanproto.Message{Type: scanproto.TypeHelloAck, HelloAck: &scanproto.HelloAck{Version: scanproto.Version, ScannerVersion: scannerVersion}}); err != nil {
		return err
	}

	request, err := nextRequest(requests)
	if err != nil {
		return err
	}
	if request.Type != scanproto.TypeScan || request.Scan == nil {
		_ = stream.Send(fatal("protocol_scan", "expected a scan request"))
		holdOpen(&reader, closeGrace)
		return fmt.Errorf("%w: expected scan request", scanproto.ErrProtocol)
	}
	if request.Scan.JobID != jobID {
		_ = stream.Send(fatal("protocol_job", "scan request does not match the handshake"))
		holdOpen(&reader, closeGrace)
		return fmt.Errorf("%w: job id mismatch", scanproto.ErrProtocol)
	}
	cfg := request.Scan.Options.Config()
	cfg.Roots = append([]string(nil), request.Scan.Roots...)
	if err := cfg.Validate(); err != nil {
		_ = stream.Send(fatal("invalid_request", "scan configuration rejected"))
		holdOpen(&reader, closeGrace)
		return fmt.Errorf("%w: invalid scan configuration: %v", scanproto.ErrProtocol, err)
	}

	stopHeartbeat := make(chan struct{})
	var heartbeat sync.WaitGroup
	heartbeat.Add(1)
	go func() {
		defer heartbeat.Done()
		ticker := time.NewTicker(heartbeatEvery)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				if err := stream.Send(scanproto.Message{Type: scanproto.TypeHeartbeat}); err != nil {
					cancel()
					return
				}
			case <-ctx.Done():
				return
			case <-stopHeartbeat:
				return
			}
		}
	}()

	result, runErr := engine.Run(ctx, cfg, request.Scan.SnapshotID,
		func(progress scanner.Progress) {
			// The gate pauses the scan by blocking its callbacks; while
			// paused only heartbeats keep the connection alive.
			if err := gate.wait(ctx); err != nil {
				cancel()
				return
			}
			progressMu.Lock()
			snapshot := progress
			lastProgress = &snapshot
			progressMu.Unlock()
			if err := stream.Send(scanproto.Message{Type: scanproto.TypeProgress, Progress: &snapshot}); err != nil {
				cancel()
			}
		},
		func(batch []model.Entry) error {
			if err := gate.wait(ctx); err != nil {
				return err
			}
			// Entries carry paths, so a fixed batch count is not a bound on
			// the encoded frame size; the stream splits as needed.
			return stream.SendEntries(batch)
		},
	)
	cancel()
	close(stopHeartbeat)
	heartbeat.Wait()

	var violation error
	select {
	case violation = <-violations:
	default:
	}
	if violation != nil {
		_ = stream.Send(fatal("protocol_violation", "the broker sent an unexpected message"))
		holdOpen(&reader, closeGrace)
		return violation
	}
	if runErr != nil {
		switch {
		case errors.Is(runErr, scanner.ErrOutputFailed):
			_ = stream.Send(fatal("output_failed", "the broker stopped accepting scan output"))
			holdOpen(&reader, closeGrace)
			return runErr
		case errors.Is(runErr, scanner.ErrCancelled) || errors.Is(runErr, context.Canceled) || errors.Is(runErr, context.DeadlineExceeded):
			// Cancelled scans still report their partial state via Done.
		default:
			_ = stream.Send(fatal("scan_failed", "the scan aborted unexpectedly"))
			holdOpen(&reader, closeGrace)
			return runErr
		}
	}
	if result.ID == "" {
		_ = stream.Send(fatal("scan_failed", "the scan produced no result"))
		holdOpen(&reader, closeGrace)
		return errors.New("scan produced no result")
	}
	// Relations are chunked to fit the frame limit: a large scan can derive
	// far more relations than fit in one frame, and a single oversized frame
	// would abort the scan after all the traversal work was already done.
	if err := stream.SendRelations(result.Relations); err != nil {
		holdOpen(&reader, closeGrace)
		return err
	}
	done := scanproto.Done{
		ScanID:           result.ID,
		Status:           result.Status,
		Partial:          result.Partial,
		Truncated:        result.Truncated,
		TruncationReason: result.TruncationReason,
		StartedAt:        result.StartedAt,
		EndedAt:          result.EndedAt,
		Roots:            result.Roots,
		ErrorCount:       result.ErrorCount,
		Errors:           result.Errors,
	}
	if done.Status == "" {
		done.Status = model.ScanStatusComplete
	}
	err = stream.Send(scanproto.Message{Type: scanproto.TypeDone, Done: &done})
	holdOpen(&reader, closeGrace)
	return err
}

// closeGrace bounds how long the scanner waits for the broker to drain the
// final frames and close the connection. Tests shorten it.
var closeGrace = 5 * time.Second

// nextRequest waits for the reader goroutine to forward a handshake frame.
func nextRequest(requests <-chan scanproto.Message) (scanproto.Message, error) {
	message, ok := <-requests
	if !ok {
		return scanproto.Message{}, fmt.Errorf("connection closed before the handshake completed")
	}
	return message, nil
}

// holdOpen keeps the connection open after the last frame: Windows
// discards unread pipe data when the writing end closes, so the scanner
// must not close first. It waits for the cancel reader to observe the
// broker's disconnect, or for the grace period to expire.
func holdOpen(reader *sync.WaitGroup, grace time.Duration) {
	drained := make(chan struct{})
	go func() {
		reader.Wait()
		close(drained)
	}()
	select {
	case <-drained:
	case <-time.After(grace):
	}
}

func fatal(code, detail string) scanproto.Message {
	return scanproto.Message{Type: scanproto.TypeFatal, Fatal: &scanproto.Fatal{Code: code, Detail: detail}}
}

// pauseGate blocks the scan's output callbacks while the broker has paused
// the scan. While paused the heartbeat goroutine keeps the connection alive,
// so the broker's watchdog does not kill a paused scanner.
type pauseGate struct {
	mu     sync.Mutex
	paused bool
	wake   chan struct{}
}

func newPauseGate() *pauseGate { return &pauseGate{wake: make(chan struct{})} }

// wait blocks until the scan is resumed or ctx is done. Cancelling a paused
// scan must still work, so ctx.Done unblocks with an error.
func (g *pauseGate) wait(ctx context.Context) error {
	g.mu.Lock()
	if !g.paused {
		g.mu.Unlock()
		return nil
	}
	wake := g.wake
	g.mu.Unlock()
	select {
	case <-wake:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (g *pauseGate) setPaused(paused bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if paused == g.paused {
		return
	}
	g.paused = paused
	if paused {
		g.wake = make(chan struct{})
	} else {
		close(g.wake)
	}
}
