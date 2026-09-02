package desktop

import (
	"context"
	"net/http"
	"sync"

	"github.com/kyfd/qijing/internal/appdir"
	"github.com/kyfd/qijing/internal/instance"
	"github.com/kyfd/qijing/internal/server"
	"github.com/kyfd/qijing/internal/tray"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	windowsoptions "github.com/wailsapp/wails/v2/pkg/options/windows"
	wailsruntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

type shell struct {
	ctx      context.Context
	lock     *instance.Lock
	server   *server.Server
	tray     *tray.Tray
	bridge   *Bridge
	mu       sync.Mutex
	quitting bool
}

func Run() error {
	lock, primary, err := instance.Acquire()
	if err != nil || !primary {
		return err
	}
	defer lock.Close()

	// The index holds real paths and file names, so it lives under
	// %LocalAppData% and never in the roaming profile (ADR 0001).
	layout, err := appdir.Ensure()
	if err != nil {
		return err
	}
	backend, err := server.New(server.Options{DataDir: layout.Data, Addr: "127.0.0.1:8765"})
	if err != nil {
		return err
	}
	defer backend.Close()

	bridge := newBridge()
	s := &shell{lock: lock, server: backend, bridge: bridge}
	return wails.Run(&options.App{
		Title: "栖境 · 文件生态系统 Agent",
		Width: 1280, Height: 820, MinWidth: 980, MinHeight: 640,
		StartHidden:       true,
		HideWindowOnClose: true,
		BackgroundColour:  options.NewRGB(11, 16, 14),
		AssetServer:       &assetserver.Options{Handler: desktopHandler(backend.Handler())},
		Bind:              []interface{}{bridge},
		OnStartup:         s.startup,
		OnBeforeClose:     s.beforeClose,
		OnShutdown:        s.shutdown,
		Windows:           &windowsoptions.Options{Theme: windowsoptions.Dark, WindowClassName: "FileEcosystemDesktop", DisablePinchZoom: true},
	})
}

func (s *shell) startup(ctx context.Context) {
	s.ctx = ctx
	s.bridge.setContext(ctx)
	s.lock.OnActivate(s.show)
	t, err := tray.New(tray.Actions{Show: s.show, Quit: s.quit})
	if err != nil {
		// A hidden application without a tray icon would be unreachable.
		wailsruntime.WindowShow(ctx)
		return
	}
	s.tray = t
	t.Start()
}

func (s *shell) show() {
	if s.ctx == nil {
		return
	}
	wailsruntime.WindowUnminimise(s.ctx)
	wailsruntime.WindowShow(s.ctx)
}

func (s *shell) quit() {
	s.mu.Lock()
	s.quitting = true
	s.mu.Unlock()
	if s.ctx != nil {
		wailsruntime.Quit(s.ctx)
	}
}

func (s *shell) beforeClose(context.Context) bool {
	s.mu.Lock()
	quitting := s.quitting
	s.mu.Unlock()
	return !quitting
}

func (s *shell) shutdown(context.Context) {
	if s.tray != nil {
		s.tray.Close()
	}
}

// desktopHandler adapts the existing HTTP application API to Wails' in-process
// asset server. No TCP listener is created; requests never leave the process.
func desktopHandler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Host = "127.0.0.1:8765"
		if r.Header.Get("Origin") == "http://wails.localhost" {
			r.Header.Set("Origin", "http://127.0.0.1:8765")
		}
		next.ServeHTTP(w, r)
	})
}
