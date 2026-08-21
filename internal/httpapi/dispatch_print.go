package httpapi

// Handing a sliced batch plate to BamBuddy: download the G-code, upload it to the
// printer manager's library, and queue it on the printer that batch was approved
// against.
//
// The default is to STAGE rather than start (cfg.BambuddyManualStart): the plate
// lands on the printer's queue and waits for a person to press start. An order's
// SKU resolving to the wrong design is a silent failure that costs filament and
// machine hours, and staging turns it into something a human sees first.

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Optiminastic/tensor-core/internal/db/gen"
	"github.com/Optiminastic/tensor-core/internal/integrations/bambuddy"
	"github.com/Optiminastic/tensor-core/internal/obs"
	"github.com/Optiminastic/tensor-core/internal/production"
)

// Live dispatch statuses - the ones still worth asking BamBuddy about. They must
// match the partial indexes in migration 0042.
const (
	dispatchPending  = "pending"
	dispatchQueued   = "queued"
	dispatchPrinting = "printing"
)

// errDispatchAlreadyOpen marks the idempotent no-op: this batch already has a
// live dispatch. Reused rather than refused, so a retry after a partial failure
// resumes the same hand-off instead of putting a second copy of the plate on a
// printer.
var errDispatchAlreadyOpen = errors.New("batch already has an open print dispatch")

// DispatchBatch is the whole hand-off for one batch: check it is dispatchable,
// open (or reuse) its dispatch row, and send the plate. It is the worker's entry
// point, and the single place that decides what "already dispatched" means.
func (s *Server) DispatchBatch(ctx context.Context, batchID uuid.UUID) error {
	dispatch, err := s.openPrintDispatch(ctx, batchID)
	if errors.Is(err, errDispatchAlreadyOpen) {
		open, findErr := s.openDispatchForBatch(ctx, batchID)
		if findErr != nil {
			return findErr
		}
		dispatch = open
	} else if err != nil {
		return err
	}

	if err := s.SendPrintDispatch(ctx, dispatch.ID); err != nil {
		// Record the reason on the row so the batch shows why nothing printed,
		// then return the error so River retries with backoff.
		reason := err.Error()
		if failErr := s.store.Q.FailPrintDispatch(ctx, gen.FailPrintDispatchParams{
			ID: dispatch.ID, Error: &reason,
		}); failErr != nil {
			obs.FromContext(ctx).Error("could not record dispatch failure",
				"dispatch", dispatch.ID, "error", failErr)
		}
		return err
	}
	return nil
}

// ScheduleDispatch validates that a batch can be printed and queues the hand-off.
// The checks run here, synchronously, so an operator gets a real reason back
// rather than a job that fails quietly in the background.
func (s *Server) ScheduleDispatch(ctx context.Context, batchID uuid.UUID) error {
	if _, _, err := s.dispatchTarget(ctx, batchID); err != nil {
		return err
	}
	if s.dispatchEnqueuer == nil {
		return &httpErr{status: http.StatusServiceUnavailable, msg: "Printing is not configured."}
	}
	return s.store.InTxWith(ctx, func(q *gen.Queries, tx pgx.Tx) error {
		return s.dispatchEnqueuer.EnqueueTx(ctx, tx, production.DispatchPrintArgs{BatchID: batchID})
	})
}

// dispatchTarget resolves a batch to the BamBuddy printer that will print it,
// refusing early - rather than at print time - when the plate is unsliced or the
// machine is not mapped to a printer.
func (s *Server) dispatchTarget(ctx context.Context, batchID uuid.UUID) (gen.Batch, int32, error) {
	if !s.cfg.BambuddyConfigured() {
		return gen.Batch{}, 0, &httpErr{
			status: http.StatusServiceUnavailable,
			msg:    "Printing is not configured.",
		}
	}

	batch, err := s.store.Q.GetBatchByID(ctx, batchID)
	if err != nil {
		if isNoRows(err) {
			return gen.Batch{}, 0, &httpErr{status: http.StatusNotFound, msg: "That batch does not exist.", cause: err}
		}
		return gen.Batch{}, 0, &httpErr{status: http.StatusInternalServerError, msg: "Could not load the batch.", cause: err}
	}
	// A plate that has not been sliced has nothing a printer can consume. This is
	// the ordering guarantee the pipeline rests on.
	if batch.GcodeKey == nil || *batch.GcodeKey == "" {
		return gen.Batch{}, 0, &httpErr{status: http.StatusConflict, msg: "That batch has not been sliced yet."}
	}
	if batch.MachineID == nil {
		return gen.Batch{}, 0, &httpErr{status: http.StatusConflict, msg: "That batch has no machine assigned."}
	}

	fleet, err := s.store.Q.GetFleetMachineForProfile(ctx, batch.MachineID)
	if err != nil {
		if isNoRows(err) {
			return gen.Batch{}, 0, &httpErr{
				status: http.StatusConflict,
				msg:    "No printer is linked to that batch's machine.",
				cause:  err,
			}
		}
		return gen.Batch{}, 0, &httpErr{status: http.StatusInternalServerError, msg: "Could not resolve the printer.", cause: err}
	}
	if fleet.BambuddyPrinterID == nil {
		return gen.Batch{}, 0, &httpErr{
			status: http.StatusConflict,
			msg:    "No printer is linked to that batch's machine.",
		}
	}
	return batch, *fleet.BambuddyPrinterID, nil
}

// openPrintDispatch creates the dispatch row for a batch. It does not enqueue:
// its caller is already the worker performing the hand-off.
func (s *Server) openPrintDispatch(ctx context.Context, batchID uuid.UUID) (gen.PrintDispatch, error) {
	_, printerID, err := s.dispatchTarget(ctx, batchID)
	if err != nil {
		return gen.PrintDispatch{}, err
	}

	dispatch, err := s.store.Q.InsertPrintDispatch(ctx, gen.InsertPrintDispatchParams{
		ID: uuid.New(), BatchID: batchID, PrinterID: printerID,
	})
	if err != nil {
		// The partial unique index on live dispatches turns a duplicate into this,
		// which is the idempotent case rather than a real failure.
		if isUniqueViolation(err) {
			return gen.PrintDispatch{}, errDispatchAlreadyOpen
		}
		return gen.PrintDispatch{}, fmt.Errorf("open dispatch: %w", err)
	}
	return dispatch, nil
}

// openDispatchForBatch finds the live dispatch for a batch, if any.
func (s *Server) openDispatchForBatch(ctx context.Context, batchID uuid.UUID) (gen.PrintDispatch, error) {
	rows, err := s.store.Q.ListPrintDispatchesForBatch(ctx, batchID)
	if err != nil {
		return gen.PrintDispatch{}, fmt.Errorf("list dispatches: %w", err)
	}
	for _, d := range rows {
		switch d.Status {
		case dispatchPending, dispatchQueued, dispatchPrinting:
			return d, nil
		}
	}
	return gen.PrintDispatch{}, fmt.Errorf("no open dispatch for batch %s", batchID)
}

// SendPrintDispatch performs the hand-off for one dispatch: download the sliced
// plate, upload it to BamBuddy, and queue it on the printer.
//
// Retry-safe step by step. The upload runs only when no library file is recorded
// yet, and queueing only when no queue item is, so a retry after a partial
// failure resumes rather than duplicating.
func (s *Server) SendPrintDispatch(ctx context.Context, dispatchID uuid.UUID) error {
	if !s.cfg.AutoPrintEnabled() {
		// The kill switch, or missing credentials. Deliberately not an error the
		// worker retries forever: nothing changes until someone acts.
		obs.FromContext(ctx).Warn("print dispatch skipped: auto-print disabled", "dispatch", dispatchID)
		return nil
	}
	if s.storage == nil {
		return fmt.Errorf("object storage is not configured")
	}

	dispatch, err := s.store.Q.GetPrintDispatch(ctx, dispatchID)
	if err != nil {
		return fmt.Errorf("load dispatch: %w", err)
	}
	// Already queued on a previous attempt: nothing left to do.
	if dispatch.QueueItemID != nil {
		return nil
	}
	batch, err := s.store.Q.GetBatchByID(ctx, dispatch.BatchID)
	if err != nil {
		return fmt.Errorf("load batch: %w", err)
	}
	if batch.GcodeKey == nil || *batch.GcodeKey == "" {
		return fmt.Errorf("batch %s has no sliced plate", batch.BatchNumber)
	}

	// The DB stores these as int32 (sqlc's mapping for an integer column) while
	// the BamBuddy client speaks int; convert at this boundary only.
	libraryFileID := dispatch.LibraryFileID
	if libraryFileID == nil {
		id, err := s.uploadPlate(ctx, batch)
		if err != nil {
			return err
		}
		stored := int32(id)
		libraryFileID = &stored
	}

	item, err := s.bambuddy.AddToQueue(ctx, s.cfg.BambuddyAPIKey, bambuddy.QueueRequest{
		LibraryFileID: int(*libraryFileID),
		PrinterID:     int(dispatch.PrinterID),
		Quantity:      1,
		ManualStart:   s.cfg.BambuddyManualStart,
	})
	if err != nil {
		return fmt.Errorf("queue plate on printer %d: %w", dispatch.PrinterID, err)
	}

	// BamBuddy reports a filament shortfall on the queue item rather than
	// refusing outright. The plate is staged either way; the warning rides along
	// so an operator sees it before pressing start.
	var warning *string
	if item.FilamentShort {
		w := "BamBuddy reports the assigned spool may not have enough filament for this plate."
		if item.WaitingReason != nil {
			w = *item.WaitingReason
		}
		warning = &w
	}

	queueItemID := int32(item.ID)
	if _, err := s.store.Q.MarkPrintDispatchQueued(ctx, gen.MarkPrintDispatchQueuedParams{
		ID: dispatchID, LibraryFileID: libraryFileID, QueueItemID: &queueItemID,
		FilamentWarning: warning,
	}); err != nil {
		return fmt.Errorf("record queued dispatch: %w", err)
	}
	obs.FromContext(ctx).Info("plate dispatched",
		"dispatch", dispatchID, "batch", batch.BatchNumber,
		"printer", dispatch.PrinterID, "queue_item", item.ID,
		"staged", s.cfg.BambuddyManualStart)
	return nil
}

// uploadPlate downloads a batch's sliced plate and uploads it to BamBuddy's
// library, returning the library file id.
func (s *Server) uploadPlate(ctx context.Context, batch gen.Batch) (int, error) {
	dir, err := os.MkdirTemp("", "dispatch-*")
	if err != nil {
		return 0, fmt.Errorf("create workdir: %w", err)
	}
	defer func() { _ = os.RemoveAll(dir) }()

	name := bambuddy.PlateFilename(batch.BatchNumber)
	local := filepath.Join(dir, name)
	if err := s.storage.Download(ctx, *batch.GcodeKey, local); err != nil {
		return 0, fmt.Errorf("download plate %s: %w", *batch.GcodeKey, err)
	}

	file, err := s.bambuddy.UploadLibraryFile(ctx, s.cfg.BambuddyAPIKey, local, name)
	if err != nil {
		return 0, fmt.Errorf("upload plate: %w", err)
	}
	return file.ID, nil
}
