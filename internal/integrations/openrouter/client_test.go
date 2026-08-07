package openrouter

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestCompleteExtractsToolArguments(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("authorization"); got != "Bearer test-key" {
			t.Errorf("authorization header = %q, want Bearer test-key", got)
		}
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}
		_, _ = w.Write([]byte(`{"choices":[{"finish_reason":"tool_calls","message":{"tool_calls":` +
			`[{"function":{"name":"advise","arguments":"{\"verdict\":\"green\"}"}}]}}]}`))
	}))
	defer srv.Close()

	c := New(5 * time.Second)
	c.baseURL = srv.URL
	out, err := c.Complete(context.Background(), "test-key", Request{
		Model: "anthropic/claude-3.7-sonnet",
		Tool:  Tool{Name: "advise", Parameters: map[string]any{"type": "object"}},
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if string(out) != `{"verdict":"green"}` {
		t.Errorf("tool arguments = %s, want {\"verdict\":\"green\"}", out)
	}
}

func TestCompleteMapsUnauthorized(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"message":"invalid key"}}`))
	}))
	defer srv.Close()

	c := New(5 * time.Second)
	c.baseURL = srv.URL
	_, err := c.Complete(context.Background(), "test-key", Request{Model: "x", Tool: Tool{Name: "advise"}})
	if err == nil {
		t.Fatal("expected an error for 401")
	}
}

func TestCompleteErrorsOnNoToolCall(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"choices":[{"finish_reason":"stop","message":{"content":"hi"}}]}`))
	}))
	defer srv.Close()

	c := New(5 * time.Second)
	c.baseURL = srv.URL
	_, err := c.Complete(context.Background(), "test-key", Request{Model: "x", Tool: Tool{Name: "advise"}})
	if err == nil {
		t.Fatal("expected an error when the model returns no tool call")
	}
}

func TestCompleteRequiresKey(t *testing.T) {
	c := New(0)
	if _, err := c.Complete(context.Background(), "", Request{Model: "x", Tool: Tool{Name: "advise"}}); err == nil {
		t.Fatal("expected an error when the API key is empty")
	}
}
