package server

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/kyfd/qijing/internal/application"
	webassets "github.com/kyfd/qijing/web"
)

type Options struct {
	DataDir string
	Addr    string
	Logger  *slog.Logger
	Secrets application.SecretStore
	// ScanFactory is forwarded to the application layer; production leaves
	// it nil so scans run in the scanner subprocess.
	ScanFactory application.ScanEngineFactory
}

type Server struct {
	token  string
	app    *application.Service
	logger *slog.Logger
	http   *http.Server
}

type rootRequest struct {
	Path string `json:"path"`
}

func New(options Options) (*Server, error) {
	if options.Addr == "" {
		options.Addr = "127.0.0.1:8765"
	}
	host, _, err := net.SplitHostPort(options.Addr)
	if err != nil || (host != "127.0.0.1" && host != "localhost" && host != "::1") {
		return nil, errors.New("server must bind to a loopback address")
	}
	if options.Logger == nil {
		options.Logger = slog.Default()
	}
	app, err := application.New(application.Options{DataDir: options.DataDir, Context: context.Background(), Secrets: options.Secrets, ScanFactory: options.ScanFactory})
	if err != nil {
		return nil, err
	}
	s := &Server{token: randomToken(), app: app, logger: options.Logger}
	mux := http.NewServeMux()
	s.routes(mux)
	s.http = &http.Server{Addr: options.Addr, Handler: s.security(mux), ReadHeaderTimeout: 5 * time.Second}
	return s, nil
}

func (s *Server) Close() error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return s.app.Close(ctx)
}
func (s *Server) Addr() string                       { return s.http.Addr }
func (s *Server) Handler() http.Handler              { return s.http.Handler }
func (s *Server) ListenAndServe() error              { return s.http.ListenAndServe() }
func (s *Server) Shutdown(ctx context.Context) error { return s.http.Shutdown(ctx) }

func (s *Server) routes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/status", s.status)
	mux.HandleFunc("GET /api/v1/roots", s.roots)
	mux.HandleFunc("POST /api/v1/roots", s.addRoot)
	mux.HandleFunc("POST /api/v1/roots/batch", s.authorizeRoots)
	mux.HandleFunc("DELETE /api/v1/roots", s.removeRoot)
	mux.HandleFunc("POST /api/v1/scan", s.scan)
	mux.HandleFunc("POST /api/v1/scan/cancel", s.cancelScan)
	mux.HandleFunc("GET /api/v1/map", s.ecosystemMap)
	mux.HandleFunc("GET /api/v1/nodes/{id}", s.node)
	mux.HandleFunc("POST /api/v1/nodes/{id}/reveal", s.reveal)
	mux.HandleFunc("POST /api/v1/recommendations/{id}/ignore", s.ignore)
	mux.HandleFunc("GET /api/v1/recycle/candidates", s.recycleCandidates)
	mux.HandleFunc("POST /api/v1/recycle/preview", s.previewRecycle)
	mux.HandleFunc("POST /api/v1/recycle/confirm", s.confirmRecycle)
	mux.HandleFunc("GET /api/v1/recycle/history", s.recycleHistory)
	mux.HandleFunc("GET /api/v1/privacy", s.privacy)
	mux.HandleFunc("POST /api/v1/demo", s.demo)
	mux.HandleFunc("GET /api/v1/model/profile", s.modelProfile)
	mux.HandleFunc("PUT /api/v1/model/profile", s.saveModelProfile)
	mux.HandleFunc("PUT /api/v1/model/key", s.setModelKey)
	mux.HandleFunc("DELETE /api/v1/model/key", s.deleteModelKey)
	mux.HandleFunc("POST /api/v1/model/test", s.testModel)
	mux.HandleFunc("PUT /api/v1/model/network", s.setNetwork)
	mux.HandleFunc("POST /api/v1/agent/preview", s.previewAgent)
	mux.HandleFunc("POST /api/v1/agent/runs", s.startAgent)
	mux.HandleFunc("POST /api/v1/agent/runs/{id}/cancel", s.cancelAgent)
	mux.HandleFunc("GET /api/v1/agent/runs/{id}", s.agentStatus)
	mux.HandleFunc("GET /api/v1/agent/runs/{id}/result", s.agentResult)
	mux.HandleFunc("GET /api/v1/agent/runs/{id}/audits", s.agentAudits)
	mux.Handle("/", http.FileServer(http.FS(mustSub(webassets.Assets, "."))))
}

func (s *Server) security(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Host != s.http.Addr && r.Host != "localhost:"+portOf(s.http.Addr) && r.Host != "127.0.0.1:"+portOf(s.http.Addr) {
			http.Error(w, "invalid host", http.StatusForbidden)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/api/") && r.Method != http.MethodGet {
			if !validOrigin(r, s.http.Addr) {
				http.Error(w, "invalid origin", http.StatusForbidden)
				return
			}
			if r.Header.Get("X-Ecosystem-Token") != s.token {
				http.Error(w, "invalid local session token", http.StatusForbidden)
				return
			}
		}
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; style-src 'self'; script-src 'self'; img-src 'self' data:; connect-src 'self'; frame-ancestors 'none'")
		next.ServeHTTP(w, r)
	})
}

func (s *Server) status(w http.ResponseWriter, r *http.Request) {
	status := s.app.Status(r.Context())
	writeJSON(w, map[string]any{"token": s.token, "scanning": status.Scanning, "state": status.State, "scan_id": status.ScanID, "task_result": status.TaskResult, "last_scan": status.LastScan, "last_error": status.LastError, "stats": status.Stats, "scan_readonly": status.ScanReadOnly, "network": status.Network, "partial": status.Partial, "truncated": status.Truncated, "truncation_reason": status.TruncationCause, "error_count": status.ErrorCount, "progress": status.Progress})
}
func (s *Server) roots(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, s.app.Roots(r.Context()))
}
func (s *Server) addRoot(w http.ResponseWriter, r *http.Request) {
	var req rootRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	result, err := s.app.AddRoot(r.Context(), req.Path)
	if err != nil {
		code := http.StatusBadRequest
		if errors.Is(err, application.ErrScanRunning) {
			code = http.StatusConflict
		}
		http.Error(w, err.Error(), code)
		return
	}
	writeJSON(w, result)
}
func (s *Server) authorizeRoots(w http.ResponseWriter, r *http.Request) {
	var req application.BatchRootsRequestDTO
	if !decodeJSON(w, r, &req) {
		return
	}
	result, err := s.app.AuthorizeRoots(r.Context(), req)
	if err != nil {
		http.Error(w, "cannot save local authorization", http.StatusInternalServerError)
		return
	}
	if !result.AuthorizationSucceeded {
		writeJSONStatus(w, http.StatusUnprocessableEntity, result)
		return
	}
	writeJSON(w, result)
}
func (s *Server) removeRoot(w http.ResponseWriter, r *http.Request) {
	var req rootRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	result, err := s.app.RemoveRoot(r.Context(), req.Path)
	if err != nil {
		code := http.StatusInternalServerError
		if errors.Is(err, application.ErrScanRunning) {
			code = http.StatusConflict
		}
		http.Error(w, err.Error(), code)
		return
	}
	writeJSON(w, result)
}
func (s *Server) scan(w http.ResponseWriter, r *http.Request) {
	result, err := s.app.StartScan(r.Context())
	if err != nil {
		code := http.StatusBadRequest
		if errors.Is(err, application.ErrScanRunning) {
			code = http.StatusConflict
		}
		http.Error(w, err.Error(), code)
		return
	}
	// Preserve the original HTTP endpoint's completed-scan behavior. The
	// application task remains detached and survives a client disconnect.
	for s.app.Status(context.Background()).Scanning {
		select {
		case <-r.Context().Done():
			writeJSON(w, result)
			return
		case <-time.After(time.Millisecond):
		}
	}
	writeJSON(w, result)
}
func (s *Server) cancelScan(w http.ResponseWriter, r *http.Request) {
	if err := s.app.CancelScan(r.Context()); err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}
func (s *Server) ecosystemMap(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, s.app.Map(r.Context()))
}
func (s *Server) node(w http.ResponseWriter, r *http.Request) {
	node, err := s.app.Node(r.Context(), r.PathValue("id"))
	if errors.Is(err, application.ErrNodeNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, node)
}
func (s *Server) reveal(w http.ResponseWriter, r *http.Request) {
	result, err := s.app.Reveal(r.Context(), r.PathValue("id"))
	if errors.Is(err, application.ErrNodeNotFound) {
		http.NotFound(w, r)
		return
	}
	if errors.Is(err, application.ErrUnauthorized) {
		http.Error(w, err.Error(), http.StatusForbidden)
		return
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, result)
}
func (s *Server) ignore(w http.ResponseWriter, r *http.Request) {
	if err := s.app.IgnoreRecommendation(r.Context(), r.PathValue("id")); errors.Is(err, application.ErrNodeNotFound) {
		http.NotFound(w, r)
		return
	} else if err != nil {
		http.Error(w, "cannot persist ignored recommendation", http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}
func (s *Server) privacy(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, s.app.Privacy(r.Context()))
}

func (s *Server) recycleCandidates(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, s.app.RecycleCandidates(r.Context()))
}

func (s *Server) previewRecycle(w http.ResponseWriter, r *http.Request) {
	var req application.RecyclePreviewRequestDTO
	if !decodeJSON(w, r, &req) {
		return
	}
	result, err := s.app.PreviewRecycle(r.Context(), req)
	if err != nil {
		code := http.StatusBadRequest
		switch {
		case errors.Is(err, application.ErrNodeNotFound):
			code = http.StatusNotFound
		case errors.Is(err, application.ErrUnauthorized):
			code = http.StatusForbidden
		case errors.Is(err, application.ErrNoScan):
			code = http.StatusConflict
		}
		http.Error(w, err.Error(), code)
		return
	}
	writeJSON(w, result)
}

func (s *Server) confirmRecycle(w http.ResponseWriter, r *http.Request) {
	var req application.RecycleConfirmRequestDTO
	if !decodeJSON(w, r, &req) {
		return
	}
	result, err := s.app.ConfirmRecycle(r.Context(), req)
	if errors.Is(err, application.ErrRecycleConfirmation) {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, result)
}

func (s *Server) recycleHistory(w http.ResponseWriter, r *http.Request) {
	result, err := s.app.RecycleHistory(r.Context(), 100)
	if err != nil {
		http.Error(w, "cannot read recycle history", http.StatusInternalServerError)
		return
	}
	writeJSON(w, result)
}
func (s *Server) demo(w http.ResponseWriter, r *http.Request) { writeJSON(w, s.app.Demo(r.Context())) }

func writeJSON(w http.ResponseWriter, value any) {
	writeJSONStatus(w, http.StatusOK, value)
}
func writeJSONStatus(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
func decodeJSON(w http.ResponseWriter, r *http.Request, dst any) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return false
	}
	return true
}
func randomToken() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}
func mustSub(source fs.FS, dir string) fs.FS {
	sub, err := fs.Sub(source, dir)
	if err != nil {
		panic(err)
	}
	return sub
}
func portOf(addr string) string { _, port, _ := net.SplitHostPort(addr); return port }

func validOrigin(r *http.Request, addr string) bool {
	origin := r.Header.Get("Origin")
	// Native clients commonly omit Origin; session-token and Host checks still apply.
	if origin == "" {
		return true
	}
	allowed := map[string]bool{
		"http://" + addr:                   true,
		"http://localhost:" + portOf(addr): true,
		"http://127.0.0.1:" + portOf(addr): true,
		"http://[::1]:" + portOf(addr):     true,
	}
	return allowed[origin]
}
