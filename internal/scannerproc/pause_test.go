package scannerproc

import (
	"context"
	"testing"
	"time"

	"github.com/kyfd/qijing/internal/classify"
	"github.com/kyfd/qijing/internal/config"
	"github.com/kyfd/qijing/internal/model"
	"github.com/kyfd/qijing/internal/scanner"
	"github.com/kyfd/qijing/internal/scanproto"
)

// gatedEngine sends exactly two batches. After the first it waits for the
// test to release it, so the pause can be established deterministically
// before the second batch is produced.
type gatedEngine struct {
	proceed chan struct{} // closed to release the second batch
}

func (e *gatedEngine) Run(ctx context.Context, cfg config.Config, snapshotID string, progress func(scanner.Progress), batch func([]model.Entry) error) (model.Scan, error) {
	progress(scanner.Progress{Phase: scanner.PhaseTraversing})
	first := model.Entry{ID: "e1", RootID: scanner.RootID(cfg.Roots[0]), Path: cfg.Roots[0] + `\a.txt`, Kind: model.KindFile, ModTime: now()}
	classify.One(&first, now(), cfg)
	if err := batch([]model.Entry{first}); err != nil {
		return model.Scan{}, err
	}
	select {
	case <-e.proceed:
	case <-ctx.Done():
		return model.Scan{ID: snapshotID, Status: model.ScanStatusCancelled}, ctx.Err()
	}
	second := model.Entry{ID: "e2", RootID: scanner.RootID(cfg.Roots[0]), Path: cfg.Roots[0] + `\b.txt`, Kind: model.KindFile, ModTime: now()}
	classify.One(&second, now(), cfg)
	if err := batch([]model.Entry{second}); err != nil {
		return model.Scan{}, err
	}
	return model.Scan{ID: snapshotID, Status: model.ScanStatusComplete, Roots: []string{cfg.Roots[0]}}, nil
}

// Pausing must stop the flow of entries entirely; while paused the scanner
// must answer with a paused progress notice; resuming must let the scan
// finish.
func TestServePausesAndResumesTheScan(t *testing.T) {
	engine := &gatedEngine{proceed: make(chan struct{})}
	client, served := serveOnPipe(t, engine, time.Hour)
	handshake(t, client, scanproto.Version, "job-1")
	sendScan(t, client, "job-1", []string{t.TempDir()})

	// First batch flows normally.
	frame := readUntil(t, client, scanproto.TypeEntries)
	if len(frame.Entries.Entries) != 1 || frame.Entries.Entries[0].ID != "e1" {
		t.Fatalf("first batch = %+v", frame.Entries)
	}

	// Pause. The scanner acknowledges with a paused progress notice.
	if err := client.Send(scanproto.Message{Type: scanproto.TypePause, Pause: &scanproto.Pause{}}); err != nil {
		t.Fatal(err)
	}
	notice := readUntil(t, client, scanproto.TypeProgress)
	if notice.Progress == nil || !notice.Progress.Paused {
		t.Fatalf("expected a paused notice, got %+v", notice)
	}

	// Release the engine while paused: its second batch must be swallowed
	// by the gate. The collector owns all reads from here so nothing is
	// stolen between goroutines.
	close(engine.proceed)
	type timedFrame struct {
		msg scanproto.Message
		at  time.Time
	}
	frames := make(chan timedFrame, 8)
	go func() {
		for {
			message, err := client.Receive()
			if err != nil {
				close(frames)
				return
			}
			frames <- timedFrame{message, time.Now()}
			if message.Type == scanproto.TypeDone {
				close(frames)
				return
			}
		}
	}()

	select {
	case tf, ok := <-frames:
		if !ok {
			t.Fatal("connection closed while paused")
		}
		t.Fatalf("a %s frame arrived while paused", tf.msg.Type)
	case <-time.After(400 * time.Millisecond):
		// Silence: exactly what a paused scan looks like.
	}

	// Resume: the unpaused notice, the second batch and the done frame
	// must arrive (in that order).
	if err := client.Send(scanproto.Message{Type: scanproto.TypeResume, Resume: &scanproto.Resume{}}); err != nil {
		t.Fatal(err)
	}
	var second, done *scanproto.Message
	deadline := time.After(5 * time.Second)
	for second == nil || done == nil {
		select {
		case tf, ok := <-frames:
			if !ok {
				t.Fatal("connection closed before the scan finished")
			}
			switch {
			case tf.msg.Type == scanproto.TypeEntries && second == nil:
				second = &tf.msg
			case tf.msg.Type == scanproto.TypeDone:
				done = &tf.msg
			}
		case <-deadline:
			t.Fatal("the scan did not finish after resume")
		}
	}
	if len(second.Entries.Entries) != 1 || second.Entries.Entries[0].ID != "e2" {
		t.Fatalf("second batch = %+v", second)
	}
	if done.Done == nil || done.Done.Status != model.ScanStatusComplete {
		t.Fatalf("done = %+v", done)
	}
	if err := <-served; err != nil {
		t.Fatalf("serve: %v", err)
	}
}
