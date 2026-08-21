// Package bambuddy is a minimal client for BamBuddy, the Bambu Lab printer
// manager that runs on the shop LAN. It covers exactly the four calls Tensor
// needs to get a sliced plate onto a printer and watch it: upload a file, queue
// it, read the queue item, read the printer.
//
// It mirrors internal/integrations/shopify: a shared keep-alive http.Client, an
// APIError carrying the retry contract from internal/retry, and the credential
// passed as an argument rather than held on the struct - so it is never logged
// and never captured in a struct dump.
package bambuddy

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Optiminastic/tensor-core/internal/retry"
)

const (
	defaultRequestTimeout = 30 * time.Second
	// apiPrefix is BamBuddy's only API version prefix. Pinned deliberately: the
	// surface is large (584 paths) with no stated stability guarantee, so a
	// version bump should be a visible change here rather than a silent drift.
	apiPrefix = "/api/v1"
	// maxErrorBody caps how much of a failure response is read before being
	// summarised into an error message.
	maxErrorBody = 4 << 10
)

// Sentinels callers match with errors.Is. Each is wrapped by an APIError, so the
// human-readable message and the machine-readable cause travel together.
var (
	// ErrUnauthorized means the API key was rejected or lacks a scope. Terminal:
	// retrying with the same key cannot succeed. The likely cause is a key minted
	// without can_control_printer, which defaults to false.
	ErrUnauthorized = errors.New("bambuddy rejected the API key")

	// ErrRateLimited is a 429. Retryable, honouring Retry-After when present.
	ErrRateLimited = errors.New("bambuddy rate limited")

	// ErrFilamentDeficit is BamBuddy's 409 on starting a queue item whose assigned
	// spool cannot satisfy the required grams. Deliberately NOT retried and not
	// swallowed: it is a real shop-floor condition (load more filament, or accept
	// the risk with skip_filament_check) and only a human or an explicit policy
	// should decide which.
	ErrFilamentDeficit = errors.New("bambuddy reports a filament deficit")

	// ErrNotFound is a 404 - an unknown printer, queue item or library file.
	ErrNotFound = errors.New("bambuddy resource not found")
)

// retryPolicy governs transient failures. Slightly more patient than Shopify's:
// the calls cross a tunnel to the shop LAN, where a brief drop is normal.
var retryPolicy = retry.Policy{MaxAttempts: 3, BaseDelay: 500 * time.Millisecond, MaxDelay: 5 * time.Second}

// Client talks to one BamBuddy instance.
type Client struct {
	http *http.Client
	// baseURL is the instance root without the /api/v1 prefix, e.g.
	// "http://bambuddy.tail1234.ts.net:8000". Unexported so same-package tests can
	// point it at an httptest server, matching the Shopify client's test seam.
	baseURL string
}

// New builds a client against baseURL. A trailing slash is tolerated. The
// transport is cloned and given a small idle-connection pool: this client talks
// to exactly one host, and polling keeps the connection warm.
func New(baseURL string, timeout time.Duration) *Client {
	if timeout <= 0 {
		timeout = defaultRequestTimeout
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.MaxIdleConnsPerHost = 2
	return &Client{
		http:    &http.Client{Timeout: timeout, Transport: transport},
		baseURL: strings.TrimRight(baseURL, "/"),
	}
}

// APIError is a BamBuddy-side failure, safe to surface without leaking the API
// key. It carries a wrapped sentinel for errors.Is and the retry contract
// internal/retry looks for.
type APIError struct {
	msg        string
	err        error // wrapped cause, never rendered to a client
	status     int
	retryable  bool
	retryAfter time.Duration
}

func (e *APIError) Error() string             { return e.msg }
func (e *APIError) Unwrap() error             { return e.err }
func (e *APIError) Retryable() bool           { return e.retryable }
func (e *APIError) RetryAfter() time.Duration { return e.retryAfter }

// Status is the HTTP status that produced the error, or 0 for a transport error.
func (e *APIError) Status() int { return e.status }

func apiErr(format string, args ...any) *APIError {
	return &APIError{msg: fmt.Sprintf(format, args...)}
}

// url builds an absolute API URL from a path relative to /api/v1.
func (c *Client) url(path string) string {
	return c.baseURL + apiPrefix + path
}

// authorize applies the API key. BamBuddy accepts a bb_-prefixed key as either
// X-API-Key or a bearer token; the bearer form is used because it is the only
// scheme declared in the OpenAPI securitySchemes, so generated clients and this
// one stay consistent.
func authorize(req *http.Request, apiKey string) {
	req.Header.Set("Authorization", "Bearer "+apiKey)
}

// doJSON sends a request that expects a JSON response, retrying transient
// failures. out may be nil when the body is not needed.
func (c *Client) doJSON(ctx context.Context, method, path, apiKey string, body, out any) error {
	var encoded []byte
	if body != nil {
		var err error
		if encoded, err = json.Marshal(body); err != nil {
			return apiErr("could not encode the request for %s", path)
		}
	}
	return retry.Do(ctx, retryPolicy, func(ctx context.Context) error {
		var reader io.Reader
		if encoded != nil {
			// A fresh reader per attempt: a retry must not replay a drained body.
			reader = bytes.NewReader(encoded)
		}
		req, err := http.NewRequestWithContext(ctx, method, c.url(path), reader)
		if err != nil {
			return apiErr("could not build the request for %s", path)
		}
		if encoded != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		req.Header.Set("Accept", "application/json")
		authorize(req, apiKey)
		return c.send(req, path, out)
	})
}

// send performs one request and decodes it, mapping the response to the error
// taxonomy. Split from doJSON so the multipart upload can share it.
func (c *Client) send(req *http.Request, path string, out any) error {
	resp, err := c.http.Do(req)
	if err != nil {
		// Transport failures are retryable: the tunnel to the shop LAN dropping
		// briefly is the expected case, not a permanent condition.
		return &APIError{
			msg:       fmt.Sprintf("could not reach BamBuddy (%s)", path),
			err:       err,
			retryable: true,
		}
	}
	defer func() { _ = resp.Body.Close() }()

	if err := statusError(resp, path); err != nil {
		return err
	}
	if out == nil {
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return apiErr("BamBuddy returned an unreadable response for %s", path)
	}
	return nil
}

// statusError maps a response status to the taxonomy, or nil when it is a
// success. The server's own "detail" message is folded into the error text,
// which is what makes a filament deficit actionable.
func statusError(resp *http.Response, path string) error {
	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		return nil

	case resp.StatusCode == http.StatusTooManyRequests:
		return &APIError{
			msg:        "BamBuddy is rate limiting requests; retrying shortly",
			err:        ErrRateLimited,
			status:     resp.StatusCode,
			retryable:  true,
			retryAfter: parseRetryAfter(resp.Header.Get("Retry-After")),
		}

	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return &APIError{
			msg: fmt.Sprintf("BamBuddy rejected the API key for %s (check the key's scopes: "+
				"can_manage_library, can_queue, can_read_status, can_control_printer)", path),
			err:    ErrUnauthorized,
			status: resp.StatusCode,
		}

	case resp.StatusCode == http.StatusConflict:
		return &APIError{
			msg:    fmt.Sprintf("BamBuddy refused the print: %s", detailOf(resp)),
			err:    ErrFilamentDeficit,
			status: resp.StatusCode,
		}

	case resp.StatusCode == http.StatusNotFound:
		return &APIError{
			msg:    fmt.Sprintf("BamBuddy has no such resource (%s)", path),
			err:    ErrNotFound,
			status: resp.StatusCode,
		}

	case resp.StatusCode >= 500:
		// Server-side and worth another attempt.
		return &APIError{
			msg:       fmt.Sprintf("BamBuddy failed on %s (%d)", path, resp.StatusCode),
			status:    resp.StatusCode,
			retryable: true,
		}

	default:
		return &APIError{
			msg:    fmt.Sprintf("BamBuddy rejected %s (%d): %s", path, resp.StatusCode, detailOf(resp)),
			status: resp.StatusCode,
		}
	}
}

// detailOf reads FastAPI's {"detail": ...} from an error response. detail may be
// a string or a structured payload (the filament deficit is an object), so it is
// rendered generically rather than assumed to be a string.
func detailOf(resp *http.Response) string {
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxErrorBody))
	if err != nil || len(body) == 0 {
		return "no reason given"
	}
	var envelope struct {
		Detail json.RawMessage `json:"detail"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil || len(envelope.Detail) == 0 {
		return strings.TrimSpace(string(body))
	}
	var asString string
	if err := json.Unmarshal(envelope.Detail, &asString); err == nil {
		return asString
	}
	return strings.TrimSpace(string(envelope.Detail))
}

// parseRetryAfter reads the delay-seconds form of Retry-After. The HTTP-date
// form is ignored (BamBuddy does not send it) and yields zero, which leaves the
// retry loop on its own backoff.
func parseRetryAfter(v string) time.Duration {
	if v == "" {
		return 0
	}
	secs, err := strconv.Atoi(strings.TrimSpace(v))
	if err != nil || secs <= 0 {
		return 0
	}
	return time.Duration(secs) * time.Second
}
