package scanproto

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/kyfd/qijing/internal/config"
	"github.com/kyfd/qijing/internal/model"
)

func TestRoundTripCarriesEveryPayload(t *testing.T) {
	messages := []Message{
		{Type: TypeHello, Hello: &Hello{Version: Version, JobID: "job-1"}},
		{Type: TypeScan, Scan: &ScanRequest{JobID: "job-1", Roots: []string{`C:\data`}, Options: OptionsFromConfig(config.Default())}},
		{Type: TypeCancel, Cancel: &Cancel{JobID: "job-1"}},
		{Type: TypeHelloAck, HelloAck: &HelloAck{Version: Version, ScannerVersion: "1.2.3"}},
		{Type: TypeProgress, Progress: &Progress{Phase: "traversing", ObservedEntries: 42, Elapsed: 3 * time.Second}},
		{Type: TypeEntries, Entries: &EntriesBatch{Entries: []model.Entry{{ID: "e1", RootID: "r1", Path: `C:\data\a.txt`, Kind: model.KindFile, Size: 3}}}},
		{Type: TypeRelations, Relations: &RelationsBatch{Relations: []model.Relation{{FromID: "e1", ToID: "e2", Type: model.RelationDuplicate}}}},
		{Type: TypeDone, Done: &Done{ScanID: "s1", Status: model.ScanStatusComplete, StartedAt: time.Unix(1, 0), EndedAt: time.Unix(2, 0), Roots: []string{`C:\data`}, ErrorCount: 2, Errors: []string{"boom"}}},
		{Type: TypeHeartbeat},
		{Type: TypeFatal, Fatal: &Fatal{Code: "scan_failed", Detail: "boom"}},
	}
	var buffer bytes.Buffer
	conn := NewConn(&buffer)
	for _, sent := range messages {
		buffer.Reset()
		if err := conn.Send(sent); err != nil {
			t.Fatalf("send %s: %v", sent.Type, err)
		}
		received, err := conn.Receive()
		if err != nil {
			t.Fatalf("receive %s: %v", sent.Type, err)
		}
		if received.Type != sent.Type {
			t.Fatalf("type = %q, want %q", received.Type, sent.Type)
		}
		switch sent.Type {
		case TypeHello:
			if received.Hello.Version != sent.Hello.Version || received.Hello.JobID != sent.Hello.JobID {
				t.Fatalf("hello = %+v", received.Hello)
			}
		case TypeEntries:
			if len(received.Entries.Entries) != 1 || received.Entries.Entries[0].Path != `C:\data\a.txt` {
				t.Fatalf("entries = %+v", received.Entries)
			}
		case TypeDone:
			if received.Done.ScanID != "s1" || received.Done.ErrorCount != 2 {
				t.Fatalf("done = %+v", received.Done)
			}
		case TypeProgress:
			if received.Progress.ObservedEntries != 42 || received.Progress.Elapsed != 3*time.Second {
				t.Fatalf("progress = %+v", received.Progress)
			}
		case TypeFatal:
			if received.Fatal.Code != "scan_failed" {
				t.Fatalf("fatal = %+v", received.Fatal)
			}
		}
	}
}

func TestReceiveRejectsOversizedFrames(t *testing.T) {
	huge := make([]byte, 8)
	binary.BigEndian.PutUint32(huge, MaxFrameBytes+1)
	conn := NewConn(readOnly{data: huge})
	if _, err := conn.Receive(); err == nil || !strings.Contains(err.Error(), "out of range") {
		t.Fatalf("expected size limit rejection, got %v", err)
	}
}

func TestReceiveRejectsZeroLengthFrames(t *testing.T) {
	conn := NewConn(readOnly{data: frame(`{"type":"hello","hello":{}}`)})
	if _, err := conn.Receive(); !errors.Is(err, ErrProtocol) {
		t.Fatalf("expected protocol error, got %v", err)
	}
}

func TestReceiveRejectsTypePayloadMismatch(t *testing.T) {
	for _, body := range []string{
		`{"type":"hello"}`,                          // missing payload
		`{"type":"hello","hello":{},"cancel":{}}`,   // two payloads
		`{"type":"hello","cancel":{}}`,              // payload under wrong type
		`{"type":"heartbeat","fatal":{"code":"x"}}`, // heartbeat with payload
		`{"type":"mystery"}`,                        // unknown type
	} {
		conn := NewConn(readOnly{data: frame(body)})
		if _, err := conn.Receive(); !errors.Is(err, ErrProtocol) {
			t.Fatalf("body %s: expected protocol error, got %v", body, err)
		}
	}
}

func TestSendRejectsInvalidOutgoingMessages(t *testing.T) {
	var buffer bytes.Buffer
	conn := NewConn(&buffer)
	if err := conn.Send(Message{Type: TypeHello}); !errors.Is(err, ErrProtocol) {
		t.Fatalf("expected outgoing validation to reject hello without payload, got %v", err)
	}
	if buffer.Len() != 0 {
		t.Fatal("invalid messages must not reach the wire")
	}
}

func TestSendRejectsOversizedOutgoingFrameBeforeWriting(t *testing.T) {
	// A single unsplittable payload must fail closed: dropping it would
	// silently corrupt the snapshot. The check happens before any bytes
	// are written, so a retry on a smaller chunk is still safe.
	var buffer bytes.Buffer
	conn := NewConn(&buffer)
	huge := strings.Repeat("x", MaxFrameBytes)
	err := conn.Send(Message{Type: TypeFatal, Fatal: &Fatal{Code: "scan_failed", Detail: huge}})
	if !errors.Is(err, ErrFrameTooLarge) {
		t.Fatalf("expected ErrFrameTooLarge, got %v", err)
	}
	if buffer.Len() != 0 {
		t.Fatalf("oversized frames must not reach the wire, wrote %d bytes", buffer.Len())
	}
}

func TestSendRelationsSplitsOversizedBatchAndArrivesWhole(t *testing.T) {
	// A relation is small, so a few tens of thousands already exceed the
	// 8 MiB frame. The sender must split rather than abort, and every
	// relation must still arrive, in order, with no duplicates.
	const n = 200_000
	relations := make([]model.Relation, n)
	for i := range relations {
		relations[i] = model.Relation{
			FromID: fmt.Sprintf("from-%06d", i),
			ToID:   fmt.Sprintf("to-%06d", i),
			Type:   model.RelationDuplicate,
		}
	}
	var buffer bytes.Buffer
	sender := NewConn(&buffer)
	if err := sender.SendRelations(relations); err != nil {
		t.Fatalf("SendRelations: %v", err)
	}
	if buffer.Len() <= 4+MaxFrameBytes {
		t.Fatalf("expected more than one frame, got %d bytes", buffer.Len())
	}

	receiver := NewConn(&buffer)
	var got []model.Relation
	frames := 0
	for buffer.Len() > 0 {
		message, err := receiver.Receive()
		if err != nil {
			t.Fatalf("receive frame %d: %v", frames, err)
		}
		if message.Type != TypeRelations || message.Relations == nil {
			t.Fatalf("frame %d: type = %s", frames, message.Type)
		}
		got = append(got, message.Relations.Relations...)
		frames++
	}
	if frames < 2 {
		t.Fatalf("frames = %d, want at least 2", frames)
	}
	if len(got) != n {
		t.Fatalf("received %d relations, want %d", len(got), n)
	}
	for i, rel := range got {
		if rel.FromID != relations[i].FromID || rel.ToID != relations[i].ToID {
			t.Fatalf("relation %d = %+v, want %+v", i, rel, relations[i])
		}
	}
}

func TestSendEntriesSplitsLongPathBatchAndArrivesWhole(t *testing.T) {
	// A modest count of very long Unicode paths exceeds the frame even
	// when the same count of short paths would fit, which is why chunking
	// is size-adaptive rather than a guessed item count.
	const n = 5000
	path := `C:\data\` + strings.Repeat("很长的目录名", 400) + `\file.txt`
	entries := make([]model.Entry, n)
	for i := range entries {
		entries[i] = model.Entry{
			ID:   fmt.Sprintf("e%d", i),
			Path: path,
			Kind: model.KindFile,
			Size: int64(i),
		}
	}
	var buffer bytes.Buffer
	sender := NewConn(&buffer)
	if err := sender.SendEntries(entries); err != nil {
		t.Fatalf("SendEntries: %v", err)
	}

	receiver := NewConn(&buffer)
	var got []model.Entry
	frames := 0
	for buffer.Len() > 0 {
		message, err := receiver.Receive()
		if err != nil {
			t.Fatalf("receive frame %d: %v", frames, err)
		}
		if message.Type != TypeEntries || message.Entries == nil {
			t.Fatalf("frame %d: type = %s", frames, message.Type)
		}
		got = append(got, message.Entries.Entries...)
		frames++
	}
	if frames < 2 {
		t.Fatalf("frames = %d, want at least 2", frames)
	}
	if len(got) != n {
		t.Fatalf("received %d entries, want %d", len(got), n)
	}
	for i, entry := range got {
		if entry.ID != entries[i].ID || entry.Path != path || entry.Size != int64(i) {
			t.Fatalf("entry %d = %+v", i, entry)
		}
	}
}

func TestSendChunkedFailsClosedOnUnsplittableItem(t *testing.T) {
	// A single item that cannot fit must be reported, never dropped.
	// Dropping it would make the snapshot look complete while missing data.
	err := sendChunked(1, func(start, end int) error {
		if start != 0 || end != 1 {
			t.Fatalf("range = [%d, %d)", start, end)
		}
		return fmt.Errorf("%w: item too large", ErrFrameTooLarge)
	})
	if !errors.Is(err, ErrFrameTooLarge) {
		t.Fatalf("expected ErrFrameTooLarge, got %v", err)
	}
}

func TestSendChunkedDoesNotRetryNonSizeErrors(t *testing.T) {
	calls := 0
	want := errors.New("pipe closed")
	err := sendChunked(8, func(start, end int) error {
		calls++
		return want
	})
	if !errors.Is(err, want) {
		t.Fatalf("got %v, want %v", err, want)
	}
	if calls != 1 {
		t.Fatalf("retried a non-size error: %d calls", calls)
	}
}

func TestSendChunkedEmptyBatchStillSendsOneFrame(t *testing.T) {
	calls := 0
	if err := sendChunked(0, func(start, end int) error {
		calls++
		if start != 0 || end != 0 {
			t.Fatalf("empty range = [%d, %d)", start, end)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("empty batch must still send one frame, got %d calls", calls)
	}
}

func TestSendEntriesEmptyBatchWritesOneFrame(t *testing.T) {
	var buffer bytes.Buffer
	sender := NewConn(&buffer)
	if err := sender.SendEntries(nil); err != nil {
		t.Fatal(err)
	}
	receiver := NewConn(&buffer)
	message, err := receiver.Receive()
	if err != nil {
		t.Fatal(err)
	}
	if message.Type != TypeEntries || message.Entries == nil || len(message.Entries.Entries) != 0 {
		t.Fatalf("empty entries frame = %+v", message)
	}
	if buffer.Len() != 0 {
		t.Fatalf("expected exactly one frame, leftover %d bytes", buffer.Len())
	}
}

func TestSendRelationsEmptyBatchWritesOneFrame(t *testing.T) {
	var buffer bytes.Buffer
	sender := NewConn(&buffer)
	if err := sender.SendRelations(nil); err != nil {
		t.Fatal(err)
	}
	receiver := NewConn(&buffer)
	message, err := receiver.Receive()
	if err != nil {
		t.Fatal(err)
	}
	if message.Type != TypeRelations || message.Relations == nil || len(message.Relations.Relations) != 0 {
		t.Fatalf("empty relations frame = %+v", message)
	}
	if buffer.Len() != 0 {
		t.Fatalf("expected exactly one frame, leftover %d bytes", buffer.Len())
	}
}

// readOnly wraps bytes as an io.ReadWriter whose writes are discarded, which
// is enough to drive the receive path in isolation.
type readOnly struct{ data []byte }

func (r readOnly) Read(p []byte) (int, error) {
	if len(r.data) == 0 {
		return 0, io.EOF
	}
	n := copy(p, r.data)
	r.data = r.data[n:]
	return n, nil
}

func (readOnly) Write(p []byte) (int, error) { return len(p), nil }

// frame length-prefixes a JSON body exactly like the wire format does.
func frame(body string) []byte {
	out := make([]byte, 4, 4+len(body))
	binary.BigEndian.PutUint32(out, uint32(len(body)))
	return append(out, body...)
}
