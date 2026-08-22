package bambubuddy

// BambuBuddy's spool inventory - the physical shelf.
//
// The two systems count filament differently, and the difference is the whole
// reason a sync is needed rather than a shared table. BambuBuddy tracks
// INDIVIDUAL SPOOLS: 129 rows, each with a label weight, a used weight and a
// cost. Tensor tracks AVAILABILITY PER MATERIAL AND COLOUR, because that is the
// question the batch planner asks - "is there enough black PLA for this bed?"
// - and it never cares which spool the grams come from.
//
// So this reads spools and the caller aggregates. Nothing here writes: spools
// are BambuBuddy's to manage.

import (
	"context"
	"strings"
)

// Spool is one physical spool on the shelf.
//
// A subset of what BambuBuddy stores - it also carries tag UIDs, k-profiles,
// scale readings and drying history, none of which Tensor has any use for.
type Spool struct {
	ID       int    `json:"id"`
	Material string `json:"material"`
	// Subtype is the vendor's line, e.g. "PRO+" for "PLA PRO+". Deliberately
	// NOT folded into Material: Tensor keys stock on the material name its
	// slicing profiles use, and inventing "PLA PRO+" would create a bucket no
	// design ever asks for.
	Subtype   string `json:"subtype"`
	ColorName string `json:"color_name"`
	RGBA      string `json:"rgba"`
	Brand     string `json:"brand"`
	// LabelWeight is the filament the spool held when full, in grams - the
	// number printed on the label, excluding the core.
	LabelWeight float64 `json:"label_weight"`
	// CoreWeight is the empty spool itself. Present so a scale reading can be
	// turned into a remaining weight; it is NOT filament and must never be
	// counted as available.
	CoreWeight float64 `json:"core_weight"`
	// WeightUsed is how much has been consumed, in grams.
	WeightUsed      float64  `json:"weight_used"`
	CostPerKg       *float64 `json:"cost_per_kg"`
	StorageLocation *string  `json:"storage_location"`
	Archived        bool     `json:"archived"`
}

// RemainingGrams is the filament actually left on this spool.
//
// Clamped at zero: BambuBuddy allows weight_used to exceed the label weight
// (a heavier-than-labelled spool, or a correction), and a negative remainder
// would silently subtract from the material's total when these are summed.
func (s Spool) RemainingGrams() float64 {
	remaining := s.LabelWeight - s.WeightUsed
	if remaining < 0 {
		return 0
	}
	return remaining
}

// Colour is the spool's colour name, normalised for use as a bucket key.
//
// Trimmed because Tensor keys stock on (material, colour) and a stray space
// would split one colour into two rows that never merge. Case is deliberately
// left alone - "Black" is how BambuBuddy presents it and how an operator will
// look for it.
func (s Spool) Colour() string {
	return strings.TrimSpace(s.ColorName)
}

// ListSpools returns every spool in BambuBuddy's inventory.
//
// Unpaginated on purpose: the endpoint answers with the whole list (129 spools
// today, in one response), and a shelf is not a collection that grows without
// bound the way a print history does.
func (c *Client) ListSpools(ctx context.Context) ([]Spool, error) {
	var out []Spool
	if err := c.get(ctx, "/api/v1/inventory/spools", &out); err != nil {
		return nil, err
	}
	return out, nil
}
