package llm

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func testClient(t *testing.T, handler http.Handler, mutate func(*Config)) *Client {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	cfg := Config{BaseURL: srv.URL, Mode: ModeLocal, Model: "test", APIKey: "super-secret", HTTPClient: srv.Client(), Timeout: time.Second}
	if mutate != nil {
		mutate(&cfg)
	}
	client, err := NewClient(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func TestChatToolCallsAndBearer(t *testing.T) {
	client := testClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer super-secret" {
			t.Fatalf("authorization = %q", got)
		}
		if r.URL.Path != "/v1/chat/completions" {
			t.Fatalf("path = %q", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"id":"x","choices":[{"index":0,"message":{"role":"assistant","tool_calls":[{"id":"c1","type":"function","function":{"name":"overview","arguments":"{}"}}]},"finish_reason":"tool_calls"}]}`)
	}), nil)
	response, err := client.Chat(context.Background(), ChatRequest{Messages: []Message{{Role: "user", Content: "hello"}}})
	if err != nil {
		t.Fatal(err)
	}
	if got := response.Choices[0].Message.ToolCalls[0].Function.Name; got != "overview" {
		t.Fatalf("tool = %q", got)
	}
	if strings.Contains(strings.Join(mapValues(client.RedactedConfig()), " "), "super-secret") {
		t.Fatal("redacted config leaked key")
	}
}

func TestRetry429AtMostOnce(t *testing.T) {
	var calls atomic.Int32
	client := testClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if calls.Add(1) == 1 {
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"ok"}}]}`)
	}), nil)
	if _, err := client.Chat(context.Background(), ChatRequest{}); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 2 {
		t.Fatalf("calls = %d", calls.Load())
	}
}

func TestMalformedAndOversizedResponses(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		limit      int64
		want       error
	}{
		{"malformed", `{`, 100, nil}, {"oversized", strings.Repeat("x", 20), 10, ErrResponseTooLarge},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := testClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { io.WriteString(w, tc.body) }), func(c *Config) { c.MaxResponseBytes = tc.limit })
			_, err := client.Chat(context.Background(), ChatRequest{})
			if err == nil {
				t.Fatal("expected error")
			}
			if tc.want != nil && !errors.Is(err, tc.want) {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestCancellation(t *testing.T) {
	client := testClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() }), nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := client.Chat(ctx, ChatRequest{})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v", err)
	}
}

func TestCloudDialAllowsLoopbackProxy(t *testing.T) {
	t.Setenv("HTTPS_PROXY", "http://127.0.0.1:7890")
	t.Setenv("HTTP_PROXY", "http://127.0.0.1:7890")
	t.Setenv("ALL_PROXY", "")
	loopback := netip.MustParseAddr("127.0.0.1")
	if !cloudDialAllowed("127.0.0.1", "7890", loopback) {
		t.Fatal("expected loopback Clash proxy to be allowed")
	}
	if !cloudDialAllowed("localhost", "7890", loopback) {
		t.Fatal("expected localhost proxy alias to be allowed")
	}
	if cloudDialAllowed("127.0.0.1", "1080", loopback) {
		t.Fatal("did not expect a different proxy port")
	}
}

func TestCloudDialRejectsUnproxiedLoopback(t *testing.T) {
	t.Setenv("HTTPS_PROXY", "")
	t.Setenv("HTTP_PROXY", "")
	t.Setenv("ALL_PROXY", "")
	if cloudDialAllowed("127.0.0.1", "443", netip.MustParseAddr("127.0.0.1")) {
		t.Fatal("cloud mode must not dial loopback without a configured proxy")
	}
	if !cloudDialAllowed("api.deepseek.com", "443", netip.MustParseAddr("1.1.1.1")) {
		t.Fatal("public provider address should be allowed")
	}
}

func TestValidateBaseURL(t *testing.T) {
	valid := []struct {
		url  string
		mode Mode
	}{{"https://api.example.com", ModeCloud}, {"http://localhost:11434", ModeLocal}, {"http://127.0.0.1:1234/v1", ModeLocal}, {"http://[::1]:1234", ModeLocal}}
	for _, tc := range valid {
		if _, err := ValidateBaseURL(tc.url, tc.mode); err != nil {
			t.Errorf("%s: %v", tc.url, err)
		}
	}
	invalid := []struct {
		url  string
		mode Mode
	}{{"http://api.example.com", ModeCloud}, {"https://127.0.0.1", ModeCloud}, {"https://10.0.0.1", ModeCloud}, {"http://192.168.1.2", ModeLocal}, {"https://user:pass@example.com", ModeCloud}}
	for _, tc := range invalid {
		if _, err := ValidateBaseURL(tc.url, tc.mode); err == nil {
			t.Errorf("accepted %s", tc.url)
		}
	}
}

func TestCrossHostRedirectRejected(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer target.Close()
	client := testClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target.URL, http.StatusFound) }), nil)
	if _, err := client.Chat(context.Background(), ChatRequest{}); err == nil || !strings.Contains(err.Error(), "cross-host") {
		t.Fatalf("error = %v", err)
	}
}

func mapValues(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for _, v := range m {
		out = append(out, v)
	}
	return out
}
