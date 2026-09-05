package httpapi

import "testing"

// The property names here are copied verbatim from a real order on the live
// store (T3DPS-114567). Before this, none of them matched the recognised list,
// so the customer's names were read from Shopify and silently discarded.
func TestMapShopifyLineItemsKeepsRealStoreProperties(t *testing.T) {
	items := []shopifyLineItem{{
		SKU: "T3DPS-DNP-2", Name: "Dual Name Plank", Quantity: 1,
		Properties: []shopifyLineProp{
			{Name: "_has_gpo", Value: "622778"},
			{Name: "STEP 3-:", Value: "2 RED HEART"},
			{Name: "STEP 4-First Name-:", Value: "RAHUL"},
			{Name: "STEP 5-Second Name:", Value: "RANU"},
			{Name: "STEP 6 - WhatsApp Number:", Value: "9911337249"},
			{Name: "_gpo_product_group", Value: "1787685810089"},
			{Name: "_gpo_addon_price", Value: "50"},
		},
	}}

	got := mapShopifyLineItems(items)
	if len(got) != 1 {
		t.Fatalf("line items = %d, want 1", len(got))
	}
	li := got[0]

	// Nothing may be lost, including Shopify's own underscore-prefixed keys.
	if len(li.Properties) != 7 {
		t.Fatalf("kept %d properties, want all 7", len(li.Properties))
	}
	if li.Properties[0].Name != "_has_gpo" || li.Properties[3].Name != "STEP 5-Second Name:" {
		t.Errorf("properties lost their original order: %+v", li.Properties)
	}

	// Both names belong to one engraving, so they are joined rather than one
	// of them being dropped.
	if li.PersonalisationName == nil || *li.PersonalisationName != "RAHUL & RANU" {
		t.Errorf("PersonalisationName = %v, want \"RAHUL & RANU\"", li.PersonalisationName)
	}
	if !li.PersonalisationRequired {
		t.Error("PersonalisationRequired = false; a named engraving needs personalisation")
	}
}

// Punctuation and spacing in a store's labels must not have to be reproduced
// exactly - they get edited, and an exact-match list breaks silently when they do.
func TestNormalisePropKey(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"step 4-first name-:", "step 4 first name"},
		{"STEP 6 - WhatsApp Number:", "step 6 whatsapp number"},
		{"  first   name  ", "first name"},
		{"_gpo_addon_price", "gpo addon price"},
		{"---", ""},
	} {
		if got := normalisePropKey(tc.in); got != tc.want {
			t.Errorf("normalisePropKey(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// A store using the older documented names must keep working unchanged.
func TestMapShopifyLineItemsStillReadsDocumentedKeys(t *testing.T) {
	got := mapShopifyLineItems([]shopifyLineItem{{
		Name: "Plaque", Quantity: 2,
		Properties: []shopifyLineProp{
			{Name: "personalisation_name", Value: "ARJUN"},
			{Name: "font", Value: "Serif"},
			{Name: "material", Value: "PLA"},
		},
	}})
	li := got[0]
	if li.PersonalisationName == nil || *li.PersonalisationName != "ARJUN" {
		t.Errorf("PersonalisationName = %v, want ARJUN", li.PersonalisationName)
	}
	if li.PersonalisationFont == nil || *li.PersonalisationFont != "Serif" {
		t.Errorf("PersonalisationFont = %v, want Serif", li.PersonalisationFont)
	}
}
