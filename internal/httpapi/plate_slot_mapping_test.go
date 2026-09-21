package httpapi

import (
	"testing"

	"github.com/Optiminastic/tensor-core/internal/meshio"
)

func tray(hex string, ams, slot int) loadedTray {
	a, s := ams, slot
	return loadedTray{Colour: hex, Type: "PLA", AmsID: &a, TrayID: &s}
}

// The plate's own slot order: body white first, lettering second.
func dnpPlate(lettering string) []meshio.Slot {
	return []meshio.Slot{
		{Colour: "#FFFFFF", Material: "PLA"},
		{Colour: lettering, Material: "PLA"},
	}
}

// The single most important assertion here: what gets sent is the TRAY's
// colour, not the plate's.
//
// The plate declares Tensor's idea of blue; the spool reports its own. Sending
// the plate's hex is what made BambuBuddy refuse plate after plate with "needs
// #1560BD, has #46A8F9". Sending the tray's hex makes the filament check
// compare a tray against itself, which it cannot fail.
func TestAssignmentsFromChoiceSendsTheTrayColour(t *testing.T) {
	// A plate built before any of this still declares Tensor's old blue.
	slots := dnpPlate("#1560BD")
	trays := []loadedTray{tray("#FFFFFF", 0, 1), tray("#2850E0", 0, 3)}

	got, err := assignmentsFromChoice(slots, trays, []int{1, 3})
	if err != nil {
		t.Fatalf("assignmentsFromChoice: %v", err)
	}
	if got[1].PlateHex != "#1560BD" {
		t.Errorf("PlateHex = %q, want the plate's own #1560BD", got[1].PlateHex)
	}
	if got[1].TrayHex != "#2850E0" {
		t.Errorf("TrayHex = %q, want the loaded spool's #2850E0 - sending the "+
			"plate's hex is the bug this path exists to fix", got[1].TrayHex)
	}
	if colours := trayColoursOf(got); colours[0] != "#FFFFFF" || colours[1] != "#2850E0" {
		t.Errorf("filament_colours = %v, want the chosen spools in plate slot order", colours)
	}
}

// The operator's choice is honoured even when it is not the closest colour.
// They can see the spools; Tensor cannot.
func TestAssignmentsFromChoiceHonoursAnUnobviousPick(t *testing.T) {
	slots := dnpPlate("#2850E0")
	// Two blues loaded. The operator picks the second one, which is further
	// from the plate's hex - because that is the spool with enough left on it.
	trays := []loadedTray{tray("#FFFFFF", 0, 0), tray("#2850E0", 0, 1), tray("#0A2989", 0, 2)}

	got, err := assignmentsFromChoice(slots, trays, []int{0, 2})
	if err != nil {
		t.Fatalf("assignmentsFromChoice: %v", err)
	}
	if got[1].TrayHex != "#0A2989" {
		t.Errorf("TrayHex = %q, want the spool the operator actually chose", got[1].TrayHex)
	}
	if mapping := amsMappingOf(got); mapping[1] != 2 {
		t.Errorf("ams_mapping = %v, want slot 2 served by tray 2", mapping)
	}
}

// ams_mapping is ams*4 + tray, confirmed against 40 of BambuBuddy's own queue
// items: an A2L reporting AMS 6 tray 1 is mapped as 25.
func TestAmsMappingUsesThePrintersOwnNumbering(t *testing.T) {
	slots := dnpPlate("#2850E0")
	trays := []loadedTray{tray("#FFFFFF", 6, 1), tray("#2850E0", 6, 3)}

	got, err := assignmentsFromChoice(slots, trays, []int{25, 27})
	if err != nil {
		t.Fatalf("assignmentsFromChoice: %v", err)
	}
	if mapping := amsMappingOf(got); mapping[0] != 25 || mapping[1] != 27 {
		t.Errorf("ams_mapping = %v, want [25 27] (AMS 6 slots 1 and 3)", mapping)
	}
}

func TestAssignmentsFromChoiceRefusals(t *testing.T) {
	slots := dnpPlate("#2850E0")
	trays := []loadedTray{tray("#FFFFFF", 0, 0), tray("#2850E0", 0, 1)}

	cases := []struct {
		name   string
		slots  []meshio.Slot
		trays  []loadedTray
		chosen []int
	}{
		{
			// Truncating the mapping is how the lettering prints in the body
			// colour, so a short answer is refused rather than padded.
			name:  "fewer choices than the plate has slots",
			slots: slots, trays: trays, chosen: []int{0},
		},
		{
			name:  "a tray the printer does not have loaded",
			slots: slots, trays: trays, chosen: []int{0, 9},
		},
		{
			name:  "the same spool for two slots",
			slots: slots, trays: trays, chosen: []int{0, 0},
		},
		{
			name:  "a plate that declares no filament",
			slots: nil, trays: trays, chosen: []int{0},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := assignmentsFromChoice(tc.slots, tc.trays, tc.chosen); err == nil {
				t.Error("accepted an impossible choice - a wrong mapping prints scrap silently")
			}
		})
	}
}

// The suggestion is only a default, but it should be the obvious one: closest
// colour, each spool offered once.
func TestSuggestSlotTraysPicksTheClosestSpoolOncePerSlot(t *testing.T) {
	slots := []queueSlot{{Index: 0, Hex: "#FFFFFF"}, {Index: 1, Hex: "#2850E0"}}
	white, blue := 0, 1
	trays := []queueTray{
		{Hex: "#FFFFFF", AmsID: &white, TrayID: &white},
		{Hex: "#2850E0", AmsID: &white, TrayID: &blue},
	}

	got := suggestSlotTrays(slots, trays)
	if len(got) != 2 || got[0] != 0 || got[1] != 1 {
		t.Errorf("suggested %v, want [0 1] - white to the white spool, blue to the blue", got)
	}
}

// Two slots of similar colours must not both land on the same spool: the
// suggestion has to be usable as-is.
func TestSuggestSlotTraysNeverOffersOneSpoolTwice(t *testing.T) {
	slots := []queueSlot{{Index: 0, Hex: "#FFFFFF"}, {Index: 1, Hex: "#FFFFF0"}}
	ams, a, b := 0, 0, 1
	trays := []queueTray{
		{Hex: "#FFFFFF", AmsID: &ams, TrayID: &a},
		{Hex: "#2850E0", AmsID: &ams, TrayID: &b},
	}

	got := suggestSlotTrays(slots, trays)
	if len(got) != 2 || got[0] == got[1] {
		t.Errorf("suggested %v, want two different spools", got)
	}
}
