package sarvam

// Reading back what the agent actually did.
//
// Two endpoints under a DIFFERENT service from the one that places calls:
// /api/analytics/v1/{org}/{workspace}/{app}/... rather than /api/outbounds/.
// Attempts lists the calls; transcripts gives one call's turns, addressed by
// the interaction id the attempt carries.
//
// The chain matters because the two ids are not interchangeable. Placing a
// call returns an ATTEMPT id and nothing else. The recording and the transcript
// are addressed by INTERACTION id, which only the attempts listing knows. So a
// call stored at dial time can only be joined to its transcript by looking it
// up here first.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
)

// Attempt is one call Sarvam placed, as its analytics sees it.
type Attempt struct {
	AttemptID string `json:"attempt_id"`
	// InteractionID addresses the transcript and the recording. Note the
	// shape: "20261007/8dfc36f1-13:19:48-ea3d34fe" - it carries slashes and
	// colons, so it must be path-escaped, never interpolated raw.
	InteractionID string `json:"interaction_id"`
	// ConnectivityStatus is "connected" or not; FailureReason says why not.
	// There is no single "outcome" field, which is why the page shows both.
	ConnectivityStatus string  `json:"connectivity_status"`
	FailureReason      string  `json:"failure_reason"`
	EndedBy            string  `json:"ended_by"`
	DurationSeconds    float64 `json:"duration_in_seconds"`
	StartDatetime      string  `json:"start_datetime"`
	LanguageName       string  `json:"language_name"`
	NumMessages        int     `json:"num_messages"`
	ChannelDirection   string  `json:"channel_direction"`
	// UserContact is the number in full; UserContactMasked is "+91******1465".
	// The full one is what joins a call back to the checkout it came from.
	UserContact       string `json:"user_contact"`
	UserContactMasked string `json:"user_contact_masked"`
	// IsDebugCall marks a call placed from Sarvam's own console rather than by
	// Tensor. Those carry an EMAIL in user_contact instead of a number, which
	// is why a log listing cannot assume it is looking at a phone.
	IsDebugCall int `json:"is_debug_call"`
	// AudioURL is the recording.
	AudioURL string `json:"audio_url"`
	// AgentVariables carries both what we sent IN (customer_name, cart_value)
	// and what the agent wrote OUT - call_summary and call_disposition, which
	// Sarvam computes itself. Typed as strings because every value this agent
	// declares is one.
	AgentVariables map[string]string `json:"-"`
	// RawVariables is the wire shape. Sarvam types this map as string-to-ANY,
	// so a numeric or null value would fail a map[string]string decode and
	// take the whole listing down with it.
	RawVariables map[string]any `json:"agent_variables"`
}

// Summary is the agent's own account of the call, if it wrote one.
func (a Attempt) Summary() string { return a.AgentVariables["call_summary"] }

// Disposition is the agent's own outcome - "no_response", and so on.
func (a Attempt) Disposition() string { return a.AgentVariables["call_disposition"] }

// TranscriptTurn is one line of the conversation.
type TranscriptTurn struct {
	TurnID int `json:"turn_id"`
	// Role is "assistant" (the agent) or "user" (the customer).
	Role         string `json:"role"`
	Content      string `json:"content"`
	LanguageName string `json:"language_name"`
}

// Transcript is one call, turn by turn.
type Transcript struct {
	InteractionID string           `json:"interaction_id"`
	Messages      []TranscriptTurn `json:"messages"`
}

// ListAttempts returns the calls this agent placed in a window, newest first.
//
// The window is required by Sarvam, not optional, so the caller always states
// how far back to look rather than discovering a default.
func (c *Client) ListAttempts(ctx context.Context, since time.Time, limit int) ([]Attempt, error) {
	if !c.Configured() {
		return nil, fmt.Errorf("Sarvam is not configured on this service")
	}
	if limit <= 0 || limit > 200 {
		limit = 100
	}

	q := url.Values{}
	q.Set("start_datetime", since.UTC().Format("2006-01-02T15:04:05Z"))
	q.Set("end_datetime", time.Now().UTC().Format("2006-01-02T15:04:05Z"))
	q.Set("limit", fmt.Sprint(limit))
	q.Set("sort_by", "start_datetime")
	q.Set("sort_order", "desc")

	var body struct {
		Items []Attempt `json:"items"`
		Total int       `json:"total"`
	}
	if err := c.getAnalytics(ctx, "attempts?"+q.Encode(), &body); err != nil {
		return nil, err
	}

	for i := range body.Items {
		body.Items[i].AgentVariables = flatten(body.Items[i].RawVariables)
	}
	return body.Items, nil
}

// Transcript returns one call's turns.
//
// The interaction id is path-escaped because it contains slashes and colons;
// interpolated raw it would be read as extra path segments and 404.
func (c *Client) Transcript(ctx context.Context, interactionID string) (Transcript, error) {
	if !c.Configured() {
		return Transcript{}, fmt.Errorf("Sarvam is not configured on this service")
	}
	var out Transcript
	err := c.getAnalytics(ctx, "transcripts/"+url.PathEscape(interactionID), &out)
	return out, err
}

// getAnalytics performs one GET against the analytics service.
func (c *Client) getAnalytics(ctx context.Context, path string, out any) error {
	endpoint := fmt.Sprintf("%s/api/analytics/v1/%s/%s/%s/%s",
		c.cfg.BaseURL, c.cfg.OrgID, c.cfg.WorkspaceID, c.cfg.AgentID, path)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	req.Header.Set("X-API-Key", c.cfg.APIKey)

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("reach Sarvam: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return fmt.Errorf("read Sarvam's reply: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		// Sarvam's own sentence, kept. Its refusals name the window, the id or
		// the key that was wrong, and that is what the reader can act on.
		return fmt.Errorf("sarvam analytics %d: %s", resp.StatusCode, firstLine(raw))
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("decode Sarvam's reply: %w", err)
	}
	return nil
}

// flatten renders Sarvam's string-to-any variables as strings.
//
// Every variable this agent declares is a string, but the field is typed as
// `any` on the wire and a number or a null would otherwise fail the decode and
// take the whole listing with it. Nulls are dropped rather than rendered as
// "<nil>", which is not a thing anybody wants on a page.
func flatten(in map[string]any) map[string]string {
	out := make(map[string]string, len(in))
	for k, v := range in {
		switch value := v.(type) {
		case nil:
			continue
		case string:
			out[k] = value
		case float64:
			out[k] = fmt.Sprintf("%g", value)
		default:
			out[k] = fmt.Sprint(value)
		}
	}
	return out
}

// firstLine trims a response body down to something a log line can hold.
func firstLine(raw []byte) string {
	const max = 300
	if len(raw) > max {
		return string(raw[:max]) + "…"
	}
	return string(raw)
}

// Recording streams one call's audio.
//
// Returns the response body for the caller to copy and close. Audio is
// megabytes; buffering it into memory to hand back a []byte would hold a whole
// call per concurrent listener for no reason.
//
// NOT the audio_url on the attempt. That one points at indus.sarvam.ai, which
// authenticates with a browser session rather than an API key and answers 403
// to anything else - measured. This endpoint takes the key, so Tensor can
// stream the audio itself and the key never reaches a browser.
func (c *Client) Recording(ctx context.Context, interactionID string) (*http.Response, error) {
	if !c.Configured() {
		return nil, fmt.Errorf("Sarvam is not configured on this service")
	}
	endpoint := fmt.Sprintf("%s/api/analytics/v1/%s/%s/%s/recordings/%s",
		c.cfg.BaseURL, c.cfg.OrgID, c.cfg.WorkspaceID, c.cfg.AgentID,
		url.PathEscape(interactionID))

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-API-Key", c.cfg.APIKey)

	// A fresh client without the 30s timeout: that bound is right for an API
	// round trip and wrong for streaming half a megabyte of audio to somebody
	// on a slow connection, where it would cut the recording off mid-sentence.
	resp, err := (&http.Client{}).Do(req)
	if err != nil {
		return nil, fmt.Errorf("reach Sarvam: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
		_ = resp.Body.Close()
		return nil, fmt.Errorf("sarvam recording %d: %s", resp.StatusCode, firstLine(raw))
	}
	return resp, nil
}
