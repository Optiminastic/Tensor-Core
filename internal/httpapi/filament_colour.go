package httpapi

// Colour-aware filament accounting. production_jobs.colours[] is a set, but
// filament_inventory is keyed per (material, colour) bucket - filamentAvailable
// /decrementFilament (filament.go) only ever touch the untinted bucket, which
// can both hide a real per-colour shortage (plenty of red, zero black, but the
// aggregate total still clears) and mis-account waste. These helpers even-split
// a job's grams across its colours and operate per bucket, for batch approval's
// reservation/shortage-check and the reprint-waste decrement.

import (
	"context"

	"github.com/Optiminastic/tensor-core/internal/db"
	"github.com/Optiminastic/tensor-core/internal/db/gen"
)

type filamentKey struct {
	Material string
	Colour   string
}

// filamentSplit even-splits totalGrams across colours (or the untinted bucket
// when colours is empty) and merges the result into acc, keyed by
// (material, colour). Call once per job/usage and accumulate into a shared acc.
func filamentSplit(acc map[filamentKey]float64, material *string, colours []string, totalGrams float64) {
	if material == nil || *material == "" || totalGrams <= 0 {
		return
	}
	if len(colours) == 0 {
		acc[filamentKey{Material: *material}] += totalGrams
		return
	}
	per := totalGrams / float64(len(colours))
	for _, c := range colours {
		acc[filamentKey{Material: *material, Colour: c}] += per
	}
}

// filamentSplitForJobs builds the combined (material, colour) -> grams map for
// every job in a batch, from each job's own material/colours/filament snapshot.
func filamentSplitForJobs(jobs []gen.ProductionJob) map[filamentKey]float64 {
	acc := make(map[filamentKey]float64)
	for _, j := range jobs {
		filamentSplit(acc, j.Material, decodeColours(j.Colours), db.NumFloat(j.FilamentGramsRequired))
	}
	return acc
}

// filamentAvailableByColour reports whether every bucket in split has enough
// stock - a batch is short if ANY bucket is short, not just the aggregate
// total. An untracked bucket (no inventory row) is treated as available,
// matching filamentAvailable's existing convention.
func (s *Server) filamentAvailableByColour(ctx context.Context, q *gen.Queries, split map[filamentKey]float64) bool {
	for key, grams := range split {
		row, err := q.GetFilamentByMaterialColour(ctx, gen.GetFilamentByMaterialColourParams{
			Material: key.Material, Colour: colourPtr(key.Colour),
		})
		if err != nil {
			continue
		}
		if db.NumFloat(row.GramsAvailable) < grams {
			return false
		}
	}
	return true
}

// adjustFilamentByColour applies sign*grams to every bucket in split -
// sign -1 to reserve/debit, +1 to release/credit back. Best-effort per bucket
// (an untracked bucket is silently skipped, stock may go negative), matching
// decrementFilament's existing convention. There is currently no batch
// "cancel" transition that would call this with sign +1 - reservation only
// ever happens once, at the guarded pending_approval -> open transition inside
// approveBatch - but the sign parameter keeps this ready for when one exists.
func (s *Server) adjustFilamentByColour(ctx context.Context, q *gen.Queries, split map[filamentKey]float64, sign float64) error {
	for key, grams := range split {
		if grams <= 0 {
			continue
		}
		if err := q.AdjustFilamentStock(ctx, gen.AdjustFilamentStockParams{
			Delta: sign * grams, Material: key.Material, Colour: colourPtr(key.Colour),
		}); err != nil {
			return err
		}
	}
	return nil
}

func colourPtr(colour string) *string {
	if colour == "" {
		return nil
	}
	return &colour
}
