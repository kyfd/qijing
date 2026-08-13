package desktop

import (
	"context"
	"errors"
	"sync"

	"fileecosystem/internal/drives"

	wailsruntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

// Bridge is the deliberately small native surface exposed to the Wails
// frontend. Application operations continue to use the in-process HTTP API.
type Bridge struct {
	mu     sync.RWMutex
	ctx    context.Context
	picker func(context.Context, wailsruntime.OpenDialogOptions) (string, error)
}

func newBridge() *Bridge {
	return &Bridge{picker: wailsruntime.OpenDirectoryDialog}
}

func (b *Bridge) setContext(ctx context.Context) {
	b.mu.Lock()
	b.ctx = ctx
	b.mu.Unlock()
}

func (b *Bridge) context() context.Context {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.ctx
}

// ChooseDirectory opens the operating system's native directory picker. An
// empty path with a nil error means the user cancelled the dialog.
func (b *Bridge) ChooseDirectory() (string, error) {
	ctx := b.context()
	if ctx == nil {
		return "", errors.New("desktop bridge has not started")
	}
	return b.picker(ctx, wailsruntime.OpenDialogOptions{
		Title: "选择观察目录",
	})
}

// ListLocalDrives returns Windows logical drives and their availability.
func (b *Bridge) ListLocalDrives() ([]drives.Drive, error) {
	return drives.List()
}
