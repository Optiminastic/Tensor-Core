package httpapi

// Sending one approved batch to BambuBuddy.
//
// Tensor uploads the merged plate UNSLICED and lets BambuBuddy slice it. That is
// the whole point of this file, and it replaces the opposite arrangement.
//
// Tensor used to slice the plate itself and hand over a finished .gcode.3mf. A
// sliced file is bound to the machine it was sliced for, so that decision had to
// be made before the plate ever reached the floor - and it was routinely the
// wrong one. A bed sliced for an H2C could not run on a free A2L, and could not
// run on the P2S that happened to hold the right colours. The only way out was
// to come back to Tensor and slice it again.
//
// BambuBuddy already solves this: a slicer pipeline slices an unsliced model for
// its printer and queues the result in one call, and re-slicing for a different
// printer class is a first-class operation there. So the machine decision moves
// to where the fleet state actually lives, and the filament check moves with it.

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/Optiminastic/tensor-core/internal/db/gen"
	"github.com/Optiminastic/tensor-core/internal/integrations/bambubuddy"
	"github.com/Optiminastic/tensor-core/internal/obs"
	"github.com/Optiminastic/tensor-core/internal/production"
)

// SendBatchToPrinter uploads a batch's plate and has BambuBuddy slice and queue
// it on a machine that can actually print it.
//
// The returned error carries an HTTP status for the handler; the dispatcher only
// cares that it failed. A response with Queued false and a Note is NOT an error:
// the plate reached the library but no machine could take it yet, which the
// operator needs told without being invited to send it again.
func (s *Server) SendBatchToPrinter(ctx context.Context, batch gen.Batch) (printBatchResponse, error) {
	log := obs.FromContext(ctx)

	if !s.bambu.Configured() {
		return printBatchResponse{}, statusErr(http.StatusConflict,
			"BambuBuddy is not configured on this service.")
	}

	// Locked only. A Draft bed is still being planned - the next planner pass
	// can dissolve it and rebuild it from different jobs - so sending one would
	// commit filament to a layout that no longer exists by the time it prints.
	if batch.Status != production.BatchOpen {
		return printBatchResponse{}, statusErr(http.StatusConflict, fmt.Sprintf(
			"Only a locked batch can be sent to a printer; this one is %s.", batch.Status))
	}

	// Already queued. The guard against a double-press, and against the
	// dispatcher racing an operator: a batch is one physical print, and a second
	// queue entry would print the whole bed again in plastic. Checked on the
	// batch rather than on the library file, because the plate is now uploaded
	// unsliced and BambuBuddy legitimately holds one copy of it however many
	// times it is sliced.
	// Either identifier means it has been dispatched. pipeline_run_id is set the
	// moment BambuBuddy accepts the work; queue_item_id only appears once slicing
	// has finished, which can be minutes later. Checking only the latter sent the
	// same plate again on the next pass.
	if batch.QueueItemID != nil || batch.PipelineRunID != nil {
		return printBatchResponse{
			Queued: false, AlreadySent: true,
			Note: "this batch is already in BambuBuddy's queue",
		}, nil
	}

	// Colour first, before anything is uploaded or sliced.
	//
	// This gate exists because BambuBuddy's does not: running a pipeline checks
	// printer class and nozzle, not the AMS trays, so a blue plate was accepted
	// and printed on a machine loaded with red and white. Checked here, ahead of
	// the upload, so a bed that cannot be printed costs nothing and holds with a
	// reason naming the spool to load.
	jobs, err := s.store.Q.ListJobsForBatch(ctx, &batch.ID)
	if err != nil {
		return printBatchResponse{}, statusErrf(http.StatusInternalServerError,
			"Could not read the batch's jobs.", err)
	}
	need := s.plateColoursFor(ctx, jobs)
	family := batchFamilyFromRows(jobs)
	if gate := s.canAnyMachinePrint(ctx, family, need); len(gate.Ready) == 0 {
		reason := gate.holdReason(need)
		s.recordPrintError(ctx, batch.ID, reason)
		log.Info("batch held before sending, no machine can print it",
			"batch", batch.BatchNumber, "needs", need, "missing", gate.Missing,
			"machines_checked", gate.Checked)
		return printBatchResponse{Queued: false, Note: reason}, nil
	}

	// The merged plate: one 3MF carrying every plank on the bed and their
	// colours. NOT a sliced file - see this file's own comment.
	plate, err := s.plateFileFor(ctx, batch)
	if err != nil {
		return printBatchResponse{}, err
	}
	obj, err := s.storage.Get(ctx, plate.StorageKey)
	if err != nil {
		const reason = "This batch's plate is missing from storage. Re-approve the batch to rebuild it."
		s.recordPrintError(ctx, batch.ID, reason)
		log.Warn("plate missing from storage", "batch", batch.ID, "key", plate.StorageKey, "error", err)
		return printBatchResponse{}, statusErr(http.StatusConflict, reason)
	}
	defer func() { _ = obj.Body.Close() }()

	// Named as the plate is named - "114556-114557-BLUE.3mf" - so an operator
	// scanning BambuBuddy's library sees whose orders a file holds without
	// cross-referencing ids.
	uploaded, err := s.bambu.UploadFile(ctx, plate.Filename, obj.Body)
	if err != nil {
		log.Error("could not send the plate to BambuBuddy", "batch", batch.ID, "error", err)
		var reason bambubuddy.ReasonError
		if errors.As(err, &reason) {
			s.recordPrintError(ctx, batch.ID, reason.Reason)
			return printBatchResponse{}, statusErr(http.StatusUnprocessableEntity, reason.Reason)
		}
		s.recordPrintError(ctx, batch.ID, "Could not send the plate to BambuBuddy.")
		return printBatchResponse{}, statusErr(http.StatusBadGateway,
			"Could not send the plate to BambuBuddy.")
	}

	return s.sliceAndQueue(ctx, batch, jobs, uploaded)
}

// sliceAndQueue finds a pipeline that can print this plate and runs it.
//
// Every pipeline is tried in turn, and the first that accepts the plate wins.
// That is how a bed finds a machine now: BambuBuddy answers 409 with an
// eligibility report when a pipeline's printer cannot take it - wrong model,
// wrong filament in an AMS slot - so trying them all asks "which of my machines
// can print this?" without Tensor knowing anything about colours or nozzles.
func (s *Server) sliceAndQueue(
	ctx context.Context, batch gen.Batch, jobs []gen.ProductionJob,
	uploaded bambubuddy.UploadedFile,
) (printBatchResponse, error) {
	log := obs.FromContext(ctx)

	pipelines, err := s.bambu.ListPipelines(ctx)
	if err != nil {
		s.recordPrintError(ctx, batch.ID, "Could not read BambuBuddy's slicer pipelines.")
		return printBatchResponse{}, statusErr(http.StatusBadGateway,
			"Could not read BambuBuddy's slicer pipelines.")
	}
	if len(pipelines) == 0 {
		// Setup, not runtime: nothing can ever be sliced until somebody
		// configures a pipeline per printer model, so say that plainly rather
		// than reporting a bed that mysteriously will not print.
		const reason = "BambuBuddy has no slicer pipelines configured, so nothing can be sliced. " +
			"Add one per printer model."
		s.recordPrintError(ctx, batch.ID, reason)
		return printBatchResponse{}, statusErr(http.StatusConflict, reason)
	}

	// Only pipelines that target this bed's printer class may slice it. Running
	// all of them and taking the first that accepts is how a plate planned for
	// one machine ended up sliced for another - harmless while every bed holds
	// the same four planks, and wrong the moment a bed is sized for the machine
	// it was planned for. See batch_pipeline_target.go.
	family := s.batchTargetFamily(ctx, batch, jobs)
	eligible := pipelinesFor(pipelines, family)
	if len(eligible) == 0 {
		reason := pipelineTargetNote(family)
		s.recordPrintError(ctx, batch.ID, reason)
		log.Info("batch held, no pipeline targets its printer class",
			"batch", batch.BatchNumber, "family", family, "pipelines", len(pipelines))
		return printBatchResponse{
			Filename: uploaded.Filename, FileID: uploaded.ID, Queued: false, Note: reason,
		}, nil
	}

	var refusals []string
	for _, p := range eligible {
		run, err := s.bambu.RunPipeline(ctx, p, uploaded.ID)
		if err == nil {
			return s.recordQueued(ctx, batch, uploaded, run), nil
		}

		var ineligible bambubuddy.NotEligibleError
		if errors.As(err, &ineligible) {
			// Not a failure - this machine cannot take this plate, which is
			// exactly what the check is for. Keep the reason and try the next.
			refusals = append(refusals, ineligible.Error())
			continue
		}
		// A transport or server failure is worth reporting, but not worth
		// abandoning the other pipelines over: one misconfigured pipeline must
		// not stop a bed printing on a machine that would take it.
		log.Warn("a slicer pipeline failed", "batch", batch.ID, "pipeline", p.Name, "error", err)
		refusals = append(refusals, fmt.Sprintf("%s: %v", p.Name, err))
	}

	// Nothing could print it. The bed stays where it is and the reasons say what
	// to load - "filament (slot 2): needs #1560BD, has #46A8F9" - so the floor
	// can act on it rather than wondering why a batch never moved.
	note := "no printer can print this plate yet: " + joinNotes(refusals)
	s.recordPrintError(ctx, batch.ID, note)
	log.Info("batch held, no eligible printer", "batch", batch.BatchNumber, "reasons", len(refusals))
	return printBatchResponse{
		Filename: uploaded.Filename, FileID: uploaded.ID, Queued: false, Note: note,
	}, nil
}

// recordQueued stores the queue entry a successful run created and reports it.
func (s *Server) recordQueued(
	ctx context.Context, batch gen.Batch, uploaded bambubuddy.UploadedFile, run bambubuddy.PipelineRun,
) printBatchResponse {
	log := obs.FromContext(ctx)

	// Success clears any previous failure and records what to follow. The queue
	// entry is what an operator sees on the Machine Management queue, and what
	// stops this batch being sent twice.
	var queueID *int32
	if id := run.QueueEntryID(); id != nil {
		queueID = int32Ptr(*id)
	}
	// The run id is always known here; the queue entry usually is not yet,
	// because slicing is asynchronous. Recording the run is what marks this bed
	// as dispatched in the meantime.
	if err := s.store.Q.ClearBatchPrintError(ctx, gen.ClearBatchPrintErrorParams{
		ID: batch.ID, QueueItemID: queueID, PipelineRunID: int32Ptr(run.ID),
	}); err != nil {
		log.Warn("could not clear the batch's print error", "batch", batch.ID, "error", err)
	}

	// Reported as queued once BambuBuddy has the work, even while the slice is
	// still running: from the floor's point of view the bed has been sent, and
	// saying otherwise invites somebody to send it again.
	note := "slicing on BambuBuddy"
	if name := run.PrinterName(); name != "" {
		note = "queued on " + name
	}
	log.Info("batch sliced and queued by BambuBuddy",
		"batch", batch.BatchNumber, "file", uploaded.Filename,
		"pipeline_run", run.ID, "printer", run.PrinterName())

	return printBatchResponse{
		Filename: uploaded.Filename, FileID: uploaded.ID,
		Queued: true, Note: note,
	}
}

// plateFileFor resolves the merged plate to send.
//
// The approved merged file when there is one, falling back to the Draft-time
// preview: both are the same geometry, and a batch approved before merged files
// were recorded should still be printable rather than stuck.
func (s *Server) plateFileFor(ctx context.Context, batch gen.Batch) (gen.FileAsset, error) {
	id := batch.MergedFileID
	if id == nil {
		id = batch.PreviewFileID
	}
	if id == nil {
		return gen.FileAsset{}, statusErr(http.StatusConflict,
			"This batch has no merged plate to send. Re-approve it to build one.")
	}
	file, err := s.store.Q.GetFileAsset(ctx, *id)
	if err != nil {
		return gen.FileAsset{}, statusErrf(http.StatusConflict,
			"This batch's plate could not be read.", err)
	}
	return file, nil
}

// joinNotes renders the refusals as one sentence an operator can act on.
func joinNotes(notes []string) string {
	switch len(notes) {
	case 0:
		return "no pipeline accepted it"
	case 1:
		return notes[0]
	}
	out := notes[0]
	for _, n := range notes[1:] {
		out += "; " + n
	}
	return out
}
