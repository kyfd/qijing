package server

import (
	"errors"
	"net/http"
	"strconv"

	"fileecosystem/internal/application"
)

type apiKeyRequest struct {
	APIKey string `json:"api_key"`
}
type networkRequest struct {
	Enabled bool `json:"enabled"`
}
type startAgentRequest struct {
	PayloadHash       string `json:"payload_hash"`
	ConfirmationToken string `json:"confirmation_token"`
}

func (s *Server) modelProfile(w http.ResponseWriter, r *http.Request) {
	v, err := s.app.GetModelProfile(r.Context())
	if err != nil {
		http.Error(w, "cannot load model profile", 500)
		return
	}
	writeJSON(w, v)
}
func (s *Server) saveModelProfile(w http.ResponseWriter, r *http.Request) {
	var v application.ModelProfileDTO
	if !decodeJSON(w, r, &v) {
		return
	}
	out, err := s.app.SaveModelProfile(r.Context(), v)
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	writeJSON(w, out)
}
func (s *Server) setModelKey(w http.ResponseWriter, r *http.Request) {
	var v apiKeyRequest
	if !decodeJSON(w, r, &v) {
		return
	}
	if err := s.app.SetAPIKey(r.Context(), v.APIKey); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}
func (s *Server) deleteModelKey(w http.ResponseWriter, r *http.Request) {
	if err := s.app.DeleteAPIKey(r.Context()); err != nil {
		http.Error(w, "cannot delete API key", 500)
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}
func (s *Server) testModel(w http.ResponseWriter, r *http.Request) {
	if err := s.app.TestModelConnection(r.Context()); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}
func (s *Server) setNetwork(w http.ResponseWriter, r *http.Request) {
	var v networkRequest
	if !decodeJSON(w, r, &v) {
		return
	}
	if err := s.app.SetNetworkEnabled(r.Context(), v.Enabled); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}
func (s *Server) previewAgent(w http.ResponseWriter, r *http.Request) {
	v, err := s.app.PreviewAgentRun(r.Context())
	if err != nil {
		agentError(w, err)
		return
	}
	writeJSON(w, v)
}
func (s *Server) startAgent(w http.ResponseWriter, r *http.Request) {
	var v startAgentRequest
	if !decodeJSON(w, r, &v) {
		return
	}
	out, err := s.app.StartAgentRun(r.Context(), v.PayloadHash, v.ConfirmationToken)
	if err != nil {
		agentError(w, err)
		return
	}
	writeJSON(w, out)
}
func (s *Server) cancelAgent(w http.ResponseWriter, r *http.Request) {
	if err := s.app.CancelAgentRun(r.Context(), r.PathValue("id")); err != nil {
		agentError(w, err)
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}
func (s *Server) agentStatus(w http.ResponseWriter, r *http.Request) {
	out, err := s.app.AgentRunStatus(r.Context(), r.PathValue("id"))
	if err != nil {
		agentError(w, err)
		return
	}
	writeJSON(w, out)
}
func (s *Server) agentResult(w http.ResponseWriter, r *http.Request) {
	out, err := s.app.AgentRunResult(r.Context(), r.PathValue("id"))
	if err != nil {
		agentError(w, err)
		return
	}
	writeJSON(w, out)
}
func (s *Server) agentAudits(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	out, err := s.app.ListAgentAudits(r.Context(), r.PathValue("id"), limit)
	if err != nil {
		agentError(w, err)
		return
	}
	writeJSON(w, map[string]any{"steps": out})
}
func agentError(w http.ResponseWriter, err error) {
	code := http.StatusBadRequest
	if errors.Is(err, application.ErrAgentRunNotFound) {
		code = http.StatusNotFound
	} else if errors.Is(err, application.ErrNetworkDisabled) || errors.Is(err, application.ErrConfirmation) {
		code = http.StatusForbidden
	}
	http.Error(w, err.Error(), code)
}
