package content

import (
	"fmt"
	"strings"
	"time"

	"github.com/Optiminastic/tensor-core/internal/whatsappbot/order"
)

const dateLayout = "Mon, 2 Jan"

// deliveryStates maps Shopify's FulfillmentDisplayStatus to customer wording.
var deliveryStates = map[string]string{
	"CONFIRMED":          "Packed and handed to the courier",
	"IN_TRANSIT":         "In transit",
	"OUT_FOR_DELIVERY":   "Out for delivery 🚚",
	"ATTEMPTED_DELIVERY": "Delivery attempted - the courier will try again",
	"DELIVERED":          "Delivered",
	"FAILURE":            "Delivery issue - our team will look into it",
	"READY_FOR_PICKUP":   "Ready for pickup",
}

// OrderStatus is the "Where is my order" answer for one order.
func OrderStatus(o order.Order, now time.Time) string {
	if o.CancelledAt != nil {
		return fmt.Sprintf("Order %s was cancelled. Type *HUMAN* if you have any questions.", o.Name)
	}
	if f, ok := o.LatestFulfillment(); ok {
		return shippedStatus(o.Name, f)
	}
	return craftingStatus(o, now)
}

func shippedStatus(name string, f order.Fulfillment) string {
	if f.DisplayStatus == "DELIVERED" {
		return fmt.Sprintf("🎉 Order %s has been delivered. We hope you love it!\n\nIf anything arrived damaged, type *MENU* and choose *Damage concern*.", name)
	}

	var b strings.Builder
	fmt.Fprintf(&b, "📦 Order %s has been dispatched.", name)
	if state, ok := deliveryStates[f.DisplayStatus]; ok {
		fmt.Fprintf(&b, "\nStatus: %s", state)
	}
	for _, t := range f.Tracking {
		if t.Company != "" {
			fmt.Fprintf(&b, "\nCourier: %s", t.Company)
		}
		if t.Number != "" {
			fmt.Fprintf(&b, "\nTracking number: %s", t.Number)
		}
		if t.URL != "" {
			fmt.Fprintf(&b, "\nTrack here: %s", t.URL)
		}
	}
	b.WriteString("\n\nDelivery usually takes 2-5 working days after dispatch.")
	return b.String()
}

func craftingStatus(o order.Order, now time.Time) string {
	dispatch := o.ExpectedDispatch()
	if now.In(order.Location).After(endOfDay(dispatch)) {
		return fmt.Sprintf("🛠️ Order %s is being handcrafted and should be dispatched any moment now. We'll share tracking as soon as it ships.", o.Name)
	}
	kind := "standard"
	if o.IsExpress() {
		kind = "express"
	}
	return fmt.Sprintf("🛠️ Order %s is being handcrafted for you.\n\nExpected dispatch (%s): by *%s*. You'll get tracking details as soon as it ships.",
		o.Name, kind, dispatch.Format(dateLayout))
}

func endOfDay(t time.Time) time.Time {
	y, m, d := t.Date()
	return time.Date(y, m, d+1, 0, 0, 0, 0, t.Location())
}
