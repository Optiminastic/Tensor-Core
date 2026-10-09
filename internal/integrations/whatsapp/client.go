// Package whatsapp sends approved message templates through Meta's WhatsApp
// Cloud API.
//
// SENDING ONLY. It posts templates to one business number and reads a template
// back to check it. It cannot receive: inbound messages and delivery receipts
// arrive by webhook at a public HTTPS callback, which Tensor does not have yet.
//
// ACCEPTED IS NOT DELIVERED. A 200 from /messages carries a message id and
// means Meta took the message, not that a phone showed it. Meta will accept a
// send to a number that is not on WhatsApp, and India applies per-user
// marketing caps that drop accepted messages silently. Until the webhook
// exists, "accepted" is the strongest claim this package can make, and nothing
// here says otherwise.
package whatsapp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const (
	defaultBaseURL = "https://graph.facebook.com"
	// DefaultAPIVersion is pinned on purpose. Meta retires versions on a
	// schedule, and a floating version breaks on a date nobody wrote down.
	// Exported because the Settings form pre-fills it.
	DefaultAPIVersion = "v25.0"
)

const defaultTimeout = 30 * time.Second

// Config is one WhatsApp Business number.
type Config struct {
	// AccessToken must be a SYSTEM USER token.
	//
	// A user token works identically for about an hour and then answers 190,
	// which is the worst possible failure for a thing that messages customers:
	// nothing breaks loudly, the sends simply start failing and the shop finds
	// out from silence. Measured - the first token this was built against was
	// type USER with fifty minutes left on it.
	AccessToken string
	// PhoneNumberID is the NUMBER's id, not the business account's. Sending
	// posts to /{phone_number_id}/messages; the WABA id there answers 404.
	PhoneNumberID string
	// WABAID owns the templates. Not needed to send - only to read a template
	// back and check it.
	WABAID string
	// TemplateName and TemplateLanguage are the default template. A caller may
	// name a different one per message.
	TemplateName     string
	TemplateLanguage string

	// BaseURL and APIVersion override the endpoint. BaseURL is for tests.
	BaseURL    string
	APIVersion string
}

// Client sends from one business number.
type Client struct {
	http *http.Client
	cfg  Config
}

// New returns a client, or nil when it could not send.
//
// Nil rather than an error, like sarvam.New and delhivery.New: a deployment
// that does not message customers is the normal case and must still start.
// The template is NOT required here - only the token and the number are,
// because those are what make a send possible at all.
func New(cfg Config) *Client {
	if strings.TrimSpace(cfg.AccessToken) == "" || strings.TrimSpace(cfg.PhoneNumberID) == "" {
		return nil
	}
	cfg.BaseURL = strings.TrimSuffix(strings.TrimSpace(cfg.BaseURL), "/")
	if cfg.BaseURL == "" {
		cfg.BaseURL = defaultBaseURL
	}
	if strings.TrimSpace(cfg.APIVersion) == "" {
		cfg.APIVersion = DefaultAPIVersion
	}
	return &Client{http: &http.Client{Timeout: defaultTimeout}, cfg: cfg}
}

// Configured reports whether this client can send.
func (c *Client) Configured() bool { return c != nil }

// TemplateName is the configured default template.
func (c *Client) TemplateName() string {
	if c == nil {
		return ""
	}
	return c.cfg.TemplateName
}

// TemplateLanguage is the configured default template's language.
func (c *Client) TemplateLanguage() string {
	if c == nil {
		return ""
	}
	return c.cfg.TemplateLanguage
}

// TemplateMessage is one approved template addressed to one number.
type TemplateMessage struct {
	// To in E.164. The leading '+' is optional to Meta and is stripped here,
	// so a caller can pass whatever the ledger stores.
	To string
	// Name and Language fall back to the configured defaults.
	//
	// THE LANGUAGE IS PART OF THE TEMPLATE'S IDENTITY, not a formatting hint.
	// "en" and "en_US" are different templates to Meta, and asking for the
	// wrong one answers 132001 - which reads as a missing template when it is
	// only the locale that is wrong. Measured.
	Name, Language string
	// BodyParams in the template's own order. An EMPTY one is refused by Meta
	// outright, so they are checked here, before the network.
	BodyParams []string
	// ButtonParams is one value per URL button, in the template's own order.
	//
	// ONE PER BUTTON, NOT ONE IN TOTAL. Every dynamic URL button needs its
	// own parameter and Meta refuses the whole send if any is missing, with
	// "Button at index 1 of type Url requires a parameter". A template that
	// gains a second button therefore breaks every send until this list grows
	// to match - which is exactly how a working win-back goes silent after
	// somebody edits it in Meta's console.
	//
	// Each value is everything after the host the approved template bakes in.
	// Empty omits the button components entirely, for a template with none.
	ButtonParams []string
}

// SendResult is Meta's acknowledgement, and nothing more than that.
type SendResult struct {
	// MessageID is the wamid - the key a delivery webhook would correlate on.
	MessageID string
	// WAID is the number Meta actually routed to. It can differ from To, and a
	// difference is the only local hint that the recipient is not who the
	// caller meant.
	WAID string
	// Status is Meta's own word, which is "accepted". See the package comment:
	// that is not "delivered".
	Status string
}

type templateParam struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type templateComponent struct {
	Type string `json:"type"`
	// SubType and Index are only set on a button, and Index is a STRING - a
	// number there is refused with an unhelpful generic error.
	SubType    string          `json:"sub_type,omitempty"`
	Index      string          `json:"index,omitempty"`
	Parameters []templateParam `json:"parameters"`
}

// SendTemplate sends one approved template.
func (c *Client) SendTemplate(ctx context.Context, msg TemplateMessage) (SendResult, error) {
	if !c.Configured() {
		return SendResult{}, fmt.Errorf("WhatsApp is not configured")
	}
	to := strings.TrimPrefix(strings.TrimSpace(msg.To), "+")
	if to == "" {
		return SendResult{}, fmt.Errorf("no number to message")
	}

	name := strings.TrimSpace(msg.Name)
	if name == "" {
		name = strings.TrimSpace(c.cfg.TemplateName)
	}
	language := strings.TrimSpace(msg.Language)
	if language == "" {
		language = strings.TrimSpace(c.cfg.TemplateLanguage)
	}
	if name == "" || language == "" {
		return SendResult{}, fmt.Errorf("no template named to send")
	}

	// Checked before the network because Meta refuses an empty parameter and
	// the refusal names neither the field nor the position. A blank customer
	// name is common - the abandoned checkout carries whatever the shopper
	// typed - so this would otherwise be a recurring mystery.
	for i, p := range msg.BodyParams {
		if strings.TrimSpace(p) == "" {
			return SendResult{}, fmt.Errorf(
				"template parameter %d is empty, which Meta refuses", i+1)
		}
	}

	components := []templateComponent{{
		Type:       "body",
		Parameters: textParams(msg.BodyParams),
	}}
	for i, p := range msg.ButtonParams {
		if strings.TrimSpace(p) == "" {
			return SendResult{}, fmt.Errorf(
				"button parameter %d is empty, which Meta refuses", i)
		}
		components = append(components, templateComponent{
			// Index is the STRING form of the position. A number there is
			// refused with an unhelpful generic error.
			Type: "button", SubType: "url", Index: strconv.Itoa(i),
			Parameters: textParams([]string{p}),
		})
	}

	body, err := json.Marshal(map[string]any{
		"messaging_product": "whatsapp",
		"recipient_type":    "individual",
		"to":                to,
		"type":              "template",
		"template": map[string]any{
			"name":       name,
			"language":   map[string]string{"code": language},
			"components": components,
		},
	})
	if err != nil {
		return SendResult{}, err
	}

	endpoint := fmt.Sprintf("%s/%s/%s/messages",
		c.cfg.BaseURL, c.cfg.APIVersion, c.cfg.PhoneNumberID)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return SendResult{}, err
	}
	req.Header.Set("Authorization", "Bearer "+c.cfg.AccessToken)
	req.Header.Set("Content-Type", "application/json")

	// NOT RETRIED, deliberately, and not even on a transport error. A send is
	// not idempotent and Meta accepts no idempotency key on /messages, so a
	// retry after a timeout can message somebody twice. The caller decides
	// what to do with a failure; see Error.Retryable.
	resp, err := c.http.Do(req)
	if err != nil {
		return SendResult{}, fmt.Errorf("could not reach WhatsApp: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return SendResult{}, fmt.Errorf("could not read WhatsApp's response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return SendResult{}, parseError(resp.StatusCode, raw)
	}

	var ok struct {
		Contacts []struct {
			WAID string `json:"wa_id"`
		} `json:"contacts"`
		Messages []struct {
			ID     string `json:"id"`
			Status string `json:"message_status"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(raw, &ok); err != nil {
		return SendResult{}, fmt.Errorf("could not decode WhatsApp's response: %w", err)
	}
	if len(ok.Messages) == 0 || strings.TrimSpace(ok.Messages[0].ID) == "" {
		// A 2xx with no message id is not a send. Treated as a failure rather
		// than a silent success, so nothing records a message that has no id
		// to correlate.
		return SendResult{}, fmt.Errorf("WhatsApp accepted the request but returned no message id")
	}

	out := SendResult{MessageID: ok.Messages[0].ID, Status: ok.Messages[0].Status}
	if out.Status == "" {
		out.Status = "accepted"
	}
	if len(ok.Contacts) > 0 {
		out.WAID = ok.Contacts[0].WAID
	}
	return out, nil
}

func textParams(values []string) []templateParam {
	out := make([]templateParam, 0, len(values))
	for _, v := range values {
		out = append(out, templateParam{Type: "text", Text: v})
	}
	return out
}
