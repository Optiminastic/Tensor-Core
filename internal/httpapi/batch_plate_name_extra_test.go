package httpapi

// The plate name has to survive a round trip through BambuBuddy, and it does
// not survive it unchanged.

import "testing"

// Every one of these is a real name from the live archive.
//
// plateFileStem writes the colour LAST; the archive hands the same plate back
// with the colour FIRST, sometimes with an extension and a plate suffix. If the
// key were positional, or a prefix, every completion would silently fail to
// match and no bed would ever close itself.
func TestPlateKeyIsStableAcrossHowBambuBuddyRenamesAPlate(t *testing.T) {
	want := "114676-114719-114728-114836"
	for _, name := range []string{
		"GOLD-114676-114719-114728-114836",
		"114676-114719-114728-114836-GOLD",
		"GOLD-114676-114719-114728-114836.3mf",
		"114676-114719-114728-114836-GOLD_plate_1.gcode.3mf",
		"gold-114836-114676-114728-114719.stl",
	} {
		if got := plateKey(name); got != want {
			t.Errorf("plateKey(%q) = %q, want %q", name, got, want)
		}
	}
}

func TestPlateKeyOnTheOtherLivePlates(t *testing.T) {
	for name, want := range map[string]string{
		"BLUE-114840-114873":                      "114840-114873",
		"BLACK-114776.stl":                        "114776",
		"WHITE-114772-114776":                     "114772-114776",
		"BABY-PINK-114617":                        "114617",
		"BLUE-114611-114728-114769-114795-114827": "114611-114728-114769-114795-114827",
	} {
		if got := plateKey(name); got != want {
			t.Errorf("plateKey(%q) = %q, want %q", name, got, want)
		}
	}
}

// A name with no order numbers matches nothing, and must not match everything.
//
// plateFileStem falls back to the batch number for a bed with no Shopify order
// behind it, and BambuBuddy's own users name plates whatever they like. Either
// could otherwise claim a bed and complete it.
func TestPlateKeyRefusesAPlateThatNamesNoOrders(t *testing.T) {
	for _, name := range []string{
		"BATCH-1001824",
		"Fidget Click Keychain - Print in Place - 15 min",
		"PHOTO_FRIDGE_MAGNET",
		"",
		"plate_1.gcode.3mf",
		// Three digits is a plate index or a split suffix, not an order.
		"RED-123",
	} {
		if got := plateKey(name); got != "" {
			t.Errorf("plateKey(%q) = %q, want \"\" - it names no orders", name, got)
		}
	}
}

// One order with two planks on the same bed appears once.
func TestPlateKeyDeduplicates(t *testing.T) {
	if got, want := plateKey("BLUE-114776-114776-114777"), "114776-114777"; got != want {
		t.Errorf("plateKey = %q, want %q", got, want)
	}
}
