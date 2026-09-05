package scanproto

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// MaxStderrTailBytes is the largest child-stderr excerpt the broker attaches
// to a connection error. It is small enough to sit in a log line and large
// enough to keep the last diagnostic the scanner printed.
const MaxStderrTailBytes = 2048

const (
	asciiBackslash = rune(92)
	asciiSlash     = rune(47)
	asciiColon     = rune(58)
	asciiAt        = rune(64)
	asciiDot       = rune(46)
)

// SanitizeDiagnostic returns a bounded, path-free excerpt of a child process
// diagnostic. The original bytes are never stored; only the sanitized tail
// is suitable for logs or error strings. Matching is conservative: a token
// that looks like a path, email, or long hex blob is replaced rather than
// kept, because a false positive is a lost diagnostic and a false negative
// is a privacy leak.
func SanitizeDiagnostic(raw []byte) string {
	if len(raw) == 0 {
		return ""
	}
	if !utf8.Valid(raw) {
		filtered := make([]byte, 0, len(raw))
		for _, b := range raw {
			switch {
			case b >= 0x20 && b < 0x7f:
				filtered = append(filtered, b)
			case b == '\n' || b == '\r' || b == '\t':
				filtered = append(filtered, b)
			default:
				filtered = append(filtered, ' ')
			}
		}
		raw = filtered
	}
	fields := strings.FieldsFunc(string(raw), unicode.IsSpace)
	out := make([]string, 0, len(fields))
	for _, tok := range fields {
		out = append(out, sanitizeToken(tok))
	}
	text := strings.Join(out, " ")
	if len(text) > MaxStderrTailBytes {
		text = text[len(text)-MaxStderrTailBytes:]
	}
	return strings.TrimSpace(text)
}

func sanitizeToken(tok string) string {
	if repl, ok := classifyToken(tok); ok {
		return repl
	}
	// Diagnostics often use key=value tokens ("hash=<hex>"), so the value
	// half is classified on its own before giving up on the token.
	if eq := strings.IndexByte(tok, '='); eq >= 0 {
		if repl, ok := classifyToken(tok[eq+1:]); ok {
			return tok[:eq+1] + repl
		}
	}
	// A path can also ride inside a token without any separator
	// ("posix</home/a" would be unusual, but costless to catch), so a
	// token that contains a path from some offset is trimmed to keep the
	// prefix and drop everything from the path onward.
	if idx := embeddedPathStart(tok); idx > 0 {
		return tok[:idx] + "<path>"
	}
	return tok
}

// classifyToken maps a whole token to its redacted form when the token is
// entirely an email, hex blob or path.
func classifyToken(tok string) (string, bool) {
	switch {
	case looksLikeEmail(tok):
		return "<redacted>", true
	case looksLikeHexBlob(tok):
		return "<hex>", true
	case looksLikePath(tok):
		return "<path>", true
	}
	return "", false
}

// embeddedPathStart reports the byte offset where a path begins inside a
// token that does not start with one, or -1 when there is none.
func embeddedPathStart(tok string) int {
	runes := []rune(tok)
	for i := 1; i < len(runes); i++ {
		switch {
		case runes[i] == asciiBackslash && i+1 < len(runes) && runes[i+1] == asciiBackslash:
			return i
		case isASCIILetter(runes[i]) && i+2 < len(runes) && runes[i+1] == asciiColon &&
			(runes[i+2] == asciiBackslash || runes[i+2] == asciiSlash):
			return i
		case runes[i] == asciiSlash:
			for j := i + 1; j < len(runes); j++ {
				if runes[j] == asciiSlash {
					return i
				}
			}
		}
	}
	return -1
}

func looksLikePath(tok string) bool {
	if len(tok) < 3 {
		return false
	}
	runes := []rune(tok)
	if len(runes) >= 2 && runes[0] == asciiBackslash && runes[1] == asciiBackslash {
		return true
	}
	if runes[0] == asciiSlash {
		for i := 1; i < len(runes); i++ {
			if runes[i] == asciiSlash {
				return true
			}
		}
	}
	if len(runes) >= 3 && isASCIILetter(runes[0]) && runes[1] == asciiColon &&
		(runes[2] == asciiBackslash || runes[2] == asciiSlash) {
		return true
	}
	for i := 0; i+2 < len(runes); i++ {
		if runes[i] == asciiColon && (runes[i+1] == asciiBackslash || runes[i+1] == asciiSlash) {
			return true
		}
	}
	return false
}

func isASCIILetter(r rune) bool {
	return (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z')
}

func looksLikeEmail(tok string) bool {
	at := strings.IndexRune(tok, asciiAt)
	if at <= 0 || at == len(tok)-1 {
		return false
	}
	rest := tok[at+1:]
	dot := strings.LastIndexByte(rest, byte(asciiDot))
	return dot > 0 && dot < len(rest)-1
}

func looksLikeHexBlob(tok string) bool {
	if len(tok) < 32 {
		return false
	}
	for i := 0; i < len(tok); i++ {
		c := tok[i]
		switch {
		case c >= '0' && c <= '9':
		case c >= 'a' && c <= 'f':
		case c >= 'A' && c <= 'F':
		default:
			return false
		}
	}
	return true
}
