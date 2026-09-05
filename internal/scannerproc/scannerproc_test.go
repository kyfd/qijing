package scannerproc

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/kyfd/qijing/internal/classify"
	"github.com/kyfd/qijing/internal/config"
	"github.com/kyfd/qijing/internal/ipcpipe"
	"github.com/kyfd/qijing/internal/model"
	"github.com/kyfd/qijing/internal/scanner"
	"github.com/kyfd/qijing/internal/scanproto"
)

func now() time.Time { return time.Now() }

// Tests do not need the production drain window; a short grace keeps the
// suite fast while still exercising the hold-open path.
func init() { closeGrace = 100 * time.Millisecond }

// fakeEngine records what the serve loop asked for and replays scripted
// behaviour.
type fakeEngine struct {
	cfg        config.Config
	snapshotID string
	result     model.Scan
	err        error
	block      chan struct{} // when set, Run blocks until closed or ctx done
	batches    int
}

func (f *fakeEngine) Run(ctx context.Context, cfg config.Config, snapshotID string, progress func(scanner.Progress), batch func([]model.Entry) error) (model.Scan, error) {
	f.cfg = cfg
	f.snapshotID = snapshotID
	progress(scanner.Progress{Phase: scanner.PhaseTraversing, ObservedEntries: 1})
	entry := model.Entry{ID: "e1", RootID: scanner.RootID(cfg.Roots[0]), Path: cfg.Roots[0] + `\a.txt`, Kind: model.KindFile, Size: 3, ModTime: now()}
	classify.One(&entry, now(), cfg)
	if err := batch([]model.Entry{entry}); err != nil {
		return model.Scan{}, err
	}
	f.batches++
	if f.block != nil {
		select {
		case <-f.block:
		case <-ctx.Done():
			return model.Scan{ID: snapshotID, Status: model.ScanStatusCancelled, Partial: true, TruncationReason: "cancelled", Roots: cfg.Roots}, ctx.Err()
		}
	}
	progress(scanner.Progress{Phase: scanner.PhaseRelations, ObservedEntries: 2})
	if f.err != nil {
		return model.Scan{}, f.err
	}
	if f.result.ID != "" {
		f.result.Roots = []string{cfg.Roots[0]}
		return f.result, nil
	}
	f.result = model.Scan{ID: snapshotID, Status: model.ScanStatusComplete, Roots: []string{cfg.Roots[0]}, ErrorCount: 0}
	return f.result, nil
}

// serveOnPipe runs Serve against a fresh named pipe and returns the client
// side of the conversation plus a channel carrying Serve's outcome.
func serveOnPipe(t *testing.T, engine Engine, heartbeat time.Duration) (*scanproto.Conn, <-chan error) {
	t.Helper()
	listener, err := ipcpipe.Listen()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { listener.Close() })
	served := make(chan error, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			served <- err
			return
		}
		served <- Serve(conn, engine, "test", heartbeat)
	}()
	client, err := ipcpipe.Dial(listener.Name(), 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { client.Close() })
	return scanproto.NewConn(client), served
}

// handshake performs the broker side of the version handshake.
func handshake(t *testing.T, client *scanproto.Conn, version int, jobID string) {
	t.Helper()
	if err := client.Send(scanproto.Message{Type: scanproto.TypeHello, Hello: &scanproto.Hello{Version: version, JobID: jobID}}); err != nil {
		t.Fatal(err)
	}
	ack, err := client.Receive()
	if err != nil {
		t.Fatal(err)
	}
	if ack.Type != scanproto.TypeHelloAck || ack.HelloAck.Version != scanproto.Version {
		t.Fatalf("handshake = %+v", ack)
	}
}

func sendScan(t *testing.T, client *scanproto.Conn, jobID string, roots []string) {
	t.Helper()
	if err := client.Send(scanproto.Message{Type: scanproto.TypeScan, Scan: &scanproto.ScanRequest{JobID: jobID, SnapshotID: "snapshot-1", Roots: roots, Options: scanproto.OptionsFromConfig(config.Default())}}); err != nil {
		t.Fatal(err)
	}
}

func TestServeStreamsBatchesProgressAndDone(t *testing.T) {
	engine := &fakeEngine{}
	client, served := serveOnPipe(t, engine, time.Hour)
	handshake(t, client, scanproto.Version, "job-1")
	sendScan(t, client, "job-1", []string{t.TempDir()})

	var sawProgress bool
	var entries []model.Entry
	var relations int
	var done *scanproto.Done
	for {
		message, err := client.Receive()
		if err != nil {
			t.Fatalf("receive: %v", err)
		}
		switch message.Type {
		case scanproto.TypeProgress:
			sawProgress = true
		case scanproto.TypeEntries:
			entries = append(entries, message.Entries.Entries...)
		case scanproto.TypeRelations:
			relations = len(message.Relations.Relations)
		case scanproto.TypeDone:
			done = message.Done
		case scanproto.TypeHeartbeat:
			// tolerated
		default:
			t.Fatalf("unexpected %s", message.Type)
		}
		if done != nil {
			break
		}
	}
	if !sawProgress {
		t.Fatal("no progress message received")
	}
	if len(entries) != 1 || len(entries[0].Classes) == 0 {
		t.Fatalf("entries must be classified: %+v", entries)
	}
	if relations != 0 {
		t.Fatalf("relations = %d", relations)
	}
	if done == nil || done.ScanID != "snapshot-1" || done.Status != model.ScanStatusComplete || len(done.Roots) != 1 {
		t.Fatalf("done = %+v", done)
	}
	if err := <-served; err != nil {
		t.Fatalf("serve: %v", err)
	}
	if engine.cfg.MaxEntries != config.Default().MaxEntries {
		t.Fatalf("engine config = %+v", engine.cfg)
	}
	if engine.snapshotID == "" {
		t.Fatal("the scan request must carry the broker's snapshot id")
	}
}

func TestServeRejectsProtocolVersionMismatch(t *testing.T) {
	client, served := serveOnPipe(t, &fakeEngine{}, time.Hour)
	if err := client.Send(scanproto.Message{Type: scanproto.TypeHello, Hello: &scanproto.Hello{Version: scanproto.Version + 7, JobID: "j"}}); err != nil {
		t.Fatal(err)
	}
	message, err := client.Receive()
	if err != nil {
		t.Fatalf("receive: %v", err)
	}
	if message.Type != scanproto.TypeFatal || message.Fatal.Code != "protocol_version" {
		t.Fatalf("expected fatal protocol_version, got %+v", message)
	}
	if err := <-served; err == nil || !strings.Contains(err.Error(), "hello") {
		t.Fatalf("serve error = %v", err)
	}
}

func TestServeRejectsInvalidScanConfiguration(t *testing.T) {
	client, served := serveOnPipe(t, &fakeEngine{}, time.Hour)
	handshake(t, client, scanproto.Version, "job-1")
	// An empty root list must be rejected by config validation before any
	// filesystem access happens.
	if err := client.Send(scanproto.Message{Type: scanproto.TypeScan, Scan: &scanproto.ScanRequest{JobID: "job-1", Options: scanproto.OptionsFromConfig(config.Default())}}); err != nil {
		t.Fatal(err)
	}
	message, err := client.Receive()
	if err != nil {
		t.Fatal(err)
	}
	if message.Type != scanproto.TypeFatal || message.Fatal.Code != "invalid_request" {
		t.Fatalf("expected fatal invalid_request, got %+v", message)
	}
	if err := <-served; err == nil {
		t.Fatal("serve must report the rejected request")
	}
}

func TestServeForwardsCancelAndReportsCancelledDone(t *testing.T) {
	blocked := make(chan struct{})
	engine := &fakeEngine{block: blocked, result: model.Scan{ID: "snapshot-1", Roots: nil}}
	client, served := serveOnPipe(t, engine, time.Hour)
	handshake(t, client, scanproto.Version, "job-1")
	sendScan(t, client, "job-1", []string{t.TempDir()})

	// Wait until the engine started, then cancel. The engine stays blocked;
	// only the cancellation may release it, which is exactly the production
	// semantics the broker relies on.
	if err := client.Send(scanproto.Message{Type: scanproto.TypeCancel, Cancel: &scanproto.Cancel{JobID: "job-1"}}); err != nil {
		t.Fatal(err)
	}

	done := readUntilDone(t, client)
	if done.Status != model.ScanStatusCancelled || !done.Partial {
		t.Fatalf("done should reflect cancellation: %+v", done)
	}
	if err := <-served; err != nil {
		t.Fatalf("serve: %v", err)
	}
}

func TestServeReportsFatalOnEngineFailure(t *testing.T) {
	engine := &fakeEngine{err: errors.New("engine exploded")}
	client, served := serveOnPipe(t, engine, time.Hour)
	handshake(t, client, scanproto.Version, "job-1")
	sendScan(t, client, "job-1", []string{t.TempDir()})
	message := readUntil(t, client, scanproto.TypeFatal)
	if message.Fatal == nil || message.Fatal.Code != "scan_failed" {
		t.Fatalf("expected fatal scan_failed, got %+v", message)
	}
	if err := <-served; err == nil {
		t.Fatal("serve must report the engine failure")
	}
}

// readUntil consumes frames until one of the wanted type arrives.
func readUntil(t *testing.T, client *scanproto.Conn, want string) scanproto.Message {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		message, err := client.Receive()
		if err != nil {
			t.Fatalf("receive: %v", err)
		}
		if message.Type == want {
			return message
		}
		select {
		case <-deadline:
			t.Fatalf("no %s message arrived", want)
		default:
		}
	}
}

func TestServeSendsHeartbeatsWhenIdle(t *testing.T) {
	blocked := make(chan struct{})
	engine := &fakeEngine{block: blocked}
	client, _ := serveOnPipe(t, engine, 50*time.Millisecond)
	handshake(t, client, scanproto.Version, "job-1")
	sendScan(t, client, "job-1", []string{t.TempDir()})

	deadline := time.After(3 * time.Second)
	for {
		message, err := client.Receive()
		if err != nil {
			t.Fatalf("receive: %v", err)
		}
		if message.Type == scanproto.TypeHeartbeat {
			close(blocked)
			return
		}
		select {
		case <-deadline:
			t.Fatal("no heartbeat within the deadline")
		default:
		}
	}
}

func TestServeRejectsUnexpectedClientMessages(t *testing.T) {
	engine := &fakeEngine{block: make(chan struct{})}
	client, served := serveOnPipe(t, engine, time.Hour)
	handshake(t, client, scanproto.Version, "job-1")
	sendScan(t, client, "job-1", []string{t.TempDir()})
	// A second scan request is a protocol violation: the loop must stop
	// instead of serving arbitrary sequences.
	if err := client.Send(scanproto.Message{Type: scanproto.TypeScan, Scan: &scanproto.ScanRequest{JobID: "job-1"}}); err != nil {
		t.Fatal(err)
	}
	if err := <-served; err == nil {
		t.Fatal("serve must fail on unexpected messages")
	}
}

func TestServeStreamsLargeRelationSetInMultipleFrames(t *testing.T) {
	// Hashing-on scans can derive far more relations than fit in one 8 MiB
	// frame. Sending them unchunked used to abort the scanner after a
	// completed traversal (observed as "scanner connection: EOF" on the
	// broker). The serve loop must split and still deliver every relation.
	const n = 200_000
	relations := make([]model.Relation, n)
	for i := range relations {
		relations[i] = model.Relation{
			FromID: fmt.Sprintf("from-%06d", i),
			ToID:   fmt.Sprintf("to-%06d", i),
			Type:   model.RelationDuplicate,
		}
	}
	engine := &fakeEngine{result: model.Scan{
		ID:        "snapshot-1",
		Status:    model.ScanStatusComplete,
		Relations: relations,
	}}
	client, served := serveOnPipe(t, engine, time.Hour)
	handshake(t, client, scanproto.Version, "job-1")
	sendScan(t, client, "job-1", []string{t.TempDir()})

	var got []model.Relation
	frames := 0
	var done *scanproto.Done
	for {
		message, err := client.Receive()
		if err != nil {
			t.Fatalf("receive after %d relation frames / %d relations: %v", frames, len(got), err)
		}
		switch message.Type {
		case scanproto.TypeRelations:
			got = append(got, message.Relations.Relations...)
			frames++
		case scanproto.TypeDone:
			done = message.Done
		case scanproto.TypeProgress, scanproto.TypeHeartbeat, scanproto.TypeEntries:
		default:
			t.Fatalf("unexpected %s", message.Type)
		}
		if done != nil {
			break
		}
	}
	if frames < 2 {
		t.Fatalf("relation frames = %d, want at least 2", frames)
	}
	if len(got) != n {
		t.Fatalf("received %d relations, want %d", len(got), n)
	}
	if got[0].FromID != relations[0].FromID || got[n-1].FromID != relations[n-1].FromID {
		t.Fatalf("relation order corrupted: first=%+v last=%+v", got[0], got[n-1])
	}
	if done.ScanID != "snapshot-1" || done.Status != model.ScanStatusComplete {
		t.Fatalf("done = %+v", done)
	}
	if err := <-served; err != nil {
		t.Fatalf("serve: %v", err)
	}
}

func TestServeReportsFatalWhenASingleUnsplittableItemCannotFit(t *testing.T) {
	// A relation whose encoded form cannot fit even as a one-item frame
	// must fail closed, never be dropped. Dropping it would make the
	// snapshot look complete while missing a derived link.
	engine := &fakeEngine{result: model.Scan{
		ID:     "snapshot-1",
		Status: model.ScanStatusComplete,
		Relations: []model.Relation{{
			FromID: strings.Repeat("f", scanproto.MaxFrameBytes),
			ToID:   "to",
			Type:   model.RelationDuplicate,
		}},
	}}
	client, served := serveOnPipe(t, engine, time.Hour)
	handshake(t, client, scanproto.Version, "job-1")
	sendScan(t, client, "job-1", []string{t.TempDir()})

	deadline := time.After(5 * time.Second)
	for {
		select {
		case err := <-served:
			if err == nil {
				t.Fatal("serve must fail when a single relation cannot fit a frame")
			}
			if !errors.Is(err, scanproto.ErrFrameTooLarge) {
				t.Fatalf("serve error = %v, want ErrFrameTooLarge", err)
			}
			return
		case <-deadline:
			t.Fatal("serve did not fail on an unsplittable relation")
		default:
			_, _ = client.Receive()
		}
	}
}

func readUntilDone(t *testing.T, client *scanproto.Conn) *scanproto.Done {
	t.Helper()
	for {
		message, err := client.Receive()
		if err != nil {
			t.Fatalf("receive: %v", err)
		}
		if message.Type == scanproto.TypeDone {
			return message.Done
		}
	}
}
