package shopify

// Where the contact details come from.
//
// Measured against the live store: of ten recent checkouts, one carried a
// billing NAME with a null billing phone while the shipping address held the
// number. Resolving the address once and reading every field off it loses that
// customer to "No phone" - which, on a page whose whole purpose is ringing
// people, means never being called at all.

import (
	"testing"
	"time"
)

func TestThePhoneIsFoundEvenWhenOnlyShippingHasIt(t *testing.T) {
	got := toAbandonedCheckout(abandonedNode{
		// The real shape: billing named them, shipping has the number.
		BillingAddress:  &abandonedAddress{FirstName: "Thanduri", LastName: "Sampath"},
		ShippingAddress: &abandonedAddress{FirstName: "Thanduri", LastName: "Sampath", Phone: "7989797791", City: "Hyderabad"},
	})

	if got.Phone != "7989797791" {
		t.Errorf("phone = %q, want the shipping number; a blank here is a customer nobody rings", got.Phone)
	}
	if got.CustomerName != "Thanduri Sampath" {
		t.Errorf("customer name = %q", got.CustomerName)
	}
	if got.City != "Hyderabad" {
		t.Errorf("city = %q, want the shipping city", got.City)
	}
}

func TestBillingWinsWhenItHasTheDetail(t *testing.T) {
	got := toAbandonedCheckout(abandonedNode{
		BillingAddress:  &abandonedAddress{FirstName: "Tarun", LastName: "Verma", Phone: "8130275916"},
		ShippingAddress: &abandonedAddress{FirstName: "Someone", LastName: "Else", Phone: "9999999999"},
	})
	if got.Phone != "8130275916" {
		t.Errorf("phone = %q, want the billing number", got.Phone)
	}
	if got.CustomerName != "Tarun Verma" {
		t.Errorf("customer name = %q, want the billing name", got.CustomerName)
	}
}

func TestNoAddressAtAllIsBlankNotACrash(t *testing.T) {
	got := toAbandonedCheckout(abandonedNode{})
	if got.Phone != "" || got.CustomerName != "" {
		t.Errorf("want everything blank, got name %q phone %q", got.CustomerName, got.Phone)
	}
}

// Whether the customer came back and paid after all.
//
// Shopify keeps a recovered checkout in the abandoned list - five of them on
// the live store in a 30-day window, one completed six days after it was
// abandoned. Miss this field and every one of those rows offers to ring a
// customer about a basket they already bought.
func TestACompletedCheckoutCarriesTheTimeItWasCompleted(t *testing.T) {
	got := toAbandonedCheckout(abandonedNode{
		CreatedAt:   "2026-09-29T13:35:30Z",
		CompletedAt: "2026-10-05T02:57:31Z",
	})

	if got.CompletedAt == nil {
		t.Fatal("completed_at is nil; the row would still read as abandoned")
	}
	if want := "2026-10-05T02:57:31Z"; got.CompletedAt.Format(time.RFC3339) != want {
		t.Errorf("completed at %s, want %s", got.CompletedAt.Format(time.RFC3339), want)
	}
}

func TestAnOpenCheckoutLeavesCompletedAtNilNotZero(t *testing.T) {
	got := toAbandonedCheckout(abandonedNode{CreatedAt: "2026-10-08T04:01:33Z"})

	// Nil, not the zero time: a zero time.Time formats as a real date, and
	// "completed in year 1" renders as recovered on the page.
	if got.CompletedAt != nil {
		t.Errorf("completed_at = %v, want nil for a checkout nobody finished", got.CompletedAt)
	}
}
