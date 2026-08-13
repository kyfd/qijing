package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// ModelProfile contains non-secret provider configuration. API keys are never stored here.
type ModelProfile struct {
	ID        string    `json:"id"`
	Provider  string    `json:"provider"`
	BaseURL   string    `json:"base_url"`
	Model     string    `json:"model"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type AgentRun struct {
	ID           string    `json:"id"`
	ScanID       string    `json:"scan_id"`
	ProfileID    string    `json:"profile_id"`
	TargetOrigin string    `json:"target_origin"`
	Model        string    `json:"model"`
	Status       string    `json:"status"`
	PayloadHash  string    `json:"payload_hash"`
	PayloadBytes int       `json:"payload_bytes"`
	ConfirmedAt  time.Time `json:"confirmed_at"`
	StartedAt    time.Time `json:"started_at"`
	EndedAt      time.Time `json:"ended_at,omitempty"`
	Error        string    `json:"error,omitempty"`
}

type AgentStep struct {
	ID      int64     `json:"id"`
	RunID   string    `json:"run_id"`
	Ordinal int       `json:"ordinal"`
	At      time.Time `json:"at"`
	Kind    string    `json:"kind"`
	Name    string    `json:"name"`
	Detail  string    `json:"detail"`
}

func (s *Store) ModelProfile(ctx context.Context, id string) (ModelProfile, error) {
	var p ModelProfile
	var created, updated string
	err := s.db.QueryRowContext(ctx, `SELECT id,provider,base_url,model,created_at,updated_at FROM model_profiles WHERE id=?`, id).Scan(&p.ID, &p.Provider, &p.BaseURL, &p.Model, &created, &updated)
	p.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
	p.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updated)
	return p, err
}

func (s *Store) SaveModelProfile(ctx context.Context, p ModelProfile) error {
	now := time.Now().UTC()
	if p.CreatedAt.IsZero() {
		p.CreatedAt = now
	}
	p.UpdatedAt = now
	_, err := s.db.ExecContext(ctx, `INSERT INTO model_profiles(id,provider,base_url,model,created_at,updated_at) VALUES(?,?,?,?,?,?) ON CONFLICT(id) DO UPDATE SET provider=excluded.provider,base_url=excluded.base_url,model=excluded.model,updated_at=excluded.updated_at`, p.ID, p.Provider, p.BaseURL, p.Model, formatTime(p.CreatedAt), formatTime(p.UpdatedAt))
	return err
}

func (s *Store) NetworkEnabled(ctx context.Context, profileID string) (bool, error) {
	var enabled bool
	err := s.db.QueryRowContext(ctx, `SELECT enabled FROM network_consents WHERE profile_id=?`, profileID).Scan(&enabled)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return enabled, err
}

func (s *Store) SetNetworkEnabled(ctx context.Context, profileID string, enabled bool) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO network_consents(profile_id,enabled,updated_at) VALUES(?,?,?) ON CONFLICT(profile_id) DO UPDATE SET enabled=excluded.enabled,updated_at=excluded.updated_at`, profileID, enabled, formatTime(time.Now()))
	return err
}

func (s *Store) CreateAgentRun(ctx context.Context, run AgentRun, payload []byte, schema string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	_, err = tx.ExecContext(ctx, `INSERT INTO agent_runs(id,scan_id,profile_id,target_origin,model,status,payload_hash,payload_bytes,confirmed_at,started_at,ended_at,error) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`, run.ID, run.ScanID, run.ProfileID, run.TargetOrigin, run.Model, run.Status, run.PayloadHash, run.PayloadBytes, formatTime(run.ConfirmedAt), formatTime(run.StartedAt), "", run.Error)
	if err == nil {
		_, err = tx.ExecContext(ctx, `INSERT INTO agent_payloads(run_id,schema_version,payload_json) VALUES(?,?,?)`, run.ID, schema, payload)
	}
	if err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) FinishAgentRun(ctx context.Context, id, status string, result []byte, message string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	_, err = tx.ExecContext(ctx, `UPDATE agent_runs SET status=?,ended_at=?,error=? WHERE id=?`, status, formatTime(time.Now()), message, id)
	if err == nil && result != nil {
		_, err = tx.ExecContext(ctx, `INSERT INTO agent_responses(run_id,response_json) VALUES(?,?) ON CONFLICT(run_id) DO UPDATE SET response_json=excluded.response_json`, id, result)
	}
	if err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) AgentRun(ctx context.Context, id string) (AgentRun, error) {
	var r AgentRun
	var confirmed, started, ended string
	err := s.db.QueryRowContext(ctx, `SELECT id,scan_id,profile_id,target_origin,model,status,payload_hash,payload_bytes,confirmed_at,started_at,ended_at,error FROM agent_runs WHERE id=?`, id).Scan(&r.ID, &r.ScanID, &r.ProfileID, &r.TargetOrigin, &r.Model, &r.Status, &r.PayloadHash, &r.PayloadBytes, &confirmed, &started, &ended, &r.Error)
	r.ConfirmedAt, _ = time.Parse(time.RFC3339Nano, confirmed)
	r.StartedAt, _ = time.Parse(time.RFC3339Nano, started)
	r.EndedAt, _ = time.Parse(time.RFC3339Nano, ended)
	return r, err
}

func (s *Store) AgentResult(ctx context.Context, id string) ([]byte, error) {
	var result []byte
	err := s.db.QueryRowContext(ctx, `SELECT response_json FROM agent_responses WHERE run_id=?`, id).Scan(&result)
	return result, err
}

func (s *Store) AddAgentStep(ctx context.Context, step AgentStep) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO agent_steps(run_id,ordinal,at,kind,name,detail) VALUES(?,?,?,?,?,?)`, step.RunID, step.Ordinal, formatTime(step.At), step.Kind, step.Name, step.Detail)
	return err
}

func (s *Store) AgentAudits(ctx context.Context, runID string, limit int) ([]AgentStep, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id,run_id,ordinal,at,kind,name,detail FROM agent_steps WHERE run_id=? ORDER BY ordinal LIMIT ?`, runID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AgentStep
	for rows.Next() {
		var v AgentStep
		var at string
		if err := rows.Scan(&v.ID, &v.RunID, &v.Ordinal, &at, &v.Kind, &v.Name, &v.Detail); err != nil {
			return nil, err
		}
		v.At, _ = time.Parse(time.RFC3339Nano, at)
		out = append(out, v)
	}
	return out, rows.Err()
}

func (s *Store) IgnoreRecommendation(ctx context.Context, scanID, id string) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO ignored_recommendations(recommendation_id,scan_id,ignored_at) VALUES(?,?,?) ON CONFLICT(recommendation_id) DO UPDATE SET scan_id=excluded.scan_id,ignored_at=excluded.ignored_at`, id, scanID, formatTime(time.Now()))
	return err
}

func (s *Store) IgnoredRecommendations(ctx context.Context, scanID string) (map[string]bool, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT recommendation_id FROM ignored_recommendations WHERE scan_id=?`, scanID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out[id] = true
	}
	return out, rows.Err()
}
