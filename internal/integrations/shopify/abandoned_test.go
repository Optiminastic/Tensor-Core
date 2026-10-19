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

// The drawer shows each address as the customer gave it. Unlike the name and
// phone, nothing is borrowed from the other one: a billing address with only a
// name stays that way rather than picking up the shipping city.
func TestAddressesAreKeptWholeAndApart(t *testing.T) {
	got := toAbandonedCheckout(abandonedNode{
		BillingAddress:  &abandonedAddress{FirstName: "Thanduri", LastName: "Sampath"},
		ShippingAddress: &abandonedAddress{FirstName: "Thanduri", Address1: "12 MG Road", City: "Hyderabad", Zip: "500001", Country: "India"},
	})

	if got.ShippingAddress == nil || got.ShippingAddress.Address1 != "12 MG Road" || got.ShippingAddress.Zip != "500001" {
		t.Fatalf("shipping address = %+v, want it whole", got.ShippingAddress)
	}
	if got.BillingAddress == nil || got.BillingAddress.City != "" {
		t.Errorf("billing address = %+v, want its own blank city, not shipping's", got.BillingAddress)
	}
}

// Lists the page iterates are [] rather than null, and a nullable tax total
// is blank rather than a crash.
func TestAbsentListsAndTaxComeBackEmpty(t *testing.T) {
	got := toAbandonedCheckout(abandonedNode{})

	if got.DiscountCodes == nil || got.Attributes == nil {
		t.Errorf("discount codes = %v, attributes = %v; want empty slices, not nil", got.DiscountCodes, got.Attributes)
	}
	if got.TaxAmount != "" {
		t.Errorf("tax = %q, want blank when Shopify sends no tax set", got.TaxAmount)
	}
	if got.ShippingAddress != nil {
		t.Errorf("shipping address = %+v, want nil when none was given", got.ShippingAddress)
	}
}

// The number the customer typed, when no address carries one.
//
// THE COMMONEST SHAPE OF AN ABANDONED CHECKOUT. Shopify calls a checkout
// abandoned once contact details are entered, which on this store happens at
// the personalisation step - before the address. So the basket has a
// "STEP 6 - WhatsApp Number" on a line item and two empty addresses, and
// reading only the addresses leaves it as "No phone": a reachable customer
// the win-back agent never rings or messages.
func TestThePhoneFallsBackToTheWhatsAppProperty(t *testing.T) {
	node := abandonedNode{}
	node.LineItems.Nodes = []abandonedLineItemNode{{
		CustomAttributes: []CheckoutAttribute{
			{Key: "STEP 1 - First Name", Value: "RAHUL"},
			{Key: "STEP 6 - WhatsApp Number", Value: "98765 43210"},
		},
	}}

	if got := toAbandonedCheckout(node); got.Phone != "98765 43210" {
		t.Fatalf("phone = %q, want the number off the line item", got.Phone)
	}
}

// An address number is the better one and must still win: it is the number
// the courier would use, and it has been through Shopify's own validation.
func TestAnAddressPhoneBeatsTheProperty(t *testing.T) {
	node := abandonedNode{
		ShippingAddress: &abandonedAddress{Phone: "7989797791"},
	}
	node.LineItems.Nodes = []abandonedLineItemNode{{
		CustomAttributes: []CheckoutAttribute{{Key: "STEP 6 - WhatsApp Number", Value: "98765 43210"}},
	}}

	if got := toAbandonedCheckout(node); got.Phone != "7989797791" {
		t.Fatalf("phone = %q, want the address number", got.Phone)
	}
}

// Matched on the word, not the whole key: the step number moves whenever a
// product's form is reordered, and a number that stops being found because
// of that is a customer who stops being called.
func TestTheWhatsAppKeyIsMatchedLoosely(t *testing.T) {
	for _, key := range []string{
		"STEP 6 - WhatsApp Number", "STEP 4 - Whatsapp Number", "whatsapp number", "WhatsApp No.",
	} {
		node := abandonedNode{CustomAttributes: []CheckoutAttribute{{Key: key, Value: "98765 43210"}}}
		if got := toAbandonedCheckout(node); got.Phone != "98765 43210" {
			t.Errorf("%q: phone = %q", key, got.Phone)
		}
	}
}

// An empty value is not a number. Shopify keeps the property with a blank
// value when the customer clears the field, and "" would read as given.
func TestABlankWhatsAppPropertyIsNotAPhone(t *testing.T) {
	node := abandonedNode{CustomAttributes: []CheckoutAttribute{
		{Key: "STEP 6 - WhatsApp Number", Value: "   "},
	}}
	if got := toAbandonedCheckout(node); got.Phone != "" {
		t.Fatalf("phone = %q, want empty", got.Phone)
	}
}
