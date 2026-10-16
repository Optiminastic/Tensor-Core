package content

import (
	"strings"
	"testing"
	"time"

	"github.com/Optiminastic/tensor-core/internal/whatsappbot/order"
)

var created = time.Date(2026, 10, 12, 4, 30, 0, 0, time.UTC) // Mon 10:00 IST

func TestOrderStatusUnfulfilled(t *testing.T) {
	now := created.Add(24 * time.Hour)
	got := OrderStatus(order.Order{Name: "T3DPS-115766", CreatedAt: created, FulfillmentStatus: "UNFULFILLED"}, now)
	for _, want := range []string{"T3DPS-115766", "handcrafted", "Fri, 23 Oct"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in %q", want, got)
		}
	}
}

func TestOrderStatusOverdue(t *testing.T) {
	now := created.Add(30 * 24 * time.Hour)
	got := OrderStatus(order.Order{Name: "T3DPS-1", CreatedAt: created, FulfillmentStatus: "UNFULFILLED"}, now)
	if !strings.Contains(got, "any moment") {
		t.Errorf("overdue order text: %q", got)
	}
}

func TestOrderStatusShipped(t *testing.T) {
	o := order.Order{
		Name: "T3DPS-115766", CreatedAt: created, FulfillmentStatus: "FULFILLED",
		Fulfillments: []order.Fulfillment{{
			DisplayStatus: "IN_TRANSIT",
			Tracking:      []order.Tracking{{Company: "Delhivery", Number: "59906610024441", URL: "https://www.delhivery.com/track/package/59906610024441"}},
		}},
	}
	got := OrderStatus(o, created)
	for _, want := range []string{"dispatched", "Delhivery", "59906610024441", "https://www.delhivery.com", "In transit"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in %q", want, got)
		}
	}
}

func TestOrderStatusDelivered(t *testing.T) {
	o := order.Order{Name: "T3DPS-9", FulfillmentStatus: "FULFILLED", Fulfillments: []order.Fulfillment{{DisplayStatus: "DELIVERED"}}}
	if got := OrderStatus(o, created); !strings.Contains(got, "delivered") {
		t.Errorf("delivered text: %q", got)
	}
}

func TestOrderStatusCancelled(t *testing.T) {
	c := created
	if got := OrderStatus(order.Order{Name: "T3DPS-9", CancelledAt: &c}, created); !strings.Contains(got, "cancelled") {
		t.Errorf("cancelled text: %q", got)
	}
}
