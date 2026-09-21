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
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

type queueBatchRequest struct {
	// MachineID is a Tensor machines row, not a BambuBuddy printer id. The
	// browser has no business knowing BambuBuddy's numbering, and the serial on
	// that row is what resolves to it.
	MachineID string `json:"machine_id" binding:"required"`
	// SlotTrays names the spool that prints each plate slot, in the plate's own
	// slot order, as ams_mapping integers.
	//
	// The operator picks these in the dialog, looking at the bed's swatches and
	// the printer's trays side by side. Tensor deliberately does not work them
	// out: an AMS reports a colour as a bare hex with no name, and 11 of the 14
	// hexes on this fleet appear in no catalogue, so any automatic answer would
	// be a guess that prints.
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
	var req queueBatchRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		detail(c, http.StatusUnprocessableEntity, "Pick a machine to send this batch to.")
		return
	}
	machineID, err := uuid.Parse(strings.TrimSpace(req.MachineID))
	if err != nil {
		detail(c, http.StatusUnprocessableEntity, "That is not a valid machine.")
		return
	}
	if !s.filesReady(c) {
		return
	}
	ctx := c.Request.Context()

	if !s.bambu.Configured() {
		detail(c, http.StatusConflict, "BambuBuddy is not configured on this service.")
		return
	}

	machine, err := s.store.Q.GetFleetMachine(ctx, machineID)
	if err != nil {
		dbError(c, err, "That machine does not exist.", "Could not load the machine.")
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

	// No colour pre-check here any more. The authoritative one is in
	// mapSlotsToTrays, which reads the PLATE's own declared slots rather than
	// re-deriving them from the jobs - and so catches what this missed, notably
	// the white plank body that queueColoursFor omits entirely.
	resp, err := s.sendBatchToMachine(ctx, batch, machine, req.SlotTrays, currentUserID(c))
	if err != nil {
		writeStatusError(c, err, "Could not send the batch to that printer.")
		return
	}
	c.JSON(http.StatusOK, resp)
}
