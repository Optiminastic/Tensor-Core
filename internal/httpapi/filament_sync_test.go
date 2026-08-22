package httpapi

import (
	"sort"
	"testing"

	"github.com/Optiminastic/tensor-core/internal/integrations/bambubuddy"
)

// The sync's whole job is turning per-spool rows into per-material buckets, so
// that is what this pins down.
func TestAggregateSpools(t *testing.T) {
	spools := []bambubuddy.Spool{
		// Three spools of the same material and colour must become one bucket.
		{Material: "PLA", ColorName: "Black", LabelWeight: 1000, WeightUsed: 0},
		{Material: "PLA", ColorName: "Black", LabelWeight: 1000, WeightUsed: 250},
		{Material: "PLA", ColorName: "Black", LabelWeight: 1000, WeightUsed: 1000},
		// Same material, different colour: a separate bucket.
		{Material: "PLA", ColorName: "Red", LabelWeight: 1000, WeightUsed: 100},
		// Whitespace must not split a colour into two buckets that never merge.
		{Material: "PLA", ColorName: "  Red  ", LabelWeight: 500, WeightUsed: 0},
		// Archived spools are off the shelf and must not be counted.
		{Material: "PLA", ColorName: "Black", LabelWeight: 1000, WeightUsed: 0, Archived: true},
		// Over-consumed clamps at zero rather than subtracting from the total.
		{Material: "PETG", ColorName: "Blue", LabelWeight: 1000, WeightUsed: 1200},
		// No material is unusable - nothing to key stock on.
		{Material: "  ", ColorName: "Green", LabelWeight: 1000},
	}

	got := aggregateSpools(spools)
	sort.Slice(got, func(i, j int) bool {
		if got[i].material != got[j].material {
			return got[i].material < got[j].material
		}
		return got[i].colour < got[j].colour
	})

	want := []filamentBucket{
		{material: "PETG", colour: "Blue", grams: 0},    // clamped, not -200
		{material: "PLA", colour: "Black", grams: 1750}, // 1000 + 750 + 0, archived excluded
		{material: "PLA", colour: "Red", grams: 1400},   // 900 + 500, whitespace merged
	}
	if len(got) != len(want) {
		t.Fatalf("buckets = %d, want %d: %+v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("bucket %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

// The core is the empty spool, not filament, and must never count as stock.
func TestRemainingGramsIgnoresCore(t *testing.T) {
	s := bambubuddy.Spool{LabelWeight: 1000, CoreWeight: 250, WeightUsed: 400}
	if got := s.RemainingGrams(); got != 600 {
		t.Fatalf("RemainingGrams() = %v, want 600 (label - used, core excluded)", got)
	}
}
