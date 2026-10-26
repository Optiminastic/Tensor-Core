package production

import "testing"

// What may share a bed, by product.
func TestTheWholePlankRangeIsOneFamily(t *testing.T) {
	// THE FAILURE THIS GUARDS. DNP-GLD and DNPWL-GLD are one gold plank
	// differing by whether an LED ships in the box; splitting them sent two
	// beds under the three-unit floor and both stalled.
	for _, sku := range []string{"DNP-GLD", "DNPWL-GLD", "DNPLB-2", "PDNP-PUR", "T3DPS-DNP-15"} {
		if got := ProductFamily(sku, ""); got != "DNP" {
			t.Errorf("ProductFamily(%q) = %q, want DNP", sku, got)
		}
	}
}

// A PREFIX RULE SWALLOWED THIS ONCE, and held four orders behind an error
// about a product they were not. DNPF is a photo frame, not a plank.
func TestThePhotoFrameIsNotAPlank(t *testing.T) {
	if got := ProductFamily("DNPF-1", ""); got != "DNPF" {
		t.Errorf("ProductFamily(DNPF-1) = %q, want DNPF", got)
	}
	if got := ProductFamily("", "Dual Name & Photo Frame - Gold"); got != "DNPF" {
		t.Errorf("by name = %q, want DNPF", got)
	}
}

func TestTheDiwaliProductsEachGetTheirOwnFamily(t *testing.T) {
	if got := ProductFamily("PDHR-R", ""); got != "PDHR" {
		t.Errorf("PDHR-R = %q, want PDHR", got)
	}
	if got := ProductFamily("PWDH-R", ""); got != "PWDH" {
		t.Errorf("PWDH-R = %q, want PWDH", got)
	}
	// The collision the shop asked about: a red Royal and a red Lotus are the
	// same colour and must not share a plate.
	if ProductFamily("PDHR-R", "") == ProductFamily("PWDH-R", "") {
		t.Error("the Royal and the Lotus share a family; a red bed would mix them")
	}
}

// Nine live plank variants carry no SKU at all, so the name has to answer.
func TestAPlankWithNoSKUIsStillAPlank(t *testing.T) {
	for _, name := range []string{
		"Dual Name Plank - GREEN / NO LIGHT",
		"Dual Name Plank with Light - GOLD",
		"PREMIUM DUAL NAME PLANK - Pink",
	} {
		if got := ProductFamily("", name); got != "DNP" {
			t.Errorf("ProductFamily(%q) = %q, want DNP", name, got)
		}
	}
}

// The longer pattern wins, or the Lotus would be filed as the Royal.
func TestTheLotusIsNotTheRoyal(t *testing.T) {
	if got := ProductFamily("", "Personalized Wall Diya Holder - Lotus - Red"); got != "PWDH" {
		t.Errorf("the Lotus = %q, want PWDH", got)
	}
	if got := ProductFamily("", "Personalized Diya Holder - Royal - Red"); got != "PDHR" {
		t.Errorf("the Royal = %q, want PDHR", got)
	}
}

// UNKNOWN IS NOT ITS OWN FAMILY. A product nothing names keeps today's
// behaviour exactly - it shares a bed by colour, as everything did before
// this existed - so this rule can only stop a NAMED family mixing, never
// split work that currently batches together.
func TestAnUnknownProductIsNotSeparated(t *testing.T) {
	if got := ProductFamily("CKC-01", "Couple's Keychain"); got != "" {
		t.Errorf("ProductFamily(CKC-01) = %q, want empty", got)
	}
	if got := ProductFamily("", ""); got != "" {
		t.Errorf("nothing named = %q, want empty", got)
	}
}
