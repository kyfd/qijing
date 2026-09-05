package scanproto

import (
	"strings"
	"testing"
)

func TestSanitizeDiagnosticStripsPathsEmailsAndHashes(t *testing.T) {
	raw := []byte("protocol violation: outgoing frame of 10699941 bytes exceeds the limit path=C:\\Users\\alice\\Documents\\secret.txt hash=0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef user=alice@example.com unc=\\\\fileserver\\share\\budget.xlsx posix=/home/alice/.ssh/id_rsa")
	got := SanitizeDiagnostic(raw)
	for _, leak := range []string{
		"C:\\Users",
		"alice",
		"secret.txt",
		"0123456789abcdef",
		"example.com",
		"fileserver",
		"budget.xlsx",
		"/home/alice",
		"id_rsa",
	} {
		if strings.Contains(got, leak) {
			t.Fatalf("sanitized diagnostic still contains %q: %q", leak, got)
		}
	}
	if !strings.Contains(got, "protocol violation") {
		t.Fatalf("lost the diagnostic itself: %q", got)
	}
	if !strings.Contains(got, "<path>") {
		t.Fatalf("expected path tokens to be replaced: %q", got)
	}
	if !strings.Contains(got, "<hex>") {
		t.Fatalf("expected hash token to be replaced: %q", got)
	}
	if !strings.Contains(got, "<redacted>") {
		t.Fatalf("expected email token to be replaced: %q", got)
	}
}

func TestSanitizeDiagnosticBoundsTail(t *testing.T) {
	raw := []byte(strings.Repeat("ok ", MaxStderrTailBytes))
	got := SanitizeDiagnostic(raw)
	if len(got) > MaxStderrTailBytes {
		t.Fatalf("len=%d, want <= %d", len(got), MaxStderrTailBytes)
	}
}

func TestSanitizeDiagnosticEmpty(t *testing.T) {
	if got := SanitizeDiagnostic(nil); got != "" {
		t.Fatalf("got %q", got)
	}
}
