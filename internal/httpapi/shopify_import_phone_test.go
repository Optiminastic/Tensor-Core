package httpapi

import "testing"

import "github.com/Optiminastic/tensor-core/internal/integrations/shopify"

// The number a shopper types at checkout does not land on the customer
// record. Shopify fills customer.phone only for an account that has one, and
// puts what they typed on the order and on the delivery address. Measured on
// the live store: 20 orders of 1,269 carry customer.phone, 1,249 carry a
// shipping-address phone - so reading only the first refused 98% of orders
// for having "no usable phone number", and the window never opened for them.
func TestTheOrderPhoneIsFoundWhereverShopifyPutIt(t *testing.T) {
	cases := []struct {
		name    string
		payload shopifyOrderPayload
		want    string
	}{{
		name:    "the customer record has it",
		payload: shopifyOrderPayload{Customer: &shopifyCustomer{Phone: "+91 97999 31864"}},
		want:    "+91 97999 31864",
	}, {
		name:    "only the order has it",
		payload: shopifyOrderPayload{Phone: "9799931864"},
		want:    "9799931864",
	}, {
		name: "only the delivery address has it - the common case",
		payload: shopifyOrderPayload{
			Customer:        &shopifyCustomer{},
			ShippingAddress: &shopify.OrderAddress{Phone: "+919799931864"},
		},
		want: "+919799931864",
	}, {
		name:    "billing is the last resort",
		payload: shopifyOrderPayload{BillingAddress: &shopify.OrderAddress{Phone: "09799931864"}},
		want:    "09799931864",
	}, {
		// Stopping at the first NON-EMPTY field would take the landline and
		// throw away the mobile sitting one field later.
		name: "an unusable number does not shadow a usable one",
		payload: shopifyOrderPayload{
			Customer:        &shopifyCustomer{Phone: "0141 2345678"},
			ShippingAddress: &shopify.OrderAddress{Phone: "+91 97999 31864"},
		},
		want: "+91 97999 31864",
	}, {
		// The column answers "what number is on this order". An operator
		// reading a bad one can see it is bad; a null tells them nothing.
		name:    "nothing usable still records what the store said",
		payload: shopifyOrderPayload{Phone: "0141 2345678"},
		want:    "0141 2345678",
	}, {
		name:    "no number at all is null",
		payload: shopifyOrderPayload{Customer: &shopifyCustomer{}},
		want:    "",
	}}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.payload.customerPhone()
			if tc.want == "" {
				if got != nil {
					t.Fatalf("phone = %q, want none", *got)
				}
				return
			}
			if got == nil {
				t.Fatalf("phone = none, want %q", tc.want)
			}
			if *got != tc.want {
				t.Errorf("phone = %q, want %q", *got, tc.want)
			}
		})
	}
}

// The same gap, on the name: 20 orders of 1,269 carry a customer name and
// 1,249 carry one on the delivery address. Without the fallback the orders
// table shows a dash for almost every customer and the personalisation
// message opens "Hi there".
func TestTheCustomerNameFallsBackToTheDeliveryName(t *testing.T) {
	cases := []struct {
		name    string
		payload shopifyOrderPayload
		want    string
	}{{
		name: "the account has it",
		payload: shopifyOrderPayload{
			Customer:        &shopifyCustomer{FirstName: "Rishi", LastName: "Patel"},
			ShippingAddress: &shopify.OrderAddress{Name: "Someone Else"},
		},
		want: "Rishi Patel",
	}, {
		name: "only the parcel has it - the common case",
		payload: shopifyOrderPayload{
			Customer:        &shopifyCustomer{},
			ShippingAddress: &shopify.OrderAddress{Name: "Priya Sharma"},
		},
		want: "Priya Sharma",
	}, {
		name:    "billing is the last resort",
		payload: shopifyOrderPayload{BillingAddress: &shopify.OrderAddress{Name: "Asha Rao"}},
		want:    "Asha Rao",
	}, {
		name:    "no name at all is null",
		payload: shopifyOrderPayload{Customer: &shopifyCustomer{}},
		want:    "",
	}}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.payload.customerName()
			if tc.want == "" {
				if got != nil {
					t.Fatalf("name = %q, want none", *got)
				}
				return
			}
			if got == nil || *got != tc.want {
				t.Fatalf("name = %v, want %q", got, tc.want)
			}
		})
	}
}
