package httpapi

import "testing"

// A real Globo Product Options line from The 3D Printing Store India: the steps
// the customer filled in, the app's own bookkeeping keys, and GPO's JSON copy
// of the same answers. The step labels are per-product, so nothing here matches
// the documented personalisation_* keys.
func gpoLineProps() []shopifyLineProp {
	return []shopifyLineProp{
		{Name: "_has_gpo", Value: "622778"},
		{Name: "STEP 3-", Value: "2 RED HEART"},
		{Name: "STEP 4-First Name-", Value: "KAJAL"},
		{Name: "STEP 5-Second Name", Value: "ANAND"},
		{Name: "STEP 6 - WhatsApp Number", Value: "8218740630"},
		{Name: "_gpo_product_group", Value: "1787306047599"},
		{Name: "_gpo_addon_price", Value: "50"},
	}
}

func TestPersonalisationOptionsKeepsCustomerFieldsOnly(t *testing.T) {
	options := personalisationOptions(gpoLineProps())

	want := []string{"STEP 3-", "STEP 4-First Name-", "STEP 5-Second Name", "STEP 6 - WhatsApp Number"}
	if len(options) != len(want) {
		t.Fatalf("options = %d (%v), want %d", len(options), options, len(want))
	}
	for i, name := range want {
		if options[i].Name != name {
			t.Errorf("options[%d].Name = %q, want %q (storefront order must survive)", i, options[i].Name, name)
		}
	}
	if options[1].Value != "KAJAL" {
		t.Errorf("first name = %q, want KAJAL", options[1].Value)
	}
}

func TestPersonalisationOptionsFallsBackToGPOBlob(t *testing.T) {
	// Only the app's own keys: the individual steps are gone, so the JSON copy
	// is the sole record of what the customer typed.
	props := []shopifyLineProp{
		{Name: "_has_gpo", Value: "622778"},
		{Name: gpoOptionsKey, Value: `{"_has_gpo":"622778","STEP 4-First Name-":"KAJAL","STEP 5-Second Name":"ANAND"}`},
	}

	options := personalisationOptions(props)

	if len(options) != 2 {
		t.Fatalf("options = %v, want the two name steps", options)
	}
	// Sorted, so a re-import of the same order produces the same line.
	if options[0].Name != "STEP 4-First Name-" || options[0].Value != "KAJAL" {
		t.Errorf("options[0] = %+v, want the first name", options[0])
	}
	if options[1].Value != "ANAND" {
		t.Errorf("options[1] = %+v, want the second name", options[1])
	}
}

func TestMapShopifyLineItemsReadsStoreNamedFields(t *testing.T) {
	items := mapShopifyLineItems([]shopifyLineItem{{
		SKU: "T3DPS-DNP-16", Name: "Dual Name Plank", Quantity: 1,
		Properties: gpoLineProps(),
	}})

	if len(items) != 1 {
		t.Fatalf("items = %d, want 1", len(items))
	}
	item := items[0]
	if item.PersonalisationName == nil || *item.PersonalisationName != "KAJAL / ANAND" {
		t.Errorf("PersonalisationName = %v, want both names joined", item.PersonalisationName)
	}
	if !item.PersonalisationRequired {
		t.Error("PersonalisationRequired = false, want true - the customer filled in four fields")
	}
	if len(item.Options) != 4 {
		t.Errorf("Options = %d, want all four steps kept verbatim", len(item.Options))
	}
	// The WhatsApp number fits no typed slot; it must still reach the operator.
	var sawPhone bool
	for _, o := range item.Options {
		if o.Value == "8218740630" {
			sawPhone = true
		}
	}
	if !sawPhone {
		t.Error("the WhatsApp number was dropped")
	}
}

func TestMapShopifyLineItemsStillPrefersDocumentedKeys(t *testing.T) {
	items := mapShopifyLineItems([]shopifyLineItem{{
		SKU: "SKU-1", Name: "Plank", Quantity: 1,
		Properties: []shopifyLineProp{
			{Name: "personalisation_name", Value: "Ada"},
			{Name: "Nickname", Value: "Ignore me"},
		},
	}})

	if got := items[0].PersonalisationName; got == nil || *got != "Ada" {
		t.Errorf("PersonalisationName = %v, want the exact-key value", got)
	}
}

func TestVariantColourReachesTheLineItem(t *testing.T) {
	items := mapShopifyLineItems([]shopifyLineItem{{
		SKU: "T3DPS-DNP-9", Name: "Dual Name Plank", Quantity: 1,
		VariantTitle: "BABY PINK / NO LIGHT",
		VariantOptions: []shopifyLineProp{
			// The real labels from this store - named for the customer walking
			// through the product's steps, not for us.
			{Name: "STEP 1 - Colour", Value: "BABY PINK"},
			{Name: "STEP 2 - Base Light", Value: "NO LIGHT"},
		},
		Properties: gpoLineProps(),
	}})

	item := items[0]
	if item.Colour == nil || *item.Colour != "BABY PINK" {
		t.Errorf("Colour = %v, want BABY PINK - this is what picks the filament", item.Colour)
	}
	// Both variant choices belong on the order, ahead of the text steps.
	if len(item.Options) != 6 || item.Options[0].Name != "STEP 1 - Colour" {
		t.Errorf("Options = %+v, want the variant first then the four steps", item.Options)
	}
}

func TestVariantTitleKeptWholeWithoutProductAccess(t *testing.T) {
	// No VariantOptions: the app could not read products, so the title is all
	// there is. Splitting it on the slash would be a guess about which half is
	// the colour, and a wrong filament costs a print.
	items := mapShopifyLineItems([]shopifyLineItem{{
		SKU: "T3DPS-DNP-9", Name: "Dual Name Plank", Quantity: 1,
		VariantTitle: "BABY PINK / NO LIGHT",
	}})

	item := items[0]
	if item.Colour != nil {
		t.Errorf("Colour = %v, want nil rather than a guess", item.Colour)
	}
	if len(item.Options) != 1 || item.Options[0].Value != "BABY PINK / NO LIGHT" {
		t.Errorf("Options = %+v, want the variant title kept whole", item.Options)
	}
}

func TestDefaultVariantIsNotAnOption(t *testing.T) {
	// A product with no real options: Shopify reports "Default Title", which
	// would otherwise show up on the order as a meaningless line.
	items := mapShopifyLineItems([]shopifyLineItem{{
		SKU: "X", Name: "Plain thing", Quantity: 1,
		VariantTitle:   "Default Title",
		VariantOptions: []shopifyLineProp{{Name: "Title", Value: "Default Title"}},
	}})

	if len(items[0].Options) != 0 {
		t.Errorf("Options = %+v, want none", items[0].Options)
	}
}
