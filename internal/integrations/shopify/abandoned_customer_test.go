package shopify

// Reading the customer record behind an abandoned checkout.
//
// A row could read "No phone" while Shopify's own admin showed one, because
// Shopify reads the CUSTOMER RECORD and Tensor could only read the checkout's
// addresses - and a checkout abandoned before the address step has none. The
// customer object needs read_customers, and asking for it without the scope
// fails the whole query with ACCESS_DENIED rather than returning null, so it
// could not simply be requested optimistically.

import (
	"strings"
	"testing"
)

// The case the scope was added for: no addresses at all, and the customer
// record carries everything.
func TestCustomerRecordSuppliesWhatTheAddressesCannot(t *testing.T) {
	got := toAbandonedCheckout(abandonedNode{
		ID: "gid://shopify/AbandonedCheckout/1",
		Customer: &abandonedCustomer{
			DisplayName: "Aarti Deshmukh",
			FirstName:   "Aarti",
			LastName:    "Deshmukh",
			Email:       "aarti@example.test",
			Phone:       "+919812345678",
		},
	})

	if got.CustomerName != "Aarti Deshmukh" {
		t.Errorf("name = %q, want the customer record's", got.CustomerName)
	}
	if got.Phone != "+919812345678" {
		t.Errorf("phone = %q - this is the row that used to read \"No phone\" "+
			"beside an admin screen showing one", got.Phone)
	}
	// The ONLY source of an email: the abandoned-checkout type has no email
	// field of its own.
	if got.Email != "aarti@example.test" {
		t.Errorf("email = %q, want the customer record's", got.Email)
	}
}

// A GUEST checkout has no customer record, and those are a large part of the
// list. The addresses must still be read.
func TestAGuestCheckoutStillReadsItsAddresses(t *testing.T) {
	got := toAbandonedCheckout(abandonedNode{
		ID: "gid://shopify/AbandonedCheckout/2",
		BillingAddress: &abandonedAddress{
			FirstName: "Rohit", LastName: "Sharma",
			Phone: "+919000000001", City: "Pune", Province: "MH",
		},
	})

	if got.CustomerName != "Rohit Sharma" {
		t.Errorf("name = %q, want the billing address's", got.CustomerName)
	}
	if got.Phone != "+919000000001" {
		t.Errorf("phone = %q, want the billing address's", got.Phone)
	}
	if got.Email != "" {
		t.Errorf("email = %q - a guest checkout carries none", got.Email)
	}
}

// The customer record WINS over the addresses, which is the order Shopify's
// own admin reads them in. A delivery address can name somebody else - a gift
// goes to the recipient - and the person to ring back is the buyer.
func TestTheCustomerRecordWinsOverTheAddress(t *testing.T) {
	got := toAbandonedCheckout(abandonedNode{
		ID:       "gid://shopify/AbandonedCheckout/3",
		Customer: &abandonedCustomer{DisplayName: "Meera Nair", Phone: "+919111111111"},
		ShippingAddress: &abandonedAddress{
			FirstName: "Gift", LastName: "Recipient", Phone: "+919222222222",
		},
	})

	if got.CustomerName != "Meera Nair" {
		t.Errorf("name = %q, want the buyer rather than the delivery address", got.CustomerName)
	}
	if got.Phone != "+919111111111" {
		t.Errorf("phone = %q, want the buyer's", got.Phone)
	}
}

// displayName first, because Shopify builds it the way the merchant expects.
// Joining the parts for a record with only one of them leaves a stray space.
func TestNameFallsBackToThePartsWithoutADisplayName(t *testing.T) {
	if got := nameOf(&abandonedCustomer{FirstName: "Sunita"}); got != "Sunita" {
		t.Errorf("one-part name = %q, want %q with no stray space", got, "Sunita")
	}
	if got := nameOf(&abandonedCustomer{DisplayName: "S. Rao", FirstName: "Sunita", LastName: "Rao"}); got != "S. Rao" {
		t.Errorf("name = %q, want the display name Shopify built", got)
	}
	if got := nameOf(nil); got != "" {
		t.Errorf("a guest checkout named itself %q", got)
	}
}

// The WhatsApp fallback must survive. It is what makes the commonest shape of
// abandoned checkout - abandoned before the address step, by a guest -
// reachable at all, and the customer record does not replace it.
func TestTheWhatsAppFallbackStillAppliesWhenNothingElseHasANumber(t *testing.T) {
	got := toAbandonedCheckout(abandonedNode{
		ID:       "gid://shopify/AbandonedCheckout/4",
		Customer: &abandonedCustomer{DisplayName: "Priya Menon"},
		LineItems: struct {
			Nodes []abandonedLineItemNode `json:"nodes"`
		}{Nodes: []abandonedLineItemNode{{
			CustomAttributes: []CheckoutAttribute{
				{Key: "STEP 6 - WhatsApp Number", Value: "+919333333333"},
			},
		}}},
	})

	if got.Phone != "+919333333333" {
		t.Errorf("phone = %q, want the number the customer typed themselves", got.Phone)
	}
}

// The scope has to be ASKED FOR at install, or the token never carries it and
// the query fails with ACCESS_DENIED on every page.
func TestTheInstallAsksForReadCustomers(t *testing.T) {
	if !strings.Contains(OrderImportScopes, "read_customers") {
		t.Fatalf("OrderImportScopes = %q, which will not grant the customer record",
			OrderImportScopes)
	}
	for _, need := range []string{"read_orders", "read_products"} {
		if !strings.Contains(OrderImportScopes, need) {
			t.Errorf("OrderImportScopes lost %s", need)
		}
	}
}

// The query must actually request it. A scope granted and never asked for
// changes nothing.
func TestTheQueryAsksForTheCustomer(t *testing.T) {
	if !strings.Contains(listAbandonedCheckoutsQuery, "customer {") {
		t.Fatal("the abandoned-checkout query does not request the customer record")
	}
	for _, field := range []string{"email", "phone", "displayName"} {
		if !strings.Contains(listAbandonedCheckoutsQuery, field) {
			t.Errorf("the query does not request %s", field)
		}
	}
}
