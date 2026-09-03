package application

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/kyfd/qijing/internal/agent"
	"github.com/kyfd/qijing/internal/llm"
	"github.com/kyfd/qijing/internal/model"
	"github.com/kyfd/qijing/internal/secret"
	"github.com/kyfd/qijing/internal/store"
)

var (
	ErrModelNotConfigured = errors.New("model profile is not configured")
	ErrAPIKeyMissing      = errors.New("API key is not configured")
	ErrNetworkDisabled    = errors.New("model network access is disabled")
	ErrConfirmation       = errors.New("preview confirmation is missing, expired, or stale")
	ErrAgentRunNotFound   = errors.New("agent run not found")
)

const defaultProfileID = "default"

type ModelProfileDTO struct {
	ID             string `json:"id"`
	Provider       string `json:"provider"`
	BaseURL        string `json:"base_url"`
	Model          string `json:"model"`
	HasAPIKey      bool   `json:"has_api_key"`
	NetworkEnabled bool   `json:"network_enabled"`
}

type AgentPreviewDTO struct {
	SchemaVersion     string             `json:"schema_version"`
	ProfileID         string             `json:"profile_id"`
	SnapshotID        string             `json:"snapshot_id"`
	TargetOrigin      string             `json:"target_origin"`
	Model             string             `json:"model"`
	Payload           agent.CloudPayload `json:"payload"`
	PayloadHash       string             `json:"payload_hash"`
	PayloadBytes      int                `json:"payload_bytes"`
	ConfirmationToken string             `json:"confirmation_token"`
}

type AgentRunDTO struct {
	RunID  string `json:"run_id"`
	Status string `json:"status"`
}
type AgentResultDTO struct {
	RunID  string          `json:"run_id"`
	Status string          `json:"status"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  string          `json:"error,omitempty"`
}

type ModelClient interface {
	Test(context.Context, store.ModelProfile, string) error
	Run(context.Context, store.ModelProfile, string, []byte) ([]byte, error)
}

type SecretStore interface {
	Set(context.Context, string, string) error
	Get(context.Context, string) (string, error)
	Delete(context.Context, string) error
}

type memorySecrets struct {
	mu   sync.RWMutex
	keys map[string]string
}

func (m *memorySecrets) Set(_ context.Context, id, key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.keys[id] = key
	return nil
}
func (m *memorySecrets) Get(_ context.Context, id string) (string, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	v := m.keys[id]
	if v == "" {
		return "", ErrAPIKeyMissing
	}
	return v, nil
}
func (m *memorySecrets) Delete(_ context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.keys, id)
	return nil
}

type osSecrets struct{ store secret.Store }

func (s osSecrets) Set(_ context.Context, id, key string) error {
	return s.store.Save(id, []byte(key))
}
func (s osSecrets) Get(_ context.Context, id string) (string, error) {
	value, err := s.store.Load(id)
	if err != nil {
		return "", ErrAPIKeyMissing
	}
	return string(value), nil
}
func (s osSecrets) Delete(_ context.Context, id string) error { return s.store.Delete(id) }

type llmModelClient struct{}

func (llmModelClient) client(p store.ModelProfile, key string) (*llm.Client, error) {
	mode := llm.ModeCloud
	if p.Provider == "local" {
		mode = llm.ModeLocal
	}
	return llm.NewClient(llm.Config{BaseURL: p.BaseURL, APIKey: key, Model: p.Model, Mode: mode})
}
func (c llmModelClient) Test(ctx context.Context, p store.ModelProfile, key string) error {
	client, err := c.client(p, key)
	if err != nil {
		return err
	}
	_, err = client.Chat(ctx, llm.ChatRequest{Messages: []llm.Message{{Role: "user", Content: "Reply OK."}}, MaxOutputTokens: 8})
	return err
}
func (c llmModelClient) Run(ctx context.Context, p store.ModelProfile, key string, body []byte) ([]byte, error) {
	client, err := c.client(p, key)
	if err != nil {
		return nil, err
	}
	var payload agent.CloudPayload
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, err
	}
	preview, err := agent.PreviewPayload(payload)
	if err != nil {
		return nil, err
	}
	result, err := (agent.Orchestrator{Client: client}).Run(ctx, agent.RunOptions{NetworkEnabled: true, ConfirmedHash: preview.Hash, Preview: preview, Payload: payload})
	if err != nil {
		return nil, err
	}
	return json.Marshal(result)
}

type confirmation struct {
	Hash, ProfileID, ProfileHash, SnapshotID string
	Payload                                  agent.CloudPayload
	Body                                     []byte
	Expires                                  time.Time
}
type AgentManager struct {
	mu      sync.Mutex
	db      *store.Store
	client  ModelClient
	secrets SecretStore
	// snapshotMeta reports which snapshot the agent may summarize, without
	// its entries: those are streamed from the store when a payload is built.
	snapshotMeta  func() model.Scan
	confirmations map[string]confirmation
	cancels       map[string]context.CancelFunc
}

func newAgentManager(db *store.Store, client ModelClient, secrets SecretStore, secretsDir string, snapshotMeta func() model.Scan) *AgentManager {
	if secrets == nil {
		secrets = osSecrets{store: secret.New(secretsDir)}
	}
	if client == nil {
		client = llmModelClient{}
	}
	return &AgentManager{db: db, client: client, secrets: secrets, snapshotMeta: snapshotMeta, confirmations: map[string]confirmation{}, cancels: map[string]context.CancelFunc{}}
}

func (s *Service) GetModelProfile(ctx context.Context) (ModelProfileDTO, error) {
	return s.agent.getProfile(ctx)
}
func (s *Service) SaveModelProfile(ctx context.Context, p ModelProfileDTO) (ModelProfileDTO, error) {
	return s.agent.saveProfile(ctx, p)
}
func (s *Service) SetAPIKey(ctx context.Context, key string) error {
	if strings.TrimSpace(key) == "" {
		return ErrAPIKeyMissing
	}
	return s.agent.secrets.Set(ctx, defaultProfileID, key)
}
func (s *Service) DeleteAPIKey(ctx context.Context) error {
	return s.agent.secrets.Delete(ctx, defaultProfileID)
}
func (s *Service) TestModelConnection(ctx context.Context) error { return s.agent.test(ctx) }
func (s *Service) SetNetworkEnabled(ctx context.Context, enabled bool) error {
	return s.agent.setNetwork(ctx, enabled)
}
func (s *Service) PreviewAgentRun(ctx context.Context) (AgentPreviewDTO, error) {
	return s.agent.preview(ctx)
}
func (s *Service) StartAgentRun(ctx context.Context, hash, token string) (AgentRunDTO, error) {
	return s.agent.start(ctx, hash, token)
}
func (s *Service) CancelAgentRun(_ context.Context, id string) error { return s.agent.cancel(id) }
func (s *Service) AgentRunStatus(ctx context.Context, id string) (store.AgentRun, error) {
	run, err := s.agent.db.AgentRun(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return store.AgentRun{}, ErrAgentRunNotFound
	}
	return run, err
}
func (s *Service) AgentRunResult(ctx context.Context, id string) (AgentResultDTO, error) {
	return s.agent.result(ctx, id)
}
func (s *Service) ListAgentAudits(ctx context.Context, id string, limit int) ([]store.AgentStep, error) {
	if _, err := s.AgentRunStatus(ctx, id); err != nil {
		return nil, err
	}
	return s.agent.db.AgentAudits(ctx, id, limit)
}

func (m *AgentManager) getProfile(ctx context.Context) (ModelProfileDTO, error) {
	p, err := m.db.ModelProfile(ctx, defaultProfileID)
	if errors.Is(err, sql.ErrNoRows) {
		return ModelProfileDTO{ID: defaultProfileID}, nil
	}
	if err != nil {
		return ModelProfileDTO{}, err
	}
	enabled, err := m.db.NetworkEnabled(ctx, p.ID)
	if err != nil {
		return ModelProfileDTO{}, err
	}
	_, keyErr := m.secrets.Get(ctx, p.ID)
	return ModelProfileDTO{ID: p.ID, Provider: p.Provider, BaseURL: p.BaseURL, Model: p.Model, HasAPIKey: keyErr == nil, NetworkEnabled: enabled}, nil
}
func (m *AgentManager) saveProfile(ctx context.Context, v ModelProfileDTO) (ModelProfileDTO, error) {
	v.ID = defaultProfileID
	v.Provider = strings.TrimSpace(v.Provider)
	v.Model = strings.TrimSpace(v.Model)
	origin, err := safeOrigin(v.BaseURL)
	if err != nil {
		return ModelProfileDTO{}, err
	}
	if v.Provider == "" || v.Model == "" {
		return ModelProfileDTO{}, ErrModelNotConfigured
	}
	v.BaseURL = origin
	if err := m.db.SaveModelProfile(ctx, store.ModelProfile{ID: v.ID, Provider: v.Provider, BaseURL: v.BaseURL, Model: v.Model}); err != nil {
		return ModelProfileDTO{}, err
	}
	return m.getProfile(ctx)
}
func safeOrigin(raw string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" || u.User != nil || (u.Scheme != "https" && u.Scheme != "http") {
		return "", errors.New("invalid model base URL")
	}
	host := u.Hostname()
	if u.Scheme == "http" && host != "localhost" && net.ParseIP(host) == nil {
		return "", errors.New("non-local model URL must use HTTPS")
	}
	if ip := net.ParseIP(host); u.Scheme == "http" && ip != nil && !ip.IsLoopback() {
		return "", errors.New("non-local model URL must use HTTPS")
	}
	return u.Scheme + "://" + u.Host, nil
}
func (m *AgentManager) modelKey(ctx context.Context, p store.ModelProfile) (string, error) {
	key, err := m.secrets.Get(ctx, p.ID)
	if err == nil {
		return key, nil
	}
	if p.Provider == "local" && errors.Is(err, ErrAPIKeyMissing) {
		return "", nil
	}
	return "", err
}
func (m *AgentManager) test(ctx context.Context) error {
	p, err := m.db.ModelProfile(ctx, defaultProfileID)
	if err != nil {
		return ErrModelNotConfigured
	}
	key, err := m.modelKey(ctx, p)
	if err != nil {
		return err
	}
	if m.client == nil {
		return errors.New("model client is not available")
	}
	return m.client.Test(ctx, p, key)
}
func (m *AgentManager) setNetwork(ctx context.Context, enabled bool) error {
	if enabled {
		if _, err := m.db.ModelProfile(ctx, defaultProfileID); err != nil {
			return ErrModelNotConfigured
		}
	}
	if err := m.db.SetNetworkEnabled(ctx, defaultProfileID, enabled); err != nil {
		return err
	}
	if !enabled {
		m.CancelAll()
	}
	return nil
}

// payload aggregates the snapshot straight out of storage: entries stream past
// the builder one at a time and only bucketed counts are kept.
func (m *AgentManager) payload(ctx context.Context) (agent.CloudPayload, []byte, string, error) {
	scan := m.snapshotMeta()
	p, err := agent.BuildCloudPayloadStream(scan, func(visit func(model.Entry) error) error {
		return m.db.EachEntry(ctx, scan.ID, visit)
	})
	if err != nil {
		return agent.CloudPayload{}, nil, "", err
	}
	preview, err := agent.PreviewPayload(p)
	if err != nil {
		return agent.CloudPayload{}, nil, "", err
	}
	return p, preview.Body, preview.Hash, nil
}
func profileHash(p store.ModelProfile) string {
	sum := sha256.Sum256([]byte(p.Provider + "\x00" + p.BaseURL + "\x00" + p.Model))
	return hex.EncodeToString(sum[:])
}

func (m *AgentManager) preview(ctx context.Context) (AgentPreviewDTO, error) {
	p, err := m.db.ModelProfile(ctx, defaultProfileID)
	if err != nil {
		return AgentPreviewDTO{}, ErrModelNotConfigured
	}
	enabled, err := m.db.NetworkEnabled(ctx, p.ID)
	if err != nil {
		return AgentPreviewDTO{}, err
	}
	if !enabled {
		return AgentPreviewDTO{}, ErrNetworkDisabled
	}
	scanID := m.snapshotMeta().ID
	payload, body, hash, err := m.payload(ctx)
	if err != nil {
		return AgentPreviewDTO{}, err
	}
	token := randomID(32)
	m.mu.Lock()
	m.confirmations[token] = confirmation{Hash: hash, ProfileID: p.ID, ProfileHash: profileHash(p), SnapshotID: scanID, Payload: payload, Body: append([]byte(nil), body...), Expires: time.Now().Add(10 * time.Minute)}
	m.mu.Unlock()
	return AgentPreviewDTO{SchemaVersion: "1", ProfileID: p.ID, SnapshotID: scanID, TargetOrigin: p.BaseURL, Model: p.Model, Payload: payload, PayloadHash: hash, PayloadBytes: len(body), ConfirmationToken: token}, nil
}
func (m *AgentManager) start(ctx context.Context, hash, token string) (AgentRunDTO, error) {
	p, err := m.db.ModelProfile(ctx, defaultProfileID)
	if err != nil {
		return AgentRunDTO{}, ErrModelNotConfigured
	}
	enabled, err := m.db.NetworkEnabled(ctx, p.ID)
	if err != nil {
		return AgentRunDTO{}, err
	}
	if !enabled {
		return AgentRunDTO{}, ErrNetworkDisabled
	}
	currentScanID := m.snapshotMeta().ID
	m.mu.Lock()
	c, ok := m.confirmations[token]
	delete(m.confirmations, token)
	m.mu.Unlock()
	if !ok || time.Now().After(c.Expires) || c.ProfileID != p.ID || c.ProfileHash != profileHash(p) || c.SnapshotID != currentScanID || subtle.ConstantTimeCompare([]byte(c.Hash), []byte(hash)) != 1 {
		return AgentRunDTO{}, ErrConfirmation
	}
	body := c.Body
	key, err := m.modelKey(ctx, p)
	if err != nil && m.client != nil {
		return AgentRunDTO{}, err
	}
	id := randomID(16)
	run := store.AgentRun{ID: id, ScanID: currentScanID, ProfileID: p.ID, TargetOrigin: p.BaseURL, Model: p.Model, Status: "running", PayloadHash: hash, PayloadBytes: len(body), ConfirmedAt: time.Now(), StartedAt: time.Now()}
	if err := m.db.CreateAgentRun(ctx, run, body, "1"); err != nil {
		return AgentRunDTO{}, err
	}
	runCtx, cancel := context.WithCancel(context.Background())
	m.mu.Lock()
	m.cancels[id] = cancel
	m.mu.Unlock()
	go m.execute(runCtx, id, p, key, body)
	return AgentRunDTO{RunID: id, Status: "running"}, nil
}
func (m *AgentManager) execute(ctx context.Context, id string, p store.ModelProfile, key string, body []byte) {
	_ = m.db.AddAgentStep(context.Background(), store.AgentStep{RunID: id, Ordinal: 1, At: time.Now(), Kind: "request", Name: "调用模型", Detail: p.Model + " · " + p.BaseURL})
	result, err := m.client.Run(ctx, p, key, body)
	status := "completed"
	message := ""
	if err != nil {
		status = "failed"
		message = err.Error()
		_ = m.db.AddAgentStep(context.Background(), store.AgentStep{RunID: id, Ordinal: 2, At: time.Now(), Kind: "error", Name: "模型返回错误", Detail: message})
	} else {
		_ = m.db.AddAgentStep(context.Background(), store.AgentStep{RunID: id, Ordinal: 2, At: time.Now(), Kind: "response", Name: "巡视完成", Detail: "模型已返回只读观察报告"})
	}
	if errors.Is(err, context.Canceled) || ctx.Err() != nil {
		status = "cancelled"
		message = "cancelled"
	}
	_ = m.db.FinishAgentRun(context.Background(), id, status, result, message)
	m.mu.Lock()
	delete(m.cancels, id)
	m.mu.Unlock()
}
func (m *AgentManager) cancel(id string) error {
	m.mu.Lock()
	cancel := m.cancels[id]
	m.mu.Unlock()
	if cancel == nil {
		return ErrAgentRunNotFound
	}
	cancel()
	return nil
}
func (m *AgentManager) CancelAll() {
	m.mu.Lock()
	cs := make([]context.CancelFunc, 0, len(m.cancels))
	for _, c := range m.cancels {
		cs = append(cs, c)
	}
	m.confirmations = map[string]confirmation{}
	m.mu.Unlock()
	for _, c := range cs {
		c()
	}
}
func (m *AgentManager) result(ctx context.Context, id string) (AgentResultDTO, error) {
	run, err := m.db.AgentRun(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return AgentResultDTO{}, ErrAgentRunNotFound
	}
	if err != nil {
		return AgentResultDTO{}, err
	}
	out := AgentResultDTO{RunID: id, Status: run.Status, Error: run.Error}
	raw, err := m.db.AgentResult(ctx, id)
	if err == nil {
		out.Result = raw
	} else if !errors.Is(err, sql.ErrNoRows) {
		return AgentResultDTO{}, err
	}
	return out, nil
}
func randomID(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(fmt.Sprintf("random ID: %v", err))
	}
	return hex.EncodeToString(b)
}
