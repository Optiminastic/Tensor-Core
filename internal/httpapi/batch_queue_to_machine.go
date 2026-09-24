package httpapi

// Sending one bed to one printer, chosen by a person.
//
// The difference from batch_send.go is who decides. That path offers the plate
// to every pipeline in turn and lets BambuBuddy place it; this one is for the
// operator standing at the machines who knows that THIS bed goes on THAT
// printer, because they can see which spools are loaded.
//
// This is only the entry point; batch_slice_send.go does the work. The bed is
// sliced FOR the chosen printer - its presets, its spool colours - and the
// sliced file is then queued with printer_id set, so the choice is honoured by
// construction rather than negotiated afterwards.
//
// The machine is re-checked against the bed's colours there, not here. The
// dialog only offers eligible machines, but a server action's arguments are
// client-controlled, so the dropdown is a convenience and never the rule.

import (
	"context"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/Optiminastic/tensor-core/internal/db/gen"
	"github.com/Optiminastic/tensor-core/internal/obs"
)

type queueBatchRequest struct {
	// MachineID is a Tensor machines row, not a BambuBuddy printer id. The
	// browser has no business knowing BambuBuddy's numbering, and the serial on
	// that row is what resolves to it.
	//
	// OPTIONAL, and normally absent. Left out, Tensor chooses the printer
	// itself - the one that frees up soonest among those whose AMS actually
	// holds this bed's colours - which is the whole point of the Queue button.
	// Sent, it overrides that choice, which is what a person standing at a
	// particular machine needs.
	MachineID string `json:"machine_id"`
	// SlotTrays names the spool that prints each plate slot, in the plate's own
	// slot order, as ams_mapping integers.
	//
	// Also optional, and bound by Tensor when absent. Supplying it without a
	// machine is meaningless - an ams_mapping only means something against the
	// printer it indexes - so the two travel together or not at all.
	SlotTrays []int `json:"slot_trays"`

	// There is deliberately no "send anyway" flag.
	//
	// The old path had one, because a colour mismatch was only a warning: the
	// plate went to BambuBuddy either way. This path SLICES the plate against a
	// named tray for every slot, so a colour the machine does not hold is not a
	// warning that can be waved past - there is no tray to point slot 2 at, and
	// nothing to put in ams_mapping. Load the spool, or pick another printer.
}

type queueBatchResponse struct {
	BatchNumber string `json:"batch_number"`
	MachineName string `json:"machine_name"`
	Filename    string `json:"filename"`
	Queued      bool   `json:"queued"`
	// Locked is true when this call locked a Draft on the way. Reported
	// separately because it is the half that cannot be undone by pressing the
	// button again.
	Locked bool `json:"locked"`
	// Pinned reports that the plate is bound to the chosen printer. True once
	// the slice is scheduled, because the sliced file is queued with printer_id
	// set - unlike the old path, where it was whatever BambuBuddy decided.
	Pinned bool   `json:"pinned"`
	Note   string `json:"note"`
}

// queueBatchToMachine locks the bed if needed, then sends it to be sliced for
// the chosen printer.
func (s *Server) queueBatchToMachine(c *gin.Context) {
	id, ok := parseUUIDParam(c, "id")
	if !ok {
		return
	}
	// An empty body is the ordinary case: press Queue, Tensor decides.
	var req queueBatchRequest
	if c.Request.ContentLength > 0 {
		if err := c.ShouldBindJSON(&req); err != nil {
			detail(c, http.StatusUnprocessableEntity, "Could not read that request.")
			return
		}
	}
	if !s.filesReady(c) {
		return
	}
	ctx := c.Request.Context()

	if !s.bambu.Configured() {
		detail(c, http.StatusConflict, "BambuBuddy is not configured on this service.")
		return
	}

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

	machine, slotTrays, err := s.targetFor(ctx, batch, req)
	if err != nil {
		writeStatusError(c, err, "Could not choose a printer for this batch.")
		return
	}

	// No colour pre-check here. The authoritative one is assignmentsFromChoice,
	// which reads the PLATE's own declared slots rather than re-deriving them
	// from the jobs - and so catches what a job-derived check missed, notably
	// the white plank body that queueColoursFor omits entirely.
	resp, err := s.sendBatchToMachine(ctx, batch, machine, slotTrays, currentUserID(c))
	if err != nil {
		writeStatusError(c, err, "Could not send the batch to that printer.")
		return
	}
	c.JSON(http.StatusOK, resp)
}

// targetFor resolves which printer prints this bed, and on which spools.
//
// Two ways in. Normally nothing is named and Tensor picks, which is what the
// Queue button does. When a machine IS named the operator has overridden the
// choice, and their slot binding is taken as given - they are standing at the
// machine and can see what is in it, which is a better source than anything
// Tensor can read.
func (s *Server) targetFor(
	ctx context.Context, batch gen.Batch, req queueBatchRequest,
) (gen.Machine, []int, error) {
	if named := strings.TrimSpace(req.MachineID); named != "" {
		machineID, err := uuid.Parse(named)
		if err != nil {
			return gen.Machine{}, nil, statusErr(http.StatusUnprocessableEntity,
				"That is not a valid machine.")
		}
		machine, err := s.store.Q.GetFleetMachine(ctx, machineID)
		if err != nil {
			return gen.Machine{}, nil, statusErr(http.StatusNotFound, "That machine does not exist.")
		}
		return machine, req.SlotTrays, nil
	}

	slots := s.queueSlotsFor(ctx, batch)
	if len(slots) == 0 {
		return gen.Machine{}, nil, statusErr(http.StatusConflict,
			"This bed's plate declares no filament. Rebuild the bed before sending it.")
	}
	// The bed's own colours, so a plate whose hex predates the colour map is
	// still recognised by the word the order used.
	jobs, err := s.store.Q.ListJobsForBatch(ctx, &batch.ID)
	if err != nil {
		return gen.Machine{}, nil, statusErr(http.StatusInternalServerError,
			"Could not read the batch's jobs.")
	}
	plan, options, err := s.planQueueForBatch(ctx, plateSlotsOf(slots),
		bedColoursOf(s.queueColoursFor(ctx, jobs)))
	if err != nil {
		return gen.Machine{}, nil, statusErr(http.StatusBadGateway, "Could not read the fleet.")
	}
	if plan.Reason == "" {
		// Refused, not fallen back. Sending a bed to a printer that cannot
		// print its colours is the failure this whole path exists to prevent,
		// so the answer names what is missing instead.
		return gen.Machine{}, nil, statusErr(http.StatusConflict, noPrinterNote(options))
	}
	obs.FromContext(ctx).Info("chose a printer for a bed",
		"batch", batch.BatchNumber, "printer", plan.Machine.Name, "why", plan.Reason)

	// The machine, but deliberately NOT the binding that picked it. That was
	// computed from machines.filaments, a mirror up to a sync interval old, and
	// a binding is a list of tray POSITIONS: a spool swapped since the last
	// sync leaves the positions valid and their contents wrong. sendBatchToMachine
	// re-binds through the colour map against the trays the printer is holding
	// when the plate is actually sliced.
	return plan.Machine, nil, nil
}
