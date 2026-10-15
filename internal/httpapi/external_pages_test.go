package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/Optiminastic/tensor-core/internal/integrations/shopify"
)

// The lists whose source cannot be paged are filtered, sorted and cut here.
// These pin that each sends one page and filters exactly as its table did.

func TestSlicePageCutsAndClamps(t *testing.T) {
	items := make([]int, 40)
	for i := range items {
		items[i] = i
	}
	p := slicePage(items, offsetPage{Page: 2, Size: 15})
	if len(p.Items) != 15 || p.Items[0] != 15 || p.Total != 40 || p.TotalPages != 3 {
		t.Fatalf("page 2 = %v (total %d, pages %d)", p.Items, p.Total, p.TotalPages)
	}
	last := slicePage(items, offsetPage{Page: 99, Size: 15})
	if last.Page != 3 || len(last.Items) != 10 || last.Items[0] != 30 {
		t.Errorf("page 99 = page %d, %v; want the last page", last.Page, last.Items)
	}
	empty := slicePage([]int{}, offsetPage{Page: 4, Size: 15})
	if empty.Page != 1 || len(empty.Items) != 0 || empty.TotalPages != 1 || empty.Items == nil {
		t.Errorf("empty = %+v, want page 1 of 1 with an empty (not null) list", empty)
	}
}

func TestLiveQueueKeepsOnlyWhatIsStillToRun(t *testing.T) {
	got := liveQueueOnly([]queueItemResponse{
		{ID: 1, Status: "pending"}, {ID: 2, Status: "completed"},
		{ID: 3, Status: "printing"}, {ID: 4, Status: "cancelled"},
	})
	if len(got) != 2 || got[0].ID != 1 || got[1].ID != 3 {
		t.Errorf("live queue = %+v, want items 1 and 3", got)
	}
}

func TestShipmentSearchReadsEveryFieldTheBoxDid(t *testing.T) {
	row := shipmentRowResponse{OrderName: "#1042", Waybill: "WB998", City: "Pune", Pincode: "411001"}
	row.LineItems = []shopify.ShipmentLineItem{{Title: "Dual Name Plank"}}
	for _, needle := range []string{"#1042", "wb998", "pune", "411001", "name plank", ""} {
		if !shipmentMatches(row, needle) {
			t.Errorf("search %q missed the parcel", needle)
		}
	}
	if shipmentMatches(row, "mumbai") {
		t.Error("search for another city matched")
	}
}

type abandonedTestPage struct {
	Items []struct {
		Name string `json:"name"`
	} `json:"items"`
	Total  int64            `json:"total"`
	Counts map[string]int64 `json:"counts"`
	Shop   string           `json:"shop"`
}

func runAbandonedPage(t *testing.T, items []shopify.AbandonedCheckout, query string) abandonedTestPage {
	t.Helper()
	gin.SetMode(gin.TestMode)
	rr := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rr)
	c.Request = httptest.NewRequest(http.MethodGet, "/x?"+query, nil)
	p, paged, ok := parseOffsetPage(c)
	if !ok || !paged {
		t.Fatalf("query %q did not ask for a page", query)
	}
	pageAbandonedCheckouts(c, items, "shop.example", p)
	var out abandonedTestPage
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v %s", err, rr.Body.String())
	}
	return out
}

func TestAbandonedCheckoutsPageFiltersSortsAndCounts(t *testing.T) {
	base := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	done := base
	var items []shopify.AbandonedCheckout
	for i := 0; i < 20; i++ {
		ck := shopify.AbandonedCheckout{
			Name: fmt.Sprintf("#%03d", i), CustomerName: fmt.Sprintf("Customer %02d", 19-i),
			CreatedAt: base.Add(time.Duration(i) * time.Hour), TotalAmount: fmt.Sprint(100 + i),
		}
		if i%2 == 0 {
			ck.Phone = "9000000000"
		}
		if i%5 == 0 {
			ck.CompletedAt = &done
		}
		items = append(items, ck)
	}

	// Default: newest abandoned first, fifteen of them, every badge.
	p := runAbandonedPage(t, items, "page=1")
	if len(p.Items) != 15 || p.Total != 20 || p.Items[0].Name != "#019" || p.Shop != "shop.example" {
		t.Fatalf("page 1 = %d rows of %d, first %v", len(p.Items), p.Total, p.Items[0])
	}
	for tab, want := range map[string]int64{"": 20, "phone": 10, "no-phone": 10, "recovered": 4} {
		if p.Counts[tab] != want {
			t.Errorf("count[%q] = %d, want %d", tab, p.Counts[tab], want)
		}
	}
	if got := runAbandonedPage(t, items, "page=1&tab=phone"); got.Total != 10 {
		t.Errorf("has phone = %d, want 10", got.Total)
	}
	if got := runAbandonedPage(t, items, "page=1&search=%23007"); got.Total != 1 || got.Items[0].Name != "#007" {
		t.Errorf("search #007 = %+v", got.Items)
	}
	// Customer A-Z: "Customer 00" belongs to #019.
	if got := runAbandonedPage(t, items, "page=1&sort=customer:asc"); got.Items[0].Name != "#019" {
		t.Errorf("customer asc starts %s, want #019", got.Items[0].Name)
	}
	if got := runAbandonedPage(t, items, "page=1&sort=value:asc"); got.Items[0].Name != "#000" {
		t.Errorf("value asc starts %s, want the smallest, #000", got.Items[0].Name)
	}
	if got := runAbandonedPage(t, items, "page=2"); len(got.Items) != 5 {
		t.Errorf("page 2 = %d rows, want 5", len(got.Items))
	}
}
