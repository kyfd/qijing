package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type Client struct {
	endpoint         *url.URL
	apiKey           string
	model            string
	maxOutputTokens  int
	maxResponseBytes int64
	httpClient       *http.Client
}

func NewClient(cfg Config) (*Client, error) {
	base, err := ValidateBaseURL(cfg.BaseURL, cfg.Mode)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(cfg.Model) == "" {
		return nil, errors.New("model is required")
	}
	endpoint := *base
	path := strings.TrimRight(endpoint.Path, "/")
	if strings.HasSuffix(path, "/v1") {
		endpoint.Path = path + "/chat/completions"
	} else if strings.HasSuffix(path, "/chat/completions") {
		endpoint.Path = path
	} else {
		endpoint.Path = path + "/v1/chat/completions"
	}
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	limit := cfg.MaxResponseBytes
	if limit <= 0 {
		limit = DefaultMaxResponseBytes
	}
	maxTokens := cfg.MaxOutputTokens
	if maxTokens <= 0 {
		maxTokens = 2048
	}
	baseClient := cfg.HTTPClient
	transport := http.DefaultTransport.(*http.Transport).Clone()
	if baseClient != nil && baseClient.Transport != nil {
		custom, ok := baseClient.Transport.(*http.Transport)
		if !ok {
			return nil, errors.New("custom HTTP transport must be *http.Transport")
		}
		transport = custom.Clone()
	}
	transport.DialContext = validatingDialer(cfg.Mode, nil)
	if cfg.Mode == ModeLocal {
		// Local providers must stay on loopback; do not send them through a system proxy.
		transport.Proxy = nil
	}
	client := &http.Client{Timeout: timeout}
	if baseClient != nil {
		*client = *baseClient
		if client.Timeout <= 0 {
			client.Timeout = timeout
		}
	}
	client.Transport = transport
	client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) == 0 || !sameHost(req.URL, via[0].URL) {
			return errors.New("cross-host redirect refused")
		}
		if len(via) >= 5 {
			return errors.New("too many redirects")
		}
		return nil
	}
	return &Client{endpoint: &endpoint, apiKey: cfg.APIKey, model: cfg.Model, maxOutputTokens: maxTokens, maxResponseBytes: limit, httpClient: client}, nil
}

func sameHost(a, b *url.URL) bool {
	return strings.EqualFold(a.Scheme, b.Scheme) && strings.EqualFold(a.Hostname(), b.Hostname()) && effectivePort(a) == effectivePort(b)
}

func effectivePort(u *url.URL) string {
	if u.Port() != "" {
		return u.Port()
	}
	if u.Scheme == "https" {
		return "443"
	}
	return "80"
}

func (c *Client) Chat(ctx context.Context, request ChatRequest) (ChatResponse, error) {
	if request.MaxOutputTokens <= 0 || request.MaxOutputTokens > c.maxOutputTokens {
		request.MaxOutputTokens = c.maxOutputTokens
	}
	body, err := json.Marshal(struct {
		Model string `json:"model"`
		ChatRequest
	}{Model: c.model, ChatRequest: request})
	if err != nil {
		return ChatResponse{}, fmt.Errorf("encode LLM request: %w", err)
	}
	for attempt := 0; attempt < 2; attempt++ {
		response, retry, err := c.do(ctx, body)
		if !retry || attempt == 1 || err == nil {
			return response, err
		}
		select {
		case <-ctx.Done():
			return ChatResponse{}, ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
	return ChatResponse{}, ErrRateLimited
}

func (c *Client) do(ctx context.Context, body []byte) (ChatResponse, bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint.String(), bytes.NewReader(body))
	if err != nil {
		return ChatResponse{}, false, err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return ChatResponse{}, false, err
	}
	defer resp.Body.Close()
	limited := io.LimitReader(resp.Body, c.maxResponseBytes+1)
	data, err := io.ReadAll(limited)
	if err != nil {
		return ChatResponse{}, false, fmt.Errorf("read LLM response: %w", err)
	}
	if int64(len(data)) > c.maxResponseBytes {
		return ChatResponse{}, false, ErrResponseTooLarge
	}
	if resp.StatusCode == http.StatusTooManyRequests {
		return ChatResponse{}, true, ErrRateLimited
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var envelope struct {
			Error struct {
				Code    any    `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		_ = json.Unmarshal(data, &envelope)
		return ChatResponse{}, false, &APIError{StatusCode: resp.StatusCode, Code: fmt.Sprint(envelope.Error.Code), Message: envelope.Error.Message}
	}
	var result ChatResponse
	if err := json.Unmarshal(data, &result); err != nil {
		return ChatResponse{}, false, fmt.Errorf("decode LLM response: %w", err)
	}
	return result, false, nil
}

// RedactedConfig returns diagnostics safe to log or expose to a UI.
func (c *Client) RedactedConfig() map[string]string {
	return map[string]string{"endpoint": c.endpoint.String(), "model": c.model, "has_api_key": strconv.FormatBool(c.apiKey != "")}
}
