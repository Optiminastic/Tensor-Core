package whatsapp

import (
	"context"
	"fmt"
	"strings"
)

// listSectionTitle heads the single section of every list this package sends.
const listSectionTitle = "Options"

// ListRow is one choice in a list message. Meta caps the title at 24
// characters and the description at 72.
type ListRow struct {
	ID          string
	Title       string
	Description string
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

// SendList sends text with a list menu: buttonText opens a sheet of up to ten
// rows. Used instead of reply buttons, which Meta caps at three. The same
// 24-hour rule as SendText applies.
func (c *Client) SendList(ctx context.Context, to, body, buttonText string, rows []ListRow) (SendResult, error) {
	if !c.Configured() {
		return SendResult{}, fmt.Errorf("WhatsApp is not configured")
	}
	items := make([]map[string]any, 0, len(rows))
	for _, r := range rows {
		item := map[string]any{"id": r.ID, "title": r.Title}
		if r.Description != "" {
			item["description"] = r.Description
		}
		items = append(items, item)
	}
	return c.post(ctx, map[string]any{
		"to":   strings.TrimPrefix(strings.TrimSpace(to), "+"),
		"type": "interactive",
		"interactive": map[string]any{
			"type": "list",
			"body": map[string]any{"text": body},
			"action": map[string]any{
				"button":   buttonText,
				"sections": []map[string]any{{"title": listSectionTitle, "rows": items}},
			},
		},
	})
}
