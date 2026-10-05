package httpapi

// What a machine is MISSING has to be the same question the binding asks.
//
// These two used to disagree. missingColours tested the bed's resolved hex for
// exact membership in the loaded list, while bindPlateToTrays went through
// acceptedHexes - so a printer the dispatcher would have bound without
// complaint was shown as missing the colour, and the screen was more
// pessimistic than the floor about the same machine.

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/Optiminastic/tensor-core/internal/db/gen"
)

// machineReporting builds a machine whose mirror reports these tray colours.
//
// Distinct from machineHolding, which takes raw JSON: these cases care only
// about which colours a printer reports, not about tray positions.
//
// No arguments means a machine reporting NOTHING - which is what every machine
// looks like once the fleet sync stops refreshing machines.filaments, and is a
// different thing from a machine whose spools are empty.
func machineReporting(hexes ...string) gen.Machine {
	trays := make([]map[string]any, 0, len(hexes))
	for i, hex := range hexes {
		ams, tray := 0, i
		trays = append(trays, map[string]any{
			"colour": hex, "type": "PLA", "ams_id": ams, "tray_id": tray,
		})
	}
	raw, err := json.Marshal(trays)
	if err != nil {
		panic(err)
	}
	return gen.Machine{Filaments: raw}
}

// goldIdentity is the shop's real situation: one colour, two spools, two hexes.
//
// The hexes are the ones the fleet actually reports. Neither is #D4AF37, which
// is what the built-in table thinks gold is - that number came from a lookup
// table and no printer has ever held it.
func goldIdentity() []colourIdentity {
	return []colourIdentity{{Name: "GOLD", Hexes: []string{"#D3C5A3", "#D3B7A7"}}}
}

// The case that prompted this: a second spool of the same colour.
//
// Only one hex can be a colour's primary, and the primary is what gets written
// into the plate. Every other mapped hex is a spool the shop has confirmed is
// also that colour, and a machine holding one is not missing anything.
func TestAMachineHoldingTheOtherGoldIsNotMissingGold(t *testing.T) {
	needed := []queueColour{{Name: "GOLD", Hex: "#D3C5A3"}} // the primary
	bed := bedColours{Names: []string{"GOLD"}}

	// The printer with the primary spool. Never in doubt.
	if got := missingColours(needed, []string{"#D3C5A3"}, goldIdentity(), bed); len(got) != 0 {
		t.Errorf("missing = %v on the machine holding the primary gold, want none", got)
	}

	// The printer with the OTHER gold. This is the one that used to read as
	// missing while the dispatcher bound it happily.
	if got := missingColours(needed, []string{"#D3B7A7"}, goldIdentity(), bed); len(got) != 0 {
		t.Errorf("missing = %v on the machine holding the alternative gold; the colour "+
			"map says that spool IS gold, and the binding already agrees", got)
	}
}

// A plate built before its colour was mapped carries an orphaned hex.
//
// It declares #D4AF37 because that is what the built-in table said gold was
// when the bed was merged. No printer reports it. Under exact matching EVERY
// machine was missing gold, however carefully the real spools were mapped -
// the bed asking for a number that only ever existed in a lookup table.
func TestAPlateCarryingTheBuiltInHexIsNotMissingEverywhere(t *testing.T) {
	needed := []queueColour{{Name: "GOLD", Hex: "#D4AF37"}}
	bed := bedColours{Names: []string{"GOLD"}}

	if got := missingColours(needed, []string{"#D3C5A3"}, goldIdentity(), bed); len(got) != 0 {
		t.Errorf("missing = %v; the plate's hex reads back as GOLD through the built-in "+
			"table, and this machine holds a confirmed gold", got)
	}
}

// Widened, not abandoned. A colour nothing on the machine can be is missing.
func TestAColourTheMachineReallyLacksIsStillMissing(t *testing.T) {
	needed := []queueColour{{Name: "GOLD", Hex: "#D3C5A3"}}
	bed := bedColours{Names: []string{"GOLD"}}

	got := missingColours(needed, []string{"#2850E0"}, goldIdentity(), bed)
	if !slices.Contains(got, "GOLD") {
		t.Errorf("missing = %v, want GOLD; a printer holding only blue cannot print "+
			"this bed, and saying otherwise sends a plate to a machine that will refuse it", got)
	}
}

// An unresolvable colour is skipped rather than reported.
//
// A bed whose colour never resolved carries an empty hex, and there is nothing
// to compare. Naming it missing would blame the printer for a colour Tensor
// could not look up.
func TestAColourWithNoHexIsNotBlamedOnTheMachine(t *testing.T) {
	needed := []queueColour{{Name: "CHARTREUSE", Hex: ""}}
	if got := missingColours(needed, []string{"#D3C5A3"}, goldIdentity(), bedColours{}); len(got) != 0 {
		t.Errorf("missing = %v, want none; the colour has no hex to be missing", got)
	}
}

// An unnormalised tray hex still matches.
//
// AMS values arrive as 8-char RGBA and the map's keys are "#RRGGBB" uppercase,
// so a raw key would silently never match and every machine would read as
// missing every colour.
func TestLoadedHexesAreNormalisedBeforeComparing(t *testing.T) {
	needed := []queueColour{{Name: "GOLD", Hex: "#D3C5A3"}}
	for _, raw := range []string{"d3c5a3", "D3C5A3FF", "#d3c5a3"} {
		if got := missingColours(needed, []string{raw}, goldIdentity(), bedColours{}); len(got) != 0 {
			t.Errorf("missing = %v for a tray reporting %q, want none", got, raw)
		}
	}
}

// The refusal has to name the spool, because naming it IS the fix.
//
// "No spool has been confirmed as one of this bed's colours. Map it under
// Inventory" told an operator to go and name a colour without saying which one,
// on a page listing every colour in the building. The hex is already sitting in
// each machine's refusal and the order's own word for it is on the bed, so the
// note can say both.
func TestTheRefusalNamesTheSpoolNobodyHasConfirmed(t *testing.T) {
	gold := []queueColour{{Name: "GOLD", Hex: "#D3C5A3"}}
	refused := []machineOption{
		{Machine: machineReporting("#2850E0"), Refusal: "no spool has been confirmed as #D3C5A3"},
		// A second printer refusing for the same reason must not say it twice.
		{Machine: machineReporting("#2850E0"), Refusal: "no spool has been confirmed as #D3C5A3"},
	}

	note := noPrinterNote(refused, gold)
	for _, want := range []string{"#D3C5A3", "GOLD", "Inventory"} {
		if !strings.Contains(note, want) {
			t.Errorf("note = %q, want it to name %q", note, want)
		}
	}
	if strings.Count(note, "#D3C5A3") != 1 {
		t.Errorf("note = %q repeats the hex; every printer refuses for the same spool", note)
	}
	if strings.Contains(strings.ToLower(note), "queue this bed") {
		t.Errorf("note = %q names a button that was deleted", note)
	}
}

// The bug this shipped with, caught on the floor.
//
// A plank is a WHITE body plus a lettering colour, so the plate has two slots
// while the bed names one colour - bedColours omits the body deliberately,
// because white matches itself. A machine that could not bind the body refused
// with "confirmed as #FFFFFF", the first version counted one lettering colour
// and concluded that hex must be it, and the note read "#FFFFFF (GOLD)".
//
// White is not gold. It sent an operator to rename a gold spool over a white
// slot. The name is now matched by hex, so a hex no bed colour claims keeps the
// bare number.
func TestTheWhiteBodySlotIsNeverNamedAfterTheLetteringColour(t *testing.T) {
	gold := []queueColour{{Name: "GOLD", Hex: "#D3C5A3"}}
	note := noPrinterNote([]machineOption{
		{Machine: machineReporting("#D3C5A3"), Refusal: "no spool has been confirmed as #FFFFFF"},
	}, gold)

	if !strings.Contains(note, "#FFFFFF") {
		t.Errorf("note = %q, want the hex that is actually missing", note)
	}
	if strings.Contains(note, "GOLD") {
		t.Errorf("note = %q calls #FFFFFF gold; it is the white plank body, and this "+
			"sends somebody to rename a spool that was never the problem", note)
	}
}

// Two colours missing: each is named from its own hex, not by position.
func TestEachMissingHexIsNamedFromItsOwnColour(t *testing.T) {
	colours := []queueColour{{Name: "GOLD", Hex: "#D3C5A3"}, {Name: "RED", Hex: "#F72323"}}
	note := noPrinterNote([]machineOption{
		{Machine: machineReporting("#2850E0"), Refusal: "no spool has been confirmed as #D3C5A3"},
		{Machine: machineReporting("#2850E0"), Refusal: "no spool has been confirmed as #F72323"},
	}, colours)

	for _, want := range []string{"#D3C5A3 (GOLD)", "#F72323 (RED)"} {
		if !strings.Contains(note, want) {
			t.Errorf("note = %q, want it to contain %q", note, want)
		}
	}
}

// Falls back rather than inventing. A refusal carrying no hex still produces
// the old sentence - vague, but never wrong.
func TestTheRefusalFallsBackWhenNoHexIsAvailable(t *testing.T) {
	note := noPrinterNote([]machineOption{
		{Machine: machineReporting("#2850E0"), Refusal: "no spool has been confirmed as banana"},
	}, []queueColour{{Name: "GOLD", Hex: "#D3C5A3"}})
	if !strings.Contains(note, "one of this bed's colours") {
		t.Errorf("note = %q, want the generic fallback for an unreadable hex", note)
	}
}

// A fleet Tensor cannot read is not a fleet that needs mapping.
//
// Every branch below the first assumes machines.filaments reflects the floor.
// When BambuBuddy goes unreachable the sync stops refreshing it, every slot
// binds against zero trays, and every bed reads "no spool has been confirmed as
// ..." - so two beds asked an operator to go and confirm #FFFFFF, white, the
// plank body, loaded in most of the fleet. The spools were fine. Saying
// "Inventory" there costs somebody half an hour looking for a problem that is
// not in the colour map.
func TestAFleetWithNoReadableTraysBlamesTheConnectionNotTheColourMap(t *testing.T) {
	blind := []machineOption{
		{Machine: machineReporting(), Refusal: "no spool has been confirmed as #FFFFFF"},
		{Machine: machineReporting(), Refusal: "no spool has been confirmed as #FFFFFF"},
	}
	note := noPrinterNote(blind, []queueColour{{Name: "GOLD", Hex: "#D3C5A3"}})

	if !strings.Contains(note, "BambuBuddy") {
		t.Errorf("note = %q, want it to name the unreachable service", note)
	}
	if strings.Contains(note, "Inventory") {
		t.Errorf("note = %q sends somebody to map a colour, but Tensor cannot see any "+
			"printer - nothing it maps there can change the answer", note)
	}
}

// One readable tray anywhere means the fleet IS being read, and a genuine
// colour problem must still be reported as one. A single printer offline is
// ordinary; the ranking already accounts for it.
func TestOneReadableTrayIsEnoughToTrustTheFleet(t *testing.T) {
	note := noPrinterNote([]machineOption{
		{Machine: machineReporting(), Refusal: "no spool has been confirmed as #D3C5A3"},
		{Machine: machineReporting("#2850E0"), Refusal: "no spool has been confirmed as #D3C5A3"},
	}, []queueColour{{Name: "GOLD", Hex: "#D3C5A3"}})

	if !strings.Contains(note, "Inventory") {
		t.Errorf("note = %q; one printer reporting trays means the mirror is live, so an "+
			"unmapped colour is a real unmapped colour", note)
	}
}
