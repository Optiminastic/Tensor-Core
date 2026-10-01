package httpapi

import (
	"testing"

	"github.com/Optiminastic/tensor-core/internal/db/gen"
)

func machineHolding(filaments string) gen.Machine {
	return gen.Machine{Filaments: []byte(filaments)}
}

func TestDecodeTraysReadsPosition(t *testing.T) {
	m := machineHolding(`[
		{"colour":"#FFFFFF","type":"PLA","remaining_grams":800,"ams_id":0,"tray_id":1},
		{"colour":"#2850E0","type":"PLA","remaining_grams":400,"ams_id":1,"tray_id":3}
	]`)

	trays := decodeTrays(m)
	if len(trays) != 2 {
		t.Fatalf("decodeTrays returned %d trays, want 2", len(trays))
	}

	got, ok := amsSlotIndex(trays[1])
	if !ok {
		t.Fatal("amsSlotIndex reported no position for a tray that has one")
	}
	if want := 1*traysPerAMS + 3; got != want {
		t.Errorf("amsSlotIndex = %d, want %d", got, want)
	}
}

// The reason AmsID and TrayID are pointers. A machine synced before the ids
// were recorded has neither, and decoding that absence as "AMS 0, tray 0" would
// be a plausible-looking wrong answer: the plate's first slot would be mapped to
// whatever spool happens to sit in the first tray, and the bed would print in
// the wrong colour with nothing reporting a fault.
func TestDecodeTraysTreatsAMissingPositionAsUnknownNotZero(t *testing.T) {
	legacy := machineHolding(`[{"colour":"#FFFFFF","type":"PLA","remaining_grams":800}]`)

	trays := decodeTrays(legacy)
	if len(trays) != 1 {
		t.Fatalf("decodeTrays returned %d trays, want 1", len(trays))
	}
	if trays[0].AmsID != nil || trays[0].TrayID != nil {
		t.Fatalf("a legacy row decoded to ams=%v tray=%v, want both nil",
			trays[0].AmsID, trays[0].TrayID)
	}
	if _, ok := amsSlotIndex(trays[0]); ok {
		t.Error("amsSlotIndex claimed a position for a tray that has none; " +
			"a mapping built from this would send a colour to the wrong slot")
	}
}

func TestDecodeTraysSurvivesAnUnreadableColumn(t *testing.T) {
	for _, filaments := range []string{"", "null", "{}", "not json"} {
		if got := decodeTrays(machineHolding(filaments)); len(got) != 0 {
			t.Errorf("decodeTrays(%q) = %v, want empty", filaments, got)
		}
	}
}

func TestLoadedColoursDedupesAndNormalises(t *testing.T) {
	m := machineHolding(`[
		{"colour":"#ffffff","type":"PLA","ams_id":0,"tray_id":0},
		{"colour":"2850E0FF","type":"PLA","ams_id":0,"tray_id":1},
		{"colour":"#FFFFFF","type":"PLA","ams_id":1,"tray_id":0},
		{"colour":"","type":"PLA","ams_id":1,"tray_id":1}
	]`)

	got := loadedColours(m)
	want := []string{"#FFFFFF", "#2850E0"}
	if len(got) != len(want) {
		t.Fatalf("loadedColours = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("loadedColours[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

// The array is read by a person walking to a slot, so it has to mean the same
// thing twice running rather than following whatever order BambuBuddy answered
// in.
func TestSortTraysOrdersByPhysicalPosition(t *testing.T) {
	two, one, zero := 2, 1, 0
	trays := []loadedTray{
		{Colour: "#C", AmsID: &one, TrayID: &two},
		{Colour: "#A", AmsID: &zero, TrayID: &one},
		{Colour: "#D"}, // no position: sorts last
		{Colour: "#B", AmsID: &one, TrayID: &zero},
	}

	sortTrays(trays)

	want := []string{"#A", "#B", "#C", "#D"}
	for i, w := range want {
		if trays[i].Colour != w {
			t.Errorf("sortTrays position %d = %q, want %q (order: %v)",
				i, trays[i].Colour, w, coloursOf(trays))
		}
	}
}

func coloursOf(trays []loadedTray) []string {
	out := make([]string, 0, len(trays))
	for _, t := range trays {
		out = append(out, t.Colour)
	}
	return out
}

// An AMS unit reports whatever id it likes - the AMS Lite on this shop's A2L
// printers reports 6 - and the sticker on the machine says "AMS 1". Labelling
// from the raw id sent an operator looking for AMS 7 on a printer with one unit.
func TestTrayLabelNumbersUnitsByPositionNotByReportedID(t *testing.T) {
	m := machineHolding(`[
		{"colour":"#FFFFFF","type":"PLA","ams_id":6,"tray_id":0},
		{"colour":"#2850E0","type":"PLA","ams_id":6,"tray_id":3}
	]`)

	got := traysFor(m)
	if len(got) != 2 {
		t.Fatalf("traysFor returned %d trays, want 2", len(got))
	}
	if got[0].Label != "AMS 1 · slot 1" {
		t.Errorf("label = %q, want %q - one unit is AMS 1 whatever id it reports",
			got[0].Label, "AMS 1 · slot 1")
	}
	if got[1].Label != "AMS 1 · slot 4" {
		t.Errorf("label = %q, want %q - slots count from one on the machine",
			got[1].Label, "AMS 1 · slot 4")
	}
	// The mapping still uses the REAL ids, which is what BambuBuddy expects.
	if idx, ok := amsSlotIndex(decodeTrays(m)[1]); !ok || idx != 27 {
		t.Errorf("ams index = %d, want 27 (6*4+3) - the label must not change the mapping", idx)
	}
}

func TestTrayLabelNumbersASecondUnitTwo(t *testing.T) {
	m := machineHolding(`[
		{"colour":"#FFFFFF","type":"PLA","ams_id":0,"tray_id":0},
		{"colour":"#2850E0","type":"PLA","ams_id":1,"tray_id":2}
	]`)

	got := traysFor(m)
	if got[0].Label != "AMS 1 · slot 1" || got[1].Label != "AMS 2 · slot 3" {
		t.Errorf("labels = %q, %q; want AMS 1 · slot 1 and AMS 2 · slot 3",
			got[0].Label, got[1].Label)
	}
}

// A tray whose position was never recorded gets no label, so the dialog shows
// its colour instead of inventing a slot to walk to.
func TestTrayLabelIsEmptyWhenThePositionIsUnknown(t *testing.T) {
	m := machineHolding(`[{"colour":"#FFFFFF","type":"PLA"}]`)
	if got := traysFor(m); len(got) != 1 || got[0].Label != "" {
		t.Errorf("label = %q, want empty for a tray with no recorded position", got[0].Label)
	}
}

// The spool Tensor could not see.
//
// An H2C's second extruder is fed from a spool on the back of the machine, not
// from the AMS, and that is where the white every plank body prints in lives.
// It appears in no AMS unit, so machines.filaments never held it, so the colour
// gate refused all three H2Cs for want of a filament that was loaded.
func TestDecodeTraysIncludesTheFixedNozzlesSpool(t *testing.T) {
	white, idx := "#FFFFFF", int32(1)
	m := gen.Machine{
		Filaments:         []byte(`[{"colour":"#C12E1E","type":"PLA","ams_id":0,"tray_id":2}]`),
		FixedNozzleColour: &white,
		FixedNozzleIndex:  &idx,
	}
	trays := decodeTrays(m)
	if len(trays) != 2 {
		t.Fatalf("decodeTrays returned %d trays, want the AMS spool and the fixed one", len(trays))
	}
	fixed := trays[len(trays)-1]
	if fixed.Colour != "#FFFFFF" {
		t.Errorf("fixed spool colour = %q, want the declared white", fixed.Colour)
	}
	// It has to be selectable, which means positioned: bindOneSlot skips any
	// tray whose position is unknown.
	index, positioned := amsSlotIndex(fixed)
	if !positioned {
		t.Fatal("the fixed spool has no position, so no plate slot could ever bind to it")
	}
	// 254, its own vt_tray id - NOT -1. Measured on H3 with one sliced file,
	// changing only this value: [-1,1] halted at the first tool change with
	// "[0700-8012] Failed to get AMS mapping table"; [254,1] completed the
	// change and printed on.
	if index != amsExternalSpool {
		t.Errorf("ams index = %d, want %d - the external spool addresses itself",
			index, amsExternalSpool)
	}
}

// A single-nozzle machine has no such spool, and a two-nozzle one nobody has
// told Tensor about has nothing to report. Neither may invent a colour.
func TestDecodeTraysAddsNothingWithoutADeclaredFixedSpool(t *testing.T) {
	loaded := []byte(`[{"colour":"#C12E1E","type":"PLA","ams_id":0,"tray_id":2}]`)
	white, idx := "#FFFFFF", int32(1)

	for name, m := range map[string]gen.Machine{
		"one nozzle":       {Filaments: loaded},
		"nothing declared": {Filaments: loaded, FixedNozzleIndex: &idx},
		"no such nozzle":   {Filaments: loaded, FixedNozzleColour: &white},
	} {
		if got := decodeTrays(m); len(got) != 1 {
			t.Errorf("%s: %d trays, want only what the AMS holds", name, len(got))
		}
	}
}

// The live re-read must not lose the fixed nozzle's spool.
//
// liveTraysFor rebuilds a machine from the printer's own AMS status, and an
// external spool is not in that status - it has no RFID. Rebuilding from the
// live read alone dropped the declared white, so the send refused a bed the
// ranking had just accepted: "H2 no longer holds this bed's colours: slot 1: no
// spool has been confirmed as #FFFFFF", for filament that was loaded.
func TestALiveTrayReadKeepsTheFixedNozzlesSpool(t *testing.T) {
	white, idx := "#FFFFFF", int32(1)
	// What liveTraysFor builds: the live AMS JSON plus the machine's own
	// declared fixed nozzle.
	live := gen.Machine{
		Filaments:         []byte(`[{"colour":"#FFF144","type":"PLA","ams_id":0,"tray_id":3}]`),
		FixedNozzleColour: &white,
		FixedNozzleIndex:  &idx,
	}
	trays := decodeTrays(live)
	if len(trays) != 2 {
		t.Fatalf("decodeTrays returned %d trays, want the AMS spool and the external one", len(trays))
	}
	var sawWhite bool
	for _, tr := range trays {
		if tr.Colour == "#FFFFFF" {
			sawWhite = true
		}
	}
	if !sawWhite {
		t.Error("the declared white was lost, so a two-colour bed would be refused at send time")
	}
}

// -1 and 254 are DIFFERENT ANSWERS and the printer treats them differently.
//
// -1 means "no tray serves this slot". 254 is the external feed's own vt_tray
// id. Conflating them is what sent seven two-colour plates into
// "[0700-8012] Failed to get AMS mapping table" at their first tool change:
// measured on H3 with one sliced file, changing only this value, [-1,1] halted
// there and [254,1] completed the change and printed on.
func TestTheExternalSpoolIsNotTheUnmappedSentinel(t *testing.T) {
	if amsExternalSpool == amsSlotUnused {
		t.Fatal("the external spool and the unmapped sentinel must stay distinct")
	}
	ams, tray := amsExternalSpool, 0
	index, ok := amsSlotIndex(loadedTray{Colour: "#FFFFFF", AmsID: &ams, TrayID: &tray})
	if !ok || index != amsExternalSpool {
		t.Fatalf("external spool index = %d (ok=%v), want %d", index, ok, amsExternalSpool)
	}
	if index == amsSlotUnused {
		t.Fatal("the external spool must never be reported as unmapped")
	}
}
