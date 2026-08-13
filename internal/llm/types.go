package llm

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"
)

const DefaultMaxResponseBytes int64 = 4 << 20

var (
	ErrInvalidURL       = errors.New("invalid provider URL")
	ErrUnsafeAddress    = errors.New("unsafe provider address")
	ErrResponseTooLarge = errors.New("LLM response too large")
	ErrRateLimited      = errors.New("LLM rate limited")
)

type Mode string

const (
	ModeCloud Mode = "cloud"
	ModeLocal Mode = "local"
)

type Config struct {
	BaseURL          string
	APIKey           string
	Model            string
	Mode             Mode
	Timeout          time.Duration
	MaxResponseBytes int64
	MaxOutputTokens  int
	HTTPClient       *http.Client
}

type Message struct {
	Role       string     `json:"role"`
	Content    string     `json:"content,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
}

type Tool struct {
	Type     string       `json:"type"`
	Function ToolFunction `json:"function"`
}

type ToolFunction struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters"`
}

type ToolCall struct {
	ID       string           `json:"id"`
	Type     string           `json:"type"`
	Function FunctionToolCall `json:"function"`
}

type FunctionToolCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type ChatRequest struct {
	Messages        []Message `json:"messages"`
	Tools           []Tool    `json:"tools,omitempty"`
	ToolChoice      any       `json:"tool_choice,omitempty"`
	MaxOutputTokens int       `json:"max_output_tokens,omitempty"`
}

type ChatResponse struct {
	ID      string   `json:"id"`
	Choices []Choice `json:"choices"`
	Usage   Usage    `json:"usage"`
}

type Choice struct {
	Index        int     `json:"index"`
	Message      Message `json:"message"`
	FinishReason string  `json:"finish_reason"`
}

type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

type APIError struct {
	StatusCode int
	Code       string
	Message    string
}

func (e *APIError) Error() string {
	if e.Message == "" {
		return fmt.Sprintf("LLM request failed with HTTP %d", e.StatusCode)
	}
	return fmt.Sprintf("LLM request failed with HTTP %d: %s", e.StatusCode, e.Message)
}
