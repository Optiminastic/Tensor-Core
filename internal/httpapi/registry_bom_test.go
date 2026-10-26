package httpapi

// Inheriting a bill of materials.
//
// The rule is all-or-nothing, and these pin it because the tempting
// alternative - merging a variant's lines over the product's - produces a kit
// that exists in neither list, costs the wrong amount, and reaches the
// assembly bench looking finished.

import "testing"

func bomLine(name string, qty float64) bomLineResponse {
	return bomLineResponse{ItemID: name, ItemName: name, Unit: "piece", Quantity: qty}
}

func bomNames(lines []bomLineResponse) []string {
	out := make([]string, 0, len(lines))
	for _, l := range lines {
		out = append(out, l.ItemName)
	}
	return out
}

func TestAVariantWithNoPartsInheritsTheProducts(t *testing.T) {
	shared := []bomLineResponse{bomLine("Red heart", 1), bomLine("Gift box", 1)}
	got := resolveBom(nil, shared)
	if len(got) != 2 {
		t.Fatalf("lines = %v, want the product's two", bomNames(got))
	}
}

func TestAVariantWithItsOwnPartsReplacesTheProducts(t *testing.T) {
	shared := []bomLineResponse{bomLine("Red heart", 1), bomLine("Gift box", 1)}
	own := []bomLineResponse{bomLine("Large gift box", 1)}

	got := resolveBom(own, shared)
	if len(got) != 1 || got[0].ItemName != "Large gift box" {
		t.Fatalf("lines = %v, want only the variant's own", bomNames(got))
	}
}

// THE FAILURE THIS RULE EXISTS FOR. A SKU that upgrades its box must not also
// inherit the standard one: merged, the bench packs two boxes, the cost is
// wrong, and nothing anywhere refuses it.
func TestAnOverrideDoesNotInheritWhatItReplaced(t *testing.T) {
	shared := []bomLineResponse{bomLine("Red heart", 1), bomLine("Gift box", 1)}
	own := []bomLineResponse{bomLine("Red heart", 1), bomLine("Large gift box", 1)}

	got := resolveBom(own, shared)
	for _, n := range bomNames(got) {
		if n == "Gift box" {
			t.Fatalf("lines = %v - the standard box came back alongside the upgrade",
				bomNames(got))
		}
	}
	if len(got) != 2 {
		t.Errorf("lines = %v, want the variant's two", bomNames(got))
	}
}

func TestNeitherLevelConfiguredIsEmpty(t *testing.T) {
	if got := resolveBom(nil, nil); len(got) != 0 {
		t.Errorf("lines = %v, want none", bomNames(got))
	}
}
