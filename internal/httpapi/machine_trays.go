package httpapi

// One shape for "what is loaded in this printer", read and written in one place.
//
// The column is machines.filaments, written by filamentsJSON from a BambuBuddy
// status and read back by the queue dialog and the send path. It used to be
// described by two separate anonymous structs, one at each end, which is how a
// field added on the writing side quietly fails to appear on the reading side.
//
// It now carries WHERE each spool is, not just what colour it is. That is the
// difference between "P3 holds blue" and "P3 holds blue in AMS 1, tray 3" - and
// only the second can produce an ams_mapping, which is what tells BambuBuddy
// that plate slot 2 prints from that particular tray.

import (
	"encoding/json"
	"sort"

	"github.com/Optiminastic/tensor-core/internal/db/gen"
)

// loadedTray is one AMS slot with filament in it.
type loadedTray struct {
	// Colour is "#RRGGBB" as the printer reports it - not as Tensor names it.
	Colour         string  `json:"colour"`
	Type           string  `json:"type"`
	RemainingGrams float64 `json:"remaining_grams"`

	// AmsID and TrayID are POINTERS because AMS 0, tray 0 is a real physical
	// slot. A row written before these were recorded has neither, and a plain
	// int would decode that absence as "AMS 0, tray 0" - a plausible-looking
	// wrong answer that would route the plank body to whatever sits in the
	// first slot. Absent has to mean "re-sync the fleet", not "slot zero".
	AmsID  *int `json:"ams_id"`
	TrayID *int `json:"tray_id"`

	// TrayInfoIdx is BambuBuddy's filament id for the slot. Unused when building
	// an ams_mapping, and carried because configuring a slot (telling a printer
	// what is actually loaded) needs it, and backfilling it later would mean
	// re-syncing every machine.
	TrayInfoIdx string `json:"tray_info_idx,omitempty"`
}

// traysPerAMS is how many slots an AMS unit holds.
//
// ASSUMED, and load-bearing: amsSlotIndex flattens (ams, tray) with it, and
// BambuBuddy's ams_mapping documents no convention at all. If a plate ever
// prints its second colour from the wrong spool on a multi-AMS machine, this
// constant is the first thing to check - see the discovery step in the plan.
const traysPerAMS = 4

// decodeTrays reads what a machine holds, in a stable order.
func decodeTrays(m gen.Machine) []loadedTray {
	var trays []loadedTray
	if err := json.Unmarshal(m.Filaments, &trays); err != nil {
		return []loadedTray{}
	}
	return trays
}

// loadedColours is every distinct colour in one machine's AMS.
//
// Deduplicated because it answers "can this printer take a bed needing blue?",
// where two blue spools are not a different answer from one. The send path uses
// decodeTrays instead, because there it matters WHICH blue spool.
func loadedColours(m gen.Machine) []string {
	trays := decodeTrays(m)
	seen := map[string]bool{}
	out := make([]string, 0, len(trays))
	for _, t := range trays {
		hex, ok := normaliseHex(t.Colour)
		if !ok || seen[hex] {
			continue
		}
		seen[hex] = true
		out = append(out, hex)
	}
	return out
}

// amsSlotIndex is the integer BambuBuddy's ams_mapping expects for this tray.
//
// Reports false when the tray predates the ids being recorded, so a caller
// refuses to build a mapping rather than sending one built from zeroes.
func amsSlotIndex(t loadedTray) (int, bool) {
	if t.AmsID == nil || t.TrayID == nil {
		return 0, false
	}
	return *t.AmsID*traysPerAMS + *t.TrayID, true
}

// sortTrays orders trays by their physical position.
//
// The array is an ordered thing now, not a set: an operator reads "AMS 1, slot
// 2" off the screen and walks to that slot, so the order has to match the
// machine rather than whatever order BambuBuddy happened to answer in.
func sortTrays(trays []loadedTray) {
	sort.SliceStable(trays, func(i, j int) bool {
		ai, aok := amsSlotIndex(trays[i])
		bi, bok := amsSlotIndex(trays[j])
		if aok && bok {
			return ai < bi
		}
		// Trays with no position keep their relative order, behind the rest.
		return aok && !bok
	})
}
