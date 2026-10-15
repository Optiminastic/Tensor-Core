package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/Optiminastic/tensor-core/internal/auth"
)

// The Orders table's numbered pages, against a real Postgres.
//
// What this pins is the point of the endpoint: a page holds at most page_size
// rows, the next page holds the next ones, and the filters run in the query -
// not in a loop over every order after the fact.

type orderPage struct {
	Items []struct {
		ID          string `json:"id"`
		OrderNumber string `json:"order_number"`
	} `json:"items"`
	Total      int64            `json:"total"`
	Page       int32            `json:"page"`
	PageSize   int32            `json:"page_size"`
	TotalPages int32            `json:"total_pages"`
	Counts     map[string]int64 `json:"counts"`
}

func getOrderPage(t *testing.T, router http.Handler, token, query string) orderPage {
	t.Helper()
	rr := doJSON(router, http.MethodGet, "/orders?source=dummy&"+query, token, nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("GET /orders?%s = %d: %s", query, rr.Code, rr.Body.String())
	}
	var out orderPage
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode %s: %v", query, err)
	}
	return out
}

func TestIntegrationOrdersTablePages(t *testing.T) {
	store := setupStore(t)
	seedAll(t, store)
	ctx := context.Background()
	if _, err := store.Pool.Exec(ctx, `TRUNCATE order_line_items`); err != nil {
		t.Fatalf("truncate line items: %v", err)
	}
	minter := newTokenMinter(t)
	router := testServer(t, store, auth.NewGuards(minter.verifier, ""))
	token := minter.mint(t, []string{auth.OrderRead.Key()})

	const n = 40
	ids := make([]uuid.UUID, n)
	for i := 0; i < n; i++ {
		ids[i] = seedOrder(t, store, int64(9000+i), []map[string]any{{"title": "Plank", "quantity": 1}})
		placed := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC).Add(time.Duration(i) * time.Hour)
		fulfilment := ""
		if i%3 == 0 {
			fulfilment = "fulfilled"
		}
		if _, err := store.Pool.Exec(ctx,
			`UPDATE orders SET placed_at = $2, imported_at = $2, fulfillment_status = NULLIF($3, ''),
			        customer_name = $4 WHERE id = $1`,
			ids[i], placed, fulfilment, fmt.Sprintf("Customer %02d", i)); err != nil {
			t.Fatalf("shape order %d: %v", i, err)
		}
		// Every fifth order holds a keychain: not a product Tensor generates.
		sku, name := "DNP-BLUE", "Dual Name Plank"
		if i%5 == 0 {
			sku, name = "KC-01", "Keychain"
		}
		if _, err := store.Pool.Exec(ctx,
			`INSERT INTO order_line_items (id, order_id, shopify_order_id, sku, product_name)
			 VALUES ($1, $2, $3, $4, $5)`, uuid.New(), ids[i], 9000+i, sku, name); err != nil {
			t.Fatalf("line item %d: %v", i, err)
		}
	}

	// Page 1: fifteen rows, newest first, and the footer's numbers.
	p1 := getOrderPage(t, router, token, "page=1")
	if len(p1.Items) != 15 || p1.Total != n || p1.TotalPages != 3 || p1.Page != 1 || p1.PageSize != 15 {
		t.Fatalf("page 1 = %d rows, total %d, %d pages, page %d, size %d",
			len(p1.Items), p1.Total, p1.TotalPages, p1.Page, p1.PageSize)
	}
	if p1.Items[0].ID != ids[n-1].String() {
		t.Errorf("page 1 starts with %s, want the newest order", p1.Items[0].OrderNumber)
	}

	// Page 2 is the NEXT fifteen: no overlap with page 1.
	p2 := getOrderPage(t, router, token, "page=2&counts=false")
	if len(p2.Items) != 15 {
		t.Fatalf("page 2 = %d rows, want 15", len(p2.Items))
	}
	seen := map[string]bool{}
	for _, it := range p1.Items {
		seen[it.ID] = true
	}
	for _, it := range p2.Items {
		if seen[it.ID] {
			t.Errorf("order %s is on both page 1 and page 2", it.OrderNumber)
		}
	}
	if p2.Counts != nil {
		t.Errorf("counts=false still sent counts: %v", p2.Counts)
	}

	// The last page holds the remainder; a page past the end is clamped to it.
	if p3 := getOrderPage(t, router, token, "page=3"); len(p3.Items) != 10 {
		t.Errorf("page 3 = %d rows, want 10", len(p3.Items))
	}
	if far := getOrderPage(t, router, token, "page=99"); far.Page != 3 || len(far.Items) != 10 {
		t.Errorf("page 99 = page %d with %d rows, want the last page (3, 10 rows)", far.Page, len(far.Items))
	}

	// The badges count the whole list.
	wantCounts := map[string]int64{"": 40, "done": 14, "unfulfilled": 26, "no-jobs": 40, "non-dnp": 8}
	for tab, want := range wantCounts {
		if got := p1.Counts[tab]; got != want {
			t.Errorf("count[%q] = %d, want %d", tab, got, want)
		}
	}

	// Tabs filter in the query; their totals match their badges.
	for tab, want := range map[string]int64{"done": 14, "unfulfilled": 26, "non-dnp": 8} {
		got := getOrderPage(t, router, token, "page=1&tab="+tab)
		if got.Total != want || int64(len(got.Items)) != min(want, 15) {
			t.Errorf("tab %s: total %d, %d rows; want total %d", tab, got.Total, len(got.Items), want)
		}
	}

	// Search and the period window, both in SQL.
	if got := getOrderPage(t, router, token, "page=1&search=Customer%2007"); got.Total != 1 {
		t.Errorf("search for one customer found %d", got.Total)
	}
	from := time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC).Format(time.RFC3339)
	to := time.Date(2026, 9, 1, 19, 0, 0, 0, time.UTC).Format(time.RFC3339)
	if got := getOrderPage(t, router, token, "page=1&from="+from+"&to="+to); got.Total != 10 {
		t.Errorf("a ten-hour window (inclusive) found %d orders, want 10", got.Total)
	}

	// An empty result is a page 1 with no rows, not an error.
	if got := getOrderPage(t, router, token, "page=1&search=nobody-by-this-name"); got.Total != 0 ||
		len(got.Items) != 0 || got.Page != 1 || got.TotalPages != 1 {
		t.Errorf("empty search = %+v", got)
	}

	// A page that is not a number is refused.
	if rr := doJSON(router, http.MethodGet, "/orders?page=two", token, nil); rr.Code != http.StatusUnprocessableEntity {
		t.Errorf("page=two answered %d, want 422", rr.Code)
	}

	// Without page, the old consumers still get the whole bare array.
	rr := doJSON(router, http.MethodGet, "/orders?source=dummy", token, nil)
	var all []json.RawMessage
	if err := json.Unmarshal(rr.Body.Bytes(), &all); err != nil || len(all) != n {
		t.Errorf("unpaged /orders = %d rows (%v), want the full %d as a bare array", len(all), err, n)
	}
}
