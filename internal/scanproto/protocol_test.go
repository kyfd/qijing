package scanproto

import (
	"bytes"
	"encoding/binary"
	"errors"
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
