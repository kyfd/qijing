package desktop

import (
	"context"
	"errors"
	"testing"

	wailsruntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

func TestBridgeChooseDirectoryRequiresStartup(t *testing.T) {
	bridge := newBridge()
	if _, err := bridge.ChooseDirectory(); err == nil {
		t.Fatal("ChooseDirectory() before startup must return an error")
	}
}

func TestBridgeChooseDirectoryReturnsSelection(t *testing.T) {
	bridge := newBridge()
	bridge.setContext(context.Background())
	bridge.picker = func(context.Context, wailsruntime.OpenDialogOptions) (string, error) {
		return `C:\Users\Example`, nil
	}

	path, err := bridge.ChooseDirectory()
	if err != nil {
		t.Fatalf("ChooseDirectory() error = %v", err)
	}
	if path != `C:\Users\Example` {
		t.Fatalf("ChooseDirectory() = %q, want selected path", path)
	}
}

func TestBridgeChooseDirectoryPreservesCancellation(t *testing.T) {
	bridge := newBridge()
	bridge.setContext(context.Background())
	bridge.picker = func(context.Context, wailsruntime.OpenDialogOptions) (string, error) {
		return "", nil
	}

	path, err := bridge.ChooseDirectory()
	if err != nil || path != "" {
		t.Fatalf("ChooseDirectory() = (%q, %v), want empty successful cancellation", path, err)
	}
}

func TestBridgeChooseDirectoryReturnsDialogError(t *testing.T) {
	bridge := newBridge()
	bridge.setContext(context.Background())
	want := errors.New("dialog failed")
	bridge.picker = func(context.Context, wailsruntime.OpenDialogOptions) (string, error) {
		return "", want
	}

	if _, err := bridge.ChooseDirectory(); !errors.Is(err, want) {
		t.Fatalf("ChooseDirectory() error = %v, want %v", err, want)
	}
}
