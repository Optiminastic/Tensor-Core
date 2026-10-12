package whatsapp

// Reading an approved template back, so a sent message can be written down in
// the words the customer actually saw.
//
// THE COPY LIVES AT META, NOT HERE. Tensor supplies five parameters and a
// button path; the sentences around them are edited in WhatsApp Manager and
// re-approved there. Keeping a second copy in this repo to render the log
// would mean two sources of truth for the same words, and the one nobody
// deploys would be the one that drifts - the log would then quietly describe
// a message that was never sent.
//
// So the body is fetched and filled. It is fetched ONCE per process and
// cached: the copy changes about as often as somebody edits it in the console,
// and a send must not pay a second round trip to write its own log line.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
)

// Template is the parts of an approved template that a log needs.
type Template struct {
	Name     string
	Language string
	// Status is Meta's own word - APPROVED, PENDING, REJECTED. Carried so a
	// caller can say why a template that exists still will not send.
	Status string
	// Body is the BODY component's text, placeholders and all: "Hi {{1}},
	// your {{2}} is still waiting". Empty if the template has no body.
	Body string
	// ButtonURLs is one entry per URL button, in the template's own order,
	// each still carrying its own {{1}}. The order is what makes a button
	// parameter land on the right button.
	ButtonURLs []string
}

// templateCache keys on name and language together, because they are one
// identity to Meta: "en" and "en_US" are different templates.
type templateCache struct {
	mu   sync.Mutex
	seen map[string]Template
}

func (c *Client) cache() *templateCache {
	c.once.Do(func() { c.templates = &templateCache{seen: map[string]Template{}} })
	return c.templates
}

// Template returns one approved template, from cache when it has been read
// before.
//
// Name and language fall back to the configured defaults, exactly as
// SendTemplate's do, so a caller logging what it just sent does not have to
// repeat itself and cannot disagree with the send.
func (c *Client) Template(ctx context.Context, name, language string) (Template, error) {
	if !c.Configured() {
		return Template{}, fmt.Errorf("WhatsApp is not configured")
	}
	if strings.TrimSpace(name) == "" {
		name = strings.TrimSpace(c.cfg.TemplateName)
	}
	if strings.TrimSpace(language) == "" {
		language = strings.TrimSpace(c.cfg.TemplateLanguage)
	}
	if name == "" || language == "" {
		return Template{}, fmt.Errorf("no template named to read")
	}
	// The WABA owns the templates; the phone number id answers 404 here, the
	// mirror image of the trap on /messages.
	if strings.TrimSpace(c.cfg.WABAID) == "" {
		return Template{}, fmt.Errorf(
			"no WhatsApp business account id is configured, so templates cannot be read")
	}

	key := name + "/" + language
	store := c.cache()
	store.mu.Lock()
	hit, ok := store.seen[key]
	store.mu.Unlock()
	if ok {
		return hit, nil
	}

	found, err := c.fetchTemplate(ctx, name, language)
	if err != nil {
		return Template{}, err
	}
	store.mu.Lock()
	store.seen[key] = found
	store.mu.Unlock()
	return found, nil
}

func (c *Client) fetchTemplate(ctx context.Context, name, language string) (Template, error) {
	// Filtered by name at Meta and by LANGUAGE here. The endpoint returns one
	// entry per translation under a single name, and asking for the wrong
	// locale is the failure this whole field exists to make visible.
	endpoint := fmt.Sprintf("%s/%s/%s/message_templates?name=%s&limit=50",
		c.cfg.BaseURL, c.cfg.APIVersion, c.cfg.WABAID, url.QueryEscape(name))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return Template{}, err
	}
	req.Header.Set("Authorization", "Bearer "+c.cfg.AccessToken)

	resp, err := c.http.Do(req)
	if err != nil {
		return Template{}, fmt.Errorf("could not reach WhatsApp: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return Template{}, fmt.Errorf("could not read WhatsApp's response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return Template{}, parseError(resp.StatusCode, raw)
	}

	var payload struct {
		Data []struct {
			Name       string `json:"name"`
			Language   string `json:"language"`
			Status     string `json:"status"`
			Components []struct {
				Type    string `json:"type"`
				Text    string `json:"text"`
				Buttons []struct {
					Type string `json:"type"`
					URL  string `json:"url"`
				} `json:"buttons"`
			} `json:"components"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return Template{}, fmt.Errorf("could not decode WhatsApp's response: %w", err)
	}

	for _, t := range payload.Data {
		if !strings.EqualFold(t.Language, language) {
			continue
		}
		out := Template{Name: t.Name, Language: t.Language, Status: t.Status}
		for _, comp := range t.Components {
			switch strings.ToUpper(comp.Type) {
			case "BODY":
				out.Body = comp.Text
			case "BUTTONS":
				for _, b := range comp.Buttons {
					if strings.EqualFold(b.Type, "URL") {
						out.ButtonURLs = append(out.ButtonURLs, b.URL)
					}
				}
			}
		}
		return out, nil
	}
	return Template{}, fmt.Errorf(
		"no %q template in %q - Meta treats each language as its own template",
		name, language)
}

// Fill substitutes {{1}}, {{2}}, … with the values that were sent.
//
// A placeholder with no value is LEFT AS IT IS rather than blanked. This runs
// over a record of something already sent, and a log that silently drops a
// value it could not account for reads as though the customer saw a gap. An
// unfilled {{3}} says plainly that the log and the send disagree.
func Fill(body string, params []string) string {
	if strings.TrimSpace(body) == "" {
		return ""
	}
	out := body
	for i, p := range params {
		out = strings.ReplaceAll(out, "{{"+strconv.Itoa(i+1)+"}}", p)
	}
	return out
}
