package application

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"fileecosystem/internal/store"
)

type testSecrets struct{ key string }

func (s *testSecrets) Set(_ context.Context, _ string, key string) error { s.key = key; return nil }
func (s *testSecrets) Get(context.Context, string) (string, error) {
	if s.key == "" {
		return "", ErrAPIKeyMissing
	}
	return s.key, nil
}
func (s *testSecrets) Delete(context.Context, string) error { s.key = ""; return nil }

type blockingModel struct{ started chan []byte }

func (m *blockingModel) Test(context.Context, store.ModelProfile, string) error { return nil }
func (m *blockingModel) Run(ctx context.Context, _ store.ModelProfile, _ string, p []byte) ([]byte, error) {
	m.started <- p
	<-ctx.Done()
	return nil, ctx.Err()
}

func TestAgentRequiresNetworkAndExactOneTimeConfirmation(t *testing.T) {
	secrets := &testSecrets{}
	model := &blockingModel{started: make(chan []byte, 1)}
	s, err := New(Options{DataDir: t.TempDir(), Model: model, Secrets: secrets})
	if err != nil {
		t.Fatal(err)
	}
	defer closeService(t, s)
	ctx := context.Background()
	if _, err = s.SaveModelProfile(ctx, ModelProfileDTO{Provider: "openai", BaseURL: "https://api.example.test/v1", Model: "test"}); err != nil {
		t.Fatal(err)
	}
	if err = s.SetAPIKey(ctx, "do-not-persist"); err != nil {
		t.Fatal(err)
	}
	if _, err = s.PreviewAgentRun(ctx); !errors.Is(err, ErrNetworkDisabled) {
		t.Fatalf("preview error=%v", err)
	}
	if err = s.SetNetworkEnabled(ctx, true); err != nil {
		t.Fatal(err)
	}
	preview, err := s.PreviewAgentRun(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.StartAgentRun(ctx, "bad", preview.ConfirmationToken); !errors.Is(err, ErrConfirmation) {
		t.Fatalf("bad hash error=%v", err)
	}
	preview, err = s.PreviewAgentRun(ctx)
	if err != nil {
		t.Fatal(err)
	}
	run, err := s.StartAgentRun(ctx, preview.PayloadHash, preview.ConfirmationToken)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case body := <-model.started:
		var got any
		if json.Unmarshal(body, &got) != nil {
			t.Fatal("invalid payload")
		}
	case <-time.After(time.Second):
		t.Fatal("model not started")
	}
	if _, err = s.StartAgentRun(ctx, preview.PayloadHash, preview.ConfirmationToken); !errors.Is(err, ErrConfirmation) {
		t.Fatalf("token was reusable: %v", err)
	}
	if err = s.SetNetworkEnabled(ctx, false); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for {
		status, err := s.AgentRunStatus(ctx, run.RunID)
		if err != nil {
			t.Fatal(err)
		}
		if status.Status == "cancelled" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("run not cancelled: %#v", status)
		}
		time.Sleep(time.Millisecond)
	}
}

type recordingModel struct {
	tested  chan string
	started chan string
}

func (m *recordingModel) Test(_ context.Context, _ store.ModelProfile, key string) error {
	m.tested <- key
	return nil
}
func (m *recordingModel) Run(_ context.Context, _ store.ModelProfile, key string, _ []byte) ([]byte, error) {
	m.started <- key
	return []byte(`{"final":{"role":"assistant","content":"ok"},"steps":1}`), nil
}

func TestLocalModelMayRunWithoutAPIKey(t *testing.T) {
	model := &recordingModel{tested: make(chan string, 1), started: make(chan string, 1)}
	s, err := New(Options{DataDir: t.TempDir(), Model: model, Secrets: &testSecrets{}})
	if err != nil {
		t.Fatal(err)
	}
	defer closeService(t, s)
	ctx := context.Background()
	if _, err = s.SaveModelProfile(ctx, ModelProfileDTO{Provider: "local", BaseURL: "http://127.0.0.1:11434/v1", Model: "local-test"}); err != nil {
		t.Fatal(err)
	}
	if err = s.TestModelConnection(ctx); err != nil {
		t.Fatal(err)
	}
	if key := <-model.tested; key != "" {
		t.Fatalf("test key = %q, want empty", key)
	}
	if err = s.SetNetworkEnabled(ctx, true); err != nil {
		t.Fatal(err)
	}
	preview, err := s.PreviewAgentRun(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.StartAgentRun(ctx, preview.PayloadHash, preview.ConfirmationToken); err != nil {
		t.Fatal(err)
	}
	select {
	case key := <-model.started:
		if key != "" {
			t.Fatalf("run key = %q, want empty", key)
		}
	case <-time.After(time.Second):
		t.Fatal("local model did not start")
	}
}

func TestCloudModelStillRequiresAPIKey(t *testing.T) {
	s, err := New(Options{DataDir: t.TempDir(), Model: &recordingModel{tested: make(chan string, 1), started: make(chan string, 1)}, Secrets: &testSecrets{}})
	if err != nil {
		t.Fatal(err)
	}
	defer closeService(t, s)
	ctx := context.Background()
	if _, err = s.SaveModelProfile(ctx, ModelProfileDTO{Provider: "cloud", BaseURL: "https://api.example.test/v1", Model: "cloud-test"}); err != nil {
		t.Fatal(err)
	}
	if err = s.TestModelConnection(ctx); !errors.Is(err, ErrAPIKeyMissing) {
		t.Fatalf("test error = %v, want missing key", err)
	}
}

func TestAPIKeyIsNotPersistedInSQLite(t *testing.T) {
	dir := t.TempDir()
	s, err := New(Options{DataDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	if err = s.SetAPIKey(context.Background(), "unique-secret-value"); err != nil {
		t.Fatal(err)
	}
	closeService(t, s)
	db, err := sql.Open("sqlite", filepath.Join(dir, "ecosystem.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	tables := []string{"model_profiles", "network_consents", "agent_runs", "agent_steps", "agent_payloads", "agent_responses", "application_settings"}
	for _, table := range tables {
		rows, err := db.Query(`SELECT * FROM ` + table)
		if err != nil {
			t.Fatal(err)
		}
		cols, _ := rows.Columns()
		values := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range values {
			ptrs[i] = &values[i]
		}
		for rows.Next() {
			if err := rows.Scan(ptrs...); err != nil {
				t.Fatal(err)
			}
			raw, _ := json.Marshal(values)
			if bytes.Contains(raw, []byte("unique-secret-value")) {
				t.Fatal("secret persisted")
			}
		}
		rows.Close()
	}
}
