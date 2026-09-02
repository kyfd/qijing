package scanbroker

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kyfd/qijing/internal/model"
	"github.com/kyfd/qijing/internal/scanproto"
)

// recordingSink captures the lifecycle the broker drives.
type recordingSink struct {
	begin          []string
	batches        [][]model.Entry
	finished       []model.Scan
	abandoned      []string
	failWriteAfter int
	failWith       error
}

func (r *recordingSink) BeginStaging(ctx context.Context, snapshotID string, roots []string) error {
	r.begin = append(r.begin, snapshotID+"\x00"+strings.Join(roots, "|"))
	return nil
}

func (r *recordingSink) WriteEntries(ctx context.Context, snapshotID string, entries []model.Entry) error {
	if r.failWriteAfter >= 0 && len(r.batches) >= r.failWriteAfter {
		r.failWriteAfter = -1
		return &SinkError{Code: "low_disk", Err: r.failWith}
	}
	batch := make([]model.Entry, len(entries))
	copy(batch, entries)
	r.batches = append(r.batches, batch)
	return nil
}

func (r *recordingSink) Finalize(ctx context.Context, scan model.Scan) error {
	r.finished = append(r.finished, scan)
	return nil
}

func (r *recordingSink) Abandon(ctx context.Context, snapshotID string, reason string) error {
	r.abandoned = append(r.abandoned, snapshotID+"\x00"+reason)
	return nil
}

// TestBrokerStreamsIntoSinkLifecycle runs the real serve loop and verifies
// the exact persistence sequence: begin → batches → finalize, with the
// broker's snapshot id.
func TestBrokerStreamsIntoSinkLifecycle(t *testing.T) {
	root := fixtureTree(t)
	pipe := serveRealEngine(t)
	sink := &recordingSink{failWriteAfter: -1}
	s := newBroker(t, root, func(o *Options) { o.Sink = sink })

	scan, err := s.converse(context.Background(), dialForTest(t, pipe), []string{root}, "snap-abc", func() {})
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if len(sink.begin) != 1 || sink.begin[0] != "snap-abc\x00"+root {
		t.Fatalf("begin = %v", sink.begin)
	}
	if len(sink.batches) == 0 || len(sink.batches[0]) == 0 {
		t.Fatalf("no batches streamed: %d", len(sink.batches))
	}
	for _, batch := range sink.batches {
		for _, entry := range batch {
			if entry.Name == "" {
				t.Fatalf("unvalidated entry in the sink: %+v", entry)
			}
		}
	}
	if len(sink.finished) != 1 || sink.finished[0].ID != "snap-abc" || sink.finished[0].Status != model.ScanStatusComplete {
		t.Fatalf("finalize = %+v", sink.finished)
	}
	if len(sink.abandoned) != 0 {
		t.Fatalf("a successful scan must not abandon: %v", sink.abandoned)
	}
	if scan.ID != "snap-abc" {
		t.Fatalf("scan id = %q", scan.ID)
	}
}

// A sink failure mid-stream must abandon the snapshot and fail the scan.
func TestBrokerAbandonsSnapshotWhenSinkFails(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "many"), 0o700); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2000; i++ {
		name := filepath.Join(root, "many", "f"+itoa(i)+".tmp")
		if err := os.WriteFile(name, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	pipe := serveRealEngine(t)
	sink := &recordingSink{failWriteAfter: 2, failWith: ErrDiskSpaceForTests}
	s := newBroker(t, root, func(o *Options) { o.Sink = sink })

	_, err := s.converse(context.Background(), dialForTest(t, pipe), []string{root}, "snap-fail", func() {})
	if err == nil {
		t.Fatal("a sink failure must fail the scan")
	}
	if len(sink.abandoned) != 1 || sink.abandoned[0] != "snap-fail\x00low_disk" {
		t.Fatalf("abandon = %v", sink.abandoned)
	}
	if len(sink.finished) != 0 {
		t.Fatalf("a failed scan must not finalize: %v", sink.finished)
	}
}

// A scanner that dies mid-conversation fails the scan and the staging
// snapshot ends up abandoned with the generic error code.
func TestBrokerAbandonsStagingOnScannerCrash(t *testing.T) {
	root := t.TempDir()
	pipe := scriptedScanner(t, func(stream *scanproto.Conn) {})
	sink := &recordingSink{failWriteAfter: -1}
	s := newBroker(t, root, func(o *Options) { o.Sink = sink })

	if _, err := s.converse(context.Background(), dialForTest(t, pipe), []string{root}, "snap-crash", func() {}); err == nil {
		t.Fatal("expected the crash to fail the scan")
	}
	if len(sink.abandoned) != 1 || sink.abandoned[0] != "snap-crash\x00error" {
		t.Fatalf("abandon = %v", sink.abandoned)
	}
	if len(sink.finished) != 0 {
		t.Fatalf("a crashed scan must not finalize: %v", sink.finished)
	}
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	digits := ""
	for i > 0 {
		digits = string(rune('0'+i%10)) + digits
		i /= 10
	}
	return digits
}

// ErrDiskSpaceForTests stands in for the application-layer disk guard.
var ErrDiskSpaceForTests = errors.New("disk space below the safety threshold")
