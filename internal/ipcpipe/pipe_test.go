package ipcpipe

import (
	"bytes"
	"io"
	"sync"
	"testing"
	"time"
)

func TestDialAndExchangeOverRealPipe(t *testing.T) {
	listener, err := Listen()
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer listener.Close()

	type acceptResult struct {
		conn Conn
		err  error
	}
	accepted := make(chan acceptResult, 1)
	go func() {
		conn, err := listener.Accept()
		accepted <- acceptResult{conn, err}
	}()

	client, err := Dial(listener.Name(), 5*time.Second)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	server := <-accepted
	if server.err != nil {
		t.Fatalf("accept: %v", server.err)
	}
	defer server.conn.Close()

	// Bidirectional exchange through the same framing the protocol uses.
	go func() {
		io.Copy(server.conn, server.conn)
	}()
	payload := []byte("栖境 pipe payload with unicode \x00\x01 bytes")
	if _, err := client.Write(payload); err != nil {
		t.Fatalf("write: %v", err)
	}
	got := make([]byte, len(payload))
	if _, err := io.ReadFull(client, got); err != nil {
		t.Fatalf("read: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("echo mismatch: %q", got)
	}
}

// Close must unblock a pending Accept: the broker relies on listener cleanup
// never hanging when the scanner never connects.
func TestCloseUnblocksAccept(t *testing.T) {
	listener, err := Listen()
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	wg.Add(1)
	acceptReturned := make(chan struct{})
	go func() {
		defer wg.Done()
		_, err := listener.Accept()
		close(acceptReturned)
		if err == nil {
			t.Error("accept after close must fail")
		}
	}()
	time.Sleep(50 * time.Millisecond)
	if err := listener.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	select {
	case <-acceptReturned:
	case <-time.After(3 * time.Second):
		t.Fatal("accept was not unblocked by Close")
	}
	wg.Wait()
}

func TestRandomNamesAreUnique(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 100; i++ {
		name, err := RandomName()
		if err != nil {
			t.Fatal(err)
		}
		if seen[name] {
			t.Fatalf("duplicate name %s", name)
		}
		seen[name] = true
	}
}
