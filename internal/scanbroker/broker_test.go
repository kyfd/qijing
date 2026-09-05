package scanbroker

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kyfd/qijing/internal/config"
	"github.com/kyfd/qijing/internal/ipcpipe"
	"github.com/kyfd/qijing/internal/model"
	"github.com/kyfd/qijing/internal/scanner"
	"github.com/kyfd/qijing/internal/scannerproc"
	"github.com/kyfd/qijing/internal/scanproto"
)

// fixtureTree builds a small authorized tree with one file.
func fixtureTree(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "note.txt"), []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	return root
}

// serveRealEngine runs the production serve loop in-process on a fresh pipe,
// so the broker is exercised end to end without spawning a binary.
func serveRealEngine(t *testing.T) string {
	t.Helper()
	listener, err := ipcpipe.Listen()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { listener.Close() })
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		_ = scannerproc.Serve(conn, scannerproc.ScannerEngine{}, "test", time.Hour)
	}()
	return listener.Name()
}

// scriptedScanner runs a hand-written server conversation for failure-path
// tests. The script takes over after the handshake has completed and the
// pipe stays open a moment afterwards so the broker can drain the frames.
func scriptedScanner(t *testing.T, script func(stream *scanproto.Conn)) string {
	t.Helper()
	listener, err := ipcpipe.Listen()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { listener.Close() })
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		stream := scanproto.NewConn(conn)
		if _, err := stream.Receive(); err != nil { // hello
			return
		}
		if err := stream.Send(scanproto.Message{Type: scanproto.TypeHelloAck, HelloAck: &scanproto.HelloAck{Version: scanproto.Version}}); err != nil {
			return
		}
		script(stream)
		time.Sleep(150 * time.Millisecond)
	}()
	return listener.Name()
}

func dialForTest(t *testing.T, pipe string) ipcpipe.Conn {
	t.Helper()
	conn, err := ipcpipe.Dial(pipe, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	return conn
}

func newBroker(t *testing.T, root string, mutate func(*Options)) *Scan {
	t.Helper()
	cfg := config.Default()
	cfg.Roots = []string{root}
	opts := Options{Config: cfg}
	if mutate != nil {
		mutate(&opts)
	}
	return New(opts)
}

func TestBrokerAssemblesCompleteScan(t *testing.T) {
	root := fixtureTree(t)
	pipe := serveRealEngine(t)
	s := newBroker(t, root, nil)

	var progressSeen bool
	s.opts.Progress = func(scanner.Progress) { progressSeen = true }

	scan, err := s.converse(context.Background(), dialForTest(t, pipe), []string{root}, "snap-test", func() {})
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if scan.Status != model.ScanStatusComplete || scan.ID == "" {
		t.Fatalf("scan = %+v", scan)
	}
	if len(scan.Entries) != 1 || scan.Entries[0].Name != "note.txt" || len(scan.Entries[0].Classes) == 0 {
		t.Fatalf("entries = %+v", scan.Entries)
	}
	if len(scan.Roots) != 1 || scan.Roots[0] != scanner.RootID(root) {
		t.Fatalf("roots = %v", scan.Roots)
	}
	if !progressSeen {
		t.Fatal("progress was not forwarded")
	}
}

func TestBrokerRejectsPathOutsideAuthorizedRoots(t *testing.T) {
	root := t.TempDir()
	other := t.TempDir()
	pipe := scriptedScanner(t, func(stream *scanproto.Conn) {
		_ = stream.Send(scanproto.Message{Type: scanproto.TypeEntries, Entries: &scanproto.EntriesBatch{Entries: []model.Entry{{
			ID: "e1", RootID: scanner.RootID(other), Path: filepath.Join(other, "x.txt"), Kind: model.KindFile,
		}}}})
	})
	s := newBroker(t, root, nil)
	_, err := s.converse(context.Background(), dialForTest(t, pipe), []string{root}, "snap-test", func() {})
	if !errors.Is(err, errViolation) {
		t.Fatalf("expected violation error, got %v", err)
	}
}

func TestBrokerRejectsUnknownRootID(t *testing.T) {
	root := t.TempDir()
	pipe := scriptedScanner(t, func(stream *scanproto.Conn) {
		_ = stream.Send(scanproto.Message{Type: scanproto.TypeEntries, Entries: &scanproto.EntriesBatch{Entries: []model.Entry{{
			ID: "e1", RootID: "invented-root-id", Path: filepath.Join(root, "x.txt"), Kind: model.KindFile,
		}}}})
	})
	s := newBroker(t, root, nil)
	_, err := s.converse(context.Background(), dialForTest(t, pipe), []string{root}, "snap-test", func() {})
	if !errors.Is(err, errViolation) {
		t.Fatalf("expected violation error, got %v", err)
	}
}

func TestBrokerRejectsRelativePath(t *testing.T) {
	root := t.TempDir()
	pipe := scriptedScanner(t, func(stream *scanproto.Conn) {
		_ = stream.Send(scanproto.Message{Type: scanproto.TypeEntries, Entries: &scanproto.EntriesBatch{Entries: []model.Entry{{
			ID: "e1", RootID: scanner.RootID(root), Path: `relative\path.txt`, Kind: model.KindFile,
		}}}})
	})
	s := newBroker(t, root, nil)
	_, err := s.converse(context.Background(), dialForTest(t, pipe), []string{root}, "snap-test", func() {})
	if !errors.Is(err, errViolation) {
		t.Fatalf("expected violation error, got %v", err)
	}
}

func TestBrokerSurvivesAbruptScannerDeath(t *testing.T) {
	root := t.TempDir()
	// The script does nothing: after the handshake the connection closes
	// immediately, like a crashed scanner process.
	pipe := scriptedScanner(t, func(stream *scanproto.Conn) {})
	s := newBroker(t, root, nil)
	_, err := s.converse(context.Background(), dialForTest(t, pipe), []string{root}, "snap-test", func() {})
	if err == nil {
		t.Fatal("expected an error from a scanner that died mid-conversation")
	}
	if errors.Is(err, errViolation) {
		t.Fatalf("a crash is not a policy violation: %v", err)
	}
}

func TestBrokerHeartbeatTimeoutTerminatesSilentScanner(t *testing.T) {
	root := t.TempDir()
	// The server completes the handshake and then goes silent, like a hung
	// scanner process.
	pipe := scriptedScanner(t, func(stream *scanproto.Conn) {
		time.Sleep(3 * time.Second)
	})
	s := newBroker(t, root, func(o *Options) {
		o.HeartbeatTimeout = 300 * time.Millisecond
	})
	start := time.Now()
	_, err := s.converse(context.Background(), dialForTest(t, pipe), []string{root}, "snap-test", func() {})
	if err == nil {
		t.Fatal("expected the heartbeat timeout to abort the scan")
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Fatalf("timeout took %v, watchdog did not fire", elapsed)
	}
}

func TestBrokerCancelStopsScanAndReportsCancelledStatus(t *testing.T) {
	root := t.TempDir()
	pipe := scriptedScanner(t, func(stream *scanproto.Conn) {
		// Wait for the broker's cancel, then report the cancelled state
		// exactly like the production serve loop would.
		for {
			message, err := stream.Receive()
			if err != nil {
				return
			}
			if message.Type == scanproto.TypeCancel {
				break
			}
		}
		_ = stream.Send(scanproto.Message{Type: scanproto.TypeDone, Done: &scanproto.Done{
			ScanID: "scan-1", Status: model.ScanStatusCancelled, Partial: true,
			TruncationReason: "cancelled", Roots: []string{scanner.RootID(root)},
		}})
	})
	s := newBroker(t, root, func(o *Options) {
		o.CancelGrace = 2 * time.Second
	})
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(300 * time.Millisecond)
		cancel()
	}()
	scan, err := s.converse(ctx, dialForTest(t, pipe), []string{root}, "snap-test", func() {})
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if scan.Status != model.ScanStatusCancelled {
		t.Fatalf("status = %q, want cancelled", scan.Status)
	}
}

func TestBrokerEnforcesEntryBudgetAgainstRogueScanner(t *testing.T) {
	root := t.TempDir()
	pipe := scriptedScanner(t, func(stream *scanproto.Conn) {
		entries := make([]model.Entry, 0, 5000)
		for i := 0; i < 5000; i++ {
			entries = append(entries, model.Entry{
				ID: "e", RootID: scanner.RootID(root), Path: filepath.Join(root, "x.txt"), Kind: model.KindFile,
			})
		}
		_ = stream.Send(scanproto.Message{Type: scanproto.TypeEntries, Entries: &scanproto.EntriesBatch{Entries: entries}})
	})
	s := newBroker(t, root, func(o *Options) {
		o.Config.MaxEntries = 100
	})
	_, err := s.converse(context.Background(), dialForTest(t, pipe), []string{root}, "snap-test", func() {})
	if !errors.Is(err, errViolation) {
		t.Fatalf("expected budget violation, got %v", err)
	}
}

func TestStderrTailKeepsOnlyTheLastBytes(t *testing.T) {
	tail := &stderrTail{limit: 8}
	if _, err := tail.Write([]byte("abcdefghij")); err != nil {
		t.Fatal(err)
	}
	if got := string(tail.buf); got != "cdefghij" {
		t.Fatalf("tail = %q, want cdefghij", got)
	}
}

func TestStderrTailTextIsSanitized(t *testing.T) {
	tail := &stderrTail{limit: 4096}
	payload := "crash at C:\\Users\\alice\\secret.txt after 12 entries"
	if _, err := tail.Write([]byte(payload)); err != nil {
		t.Fatal(err)
	}
	got := tail.Text()
	if strings.Contains(got, "alice") || strings.Contains(got, "C:\\") {
		t.Fatalf("unsanitized stderr text: %q", got)
	}
	if !strings.Contains(got, "crash at") || !strings.Contains(got, "<path>") {
		t.Fatalf("diagnostic lost: %q", got)
	}
}

func TestWithChildStderrLeavesSuccessAndEmptyTailsAlone(t *testing.T) {
	base := errors.New("scanner connection: EOF")
	if got := withChildStderr(nil, "anything"); got != nil {
		t.Fatalf("nil error must stay nil, got %v", got)
	}
	if got := withChildStderr(base, ""); got != base {
		t.Fatalf("empty diagnostic must not wrap, got %v", got)
	}
	wrapped := withChildStderr(base, "protocol violation: outgoing frame of 10 bytes exceeds the limit")
	if !errors.Is(wrapped, base) {
		t.Fatalf("wrapped error lost identity: %v", wrapped)
	}
	if !strings.Contains(wrapped.Error(), "scanner: protocol violation") {
		t.Fatalf("wrapped error missing diagnostic: %v", wrapped)
	}
}
