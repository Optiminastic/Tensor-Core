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
	"slices"
	"strings"

	"github.com/Optiminastic/tensor-core/internal/meshio"
	"github.com/Optiminastic/tensor-core/internal/production"
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
				"slot %d was given a spool this printer does not have loaded; it may have been changed since",
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

// bindPlateToTrays decides, without a human, which spool prints each slot.
//
// This is the automatic sibling of assignmentsFromChoice, and it answers a
// deliberately narrower question: not "which tray looks closest" but "which
// tray IS this colour". The difference is the whole design.
//
// Nearest-colour matching cannot be the gate, and the fleet's own spools prove
// it. Distances below are squared RGB, as nearestColourDistance returns them.
// Tensor must ACCEPT the drift between the catalogue and the spool - BLUE is
// written #1560BD into plates and reported #2850E0 by the AMS, a distance of
// 1842. Tensor must REJECT two spools the shop treats as different - #161616
// and #000000 are two blacks, 1452 apart. 1842 > 1452, so no threshold exists
// that does both, and that is before gold, whose catalogue hex is 12609 from
// the spool because it is simply wrong (#D3B7A7 is the one BambuBuddy calls
// Latte Brown).
//
// So the gate is the colour map: a shop colour name accepting several
// operator-confirmed hexes, which is exactly what that table was built for. A
// tray serves a slot when it holds one of the hexes confirmed for that slot's
// colour - or the slot's own hex, which needs no map at all and is what lets a
// white plank body print on a fleet where nobody has typed "WHITE" anywhere.
//
// Nearest-colour survives in one place: ordering the trays that already
// qualify, so the closest of two confirmed BLUE spools is the one used.
func bindPlateToTrays(
	slots []meshio.Slot, trays []loadedTray, identities []colourIdentity,
) ([]int, error) {
	if len(slots) == 0 {
		return nil, fmt.Errorf("this bed's plate declares no filament at all; rebuild the bed")
	}
	if len(trays) < len(slots) {
		return nil, fmt.Errorf("holds %d %s; this bed needs %d",
			len(trays), plural(len(trays), "spool"), len(slots))
	}

	used := map[int]bool{}
	out := make([]int, 0, len(slots))
	for i, slot := range slots {
		index, err := bindOneSlot(slot, trays, identities, used)
		if err != nil {
			return nil, fmt.Errorf("slot %d: %w", i+1, err)
		}
		used[index] = true
		out = append(out, index)
	}
	return out, nil
}

// bindOneSlot picks the tray for one slot, or explains why none will do.
func bindOneSlot(
	slot meshio.Slot, trays []loadedTray, identities []colourIdentity, used map[int]bool,
) (int, error) {
	wanted, ok := normaliseHex(slot.Colour)
	if !ok {
		return 0, fmt.Errorf("the plate declares no readable colour; rebuild the bed")
	}
	accepted := acceptedHexes(wanted, identities)

	best, bestDistance := -1, 0
	for _, tray := range trays {
		index, positioned := amsSlotIndex(tray)
		if !positioned || used[index] {
			continue
		}
		hex, readable := normaliseHex(tray.Colour)
		if !readable || !accepted[hex] || !materialsAgree(slot.Material, tray.Type) {
			continue
		}
		// Among spools that all count as this colour, the closest one.
		d, measurable := nearestColourDistance(wanted, []string{hex})
		if !measurable {
			continue
		}
		if best < 0 || d < bestDistance {
			best, bestDistance = index, d
		}
	}
	if best < 0 {
		return 0, unservedSlotError(slot, wanted, trays, identities)
	}
	return best, nil
}

// acceptedHexes is every hex that counts as this slot's colour.
//
// Always includes the plate's own hex. An exact identity needs no confirmation
// from anybody - #FFFFFF is the plank body on every bed and sits in nine of
// this fleet's fourteen printers - and refusing it because the colour map has
// no WHITE row would be absurd. The map's job is to reconcile hexes that
// DISAGREE, so it is consulted for exactly that.
func acceptedHexes(wanted string, identities []colourIdentity) map[string]bool {
	out := map[string]bool{wanted: true}
	for _, id := range identities {
		if !slices.Contains(id.Hexes, wanted) {
			continue
		}
		for _, h := range id.Hexes {
			out[h] = true
		}
	}
	return out
}

// materialsAgree compares the plate's plastic with the tray's.
//
// Both sides are normalised first because the two vocabularies are close but
// not identical: Tensor writes PA-CF where Bambu reports PA6-CF, and the shelf
// qualifies a polymer with its product line ("PLA Matte"). An unknown material
// falls through unchanged on both sides, so an exotic plastic compares by its
// own name rather than being silently accepted.
//
// A plate or tray that declares nothing is not treated as a mismatch: an
// unstated material is no signal, and refusing on it would ground beds over a
// missing field.
func materialsAgree(plate, tray string) bool {
	want := production.FilamentType(strings.TrimSpace(plate))
	have := production.FilamentType(strings.TrimSpace(tray))
	if want == "" || have == "" {
		return true
	}
	return strings.EqualFold(want, have)
}

// Why a machine could not serve a slot. The distinction matters because each
// one asks the operator for a different thing.
const (
	// slotUnmapped: nothing anywhere says which spool is this colour. The fix
	// is a colour-map entry, not a different printer.
	slotUnmapped = "unmapped"
	// slotNotLoaded: the colour is known and this printer does not hold it.
	// The fix is another printer, or loading the spool.
	slotNotLoaded = "not_loaded"
	// slotWrongMaterial: the right colour in the wrong plastic.
	slotWrongMaterial = "wrong_material"
	// slotTrayTaken: the one qualifying spool is already printing another slot
	// of this same bed.
	slotTrayTaken = "tray_taken"
)

// slotUnservedError is one slot this machine cannot print, and why.
//
// Typed rather than a formatted string because the useful message names the
// colour in the words the ORDER used - "GOLD" - and that word lives with the
// jobs, not with the plate file this function reads. The caller knows it; this
// does not, and inventing a hex-coloured sentence here would put "#D4AF37" in
// front of somebody holding a spool.
type slotUnservedError struct {
	SlotIndex int
	Hex       string
	// Material is what the bed needs; TrayMaterial is what the one spool of the
	// right colour actually holds. Naming both is the difference between "load
	// a spool" and "that is the wrong plastic".
	Material     string
	TrayMaterial string
	Reason       string
}

func (e slotUnservedError) Error() string {
	switch e.Reason {
	case slotUnmapped:
		return fmt.Sprintf("no spool has been confirmed as %s", e.Hex)
	case slotWrongMaterial:
		return fmt.Sprintf("holds %s in %s; this bed is %s", e.Hex, e.TrayMaterial, e.Material)
	case slotTrayTaken:
		return fmt.Sprintf("has only one spool of %s, and this bed needs two slots of it", e.Hex)
	default:
		return fmt.Sprintf("does not hold %s", e.Hex)
	}
}

// unservedSlotError works out which of the four refusals applies.
//
// Ordered from the most specific cause to the least, so an operator is told the
// thing they can act on: a spool that is present but already committed is a
// different problem from one that is absent, and both are different from a
// colour nobody has ever confirmed.
func unservedSlotError(
	slot meshio.Slot, wanted string, trays []loadedTray, identities []colourIdentity,
) error {
	out := slotUnservedError{Hex: wanted, Material: production.FilamentType(slot.Material)}
	accepted := acceptedHexes(wanted, identities)

	var wrongMaterial bool
	for _, tray := range trays {
		hex, readable := normaliseHex(tray.Colour)
		if !readable || !accepted[hex] {
			continue
		}
		if !materialsAgree(slot.Material, tray.Type) {
			wrongMaterial = true
			out.TrayMaterial = production.FilamentType(tray.Type)
			continue
		}
		// Right colour, right plastic, readable position - so it was taken by
		// an earlier slot, or it has no recorded AMS position at all.
		if _, positioned := amsSlotIndex(tray); positioned {
			out.Reason = slotTrayTaken
			return out
		}
	}
	switch {
	case wrongMaterial:
		out.Reason = slotWrongMaterial
	case len(accepted) == 1 && !anyTrayHolds(wanted, trays):
		// Only the plate's own hex counts as this colour - no colour-map row
		// names it - and no printer reports that hex either. Nothing in the
		// system knows what this colour is.
		out.Reason = slotUnmapped
	default:
		out.Reason = slotNotLoaded
	}
	return out
}

func anyTrayHolds(hex string, trays []loadedTray) bool {
	for _, t := range trays {
		if h, ok := normaliseHex(t.Colour); ok && h == hex {
			return true
		}
	}
	return false
}
