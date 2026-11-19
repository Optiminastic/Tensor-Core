package shopify

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// The quick sync asks Shopify only for what changed since its last run - a
// full timestamp in Shopify's search syntax, not a date and not created_at.
func TestListOrdersUpdatedSinceAsksForUpdatesAfterTheMark(t *testing.T) {
	var seenQuery string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Variables map[string]any `json:"variables"`
		}
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &req)
		seenQuery, _ = req.Variables["query"].(string)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"orders":{"nodes":[]}}}`))
	}))
	defer server.Close()

	since := time.Date(2026, 11, 18, 10, 4, 5, 0, time.FixedZone("IST", 5*3600+1800))
	if _, err := testClient(server.URL).ListOrdersUpdatedSince(
		context.Background(), "shop.myshopify.com", "tok", 0, since); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if want := "updated_at:>='2026-11-18T04:34:05Z'"; seenQuery != want {
		t.Errorf("query = %q, want %q", seenQuery, want)
	}
}
