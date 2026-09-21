package httpapi

// Binding each part of the plate to the spool that prints it.
//
// This decides what plastic comes out, so it is pure: everything it needs is
// passed in, and it can be tested without a database, a printer or a network.
//
// It does NOT work out which tray is "blue". An AMS reports a colour as a bare
// hex with no name attached, an order names one in words, and on this fleet 11
// of 14 loaded hexes appear in no catalogue at all - so any automatic answer is
// a guess, and a guess here prints a plank in a colour nobody ordered. The
// operator, looking at both swatches, says which tray serves which slot; this
// checks their answer is physically possible and refuses when it is not.

import (
	"fmt"

	"github.com/Optiminastic/tensor-core/internal/meshio"
)

// amsExternalSpool is the ams_mapping value for a printer's external spool
// holder rather than an AMS slot. Observed on this fleet's A1.
const amsExternalSpool = 254

// amsSlotUnused is the ams_mapping value for a declared slot that no tray
// serves. Observed in BambuBuddy's own queue items, e.g. [-1, 2, 1].
const amsSlotUnused = -1

// colourIdentity is one shop colour and every hex a printer may report for it.
//
// Several hexes per name because thirteen printers do not agree on blue - the
// spools are generic, so each reports its own value, and a bed needing BLUE is
// satisfied by any spool the shop has confirmed IS blue.
type colourIdentity struct {
	Name string
	// Hexes are normalised "#RRGGBB", primary first.
	Hexes []string
}

// slotAssignment is one plate slot bound to one physical tray.
type slotAssignment struct {
	// SlotIndex is 0-based, in the plate's own declared order.
	SlotIndex  int
	ColourName string
	// PlateHex is what the plate declares. Kept for the log and the operator's
	// note: when it differs from TrayHex, that difference is the bug this whole
	// path exists to absorb.
	PlateHex string
	// TrayHex is what the printer actually reports for the chosen tray. THIS is
	// what gets sent as filament_colours, so the sliced file declares the colour
	// that is physically loaded and the filament check compares a tray with
	// itself.
	TrayHex string
	// AmsIndex is the ams_mapping integer for that tray.
	AmsIndex int
	Material string
}

// trayColoursOf is the filament_colours array for a slice request: the colours
// PHYSICALLY LOADED, in the plate's slot order.
func trayColoursOf(assignments []slotAssignment) []string {
	out := make([]string, 0, len(assignments))
	for _, a := range assignments {
		out = append(out, a.TrayHex)
	}
	return out
}

// amsMappingOf is the ams_mapping array: which tray serves each plate slot.
func amsMappingOf(assignments []slotAssignment) []int {
	out := make([]int, 0, len(assignments))
	for _, a := range assignments {
		out = append(out, a.AmsIndex)
	}
	return out
}

// assignmentsFromChoice binds plate slots to the trays an operator picked.
//
// This is the path the dialog uses, and it needs no colour map at all: the
// operator is looking at both swatches and says which tray prints which slot,
// which is the judgement they were making by eye anyway. Tensor's job is to
// check the answer is physically possible, not to second-guess it.
//
// What it still refuses, because these are not judgement calls:
//   - a choice that does not cover every slot, since a truncated ams_mapping
//     prints the lettering in the body colour;
//   - a tray that is not on this machine, which would map a slot to nothing;
//   - the same tray twice, which would ask one spool to print two colours.
func assignmentsFromChoice(
	slots []meshio.Slot, trays []loadedTray, chosen []int,
) ([]slotAssignment, error) {
	if len(slots) == 0 {
		return nil, fmt.Errorf("this bed's plate declares no filament at all; rebuild the bed")
	}
	if len(chosen) != len(slots) {
		return nil, fmt.Errorf(
			"this bed needs a spool chosen for each of its %d slots, but %d were given",
			len(slots), len(chosen))
	}

	byIndex := map[int]loadedTray{}
	for _, t := range trays {
		if index, ok := amsSlotIndex(t); ok {
			byIndex[index] = t
		}
	}

	used := map[int]bool{}
	out := make([]slotAssignment, 0, len(slots))
	for i, amsIndex := range chosen {
		tray, ok := byIndex[amsIndex]
		if !ok {
			return nil, fmt.Errorf(
				"slot %d was given a spool this printer does not have loaded; reopen the dialog and pick again",
				i+1)
		}
		if used[amsIndex] {
			return nil, fmt.Errorf(
				"slot %d was given the same spool as an earlier slot; one spool cannot print two colours",
				i+1)
		}
		trayHex, ok := normaliseHex(tray.Colour)
		if !ok {
			return nil, fmt.Errorf("the spool chosen for slot %d reports no readable colour", i+1)
		}
		used[amsIndex] = true

		plateHex, _ := normaliseHex(slots[i].Colour)
		out = append(out, slotAssignment{
			SlotIndex: i, PlateHex: plateHex, TrayHex: trayHex,
			AmsIndex: amsIndex, Material: slots[i].Material,
		})
	}
	return out, nil
}
