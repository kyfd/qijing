package scanbroker

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Pause and resume on a live scan must work end to end: the scan finishes
// only after it is resumed, and pausing an unconnected scan fails clearly.
func TestBrokerPauseAndResumeOnLiveScan(t *testing.T) {
	if testing.Short() {
		t.Skip("hashes 192 MiB of fixture data")
	}
	root := t.TempDir()
	// Hashing 192 MiB keeps the scanner busy long enough to pause it
	// deterministically after the connection is up.
	for i := 0; i < 3; i++ {
		if err := os.WriteFile(filepath.Join(root, "blob"+itoa(i)+".dat"), make([]byte, 64<<20), 0o600); err != nil {
			t.Skipf("cannot allocate hashing fixture: %v", err)
		}
	}
	pipe := serveRealEngine(t)
	s := newBroker(t, root, func(o *Options) { o.Config.HashSHA256 = true })

	// Not connected yet: pause must fail with a clear error.
	if err := s.Pause(); err == nil {
		t.Fatal("pausing an unconnected scan must fail")
	}

	type outcome struct {
		complete bool
		err      error
	}
	done := make(chan outcome, 1)
	go func() {
		scan, err := s.converse(context.Background(), dialForTest(t, pipe), []string{root}, "snap-pause", func() {})
		done <- outcome{complete: err == nil && scan.Status == "complete", err: err}
	}()

	// Wait for the conversation to be live, then pause while the scanner is
	// hashing. If the scan somehow finished first, the done channel tells.
	deadline := time.Now().Add(5 * time.Second)
	for {
		s.streamMu.Lock()
		connected := s.stream != nil
		s.streamMu.Unlock()
		if connected || time.Now().After(deadline) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	paused := false
	for !paused {
		err := s.Pause()
		if err == nil {
			paused = true
			break
		}
		select {
		case result := <-done:
			t.Fatalf("scan finished before it could be paused: complete=%v err=%v", result.complete, result.err)
		default:
		}
		if errors.Is(err, ErrNotConnected) {
			t.Fatalf("scan ended before pausing: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
		if time.Now().After(deadline) {
			t.Fatalf("could not pause the scan: %v", err)
		}
	}
	if err := s.Pause(); err != nil {
		t.Fatalf("pausing twice must be a no-op: %v", err)
	}
	time.Sleep(300 * time.Millisecond)
	if err := s.Resume(); err != nil {
		t.Fatalf("resume: %v", err)
	}
	if err := s.Resume(); err != nil {
		t.Fatalf("resuming twice must be a no-op: %v", err)
	}

	select {
	case result := <-done:
		if result.err != nil {
			t.Fatalf("scan: %v", result.err)
		}
		if !result.complete {
			t.Fatal("scan did not complete")
		}
	case <-time.After(60 * time.Second):
		t.Fatal("resumed scan never finished")
	}
}
