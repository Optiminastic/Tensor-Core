// Package openrouter is a minimal client for the one call Tensor needs: a single
// chat-completion that forces structured JSON out via a function tool. OpenRouter
// exposes an OpenAI-compatible API and proxies many models (incl. Claude), chosen
// per request by model slug. It mirrors internal/integrations/shopify (shared
// keep-alive client, custom APIError, retry on rate limiting). The API key is an
// argument, never stored on the client and never logged.
package openrouter

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/Optiminastic/tensor-core/internal/retry"
)

const (
	defaultRequestTimeout = 60 * time.Second
	completionsEndpoint   = "https://openrouter.ai/api/v1/chat/completions"
	defaultMaxTokens      = 4096
)

// ErrRateLimited is wrapped by the APIError returned on 429 so callers and the
// retry loop can recognise a transient rate limit.
var ErrRateLimited = errors.New("openrouter rate limited")

// retryPolicy retries only rate-limit responses (the request was rejected before
// producing output, so a retry cannot duplicate side effects).
var retryPolicy = retry.Policy{MaxAttempts: 3, BaseDelay: time.Second, MaxDelay: 8 * time.Second}

// Tool is the single output function that forces the model to answer as JSON
// matching Parameters (a JSON Schema object).
type Tool struct {
	Name        string
	Description string
	Parameters  map[string]any
}

// Request is one structured completion: the model slug, the system + user prompts,
// and the output tool whose arguments the model must fill.
type Request struct {
	Model     string
	MaxTokens int
	System    string
	User      string
	Tool      Tool
}

// Client talks to the OpenRouter chat-completions API.
type Client struct {
	http *http.Client
	// baseURL overrides the endpoint (tests point it at an httptest server). Empty
	// means the real https://openrouter.ai endpoint.
	baseURL string
}

// New builds a client with the given per-request timeout (zero uses the default).
// It carries a keep-alive transport; construct it once and share it.
func New(timeout time.Duration) *Client {
	if timeout <= 0 {
		timeout = defaultRequestTimeout
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.MaxIdleConnsPerHost = 2
	return &Client{http: &http.Client{Timeout: timeout, Transport: transport}}
}

// APIError is an OpenRouter-side failure (transport or HTTP status), safe to
// surface without leaking the key. It can carry a wrapped cause and retry hints.
type APIError struct {
	msg        string
	err        error
	retryable  bool
	retryAfter time.Duration
}

func (e *APIError) Error() string             { return e.msg }
func (e *APIError) Unwrap() error             { return e.err }
func (e *APIError) Retryable() bool           { return e.retryable }
func (e *APIError) RetryAfter() time.Duration { return e.retryAfter }

func apiErr(format string, args ...any) *APIError {
	return &APIError{msg: fmt.Sprintf(format, args...)}
}

// Complete sends the request and returns the tool call's arguments - the model's
// answer as structured JSON. Forcing tool_choice guarantees exactly one call, so
// the caller can unmarshal the returned bytes straight into its result type.
func (c *Client) Complete(ctx context.Context, key string, req Request) (json.RawMessage, error) {
	if key == "" {
		return nil, apiErr("openrouter: missing API key")
	}
	maxTokens := req.MaxTokens
	if maxTokens <= 0 {
		maxTokens = defaultMaxTokens
	}
	body, err := json.Marshal(map[string]any{
		"model":      req.Model,
		"max_tokens": maxTokens,
		"messages": []any{
			map[string]any{"role": "system", "content": req.System},
			map[string]any{"role": "user", "content": req.User},
		},
		"tools": []any{map[string]any{
			"type": "function",
			"function": map[string]any{
				"name":        req.Tool.Name,
				"description": req.Tool.Description,
				"parameters":  req.Tool.Parameters,
			},
		}},
		"tool_choice": map[string]any{
			"type":     "function",
			"function": map[string]any{"name": req.Tool.Name},
		},
	})
	if err != nil {
		return nil, err
	}

	var out json.RawMessage
	runErr := retry.Do(ctx, retryPolicy, func(ctx context.Context) error {
		var innerErr error
		out, innerErr = c.doOnce(ctx, key, body)
		return innerErr
	})
	if runErr != nil {
		return nil, runErr
	}
	return out, nil
}

// doOnce performs a single chat-completion request and extracts the forced tool
// call's arguments. A 429 becomes a retryable APIError; other non-2xx is terminal.
func (c *Client) doOnce(ctx context.Context, key string, body []byte) (json.RawMessage, error) {
	endpoint := completionsEndpoint
	if c.baseURL != "" {
		endpoint = c.baseURL
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("content-type", "application/json")
	req.Header.Set("authorization", "Bearer "+key)
	// Optional attribution headers OpenRouter recommends; harmless if ignored.
	req.Header.Set("x-title", "Tensor")

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, &APIError{msg: fmt.Sprintf("could not reach OpenRouter: %v", err), err: err}
	}
	defer func() { _ = resp.Body.Close() }()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, apiErr("could not read OpenRouter response: %v", err)
	}

	if resp.StatusCode == http.StatusTooManyRequests {
		return nil, &APIError{
			msg:        "OpenRouter is rate limiting requests; please retry shortly",
			err:        ErrRateLimited,
			retryable:  true,
			retryAfter: parseRetryAfter(resp.Header.Get("retry-after")),
		}
	}
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return nil, apiErr("OpenRouter rejected the API key")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, apiErr("OpenRouter request failed (%d): %s", resp.StatusCode, errorMessage(raw))
	}

	var decoded struct {
		Choices []struct {
			Message struct {
				ToolCalls []struct {
					Function struct {
						Name      string `json:"name"`
						Arguments string `json:"arguments"`
					} `json:"function"`
				} `json:"tool_calls"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return nil, apiErr("could not decode OpenRouter response: %v", err)
	}
	if len(decoded.Choices) == 0 || len(decoded.Choices[0].Message.ToolCalls) == 0 {
		return nil, apiErr("OpenRouter returned no tool call")
	}
	// The function arguments are a JSON string; that string IS the structured
	// result the caller unmarshals.
	args := decoded.Choices[0].Message.ToolCalls[0].Function.Arguments
	if args == "" {
		return nil, apiErr("OpenRouter returned an empty tool call")
	}
	return json.RawMessage(args), nil
}

// errorMessage pulls the human-readable message from an OpenRouter error body,
// falling back to empty when the shape is unexpected.
func errorMessage(raw []byte) string {
	var e struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	_ = json.Unmarshal(raw, &e)
	return e.Error.Message
}

// parseRetryAfter reads a Retry-After header expressed in seconds. Absent or
// unparseable yields zero, letting the retry loop use its own backoff.
func parseRetryAfter(v string) time.Duration {
	if v == "" {
		return 0
	}
	secs, err := strconv.ParseFloat(v, 64)
	if err != nil || secs <= 0 {
		return 0
	}
	return time.Duration(secs * float64(time.Second))
}
