package server

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"fileecosystem/internal/application"
)

func TestStatusIncludesNestedProgressSchema(t *testing.T) {
	srv, err := New(Options{DataDir: t.TempDir(), Addr: "127.0.0.1:8765"})
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	srv.app.Status(context.Background())
	response := request(t, srv, http.MethodGet, "/api/v1/status", nil, "")
	var status struct {
		State    application.ScanState        `json:"state"`
		Progress *application.ScanProgressDTO `json:"progress"`
		Stats    application.StatsDTO         `json:"stats"`
	}
	decodeResponse(t, response, &status)
	if response.Code != http.StatusOK || status.State != application.ScanIdle || status.Progress != nil {
		t.Fatalf("idle status=%#v code=%d", status, response.Code)
	}
	if !bytes.Contains(response.Body.Bytes(), []byte(`"progress":null`)) || !bytes.Contains(response.Body.Bytes(), []byte(`"stats"`)) {
		t.Fatalf("status schema=%s", response.Body.String())
	}
}

func TestBatchRootsEndpointIsAtomicAndCanStartReadOnlyScan(t *testing.T) {
	dataDir := t.TempDir()
	root := t.TempDir()
	target := filepath.Join(root, "note.txt")
	content := []byte("unchanged source data")
	if err := os.WriteFile(target, content, 0o600); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}
	beforeHash := sha256.Sum256(content)

	srv, err := New(Options{DataDir: dataDir, Addr: "127.0.0.1:8765"})
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	status := request(t, srv, http.MethodGet, "/api/v1/status", nil, "")
	var bootstrap map[string]any
	_ = json.Unmarshal(status.Body.Bytes(), &bootstrap)
	token, _ := bootstrap["token"].(string)

	invalidBody, _ := json.Marshal(map[string]any{"paths": []string{root, filepath.Join(root, "missing")}, "start_scan": true})
	invalid := request(t, srv, http.MethodPost, "/api/v1/roots/batch", invalidBody, token)
	if invalid.Code != http.StatusUnprocessableEntity {
		t.Fatalf("invalid batch: %d %s", invalid.Code, invalid.Body.String())
	}
	var invalidResult struct {
		AuthorizationSucceeded bool                                     `json:"authorization_succeeded"`
		Results                []application.RootAuthorizationResultDTO `json:"results"`
		Roots                  []application.RootDTO                    `json:"roots"`
	}
	if err := json.Unmarshal(invalid.Body.Bytes(), &invalidResult); err != nil {
		t.Fatal(err)
	}
	if invalidResult.AuthorizationSucceeded || len(invalidResult.Roots) != 0 || len(invalidResult.Results) != 2 || invalidResult.Results[1].Status != application.RootAuthorizationInvalid {
		t.Fatalf("invalid result=%#v", invalidResult)
	}

	validBody, _ := json.Marshal(map[string]any{"paths": []string{root, root + string(filepath.Separator)}, "start_scan": true})
	valid := request(t, srv, http.MethodPost, "/api/v1/roots/batch", validBody, token)
	if valid.Code != http.StatusOK {
		t.Fatalf("valid batch: %d %s", valid.Code, valid.Body.String())
	}
	var validResult application.BatchRootsDTO
	if err := json.Unmarshal(valid.Body.Bytes(), &validResult); err != nil {
		t.Fatal(err)
	}
	if !validResult.AuthorizationSucceeded || len(validResult.Roots) != 1 || validResult.Scan == nil || !validResult.Scan.Accepted {
		t.Fatalf("valid result=%#v", validResult)
	}
	waitServerIdle(t, srv)
	after, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}
	afterContent, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if beforeHash != sha256.Sum256(afterContent) || before.Size() != after.Size() || !before.ModTime().Equal(after.ModTime()) {
		t.Fatal("batch-triggered scan changed the authorized source file")
	}
}

func TestAPIRequiresTokenAndScanIsReadOnly(t *testing.T) {
	dataDir := t.TempDir()
	root := t.TempDir()
	target := filepath.Join(root, "note.txt")
	content := []byte("unchanged source data")
	if err := os.WriteFile(target, content, 0o600); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}
	beforeHash := sha256.Sum256(content)

	srv, err := New(Options{DataDir: dataDir, Addr: "127.0.0.1:8765"})
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()

	status := request(t, srv, http.MethodGet, "/api/v1/status", nil, "")
	if status.Code != http.StatusOK {
		t.Fatalf("status: %d %s", status.Code, status.Body.String())
	}
	var bootstrap map[string]any
	if err := json.Unmarshal(status.Body.Bytes(), &bootstrap); err != nil {
		t.Fatal(err)
	}
	token, _ := bootstrap["token"].(string)
	if token == "" {
		t.Fatal("missing session token")
	}

	body, _ := json.Marshal(rootRequest{Path: root})
	denied := request(t, srv, http.MethodPost, "/api/v1/roots", body, "")
	if denied.Code != http.StatusForbidden {
		t.Fatalf("mutation without token returned %d", denied.Code)
	}
	added := request(t, srv, http.MethodPost, "/api/v1/roots", body, token)
	if added.Code != http.StatusOK {
		t.Fatalf("add root: %d %s", added.Code, added.Body.String())
	}
	scanned := request(t, srv, http.MethodPost, "/api/v1/scan", []byte(`{}`), token)
	if scanned.Code != http.StatusOK {
		t.Fatalf("scan: %d %s", scanned.Code, scanned.Body.String())
	}
	mapped := request(t, srv, http.MethodGet, "/api/v1/map", nil, "")
	if mapped.Code != http.StatusOK || !bytes.Contains(mapped.Body.Bytes(), []byte("note.txt")) {
		t.Fatalf("map: %d %s", mapped.Code, mapped.Body.String())
	}

	after, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}
	afterContent, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	afterHash := sha256.Sum256(afterContent)
	if beforeHash != afterHash || before.Size() != after.Size() || !before.ModTime().Equal(after.ModTime()) {
		t.Fatal("scan changed the authorized source file")
	}
}

func TestOriginRejectedAndModelKeyNeverReturned(t *testing.T) {
	srv, err := New(Options{DataDir: t.TempDir(), Addr: "127.0.0.1:8765"})
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	status := request(t, srv, http.MethodGet, "/api/v1/status", nil, "")
	var bootstrap map[string]any
	_ = json.Unmarshal(status.Body.Bytes(), &bootstrap)
	token, _ := bootstrap["token"].(string)

	body := []byte(`{"provider":"openai","base_url":"https://api.example.test/v1","model":"safe"}`)
	req := httptest.NewRequest(http.MethodPut, "http://127.0.0.1:8765/api/v1/model/profile", bytes.NewReader(body))
	req.Host = "127.0.0.1:8765"
	req.Header.Set("Origin", "https://evil.example")
	req.Header.Set("X-Ecosystem-Token", token)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("cross-origin mutation=%d", rec.Code)
	}

	key := []byte(`{"api_key":"super-secret"}`)
	if got := request(t, srv, http.MethodPut, "/api/v1/model/key", key, token); got.Code != http.StatusOK {
		t.Fatalf("set key: %d %s", got.Code, got.Body.String())
	}
	profile := request(t, srv, http.MethodGet, "/api/v1/model/profile", nil, "")
	if bytes.Contains(profile.Body.Bytes(), []byte("super-secret")) {
		t.Fatalf("key leaked: %s", profile.Body.String())
	}
}

func TestAgentHTTPIntegrationWithKeylessLocalModel(t *testing.T) {
	var (
		modelMu       sync.Mutex
		modelRequests []map[string]any
		modelHeaders  []http.Header
	)
	modelServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			http.NotFound(w, r)
			return
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		modelMu.Lock()
		modelRequests = append(modelRequests, body)
		modelHeaders = append(modelHeaders, r.Header.Clone())
		call := len(modelRequests)
		modelMu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		if call == 1 {
			_, _ = io.WriteString(w, `{"id":"first","choices":[{"index":0,"message":{"role":"assistant","tool_calls":[{"id":"overview-1","type":"function","function":{"name":"overview","arguments":"{}"}}]},"finish_reason":"tool_calls"}]}`)
			return
		}
		_, _ = io.WriteString(w, `{"id":"final","choices":[{"index":0,"message":{"role":"assistant","content":"The ecosystem is healthy."},"finish_reason":"stop"}]}`)
	}))
	defer modelServer.Close()

	srv, err := New(Options{DataDir: t.TempDir(), Addr: "127.0.0.1:8765", Secrets: &emptySecretStore{}})
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()

	status := request(t, srv, http.MethodGet, "/api/v1/status", nil, "")
	var bootstrap map[string]any
	decodeResponse(t, status, &bootstrap)
	token, _ := bootstrap["token"].(string)
	if token == "" {
		t.Fatal("missing session token")
	}

	profileBody, _ := json.Marshal(map[string]any{"provider": "local", "base_url": modelServer.URL + "/v1", "model": "local-e2e"})
	profileResponse := request(t, srv, http.MethodPut, "/api/v1/model/profile", profileBody, token)
	if profileResponse.Code != http.StatusOK {
		t.Fatalf("save profile: %d %s", profileResponse.Code, profileResponse.Body.String())
	}
	var profile application.ModelProfileDTO
	decodeResponse(t, profileResponse, &profile)
	if profile.Provider != "local" || profile.HasAPIKey || profile.BaseURL != modelServer.URL {
		t.Fatalf("saved profile = %#v", profile)
	}

	withoutConsent := request(t, srv, http.MethodPost, "/api/v1/agent/preview", []byte(`{}`), token)
	if withoutConsent.Code != http.StatusForbidden || !bytes.Contains(withoutConsent.Body.Bytes(), []byte(application.ErrNetworkDisabled.Error())) {
		t.Fatalf("preview without consent: %d %s", withoutConsent.Code, withoutConsent.Body.String())
	}
	consent := request(t, srv, http.MethodPut, "/api/v1/model/network", []byte(`{"enabled":true}`), token)
	if consent.Code != http.StatusOK {
		t.Fatalf("enable network: %d %s", consent.Code, consent.Body.String())
	}

	previewResponse := request(t, srv, http.MethodPost, "/api/v1/agent/preview", []byte(`{}`), token)
	if previewResponse.Code != http.StatusOK {
		t.Fatalf("preview: %d %s", previewResponse.Code, previewResponse.Body.String())
	}
	var preview application.AgentPreviewDTO
	decodeResponse(t, previewResponse, &preview)
	exactBody, err := json.Marshal(preview.Payload)
	if err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256(exactBody)
	if preview.PayloadHash != fmt.Sprintf("%x", hash) || preview.PayloadBytes != len(exactBody) {
		t.Fatalf("preview body metadata = hash %q bytes %d, want %x and %d", preview.PayloadHash, preview.PayloadBytes, hash, len(exactBody))
	}

	startBody, _ := json.Marshal(map[string]string{"payload_hash": preview.PayloadHash, "confirmation_token": preview.ConfirmationToken})
	startResponse := request(t, srv, http.MethodPost, "/api/v1/agent/runs", startBody, token)
	if startResponse.Code != http.StatusOK {
		t.Fatalf("start: %d %s", startResponse.Code, startResponse.Body.String())
	}
	var started application.AgentRunDTO
	decodeResponse(t, startResponse, &started)
	if started.RunID == "" || started.Status != "running" {
		t.Fatalf("started run = %#v", started)
	}

	var runStatus map[string]any
	deadline := time.Now().Add(5 * time.Second)
	for {
		response := request(t, srv, http.MethodGet, "/api/v1/agent/runs/"+started.RunID, nil, "")
		if response.Code != http.StatusOK {
			t.Fatalf("run status: %d %s", response.Code, response.Body.String())
		}
		decodeResponse(t, response, &runStatus)
		if runStatus["status"] == "completed" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("run did not complete: %#v", runStatus)
		}
		time.Sleep(time.Millisecond)
	}
	if runStatus["payload_hash"] != preview.PayloadHash || int(runStatus["payload_bytes"].(float64)) != len(exactBody) {
		t.Fatalf("run status does not preserve preview metadata: %#v", runStatus)
	}

	resultResponse := request(t, srv, http.MethodGet, "/api/v1/agent/runs/"+started.RunID+"/result", nil, "")
	var result application.AgentResultDTO
	decodeResponse(t, resultResponse, &result)
	var orchestration struct {
		Final struct {
			Content string `json:"content"`
		} `json:"final"`
		Steps       int      `json:"steps"`
		ToolsCalled []string `json:"tools_called"`
	}
	if result.Status != "completed" || json.Unmarshal(result.Result, &orchestration) != nil || orchestration.Final.Content != "The ecosystem is healthy." || orchestration.Steps != 2 || len(orchestration.ToolsCalled) != 1 || orchestration.ToolsCalled[0] != "overview" {
		t.Fatalf("agent result = status %q result %s", result.Status, result.Result)
	}

	auditsResponse := request(t, srv, http.MethodGet, "/api/v1/agent/runs/"+started.RunID+"/audits", nil, "")
	var audits struct {
		Steps []struct {
			Kind   string `json:"kind"`
			Name   string `json:"name"`
			Detail string `json:"detail"`
		} `json:"steps"`
	}
	decodeResponse(t, auditsResponse, &audits)
	if len(audits.Steps) != 1 || audits.Steps[0].Kind != "request" || audits.Steps[0].Name != "model" || audits.Steps[0].Detail != modelServer.URL {
		t.Fatalf("audits = %#v", audits.Steps)
	}

	modelMu.Lock()
	defer modelMu.Unlock()
	if len(modelRequests) != 2 || len(modelHeaders) != 2 {
		t.Fatalf("model requests = %d, want tool call and final response", len(modelRequests))
	}
	for i, header := range modelHeaders {
		if value := header.Get("Authorization"); value != "" {
			t.Fatalf("model request %d authorization = %q, want absent", i+1, value)
		}
	}
	firstMessages := modelRequests[0]["messages"].([]any)
	firstUser := firstMessages[1].(map[string]any)["content"]
	if firstUser != string(exactBody) {
		t.Fatalf("model payload differs from preview\ngot:  %s\nwant: %s", firstUser, exactBody)
	}
	secondMessages := modelRequests[1]["messages"].([]any)
	toolMessage := secondMessages[len(secondMessages)-1].(map[string]any)
	if toolMessage["role"] != "tool" || toolMessage["tool_call_id"] != "overview-1" {
		t.Fatalf("tool result message = %#v", toolMessage)
	}
}

type emptySecretStore struct{}

func (*emptySecretStore) Set(context.Context, string, string) error { return nil }
func (*emptySecretStore) Get(context.Context, string) (string, error) {
	return "", application.ErrAPIKeyMissing
}
func (*emptySecretStore) Delete(context.Context, string) error { return nil }

func decodeResponse(t *testing.T, response *httptest.ResponseRecorder, target any) {
	t.Helper()
	if err := json.Unmarshal(response.Body.Bytes(), target); err != nil {
		t.Fatalf("decode response %d %q: %v", response.Code, response.Body.String(), err)
	}
}

func TestServerRejectsNonLoopbackAddress(t *testing.T) {
	if _, err := New(Options{DataDir: t.TempDir(), Addr: "0.0.0.0:8765"}); err == nil {
		t.Fatal("non-loopback bind was accepted")
	}
}

func waitServerIdle(t *testing.T, srv *Server) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for srv.app.Status(context.Background()).Scanning {
		if time.Now().After(deadline) {
			t.Fatal("scan did not finish")
		}
		time.Sleep(time.Millisecond)
	}
}

func request(t *testing.T, srv *Server, method, path string, body []byte, token string) *httptest.ResponseRecorder {
	t.Helper()
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req := httptest.NewRequest(method, "http://127.0.0.1:8765"+path, reader)
	req.Host = "127.0.0.1:8765"
	if token != "" {
		req.Header.Set("X-Ecosystem-Token", token)
	}
	recorder := httptest.NewRecorder()
	srv.http.Handler.ServeHTTP(recorder, req)
	return recorder
}
