package httpapi

// What this bed needs, what each printer holds, and where.
//
// This does not decide anything. It reports what is true - the slots the plate
// declares, and the spool sitting in every tray of every printer - and a person
// binds one to the other. That is deliberately the opposite of the automatic
// picker it replaces, which scored the fleet and sent the plate wherever it
// judged best.
//
// It deliberately does NOT try to work out which tray is "blue". An AMS reports
// a colour as a bare hex with no name, an order names one in words, and nothing
// reconciles the two reliably - 11 of the 14 hexes loaded on this fleet appear
// in no catalogue at all. Tensor showing both swatches and letting the operator
// say "that one" is honest; Tensor guessing and being wrong prints a plank in a
// colour nobody ordered.
//
// Read from Tensor's own mirror of the fleet rather than from BambuBuddy: the
// sync writes every printer's AMS trays into machines.filaments (see
// filamentsJSON in bambubuddy_sync.go), so thirteen printers cost one query
// instead of thirteen calls. Up to a sync interval stale, which is the right
// trade for a list somebody reads before choosing.

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/Optiminastic/tensor-core/internal/db/gen"
	"github.com/Optiminastic/tensor-core/internal/meshio"
	"github.com/Optiminastic/tensor-core/internal/obs"
)

// queueOptionsResponse is what the Queue dialog draws.
type queueOptionsResponse struct {
	BatchNumber string `json:"batch_number"`
	Status      string `json:"status"`
	// Colours is what the bed needs loaded, name and swatch together: an
	// operator thinks in "blue", the printer answers in "#1560BD", and the
	// dialog has to speak both.
	Colours []queueColour `json:"colours"`
	// Slots are what the PLATE declares, in its own order - the thing the
	// operator actually binds to trays. Read from the file rather than derived
	// from the jobs, because the order is meshio's rule (the plank body takes
	// slot 1) and the jobs do not know it.
	Slots    []queueSlot    `json:"slots"`
	Machines []queueMachine `json:"machines"`
	// AutoReason is one line explaining the printer Tensor chose, shown beside
	// it so the choice can be read rather than merely accepted. Empty when no
	// printer can take the bed.
	AutoReason string `json:"auto_reason"`
	// Note says why nothing is offered when nothing is, rather than leaving a
	// blank dropdown to be interpreted.
	Note string `json:"note"`
}

type queueColour struct {
	Name string `json:"name"`
	Hex  string `json:"hex"`
}

// queueSlot is one filament slot the plate asks for.
type queueSlot struct {
	Index int    `json:"index"`
	Hex   string `json:"hex"`
	// Name is Tensor's word for the colour when it has one, for the operator's
	// benefit. Empty is normal and not a problem: the swatch is the thing being
	// matched, and the hex is shown beside it.
	Name     string `json:"name"`
	Material string `json:"material"`
}

// queueTray is one AMS slot, with where it is as well as what is in it.
type queueTray struct {
	Hex  string `json:"hex"`
	Type string `json:"type"`
	// Label is where this spool is, in the numbering printed ON THE MACHINE:
	// "AMS 1 - slot 4".
	//
	// Computed here rather than in the browser because only this side can see
	// the whole machine. An AMS unit reports an id of its own choosing - the AMS
	// Lite on this shop's A2L printers reports 6 - so labelling from the raw id
	// would send an operator looking for "AMS 7" on a printer that has one unit.
	// Position in the machine's own sorted list is what matches the sticker.
	Label          string  `json:"label"`
	AmsID          *int    `json:"ams_id"`
	TrayID         *int    `json:"tray_id"`
	RemainingGrams float64 `json:"remaining_grams"`
}

// traysFor renders a machine's slots for the dialog.
//
// A tray whose colour cannot be read is dropped rather than shown blank: an
// empty swatch beside a slot number reads as "this slot is empty", which is a
// different and wrong statement.
func traysFor(m gen.Machine) []queueTray {
	trays := decodeTrays(m)
	sortTrays(trays)

	// Number the AMS units by the order they appear, not by the id they report.
	// One unit is always "AMS 1" whatever it calls itself.
	unitNumber := map[int]int{}
	for _, t := range trays {
		if t.AmsID == nil {
			continue
		}
		if _, seen := unitNumber[*t.AmsID]; !seen {
			unitNumber[*t.AmsID] = len(unitNumber) + 1
		}
	}

	out := make([]queueTray, 0, len(trays))
	for _, t := range trays {
		hex, ok := normaliseHex(t.Colour)
		if !ok {
			continue
		}
		out = append(out, queueTray{
			Hex: hex, Type: t.Type, Label: trayLabel(t, unitNumber),
			AmsID: t.AmsID, TrayID: t.TrayID,
			RemainingGrams: t.RemainingGrams,
		})
	}
	return out
}

// trayLabel is where a spool is, as the machine's own labelling has it.
//
// Empty when the position was never recorded, so the caller shows the colour
// rather than inventing a location - a wrong slot number sends somebody to the
// wrong spool, which is worse than no slot number at all.
func trayLabel(t loadedTray, unitNumber map[int]int) string {
	if t.AmsID == nil || t.TrayID == nil {
		return ""
	}
	// Bambu's external spool holder, which is not an AMS slot at all.
	if *t.TrayID == amsExternalSpool {
		return "external spool"
	}
	unit, ok := unitNumber[*t.AmsID]
	if !ok {
		unit = 1
	}
	// Both ids count from zero; the machine's own labels count from one.
	return fmt.Sprintf("AMS %d · slot %d", unit, *t.TrayID+1)
}

// queueMachine is one printer and whether it can take this bed.
//
// Ineligible machines are listed too, with what they are missing. A printer an
// operator can see standing idle, absent from the list with no explanation, is
// the kind of thing that gets worked around rather than fixed.
type queueMachine struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Model  string `json:"model"`
	Status string `json:"status"`
	// Loaded is every colour in this printer's AMS right now, as hexes - the
	// dialog draws them as swatches beside the name.
	Loaded []string `json:"loaded"`
	// Trays is the same filament with its physical position attached, so the
	// dialog can say "AMS 1, slot 2" and an operator can walk to that slot.
	// Loaded stays because it answers the simpler question the swatch row asks.
	Trays []queueTray `json:"trays"`
	// SuggestedSlotTrays is the tray to use for each plate slot, in slot order,
	// as ams_mapping integers. Nearest colour, so the dialog opens on the
	// obvious answer - and it is only a default, shown beside both swatches for
	// the operator to correct.
	SuggestedSlotTrays []int `json:"suggested_slot_trays"`
	// Missing names the bed's colours this printer does not hold, by name
	// rather than by hex. Only populated when the colour map can say what a
	// colour is; empty otherwise, because a guess dressed as a fact is worse
	// than silence.
	Missing  []string `json:"missing"`
	Eligible bool     `json:"eligible"`
	// Suggested marks the one printer the dialog should offer first - the
	// closest colour match, idle before busy. A suggestion, never a decision:
	// the operator can pick any machine in the list.
	Suggested bool `json:"suggested"`
	// Reason explains an ineligible machine in one line.
	Reason string `json:"reason,omitempty"`
}

// batchQueueOptions reports what the bed needs and what each printer holds.
func (s *Server) batchQueueOptions(c *gin.Context) {
	id, ok := parseUUIDParam(c, "id")
	if !ok {
		return
	}
	ctx := c.Request.Context()

	batch, err := s.store.Q.GetBatchByID(ctx, id)
	if err != nil {
		dbError(c, err, "That batch does not exist.", "Could not load the batch.")
		return
	}
	jobs, err := s.store.Q.ListJobsForBatch(ctx, &id)
	if err != nil {
		detail(c, http.StatusInternalServerError, "Could not read the batch's jobs.")
		return
	}
	if len(jobs) == 0 {
		detail(c, http.StatusUnprocessableEntity, "This batch holds no jobs.")
		return
	}

	out := queueOptionsResponse{
		BatchNumber: batch.BatchNumber,
		Status:      batch.Status,
		Colours:     []queueColour{},
		Slots:       []queueSlot{},
		Machines:    []queueMachine{},
	}
	out.Colours = s.queueColoursFor(ctx, jobs)
	out.Slots = s.queueSlotsFor(ctx, batch)

	// Named colours, when the shop has recorded any. Used to label a slot and
	// to say what a machine is missing - never to decide anything, so an empty
	// map costs a label rather than the ability to send.
	identities, err := s.colourIdentities(ctx)
	if err != nil {
		obs.FromContext(ctx).Warn("could not read the colour map", "error", err)
	}
	nameSlots(out.Slots, identities)

	// Tensor's own answer to "which printer", worked out here rather than left
	// to the operator's eye. The dialog still opens, still shows every machine
	// and still lets any of them be chosen - what changes is that the obvious
	// one is already selected, with its spools already bound.
	plan, options, err := s.planQueueForBatch(ctx, plateSlotsOf(out.Slots), bedColoursOf(out.Colours))
	if err != nil {
		detail(c, http.StatusInternalServerError, "Could not read the fleet.")
		return
	}
	sortOptions(options)

	eligible := 0
	for _, o := range options {
		m := o.Machine
		loaded, trays := loadedColours(m), traysFor(m)
		machine := queueMachine{
			ID: m.ID.String(), Name: m.Name, Model: deref(m.Model), Status: m.Status,
			Loaded: loaded, Trays: trays,
			Missing:  missingColours(out.Colours, loaded),
			Eligible: o.Eligible, Reason: o.Refusal,
			// Nearest colour, as a starting point for a machine the operator
			// deliberately overrides to. It is a guess and it is offered as
			// one; the checked binding below replaces it wherever there is one.
			SuggestedSlotTrays: suggestSlotTrays(out.Slots, trays),
		}
		if o.Eligible {
			machine.SuggestedSlotTrays = o.SlotTrays
			eligible++
		}
		if o.Eligible && m.ID == plan.Machine.ID {
			machine.Suggested = true
			out.AutoReason = plan.Reason
		}
		out.Machines = append(out.Machines, machine)
	}

	switch {
	case len(out.Machines) == 0:
		out.Note = "No machines are known yet. Sync the fleet from BambuBuddy first."
	case len(out.Slots) == 0:
		out.Note = "This bed's plate declares no filament. Rebuild the bed before sending it."
	case eligible == 0:
		out.Note = noPrinterNote(options)
	}
	c.JSON(http.StatusOK, out)
}

// plateSlotsOf narrows the dialog's slots back to what the plate declared.
//
// queueSlot carries a name and an index for rendering; the picker wants only
// the two facts the file states, and reading them off the same list keeps the
// dialog and the decision looking at one plate.
func plateSlotsOf(slots []queueSlot) []meshio.Slot {
	out := make([]meshio.Slot, 0, len(slots))
	for _, s := range slots {
		out = append(out, meshio.Slot{Colour: s.Hex, Material: s.Material})
	}
	return out
}

// noPrinterNote explains a fleet that can take nothing, in terms of the fix.
//
// "No printer can take this bed" is true and useless. The causes need different
// people to do different things - map a colour, load a spool, or simply wait -
// so the note names whichever one accounts for the fleet.
func noPrinterNote(options []machineOption) string {
	var unmapped, loadable int
	for _, o := range options {
		switch {
		case strings.Contains(o.Refusal, "confirmed as"):
			unmapped++
		case strings.Contains(o.Refusal, "does not hold"):
			loadable++
		}
	}
	switch {
	case unmapped > 0:
		return "No spool has been confirmed as one of this bed's colours. " +
			"Map it under Inventory, then queue this bed."
	case loadable > 0:
		return "No printer has this bed's colours loaded. Load a spool, or wait for one to free up."
	default:
		return "No printer can take this bed yet."
	}
}

// queueSlotsFor reads the slots the bed's plate declares.
//
// Empty when the plate cannot be read, which the caller reports rather than
// treating as an error: an operator opening the dialog should be told the bed
// needs rebuilding, not shown a stack trace.
func (s *Server) queueSlotsFor(ctx context.Context, batch gen.Batch) []queueSlot {
	if s.storage == nil {
		return []queueSlot{}
	}
	plate, err := s.plateFileFor(ctx, batch)
	if err != nil {
		return []queueSlot{}
	}
	slots, err := s.plateSlots(ctx, plate)
	if err != nil {
		obs.FromContext(ctx).Info("could not read the plate's slots for the queue dialog",
			"batch", batch.BatchNumber, "error", err)
		return []queueSlot{}
	}
	out := make([]queueSlot, 0, len(slots))
	for i, slot := range slots {
		out = append(out, queueSlot{Index: i, Hex: slot.Colour, Material: slot.Material})
	}
	return out
}

// nameSlots labels each slot with the shop's word for its colour, where there
// is one. Purely for reading; nothing downstream depends on it.
func nameSlots(slots []queueSlot, identities []colourIdentity) {
	byHex := map[string]string{}
	for _, id := range identities {
		for _, hex := range id.Hexes {
			byHex[hex] = id.Name
		}
	}
	for i := range slots {
		if name, ok := byHex[slots[i].Hex]; ok {
			slots[i].Name = name
			continue
		}
		// The plank body is the one slot Tensor can always name, because it
		// writes it itself.
		if slots[i].Hex == BasePlateColour {
			slots[i].Name = "plank body"
		}
	}
}

// suggestSlotTrays proposes a tray for each slot, by nearest colour.
//
// A DEFAULT, not a decision: it is shown beside both swatches with the tray
// named, and one click changes it. Nearest is the right rule for that, and the
// wrong rule for anything that prints without being looked at - which is why
// the send takes the operator's answer rather than recomputing this.
//
// Each tray is offered once, so two slots never land on one spool.
func suggestSlotTrays(slots []queueSlot, trays []queueTray) []int {
	out := make([]int, 0, len(slots))
	used := map[int]bool{}
	for _, slot := range slots {
		best, bestDistance := -1, 0
		for i, tray := range trays {
			if used[i] {
				continue
			}
			d, ok := nearestColourDistance(slot.Hex, []string{tray.Hex})
			if !ok {
				continue
			}
			if best < 0 || d < bestDistance {
				best, bestDistance = i, d
			}
		}
		if best < 0 {
			out = append(out, amsSlotUnused)
			continue
		}
		used[best] = true
		out = append(out, trayAmsIndex(trays[best]))
	}
	return out
}

// trayAmsIndex is the ams_mapping integer for a tray the dialog offered.
func trayAmsIndex(t queueTray) int {
	if t.AmsID == nil || t.TrayID == nil {
		return amsSlotUnused
	}
	return *t.AmsID*traysPerAMS + *t.TrayID
}

func plural(n int, word string) string {
	if n == 1 {
		return word
	}
	return word + "s"
}

// queueColoursFor is the bed's colours by name, with a swatch where Tensor has
// one.
//
// Job-derived and therefore incomplete by design: it knows the lettering
// colours an order asked for, not the plank body Tensor adds itself. The SLOTS
// are the authoritative list - see queueSlotsFor - and this exists to label the
// dialog in the operator's own vocabulary.
func (s *Server) queueColoursFor(ctx context.Context, jobs []gen.ProductionJob) []queueColour {
	out := make([]queueColour, 0, 4)
	for _, name := range planColoursFromJobs(jobs) {
		colour := queueColour{Name: name}
		if hex, err := s.resolveColourHex(ctx, name); err == nil {
			colour.Hex = hex
		}
		out = append(out, colour)
	}
	return out
}

// missingColours names the bed's colours a machine does not hold.
//
// Advisory now, not a gate: the operator binds slots to trays explicitly, so
// this only annotates the dropdown. A colour with no resolvable hex is skipped
// rather than reported missing - saying a printer lacks a colour Tensor cannot
// describe is not information.
func missingColours(needed []queueColour, loaded []string) []string {
	have := map[string]bool{}
	for _, hex := range loaded {
		have[hex] = true
	}
	missing := make([]string, 0, len(needed))
	for _, colour := range needed {
		if colour.Hex == "" {
			continue
		}
		if !have[colour.Hex] {
			missing = append(missing, colour.Name)
		}
	}
	return missing
}

// nearestColourDistance is how far the closest loaded spool is from want, as a
// squared RGB distance. Reports false when either side cannot be read.
func nearestColourDistance(want string, loaded []string) (int, bool) {
	wr, wg, wb, ok := rgbOf(want)
	if !ok {
		return 0, false
	}
	best, found := 0, false
	for _, hex := range loaded {
		lr, lg, lb, ok := rgbOf(hex)
		if !ok {
			continue
		}
		d := (wr-lr)*(wr-lr) + (wg-lg)*(wg-lg) + (wb-lb)*(wb-lb)
		if !found || d < best {
			best, found = d, true
		}
	}
	return best, found
}

// rgbOf reads "#RRGGBB" into its channels.
func rgbOf(hex string) (r, g, b int, ok bool) {
	normalised, ok := normaliseHex(hex)
	if !ok {
		return 0, 0, 0, false
	}
	var rr, gg, bb int
	if _, err := fmt.Sscanf(normalised, "#%02X%02X%02X", &rr, &gg, &bb); err != nil {
		return 0, 0, 0, false
	}
	return rr, gg, bb, true
}
