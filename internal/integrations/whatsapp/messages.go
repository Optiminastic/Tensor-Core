package whatsapp

import (
	"context"
	"fmt"
	"strings"
	"unicode/utf8"
)

// Meta's limits, checked here because a refusal from Meta names neither the
// button nor the rule.
const (
	maxReplyButtons     = 3
	maxButtonTitleRunes = 20
	// A list holds ten rows against a button message's three, which is the
	// whole reason it exists here: an FAQ with seven answers cannot be three
	// buttons, and burying four of them behind another "More" is a menu
	// nobody reaches the bottom of.
	maxListRows        = 10
	maxRowTitleRunes   = 24
	maxRowDescription  = 72
	maxListButtonRunes = 20
)

// Button is one reply button shown under a message.
type Button struct {
	ID    string
	Title string
}

// SendText sends a free-form message.
//
// ONLY INSIDE THE 24-HOUR WINDOW. Meta accepts free-form text only within 24
// hours of the customer's last message; outside it the send is refused with
// 131047 and only an approved template can reach them.
func (c *Client) SendText(ctx context.Context, to, text string) (SendResult, error) {
	if !c.Configured() {
		return SendResult{}, fmt.Errorf("WhatsApp is not configured")
	}
	if strings.TrimSpace(text) == "" {
		return SendResult{}, fmt.Errorf("no text to send")
	}
	return c.post(ctx, map[string]any{
		"to":   strings.TrimPrefix(strings.TrimSpace(to), "+"),
		"type": "text",
		"text": map[string]any{"preview_url": true, "body": text},
	})
}

// SendButtons sends text with up to three reply buttons shown right under it.
// The same 24-hour rule as SendText applies.
func (c *Client) SendButtons(ctx context.Context, to, body string, buttons []Button) (SendResult, error) {
	if !c.Configured() {
		return SendResult{}, fmt.Errorf("WhatsApp is not configured")
	}
	if len(buttons) == 0 || len(buttons) > maxReplyButtons {
		return SendResult{}, fmt.Errorf("a message carries 1 to %d reply buttons, not %d", maxReplyButtons, len(buttons))
	}
	replies := make([]map[string]any, 0, len(buttons))
	for _, b := range buttons {
		if utf8.RuneCountInString(b.Title) > maxButtonTitleRunes {
			return SendResult{}, fmt.Errorf("button %q is over %d characters", b.Title, maxButtonTitleRunes)
		}
		replies = append(replies, map[string]any{
			"type":  "reply",
			"reply": map[string]any{"id": b.ID, "title": b.Title},
		})
	}
	return c.post(ctx, map[string]any{
		"to":   strings.TrimPrefix(strings.TrimSpace(to), "+"),
		"type": "interactive",
		"interactive": map[string]any{
			"type":   "button",
			"body":   map[string]any{"text": body},
			"action": map[string]any{"buttons": replies},
		},
	})
}

// ListRow is one line of a list message. Description may be empty.
type ListRow struct {
	ID          string
	Title       string
	Description string
}

// SendList sends text with a tappable list behind one button.
//
// Ten rows instead of three buttons, and a customer taps the button to open
// them. A row comes back exactly as a reply button does - the webhook reads a
// list_reply into the same id - so the bot handles both with one branch.
//
// The same 24-hour rule as SendText applies.
func (c *Client) SendList(ctx context.Context, to, body, buttonText string, rows []ListRow) (SendResult, error) {
	if !c.Configured() {
		return SendResult{}, fmt.Errorf("WhatsApp is not configured")
	}
	if len(rows) == 0 || len(rows) > maxListRows {
		return SendResult{}, fmt.Errorf("a list carries 1 to %d rows, not %d", maxListRows, len(rows))
	}
	if utf8.RuneCountInString(buttonText) > maxListButtonRunes {
		return SendResult{}, fmt.Errorf("the list button %q is over %d characters", buttonText, maxListButtonRunes)
	}
	entries := make([]map[string]any, 0, len(rows))
	for _, r := range rows {
		if utf8.RuneCountInString(r.Title) > maxRowTitleRunes {
			return SendResult{}, fmt.Errorf("list row %q is over %d characters", r.Title, maxRowTitleRunes)
		}
		if utf8.RuneCountInString(r.Description) > maxRowDescription {
			return SendResult{}, fmt.Errorf("the description on %q is over %d characters", r.Title, maxRowDescription)
		}
		entry := map[string]any{"id": r.ID, "title": r.Title}
		// Omitted rather than sent empty: Meta refuses a blank description.
		if r.Description != "" {
			entry["description"] = r.Description
		}
		entries = append(entries, entry)
	}
	return c.post(ctx, map[string]any{
		"to":   strings.TrimPrefix(strings.TrimSpace(to), "+"),
		"type": "interactive",
		"interactive": map[string]any{
			"type": "list",
			"body": map[string]any{"text": body},
			"action": map[string]any{
				"button":   buttonText,
				"sections": []map[string]any{{"title": "Questions", "rows": entries}},
			},
		},
	})
}
