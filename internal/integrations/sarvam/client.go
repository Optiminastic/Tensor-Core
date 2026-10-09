// Package sarvam places outbound voice calls through Sarvam's Voice Agents.
//
// One endpoint - Instant Outbound - which turns a POST into a live phone
// conversation: Sarvam dials the customer, runs the agent, and hands back an
// attempt_id. Everything per-customer travels in agent_variables, so the agent
// answers already knowing who it is talking to and why.
//
// THE REQUEST SHAPE IS NOT THE ONE IN SARVAM'S QUICKSTART. That example does
// not validate; measured against the live API, the differences are:
//
//	app_version        an INTEGER (2), not the string "v2"
//	connection_config  NESTED INSIDE app_config, not a sibling of it
//	the customer       user_config.user_phone_number, not customer_number
//	agent_variables    inside APP_CONFIG
//
// The first three were each a 422 naming the field, which is how they were
// found. agent_variables was not: putting it in the wrong object is accepted,
// the call is placed, and the agent simply has no values - so it was found by
// a customer not hearing their own name. Only the TOP level of the body is
// validated strictly; an unknown key inside app_config or user_config is
// dropped in silence.
//
// The struct below is the shape that passes AND personalises. Checked against
// docs.sarvam.ai/api-reference/instant-outbound/create.
//
// TWO KINDS OF KEY, and only one works. Sarvam issues keys for its model APIs
// (sk_...) at key-management, and separate keys for Voice Agents (sk_samvaad_...)
// from the Voice Agents console. A model-API key here answers 401 "Invalid API
// key format" rather than anything that names the real problem.
package sarvam

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const defaultBaseURL = "https://apps.sarvam.ai"

// Config is what the server knows about Sarvam.
//
// Six values rather than one, because an outbound call names four things that
// are configured in Sarvam's console and cannot be guessed: which agent, which
// version of it, which telephony connection, and which number to dial FROM.
type Config struct {
	BaseURL     string
	APIKey      string
	OrgID       string
	WorkspaceID string
	AgentID     string
	// AgentVersion is the agent revision to run. An integer: Sarvam's console
	// shows it as "v2" and its API rejects that string.
	AgentVersion int
	// ConnectionID is the telephony connection (a Plivo or Vobiz account
	// onboarded in Sarvam), and AgentPhoneNumber is the number on it that the
	// call dials from. The number must be imported onto THAT connection, or
	// the API answers 404 naming both.
	ConnectionID     string
	AgentPhoneNumber string
}

// Client talks to Sarvam for one workspace.
type Client struct {
	cfg  Config
	http *http.Client
}

// New builds a client, or nil when Sarvam is not configured.
//
// Nil rather than an error, checked with Configured: a Tensor that does not
// ring customers is the normal case and must still start and serve everything
// else.
func New(cfg Config) *Client {
	for _, required := range []string{
		cfg.APIKey, cfg.OrgID, cfg.WorkspaceID,
		cfg.AgentID, cfg.ConnectionID, cfg.AgentPhoneNumber,
	} {
		if strings.TrimSpace(required) == "" {
			return nil
		}
	}
	if cfg.BaseURL = strings.TrimSuffix(strings.TrimSpace(cfg.BaseURL), "/"); cfg.BaseURL == "" {
		cfg.BaseURL = defaultBaseURL
	}
	if cfg.AgentVersion <= 0 {
		cfg.AgentVersion = 1
	}
	return &Client{
		cfg: cfg,
		// The request returns once Sarvam has accepted the call, not when
		// anybody answers, so this bounds an API round trip and nothing more.
		http: &http.Client{Timeout: 30 * time.Second},
	}
}

// Configured reports whether calls can be placed.
func (c *Client) Configured() bool { return c != nil }

// AgentPhoneNumber is the number calls are placed from, for display.
func (c *Client) AgentPhoneNumber() string {
	if c == nil {
		return ""
	}
	return c.cfg.AgentPhoneNumber
}

// CallRequest is one call to one customer.
type CallRequest struct {
	// CustomerNumber in E.164, the number Sarvam dials.
	CustomerNumber string
	// Variables are the agent's own variables, by the names defined on the
	// agent in Sarvam's console. A name that does not exist there is ignored
	// silently, and a name the prompt uses but the call omits leaves the agent
	// saying the literal placeholder down the phone - so these are the agent's
	// contract, not a free-form bag.
	Variables map[string]string
}

// CallResult is Sarvam's acknowledgement.
type CallResult struct {
	// AttemptID is the correlation key for everything that follows: the
	// completion webhook carries the same value, and the call logs are found
	// by it.
	AttemptID string `json:"attempt_id"`
}

type appConfig struct {
	AppID            string           `json:"app_id"`
	AppVersion       int              `json:"app_version"`
	AppType          string           `json:"app_type"`
	ConnectionConfig connectionConfig `json:"connection_config"`
	// AgentVariables lives HERE, beside the agent it personalises - not in
	// user_config, where it is accepted and ignored.
	AgentVariables map[string]string `json:"agent_variables,omitempty"`
}

type connectionConfig struct {
	ConnectionID     string `json:"connection_id"`
	AgentPhoneNumber string `json:"agent_phone_number"`
}

type userConfig struct {
	UserPhoneNumber string `json:"user_phone_number"`
}

type callBody struct {
	AppConfig  appConfig  `json:"app_config"`
	UserConfig userConfig `json:"user_config"`
}

// Call places one outbound call and returns Sarvam's attempt id.
//
// The caller stores that id against whatever the call is about BEFORE anything
// rings, because it is the only thing tying the completion webhook back to a
// customer. Correlating on the phone number instead collides the moment two
// people share one, or the same person is called twice.
func (c *Client) Call(ctx context.Context, req CallRequest) (CallResult, error) {
	if !c.Configured() {
		return CallResult{}, fmt.Errorf("Sarvam is not configured on this service")
	}
	number := strings.TrimSpace(req.CustomerNumber)
	if number == "" {
		return CallResult{}, fmt.Errorf("a call needs a phone number")
	}

	body, err := json.Marshal(callBody{
		AppConfig: appConfig{
			AppID:      c.cfg.AgentID,
			AppVersion: c.cfg.AgentVersion,
			AppType:    "agent",
			ConnectionConfig: connectionConfig{
				ConnectionID:     c.cfg.ConnectionID,
				AgentPhoneNumber: c.cfg.AgentPhoneNumber,
			},
			AgentVariables: req.Variables,
		},
		UserConfig: userConfig{UserPhoneNumber: number},
	})
	if err != nil {
		return CallResult{}, fmt.Errorf("build the call request: %w", err)
	}

	url := fmt.Sprintf("%s/api/outbounds/v1/orgs/%s/workspaces/%s/outbounds",
		c.cfg.BaseURL, c.cfg.OrgID, c.cfg.WorkspaceID)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return CallResult{}, err
	}
	httpReq.Header.Set("X-API-Key", c.cfg.APIKey)
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(httpReq)
	if err != nil {
		return CallResult{}, fmt.Errorf("reach Sarvam: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	// Read first, decode second: Sarvam's errors name the field or the missing
	// number, and "unexpected status 404" while discarding that sentence is
	// how an afternoon disappears.
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return CallResult{}, fmt.Errorf("read Sarvam's reply: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return CallResult{}, fmt.Errorf("Sarvam refused the call: %s", reason(raw, resp.StatusCode))
	}

	var out CallResult
	if err := json.Unmarshal(raw, &out); err != nil {
		return CallResult{}, fmt.Errorf("decode Sarvam's reply: %w", err)
	}
	if out.AttemptID == "" {
		return CallResult{}, fmt.Errorf("Sarvam accepted the call but named no attempt id")
	}
	return out, nil
}

// reason pulls the human sentence out of a Sarvam error body.
//
// Their errors nest it: {"error":{"message":...,"data":{"details":...}}}, and
// details is the useful half - "Agent phone number '+91...' not found under
// org ... connection ..." says exactly what to go and fix.
func reason(raw []byte, status int) string {
	var envelope struct {
		Error struct {
			Message string `json:"message"`
			Data    struct {
				Details string `json:"details"`
			} `json:"data"`
		} `json:"error"`
	}
	if err := json.Unmarshal(raw, &envelope); err == nil {
		switch {
		case envelope.Error.Data.Details != "":
			return envelope.Error.Data.Details
		case envelope.Error.Message != "":
			return envelope.Error.Message
		}
	}
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" {
		return fmt.Sprintf("HTTP %d", status)
	}
	if len(trimmed) > 300 {
		trimmed = trimmed[:300]
	}
	return trimmed
}
