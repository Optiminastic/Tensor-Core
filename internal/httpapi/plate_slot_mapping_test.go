package httpapi

import (
	"errors"
	"slices"
	"strings"
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

// --- bindPlateToTrays: the automatic gate ---

func trayAt(ams, tray int, hex, material string) loadedTray {
	a, t := ams, tray
	return loadedTray{Colour: hex, Type: material, AmsID: &a, TrayID: &t}
}

func plateSlot(hex, material string) meshio.Slot {
	return meshio.Slot{Colour: hex, Material: material}
}

// The most common bed on this floor, and the case that must work with an empty
// colour map: a white plank body plus a lettering colour, on a printer that
// reports exactly those hexes. Nobody has to confirm that #FFFFFF is white.
func TestBindPlateToTraysMatchesAnExactHexWithNoColourMapAtAll(t *testing.T) {
	got, err := bindPlateToTrays(
		[]meshio.Slot{plateSlot("#FFFFFF", "PLA"), plateSlot("#2850E0", "PLA")},
		[]loadedTray{trayAt(0, 0, "#FFFFFF", "PLA"), trayAt(0, 1, "#2850E0", "PLA")},
		nil,
	)
	if err != nil {
		t.Fatalf("bindPlateToTrays refused an exact match with no map: %v", err)
	}
	if want := []int{0, 1}; !slices.Equal(got, want) {
		t.Errorf("binding = %v, want %v", got, want)
	}
}

// The reason the colour map exists. Tensor writes #1560BD into the plate for
// BLUE; every printer on this fleet reports #2850E0. One confirmed row
// reconciles them, and without it a blue bed can never be queued.
func TestBindPlateToTraysReconcilesThePlateAndTheSpoolThroughTheColourMap(t *testing.T) {
	got, err := bindPlateToTrays(
		[]meshio.Slot{plateSlot("#1560BD", "PLA")},
		[]loadedTray{trayAt(1, 2, "#2850E0", "PLA")},
		[]colourIdentity{{Name: "BLUE", Hexes: []string{"#1560BD", "#2850E0"}}},
	)
	if err != nil {
		t.Fatalf("bindPlateToTrays refused a mapped colour: %v", err)
	}
	if want := []int{1*traysPerAMS + 2}; !slices.Equal(got, want) {
		t.Errorf("binding = %v, want %v", got, want)
	}
}

// Nearest-RGB as a gate would merge these. They are 1452 apart, which is LESS
// than the 1842 of drift a blue bed legitimately needs tolerated - so any
// threshold wide enough for blue prints this bed in the wrong black.
func TestBindPlateToTraysDoesNotMergeTwoBlacks(t *testing.T) {
	_, err := bindPlateToTrays(
		[]meshio.Slot{plateSlot("#161616", "PLA")},
		[]loadedTray{trayAt(0, 0, "#000000", "PLA")},
		nil,
	)
	if err == nil {
		t.Fatal("bound #161616 to a #000000 spool; these are two different blacks")
	}
}

// 212 apart, and genuinely different spools.
func TestBindPlateToTraysDoesNotMergeTwoTans(t *testing.T) {
	_, err := bindPlateToTrays(
		[]meshio.Slot{plateSlot("#D3B7A7", "PLA")},
		[]loadedTray{trayAt(0, 0, "#D3C5A3", "PLA")},
		nil,
	)
	if err == nil {
		t.Fatal("bound #D3B7A7 to a #D3C5A3 spool; these are two different spools")
	}
}

func TestBindPlateToTraysRefusesTheRightColourInTheWrongPlastic(t *testing.T) {
	_, err := bindPlateToTrays(
		[]meshio.Slot{plateSlot("#FFFFFF", "PLA")},
		[]loadedTray{trayAt(0, 0, "#FFFFFF", "PETG")},
		nil,
	)
	var unserved slotUnservedError
	if !errors.As(err, &unserved) || unserved.Reason != slotWrongMaterial {
		t.Fatalf("err = %v, want a wrong-material refusal", err)
	}
	if !strings.Contains(unserved.Error(), "PETG") || !strings.Contains(unserved.Error(), "PLA") {
		t.Errorf("message %q names neither plastic", unserved.Error())
	}
}

// PA-CF on Tensor's side is PA6-CF on Bambu's. Same spool, and refusing it
// would ground a bed over a spelling difference.
func TestBindPlateToTraysToleratesTheShelfsMaterialSpelling(t *testing.T) {
	if _, err := bindPlateToTrays(
		[]meshio.Slot{plateSlot("#FFFFFF", "PLA")},
		[]loadedTray{trayAt(0, 0, "#FFFFFF", "PLA Matte")},
		nil,
	); err != nil {
		t.Fatalf("refused a PLA bed on a 'PLA Matte' spool: %v", err)
	}
}

// Three refusals, three different things for the operator to do.
func TestBindPlateToTraysDistinguishesItsRefusals(t *testing.T) {
	blue := []colourIdentity{{Name: "BLUE", Hexes: []string{"#1560BD", "#2850E0"}}}
	white := trayAt(0, 0, "#FFFFFF", "PLA")

	cases := []struct {
		name       string
		slot       meshio.Slot
		trays      []loadedTray
		identities []colourIdentity
		want       string
	}{
		{
			name: "a colour nothing in the system can name",
			slot: plateSlot("#D4AF37", "PLA"), trays: []loadedTray{white},
			want: slotUnmapped,
		},
		{
			name: "a known colour this printer does not hold",
			slot: plateSlot("#1560BD", "PLA"), trays: []loadedTray{white}, identities: blue,
			want: slotNotLoaded,
		},
		{
			name: "a colour no row names but some printer does report",
			slot: plateSlot("#0ACC38", "PLA"),
			// The spool is right there, just already committed below.
			trays: []loadedTray{trayAt(0, 0, "#0ACC38", "PLA")},
			want:  slotTrayTaken,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			used := map[int]bool{}
			if tc.want == slotTrayTaken {
				used[0] = true // an earlier slot of the same bed took it
			}
			_, err := bindOneSlot(tc.slot, tc.trays, tc.identities, used)
			var unserved slotUnservedError
			if !errors.As(err, &unserved) {
				t.Fatalf("err = %v, want a slotUnservedError", err)
			}
			if unserved.Reason != tc.want {
				t.Errorf("reason = %q, want %q (message: %s)", unserved.Reason, tc.want, unserved.Error())
			}
		})
	}
}

func TestBindPlateToTraysRefusesAPrinterWithFewerSpoolsThanSlots(t *testing.T) {
	_, err := bindPlateToTrays(
		[]meshio.Slot{plateSlot("#FFFFFF", "PLA"), plateSlot("#2850E0", "PLA")},
		[]loadedTray{trayAt(0, 0, "#FFFFFF", "PLA")},
		nil,
	)
	if err == nil {
		t.Fatal("bound a two-slot bed to a printer holding one spool")
	}
}

// Never hand one spool to two slots: the second would print in the first's
// colour and nothing downstream would report a fault.
func TestBindPlateToTraysNeverUsesOneSpoolTwice(t *testing.T) {
	_, err := bindPlateToTrays(
		[]meshio.Slot{plateSlot("#FFFFFF", "PLA"), plateSlot("#FFFFFF", "PLA")},
		[]loadedTray{trayAt(0, 0, "#FFFFFF", "PLA"), trayAt(0, 1, "#161616", "PLA")},
		nil,
	)
	var unserved slotUnservedError
	if !errors.As(err, &unserved) || unserved.Reason != slotTrayTaken {
		t.Fatalf("err = %v, want the second white slot to be refused", err)
	}
}

// THE test. The automatic binding and the validator that guards the send path
// must agree exactly - a binding this produces that assignmentsFromChoice then
// rejects would be an auto-queue that 409s every time, and the two live on
// opposite sides of an HTTP round trip where nothing would catch it.
func TestBindPlateToTraysProducesABindingAssignmentsFromChoiceAccepts(t *testing.T) {
	slots := []meshio.Slot{plateSlot("#1560BD", "PLA"), plateSlot("#FFFFFF", "PLA")}
	trays := []loadedTray{
		trayAt(0, 0, "#FFFFFF", "PLA"),
		trayAt(1, 3, "#2850E0", "PLA"),
		trayAt(0, 2, "#161616", "PLA"),
	}
	identities := []colourIdentity{{Name: "BLUE", Hexes: []string{"#1560BD", "#2850E0"}}}

	chosen, err := bindPlateToTrays(slots, trays, identities)
	if err != nil {
		t.Fatalf("bindPlateToTrays: %v", err)
	}

	assignments, err := assignmentsFromChoice(slots, trays, chosen)
	if err != nil {
		t.Fatalf("assignmentsFromChoice rejected what bindPlateToTrays chose (%v): %v", chosen, err)
	}
	// And the colours sent to the slicer are the TRAYS', not the plate's - the
	// whole point of reconciling them.
	if got := trayColoursOf(assignments); !slices.Equal(got, []string{"#2850E0", "#FFFFFF"}) {
		t.Errorf("filament colours = %v, want the spools' own hexes", got)
	}
}

// The hole this closes. The machine is chosen from machines.filaments, a mirror
// up to a sync interval old, and a binding is a list of tray POSITIONS - so a
// spool swapped in that minute leaves the positions valid and their contents
// wrong. Re-running the binding against the live trays refuses; re-using the
// stale one would have sliced the bed declaring whatever now sits in that slot,
// and printed the lettering in it.
func TestABindingFromStaleTraysIsRefusedWhenTheSpoolHasChanged(t *testing.T) {
	slots := []meshio.Slot{plateSlot("#FFFFFF", "PLA"), plateSlot("#2850E0", "PLA")}
	mirror := []loadedTray{trayAt(0, 0, "#FFFFFF", "PLA"), trayAt(0, 1, "#2850E0", "PLA")}

	stale, err := bindPlateToTrays(slots, mirror, nil)
	if err != nil {
		t.Fatalf("binding against the mirror: %v", err)
	}

	// Somebody pulled the blue out of slot 2 and put red in.
	live := []loadedTray{trayAt(0, 0, "#FFFFFF", "PLA"), trayAt(0, 1, "#F72323", "PLA")}

	// The stale binding still validates, because those tray positions exist -
	// which is exactly why validating it is not enough.
	if _, err := assignmentsFromChoice(slots, live, stale); err != nil {
		t.Fatalf("expected the stale mapping to still look valid, got %v", err)
	}
	// Re-binding through the colour map catches it.
	if _, err := bindPlateToTrays(slots, live, nil); err == nil {
		t.Fatal("re-binding accepted a printer that no longer holds this bed's blue")
	}
}

// And the operator's own answer is still taken as given: they are standing at
// the machine, and a spool Tensor cannot name is one they can see.
func TestAnOperatorsChoiceIsNotSecondGuessedByTheColourMap(t *testing.T) {
	slots := []meshio.Slot{plateSlot("#FFFFFF", "PLA"), plateSlot("#D4AF37", "PLA")}
	trays := []loadedTray{trayAt(0, 0, "#FFFFFF", "PLA"), trayAt(0, 1, "#D3B7A7", "PLA")}

	// GOLD is unmapped, so Tensor would refuse this bed on its own.
	if _, err := bindPlateToTrays(slots, trays, nil); err == nil {
		t.Fatal("expected an unmapped gold to be refused automatically")
	}
	// The operator says slot 2 prints from the second tray, and that stands.
	if _, err := assignmentsFromChoice(slots, trays, []int{0, 1}); err != nil {
		t.Fatalf("an operator's explicit choice was refused: %v", err)
	}
}
