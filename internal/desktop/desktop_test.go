package desktop

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/kyfd/qijing/internal/server"
)

func TestDesktopHandlerUsesExpectedInProcessHost(t *testing.T) {
	var host, origin string
	handler := desktopHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host = r.Host
		origin = r.Header.Get("Origin")
		w.WriteHeader(http.StatusNoContent)
	}))

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "http://wails.localhost/api/v1/status", nil)
	request.Header.Set("Origin", "http://wails.localhost")
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusNoContent)
	}
	if host != "127.0.0.1:8765" {
		t.Fatalf("host = %q, want loopback application host", host)
	}
	if origin != "http://127.0.0.1:8765" {
		t.Fatalf("origin = %q, want loopback application origin", origin)
	}
}

func TestDesktopHandlerDoesNotRewriteUnexpectedOrigin(t *testing.T) {
	var origin string
	handler := desktopHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin = r.Header.Get("Origin")
		w.WriteHeader(http.StatusNoContent)
	}))

	request := httptest.NewRequest(http.MethodPost, "http://wails.localhost/api/v1/status", nil)
	request.Header.Set("Origin", "https://attacker.example")
	handler.ServeHTTP(httptest.NewRecorder(), request)
	if origin != "https://attacker.example" {
		t.Fatalf("unexpected origin was rewritten to %q", origin)
	}
}

func TestDesktopHandlerWithServerAllowsWailsMutationAndRejectsForeignOrigin(t *testing.T) {
	backend, err := server.New(server.Options{DataDir: t.TempDir(), Addr: "127.0.0.1:8765"})
	if err != nil {
		t.Fatal(err)
	}
	defer backend.Close()
	handler := desktopHandler(backend.Handler())

	statusRequest := httptest.NewRequest(http.MethodGet, "http://wails.localhost/api/v1/status", nil)
	statusRecorder := httptest.NewRecorder()
	handler.ServeHTTP(statusRecorder, statusRequest)
	if statusRecorder.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", statusRecorder.Code, statusRecorder.Body.String())
	}
	var status struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(statusRecorder.Body.Bytes(), &status); err != nil || status.Token == "" {
		t.Fatalf("decode token: %v, body %s", err, statusRecorder.Body.String())
	}

	mutation := func(origin string) *httptest.ResponseRecorder {
		request := httptest.NewRequest(http.MethodPost, "http://wails.localhost/api/v1/demo", nil)
		request.Header.Set("Origin", origin)
		request.Header.Set("X-Ecosystem-Token", status.Token)
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)
		return recorder
	}
	if recorder := mutation("http://wails.localhost"); recorder.Code != http.StatusOK {
		t.Fatalf("Wails-origin mutation = %d: %s", recorder.Code, recorder.Body.String())
	}
	if recorder := mutation("https://attacker.example"); recorder.Code != http.StatusForbidden {
		t.Fatalf("foreign-origin mutation = %d, want %d", recorder.Code, http.StatusForbidden)
	}
}

func TestBeforeCloseOnlyAllowsExplicitTrayQuit(t *testing.T) {
	s := &shell{}
	if !s.beforeClose(nil) {
		t.Fatal("ordinary window close must be prevented so the app remains in the tray")
	}
	s.mu.Lock()
	s.quitting = true
	s.mu.Unlock()
	if s.beforeClose(nil) {
		t.Fatal("explicit tray quit must allow shutdown")
	}
}
