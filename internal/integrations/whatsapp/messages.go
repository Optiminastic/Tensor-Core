package whatsapp

import (
	"context"
	"fmt"
	"strings"
	"unicode/utf8"
)

// Meta's limits on a reply-button message, checked here because a refusal
// from Meta names neither the button nor the rule.
const (
	maxReplyButtons     = 3
	maxButtonTitleRunes = 20
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
