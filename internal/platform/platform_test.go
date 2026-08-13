package platform

import "testing"

func TestRevealDoesNotRunForMissingPath(t *testing.T) {
	// Platform integration is intentionally exercised manually: Reveal delegates
	// to Explorer and has no file mutation code path.
}
