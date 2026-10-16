// Package order holds the store's order shape as the bot needs it, independent
// of how Shopify returns it, plus the dispatch-date rules from the storefront:
// standard dispatch in 10 working days, express in 3.
package order

import (
	"strings"
	"time"
	_ "time/tzdata" // the runtime image has no zoneinfo

	"github.com/Optiminastic/tensor-core/internal/whatsappbot/phone"
)

const (
	StandardDispatchDays = 10
	ExpressDispatchDays  = 3
	expressMarker        = "EXPRESS"
)

// Location is where the workshop runs and where customers read dates. Times are
// stored in UTC and converted only when shown to a customer.
var Location = mustLoad("Asia/Kolkata")

type Tracking struct {
	Company string
	Number  string
	URL     string
}

type Fulfillment struct {
	// DisplayStatus is Shopify's FulfillmentDisplayStatus, e.g. IN_TRANSIT, DELIVERED.
	DisplayStatus string
	Tracking      []Tracking
}

type Order struct {
	ID                string // Shopify GID, gid://shopify/Order/...
	Name              string // T3DPS-115766
	CreatedAt         time.Time
	CancelledAt       *time.Time
	ShippingTitle     string
	FulfillmentStatus string // Shopify displayFulfillmentStatus
	Fulfillments      []Fulfillment
	// Phones are every number attached to the order, used for the privacy check.
	Phones []string
}

func (o Order) IsExpress() bool {
	return strings.Contains(strings.ToUpper(o.ShippingTitle), expressMarker)
}

// ExpectedDispatch is the promised dispatch date, in the shop's time zone.
func (o Order) ExpectedDispatch() time.Time {
	days := StandardDispatchDays
	if o.IsExpress() {
		days = ExpressDispatchDays
	}
	return AddWorkingDays(o.CreatedAt.In(Location), days)
}

// HasPhone reports whether a WhatsApp sender is one of the order's numbers.
func (o Order) HasPhone(waID string) bool {
	for _, p := range o.Phones {
		if phone.Match(waID, p) {
			return true
		}
	}
	return false
}

// LatestFulfillment returns the most recent shipment, if any.
func (o Order) LatestFulfillment() (Fulfillment, bool) {
	if len(o.Fulfillments) == 0 {
		return Fulfillment{}, false
	}
	return o.Fulfillments[len(o.Fulfillments)-1], true
}

// AddWorkingDays moves forward n days, skipping Sundays.
func AddWorkingDays(t time.Time, n int) time.Time {
	for added := 0; added < n; {
		t = t.AddDate(0, 0, 1)
		if t.Weekday() != time.Sunday {
			added++
		}
	}
	return t
}

func mustLoad(name string) *time.Location {
	loc, err := time.LoadLocation(name)
	if err != nil {
		panic(err)
	}
	return loc
}
