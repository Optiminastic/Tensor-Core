package httpapi

// Which orders are on each bed, for the Batches table's Jobs column.
//
// The column used to be a count, and on the list response it was not even that:
// jobs_count is only ever set on the single-batch detail, so every row of the
// table rendered a dash. "4" was not much of an answer anyway - standing at a
// printer, the question is which customers' work is on this plate, and that is
// what the order numbers say.
//
// Read back out of job_number (JOB-114556) rather than joined through to the
// orders table, for the same reason plateFileStem does it: the job number is
// minted from the order's own number precisely so it carries it, and this is
// asked for a whole page of batches at once.

import (
	"context"
	"strings"

	"github.com/google/uuid"

	"github.com/Optiminastic/tensor-core/internal/db/gen"
	"github.com/Optiminastic/tensor-core/internal/obs"
)

// BatchColour is one filament colour on a bed: the name the shop uses and the
// swatch to draw it with.
//
// Both, because neither alone is enough on screen. "BABY PINK" says which spool
// to fetch; the hex is what a circle can actually be filled with, and an
// operator recognises a colour far faster than they read one.
type BatchColour struct {
	Name string `json:"name"`
	Hex  string `json:"hex"`
}

// batchColoursFor is the distinct colours on each batch, keyed by batch id.
//
// Under colour batching a bed has exactly one, which is the case this serves.
// More than one is still reported rather than collapsed - a hand-assembled batch
// should show what is actually on it.
func (s *Server) batchColoursFor(ctx context.Context, rows []gen.ListJobNumbersForBatchesRow) map[uuid.UUID][]BatchColour {
	out := map[uuid.UUID][]BatchColour{}
	seen := map[uuid.UUID]map[string]bool{}
	for _, r := range rows {
		if r.BatchID == nil || r.Colour == nil {
			continue
		}
		name := strings.ToUpper(strings.TrimSpace(*r.Colour))
		if name == "" {
			continue
		}
		if seen[*r.BatchID] == nil {
			seen[*r.BatchID] = map[string]bool{}
		}
		if seen[*r.BatchID][name] {
			continue
		}
		seen[*r.BatchID][name] = true

		// The hex is best-effort: a colour the shelf has never heard of still
		// gets a row, just without a swatch. Dropping it would hide a colour the
		// bed genuinely needs.
		hex, err := s.resolveColourHex(ctx, name)
		if err != nil {
			hex = ""
		}
		out[*r.BatchID] = append(out[*r.BatchID], BatchColour{Name: name, Hex: hex})
	}
	return out
}

// batchJobRows reads the per-batch job rows both decorated columns need, once.
func (s *Server) batchJobRows(ctx context.Context, ids []uuid.UUID) []gen.ListJobNumbersForBatchesRow {
	if len(ids) == 0 {
		return nil
	}
	rows, err := s.store.Q.ListJobNumbersForBatches(ctx, ids)
	if err != nil {
		obs.FromContext(ctx).Warn("could not read the batches' jobs", "error", err)
		return nil
	}
	return rows
}

// orderNumbersByBatch groups the order numbers a batch's jobs came from.
func orderNumbersByBatch(rows []gen.ListJobNumbersForBatchesRow) map[uuid.UUID][]string {
	if len(rows) == 0 {
		return nil
	}

	seen := map[uuid.UUID]map[string]bool{}
	out := map[uuid.UUID][]string{}
	for _, r := range rows {
		if r.BatchID == nil {
			continue
		}
		number := orderNumberFromJobNumber(r.JobNumber)
		if number == "" {
			continue
		}
		// Deduplicated per batch: one order with two planks on the same bed is
		// one order, and listing it twice would make a bed of four jobs look
		// like it served four customers when it served three.
		if seen[*r.BatchID] == nil {
			seen[*r.BatchID] = map[string]bool{}
		}
		if seen[*r.BatchID][number] {
			continue
		}
		seen[*r.BatchID][number] = true
		out[*r.BatchID] = append(out[*r.BatchID], number)
	}
	for id := range out {
		sortOrderNumbers(out[id])
	}
	return out
}

// decorateBatches attaches the order numbers and colours both extra columns of
// the Batches table need, from one read of the batches' jobs.
func (s *Server) decorateBatches(ctx context.Context, out []batchResponse, ids []uuid.UUID) []batchResponse {
	rows := s.batchJobRows(ctx, ids)
	if len(rows) == 0 {
		return out
	}
	orders := orderNumbersByBatch(rows)
	colours := s.batchColoursFor(ctx, rows)
	for i := range out {
		id, err := uuid.Parse(out[i].ID)
		if err != nil {
			continue
		}
		out[i].OrderNumbers = orders[id]
		out[i].Colours = colours[id]
	}
	return out
}

// batchIDsOf reads the ids off a page of batches, for the bulk lookup above.
func batchIDsOf(rows []gen.Batch) []uuid.UUID {
	ids := make([]uuid.UUID, 0, len(rows))
	for _, b := range rows {
		ids = append(ids, b.ID)
	}
	return ids
}
