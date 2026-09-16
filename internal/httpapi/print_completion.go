package httpapi

// Finding out that a print finished, and letting the planks move on.
//
// Nothing told Tensor. BambuBuddy knows a plate finished - it holds the archive,
// the real duration, the real filament and the failure reason - and none of it
// came back, so a bed only ever reached 'completed' when a person ticked its
// planks in the Done dialog. Since completing a bed is the ONE thing that
// releases its jobs to Assembly, the entire downstream board waited on that
// person.
//
// Tensor ASKS rather than waiting to be told, and that is a deliberate choice
// against the push receiver that already exists in bambubuddy_events.go:
//
//   - BAMBUBUDDY_WEBHOOK_SECRET is unset, so that endpoint answers 503.
//   - BambuBuddy has no notification providers configured; it pushes to nobody.
//   - Push needs BambuBuddy to reach Tensor INBOUND, and it sits on a different
//     tailnet from the production host. Tensor can call out; it cannot call in.
//   - The push payload carries no identifiers at all - a filename, a human
//     duration string - from an operator-editable template. Releasing finished
//     goods into Assembly should not rest on that.
//
// A fold over current state is also idempotent for free, which a delivery count
// never is.

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/Optiminastic/tensor-core/internal/db"
	"github.com/Optiminastic/tensor-core/internal/db/gen"
	"github.com/Optiminastic/tensor-core/internal/integrations/bambubuddy"
	"github.com/Optiminastic/tensor-core/internal/obs"
	"github.com/Optiminastic/tensor-core/internal/production"
)

// PrintReconcileOutcome is what one pass did, for the log and the tests.
type PrintReconcileOutcome struct {
	Considered int
	// Backfilled counts beds whose queue_item_id was filled in from the queue.
	Backfilled int
	Completed  int
	Failed     int
	// Repaired counts beds that held an outcome but never reached 'completed' -
	// a crash between the two writes.
	Repaired int
	// Unmatched counts in-flight beds whose queue item has gone and whose plate
	// matched no archive. Worth a line each: it is what a naming drift looks
	// like from here, and the alternative is silence.
	Unmatched int
}

// ReconcileFinishedPrints resolves every in-flight bed against BambuBuddy.
//
// Best-effort throughout, and returns no error: this runs off the fleet-sync
// worker, and a reconciliation hiccup must not make River retry a fleet refresh
// that succeeded.
func (s *Server) ReconcileFinishedPrints(ctx context.Context) PrintReconcileOutcome {
	log := obs.FromContext(ctx)
	var out PrintReconcileOutcome

	if s.bambu == nil || !s.bambu.Configured() {
		return out
	}

	// Repair first, before anything else can fail: a bed holding an outcome
	// with no completion is a job set stranded one write short of Assembly.
	out.Repaired = s.repairResolvedBatches(ctx)

	inFlight, err := s.store.Q.ListBatchesInFlight(ctx)
	if err != nil {
		log.Warn("could not list beds in flight", "error", err)
		return out
	}
	out.Considered = len(inFlight)
	if len(inFlight) == 0 {
		// A quiet shop costs nothing upstream.
		return out
	}

	queue, err := s.bambu.ListQueue(ctx)
	if err != nil {
		log.Warn("could not read BambuBuddy's queue to reconcile prints", "error", err)
		return out
	}
	archives, err := s.bambu.ListArchives(ctx, s.archiveReconcileLimit())
	if err != nil {
		log.Warn("could not read BambuBuddy's archive to reconcile prints", "error", err)
		return out
	}

	queueByID := make(map[int32]bambubuddy.QueueItem, len(queue))
	queueByPlate := make(map[string]bambubuddy.QueueItem, len(queue))
	for _, item := range queue {
		queueByID[int32(item.ID)] = item
		if key := plateKey(item.Name()); key != "" {
			queueByPlate[key] = item
		}
	}
	// Archives are newest-first, so the FIRST match for a key is the most
	// recent print of that bed - which is the one an in-flight bed is waiting
	// on. A reprint reuses the order numbers, so taking the newest is what
	// stops a bed being completed by a print from last month.
	archiveByPlate := map[string]bambubuddy.Archive{}
	archiveByID := make(map[int32]bambubuddy.Archive, len(archives))
	for _, a := range archives {
		archiveByID[int32(a.ID)] = a
		if key := plateKey(a.Name()); key != "" {
			if _, seen := archiveByPlate[key]; !seen {
				archiveByPlate[key] = a
			}
		}
	}

	for _, b := range inFlight {
		key := plateKey(deref(b.PlateFilename))

		// Backfill the queue item while the queue still holds it. recordQueued
		// cannot write it - slicing is async, so RunPipeline answers 202 with
		// no queue entry yet - and nothing ever filled it in, which also kept
		// these beds out of plate measurement for ever.
		if b.QueueItemID == nil && key != "" {
			if item, ok := queueByPlate[key]; ok {
				if err := s.store.Q.SetBatchQueueItem(ctx, gen.SetBatchQueueItemParams{
					ID: b.ID, QueueItemID: int32Ptr(item.ID),
				}); err != nil {
					log.Warn("could not backfill a bed's queue item",
						"batch", b.BatchNumber, "error", err)
				} else {
					out.Backfilled++
					b.QueueItemID = int32Ptr(item.ID)
				}
			}
		}

		// Still in the queue and not finished there: nothing to resolve yet.
		if b.QueueItemID != nil {
			if item, ok := queueByID[*b.QueueItemID]; ok && !finishedQueueStatus(item.Status) {
				continue
			}
		}

		archive, ok := s.archiveForBatch(b, key, archiveByID, archiveByPlate)
		if !ok {
			out.Unmatched++
			// One line per bed, naming the plate. An unmatched name in the log
			// is the diagnostic that says the naming convention has drifted.
			log.Warn("a bed left BambuBuddy's queue but matched no archive",
				"batch", b.BatchNumber, "plate", deref(b.PlateFilename), "key", key)
			continue
		}
		if !archive.Finished() {
			continue
		}

		if archive.Status == bambubuddy.ArchiveFailed || archive.Status == bambubuddy.ArchiveCancelled {
			if s.recordFailedPrint(ctx, b, archive) {
				out.Failed++
			}
			continue
		}
		if s.completePrintedBatch(ctx, b, archive) {
			out.Completed++
		}
	}
	return out
}

// archiveForBatch resolves a bed to the print that ran it.
//
// By archive id when the queue item names one, else by the plate's order-number
// key. The time fence matters: a reprint reuses the order numbers, so an archive
// that finished before this bed was even approved describes a different print of
// the same planks.
func (s *Server) archiveForBatch(
	b gen.ListBatchesInFlightRow, key string,
	byID map[int32]bambubuddy.Archive, byPlate map[string]bambubuddy.Archive,
) (bambubuddy.Archive, bool) {
	if b.ArchiveID != nil {
		if a, ok := byID[*b.ArchiveID]; ok {
			return a, true
		}
	}
	if key == "" {
		return bambubuddy.Archive{}, false
	}
	a, ok := byPlate[key]
	if !ok {
		return bambubuddy.Archive{}, false
	}
	if b.ApprovedAt.Valid && !archiveRanAfter(a, b.ApprovedAt.Time) {
		return bambubuddy.Archive{}, false
	}
	return a, true
}

// archiveRanAfter reports whether an archive's print happened after a moment.
//
// Unparseable or absent timestamps answer true: refusing to match on a missing
// timestamp would strand a bed for ever, and the plate key already had to agree.
func archiveRanAfter(a bambubuddy.Archive, cutoff time.Time) bool {
	for _, raw := range []string{a.CompletedAt, a.StartedAt, a.CreatedAt} {
		if strings.TrimSpace(raw) == "" {
			continue
		}
		t, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			// BambuBuddy also emits timestamps with no zone.
			if t, err = time.Parse("2006-01-02T15:04:05.999999", raw); err != nil {
				continue
			}
		}
		return !t.Before(cutoff.Add(-archiveClockSkew))
	}
	return true
}

// archiveClockSkew is how far BambuBuddy's clock may run behind Tensor's before
// a genuine print looks older than the bed that asked for it.
const archiveClockSkew = 10 * time.Minute

// completePrintedBatch records the print and releases the bed's planks.
func (s *Server) completePrintedBatch(
	ctx context.Context, b gen.ListBatchesInFlightRow, a bambubuddy.Archive,
) bool {
	log := obs.FromContext(ctx)
	if !s.claimPrintOutcome(ctx, b, a, production.BatchCompleted) {
		return false
	}
	if !s.cfg.BatchAutoComplete {
		// The outcome is recorded either way, so turning the switch on later
		// completes these beds through the repair path rather than losing them.
		log.Info("a bed finished printing; not completing it (BATCH_AUTO_COMPLETE is off)",
			"batch", b.BatchNumber, "archive", a.ID)
		return false
	}

	// SetBatchStatus, not a second lifecycle implementation. It runs
	// CompleteProductionJobsForBatch inside its own transaction, which is what
	// makes the jobs satisfy the Assembly station's precondition - and which
	// deliberately skips a job that failed on its own, so a bad plank is not
	// force-completed into finished goods.
	if _, err := s.SetBatchStatus(ctx, b.ID, production.BatchCompleted); err != nil {
		log.Error("a bed finished printing but could not be completed",
			"batch", b.BatchNumber, "error", err)
		return false
	}
	log.Info("bed finished printing; its planks are now in assembly",
		"batch", b.BatchNumber, "archive", a.ID,
		"took_seconds", a.DurationSeconds(), "filament_g", a.FilamentGrams())
	return true
}

// recordFailedPrint records a failure without releasing anything.
//
// A failed plate is not finished goods. The bed keeps its status - a failure is
// "still locked, and here is why" rather than a lifecycle state - and the
// operator gets BambuBuddy's own wording on the Batches list, where print_error
// is already rendered.
//
// Notably it does NOT call FailProductionJob per plank. That mints a reprint for
// every job on the bed and debits waste per job; a whole-plate failure is
// usually one re-run of a perfectly good plate, and per-plank failure stays a
// human judgement.
func (s *Server) recordFailedPrint(
	ctx context.Context, b gen.ListBatchesInFlightRow, a bambubuddy.Archive,
) bool {
	if !s.claimPrintOutcome(ctx, b, a, a.Status) {
		return false
	}
	reason := strings.TrimSpace(a.FailureReason)
	if reason == "" {
		reason = fmt.Sprintf("The print %s on BambuBuddy.", a.Status)
	}
	s.recordPrintError(ctx, b.ID, reason)
	obs.FromContext(ctx).Warn("a bed's print did not finish",
		"batch", b.BatchNumber, "archive", a.ID, "outcome", a.Status, "reason", reason)
	return true
}

// claimPrintOutcome is the idempotency gate. Exactly one caller wins.
func (s *Server) claimPrintOutcome(
	ctx context.Context, b gen.ListBatchesInFlightRow, a bambubuddy.Archive, outcome string,
) bool {
	params := gen.RecordBatchPrintOutcomeParams{
		ID: b.ID, PrintOutcome: &outcome, ArchiveID: int32Ptr(a.ID),
		PrintStartedAt:      db.Timestamptz(parseArchiveTime(a.StartedAt)),
		PrintFinishedAt:     db.Timestamptz(parseArchiveTime(a.CompletedAt)),
		ActualFilamentGrams: gramsNumeric(a.FilamentGrams()),
	}
	if mins := a.DurationSeconds() / 60; mins > 0 {
		n := int32(mins)
		params.ActualPrintTimeMinutes = &n
	}
	if _, err := s.store.Q.RecordBatchPrintOutcome(ctx, params); err != nil {
		if isNoRows(err) {
			// Somebody already recorded this print. Correct and expected: the
			// pass runs every fleet sync.
			return false
		}
		obs.FromContext(ctx).Warn("could not record a bed's print outcome",
			"batch", b.BatchNumber, "error", err)
		return false
	}
	return true
}

// repairResolvedBatches finishes what a crash between the two writes left half
// done, and picks up beds recorded while BATCH_AUTO_COMPLETE was off.
func (s *Server) repairResolvedBatches(ctx context.Context) int {
	if !s.cfg.BatchAutoComplete {
		return 0
	}
	stranded, err := s.store.Q.ListBatchesResolvedButNotClosed(ctx)
	if err != nil || len(stranded) == 0 {
		return 0
	}
	log := obs.FromContext(ctx)
	repaired := 0
	for _, b := range stranded {
		if _, err := s.SetBatchStatus(ctx, b.ID, production.BatchCompleted); err != nil {
			log.Warn("could not complete a bed whose print had already resolved",
				"batch", b.BatchNumber, "error", err)
			continue
		}
		repaired++
		log.Info("completed a bed whose print had resolved but was never closed",
			"batch", b.BatchNumber)
	}
	return repaired
}

// finishedQueueStatus reports whether BambuBuddy's queue considers an item done.
func finishedQueueStatus(status string) bool {
	switch status {
	case bambubuddy.QueueCompleted, bambubuddy.QueueCancelled, bambubuddy.QueueFailed:
		return true
	}
	return false
}

func parseArchiveTime(raw string) *time.Time {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	for _, layout := range []string{time.RFC3339, "2006-01-02T15:04:05.999999"} {
		if t, err := time.Parse(layout, raw); err == nil {
			return &t
		}
	}
	return nil
}

// gramsNumeric is a weight as the column wants it, null when nothing was
// measured. Zero grams and "the printer never said" are different facts.
func gramsNumeric(g float64) pgtype.Numeric {
	var n pgtype.Numeric
	if g <= 0 {
		return n
	}
	if err := n.Scan(strconv.FormatFloat(g, 'f', 2, 64)); err != nil {
		return pgtype.Numeric{}
	}
	return n
}

// archiveReconcileLimit is how much history one pass reads.
func (s *Server) archiveReconcileLimit() int {
	if n := s.cfg.ArchiveReconcileLimit; n > 0 {
		return n
	}
	return defaultArchiveReconcileLimit
}

const defaultArchiveReconcileLimit = 200
