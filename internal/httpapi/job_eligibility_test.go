package httpapi

import (
	"testing"

	"github.com/Optiminastic/tensor-core/internal/db/gen"
)

// Which orders reach the print floor.
//
// The priority-dispatch exclusion is gone. It used to keep every "PRIORITY
// DISPATCH ⚡️" order out of the automatic queue on the instruction that those
// were handled elsewhere; that is no longer how the floor works, and nineteen
// unfulfilled priority orders were being held out of production by it. Its
// removal is pinned here so the rule cannot come back by accident.
func TestShouldCreateJobs(t *testing.T) {
	ship := func(s string) *string { return &s }
	status := func(s string) *string { return &s }
	const free = "FREE DISPATCH - BEST DEAL 🎉"
	const priority = "PRIORITY DISPATCH ⚡️"

	for _, c := range []struct {
		name  string
		order gen.Order
		want  bool
	}{
		{"the first order of the run", gen.Order{OrderNumber: "T3DPS-114599", ShippingTitle: ship(free)}, true},
		{"well after the cutoff", gen.Order{OrderNumber: "T3DPS-114765", ShippingTitle: ship(free)}, true},
		// One below the cutoff, placed hours earlier on the previous day.
		{"just before the cutoff", gen.Order{OrderNumber: "T3DPS-114598", ShippingTitle: ship(free)}, false},
		{"long before the cutoff", gen.Order{OrderNumber: "T3DPS-114567", ShippingTitle: ship(free)}, false},

		// Priority dispatch prints like anything else now.
		{"priority, after the cutoff", gen.Order{OrderNumber: "T3DPS-114700", ShippingTitle: ship(priority)}, true},
		{"priority reworded", gen.Order{OrderNumber: "T3DPS-114700", ShippingTitle: ship("Priority Shipping")}, true},
		// Still subject to every other rule.
		{"priority, before the cutoff", gen.Order{OrderNumber: "T3DPS-114500", ShippingTitle: ship(priority)}, false},

		// Already shipped: the plank was made and posted, by Tensor or by hand.
		// Creating jobs for it would put work on the floor that is out the door.
		{"fulfilled", gen.Order{
			OrderNumber: "T3DPS-114700", ShippingTitle: ship(free),
			FulfillmentStatus: status("fulfilled"),
		}, false},
		{"fulfilled, mixed case", gen.Order{
			OrderNumber: "T3DPS-114700", FulfillmentStatus: status("Fulfilled"),
		}, false},
		{"fulfilled priority order", gen.Order{
			OrderNumber: "T3DPS-114700", ShippingTitle: ship(priority),
			FulfillmentStatus: status("fulfilled"),
		}, false},
		// Part of an order having shipped says nothing about the plank still to
		// be made - dropping that would lose work somebody is waiting for.
		{"partially fulfilled", gen.Order{
			OrderNumber: "T3DPS-114700", FulfillmentStatus: status("partially_fulfilled"),
		}, true},
		{"unfulfilled", gen.Order{
			OrderNumber: "T3DPS-114700", FulfillmentStatus: status("unfulfilled"),
		}, true},
		{"no fulfilment status at all", gen.Order{OrderNumber: "T3DPS-114700"}, true},

		// A number nothing can be read from must not start a print on a guess.
		{"unreadable number", gen.Order{OrderNumber: "DRAFT", ShippingTitle: ship(free)}, false},
		{"empty number", gen.Order{OrderNumber: "", ShippingTitle: ship(free)}, false},
		// The store's prefix is not stable - one live order is "T3PS-", short
		// a letter - so only the trailing digits decide.
		{"malformed prefix", gen.Order{OrderNumber: "T3PS-114743", ShippingTitle: ship(free)}, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := ShouldCreateJobs(c.order); got != c.want {
				t.Errorf("ShouldCreateJobs(%q, ship=%v, fulfilment=%v) = %v, want %v",
					c.order.OrderNumber, c.order.ShippingTitle, c.order.FulfillmentStatus, got, c.want)
			}
		})
	}
}
