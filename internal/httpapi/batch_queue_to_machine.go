package httpapi

// Sending one bed to one printer, chosen by a person.
//
// The difference from batch_send.go is who decides. That path offers the plate
// to every pipeline in turn and lets BambuBuddy place it; this one is for the
// operator standing at the machines who knows that THIS bed goes on THAT
// printer, because they can see which spools are loaded.
//
// Targeting works in two moves, because BambuBuddy's API has no single call for
// it. A pipeline run names a printer CLASS - its payload takes a file, a copy
// count and a force flag, and nothing else - so the plate is sliced through the
// pipeline for the chosen machine's model, and only then is the resulting queue
// item pinned to the machine itself. Pinning is what makes the choice real: an
// unpinned item is dispatched to whichever printer of that class frees up first,
// which is the behaviour this exists to override.
//
// The chosen machine is re-checked here against the bed's colours. The dialog
// only offers eligible machines, but a server action's arguments are
// client-controlled - the dropdown is a convenience, not the rule.

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/Optiminastic/tensor-core/internal/db/gen"
	"github.com/Optiminastic/tensor-core/internal/integrations/bambubuddy"
	"github.com/Optiminastic/tensor-core/internal/obs"
	"github.com/Optiminastic/tensor-core/internal/production"
)

type queueBatchRequest struct {
	// MachineID is a Tensor machines row, not a BambuBuddy printer id. The
	// browser has no business knowing BambuBuddy's numbering, and the serial on
	// that row is what resolves to it.
	MachineID string `json:"machine_id" binding:"required"`
	// SkipColourCheck sends the bed to a printer that does not hold its colours.
	//
	// Exposed because the shop sometimes loads a spool between reading the page
	// and pressing the button, and refusing then would be Tensor arguing with
	// somebody standing in front of the machine. It is never the default, and
	// the response says plainly that it was used.
	SkipColourCheck bool `json:"skip_colour_check"`
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
	// Pinned is false when the plate was queued but could not be tied to the
	// chosen printer - see the pin step below. The bed still prints; it may
	// print on a different machine of the same model.
	Pinned bool   `json:"pinned"`
	Note   string `json:"note"`
}

// queueBatchToMachine locks the bed if needed, sends its plate, and pins the
// queued item to the chosen printer.
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

	// The same rule the dialog drew, applied where it counts - the dropdown is a
	// convenience, and a server action's arguments are client-controlled.
	//
	// Skipped when Tensor cannot verify colours at all (see
	// colourMatchingPossible), because refusing on a comparison that is known to
	// be meaningless would block every send. The dialog says as much, and the
	// operator is the one looking at the spools.
	if !req.SkipColourCheck && s.colourMatchingPossible(ctx) {
		colours := s.queueColoursFor(ctx, jobs)
		if missing := missingColours(colours, loadedColours(machine)); len(missing) > 0 {
			detail(c, http.StatusConflict, fmt.Sprintf(
				"%s does not hold %s. Load the spool, or send it anyway.",
				machine.Name, strings.Join(missing, ", ")))
			return
		}
	}

	resp, err := s.sendBatchToMachine(ctx, batch, jobs, machine, currentUserID(c))
	if err != nil {
		writeStatusError(c, err, "Could not send the batch to that printer.")
		return
	}
	c.JSON(http.StatusOK, resp)
}

// sendBatchToMachine does the work: lock, upload, slice for the machine's class,
// pin to the machine.
func (s *Server) sendBatchToMachine(
	ctx context.Context, batch gen.Batch, jobs []gen.ProductionJob,
	machine gen.Machine, actor string,
) (queueBatchResponse, error) {
	log := obs.FromContext(ctx)
	out := queueBatchResponse{BatchNumber: batch.BatchNumber, MachineName: machine.Name}

	// A Draft is locked first, never sent as a Draft: the next planning pass can
	// dissolve a Draft and rebuild it from different jobs, so sending one would
	// commit a printer to a layout that no longer exists by the time it prints.
	switch batch.Status {
	case production.BatchPendingApproval:
		approved, err := s.ApproveBatchFor(ctx, batch.ID, nil, actor)
		if err != nil {
			return out, err
		}
		batch = approved
		out.Locked = true

	case production.BatchOpen:
		// A bed whose last print failed keeps its 'open' status and its outcome.
		// Clearing it is the deliberate human "run that again".
		if batch.PrintOutcome != nil {
			if err := s.store.Q.ClearBatchPrintOutcome(ctx, batch.ID); err != nil {
				return out, statusErrf(http.StatusInternalServerError,
					"Could not clear the previous print result.", err)
			}
			batch.PrintOutcome = nil
			batch.QueueItemID = nil
			batch.PipelineRunID = nil
		}

	case production.BatchInProgress:
		return out, statusErr(http.StatusConflict, "This batch is already printing.")

	default:
		return out, statusErr(http.StatusConflict, fmt.Sprintf(
			"A %s batch cannot be queued.", batch.Status))
	}

	// One physical print per bed. Checked after the lock so a Draft that was
	// locked by this call still reports it.
	if batch.QueueItemID != nil || batch.PipelineRunID != nil {
		out.Note = "this batch is already in BambuBuddy's queue"
		return out, nil
	}

	plate, err := s.plateFileFor(ctx, batch)
	if err != nil {
		return out, err
	}
	obj, err := s.storage.Get(ctx, plate.StorageKey)
	if err != nil {
		const reason = "This batch's plate is missing from storage. Re-approve the batch to rebuild it."
		s.recordPrintError(ctx, batch.ID, reason)
		return out, statusErr(http.StatusConflict, reason)
	}
	defer func() { _ = obj.Body.Close() }()

	uploaded, err := s.bambu.UploadFile(ctx, plate.Filename, obj.Body)
	if err != nil {
		var reason bambubuddy.ReasonError
		if errors.As(err, &reason) {
			s.recordPrintError(ctx, batch.ID, reason.Reason)
			return out, statusErr(http.StatusUnprocessableEntity, reason.Reason)
		}
		s.recordPrintError(ctx, batch.ID, "Could not send the plate to BambuBuddy.")
		return out, statusErr(http.StatusBadGateway, "Could not send the plate to BambuBuddy.")
	}
	out.Filename = uploaded.Filename

	// Slice through a pipeline for THIS machine's model. Class, not printer:
	// every pipeline on the install targets a class, and the slice depends on
	// the class rather than on which unit of it runs the plate.
	pipelines, err := s.bambu.ListPipelines(ctx)
	if err != nil {
		s.recordPrintError(ctx, batch.ID, "Could not read BambuBuddy's slicer pipelines.")
		return out, statusErr(http.StatusBadGateway, "Could not read BambuBuddy's slicer pipelines.")
	}
	model := strings.TrimSpace(deref(machine.Model))
	eligible := pipelinesFor(pipelines, model)
	if len(eligible) == 0 {
		reason := pipelineTargetNote(model)
		s.recordPrintError(ctx, batch.ID, reason)
		out.Note = reason
		return out, nil
	}

	var refusals []string
	for _, p := range eligible {
		run, runErr := s.bambu.RunPipeline(ctx, p, uploaded.ID)
		if runErr == nil {
			s.recordQueued(ctx, batch, jobs, uploaded, run)
			out.Queued = true
			out.Pinned = s.pinToPrinter(ctx, batch, run, p, machine)
			out.Note = queueNote(out.Pinned, machine.Name, run)
			log.Info("batch queued to a chosen printer",
				"batch", batch.BatchNumber, "machine", machine.Name,
				"pipeline_run", run.ID, "pinned", out.Pinned)
			return out, nil
		}
		var ineligible bambubuddy.NotEligibleError
		if errors.As(runErr, &ineligible) {
			refusals = append(refusals, ineligible.Error())
			continue
		}
		refusals = append(refusals, fmt.Sprintf("%s: %v", p.Name, runErr))
	}

	note := "BambuBuddy could not slice this plate for " + machine.Name + ": " + joinNotes(refusals)
	s.recordPrintError(ctx, batch.ID, note)
	out.Note = note
	return out, nil
}

// pinToPrinter ties the queued plate to the chosen machine, reporting whether
// the choice will be honoured.
//
// Two paths, because the queue entry usually does not exist yet. RunPipeline
// answers 202 while slicing is still running, and printer_id lives on the queue
// entry - so most sends pin immediately by scheduling a background job that
// waits for the slice, and only a run that queued instantly can be pinned here
// and now.
//
// Either way the plate is already in BambuBuddy's hands, so nothing below is
// allowed to fail the send. What it reports is whether the operator's choice
// will stick, which is a different question from whether the bed will print.
func (s *Server) pinToPrinter(
	ctx context.Context, batch gen.Batch, run bambubuddy.PipelineRun,
	pipeline bambubuddy.Pipeline, machine gen.Machine,
) bool {
	log := obs.FromContext(ctx)

	// Resolved here, while the fleet index is warm, rather than in the worker:
	// the job outlives this request, and a serial that cannot be resolved later
	// would be a pin that silently never happens.
	printerID, err := s.bambuCache.printerID(ctx, machine.MachineID, s.bambu.ListPrinters)
	if err != nil || printerID <= 0 {
		log.Warn("could not resolve the chosen machine to a BambuBuddy printer",
			"machine", machine.Name, "serial", machine.MachineID, "error", err)
		return false
	}

	// Already queued - slicing was quick, or the plate was cached. Pin it now.
	if itemID := run.QueueEntryID(); itemID != nil {
		if err := s.bambu.AssignQueueItemToPrinter(ctx, *itemID, printerID); err != nil {
			log.Warn("could not pin the queued plate to the chosen printer",
				"machine", machine.Name, "queue_item", *itemID, "error", err)
			return false
		}
		return true
	}

	// The usual case: still slicing. Hand the wait to a worker.
	if s.pinEnqueuer == nil {
		log.Warn("no queue-pin enqueuer configured; BambuBuddy will place this plate itself",
			"batch", batch.BatchNumber, "machine", machine.Name)
		return false
	}
	if err := s.pinEnqueuer.Enqueue(ctx, production.PinQueueItemArgs{
		BatchID: batch.ID, PipelineID: pipeline.ID, PipelineRunID: run.ID,
		PrinterID: printerID, MachineName: machine.Name,
	}); err != nil {
		log.Warn("could not schedule the pin to the chosen printer",
			"batch", batch.BatchNumber, "machine", machine.Name, "error", err)
		return false
	}
	log.Info("pin scheduled; it lands when BambuBuddy finishes slicing",
		"batch", batch.BatchNumber, "machine", machine.Name, "pipeline_run", run.ID)
	return true
}

// queueNote says where the bed went, in the operator's terms.
func queueNote(pinned bool, machineName string, run bambubuddy.PipelineRun) string {
	if pinned {
		if run.QueueEntryID() != nil {
			return "queued on " + machineName
		}
		// Honest about the delay: the plate is queued, and it moves onto the
		// chosen printer once the slice finishes. Saying "queued on P3" here
		// would be true in a minute and false right now.
		return "slicing now, then it goes on " + machineName
	}
	if name := run.PrinterName(); name != "" {
		return "sliced and queued, but BambuBuddy placed it on " + name +
			" - it could not be held for " + machineName
	}
	return "sliced and queued, but it could not be held for " + machineName +
		" - BambuBuddy will place it on a free printer of the same model"
}
