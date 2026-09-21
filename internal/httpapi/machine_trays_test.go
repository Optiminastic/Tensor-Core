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
