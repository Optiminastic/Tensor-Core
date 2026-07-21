// Package brandpolicy is the only place the pricing path reads the database. It
// loads a brand's editable pricing policy (ladder, thresholds, entry rule) so
// the pure engine can be fed plain values. When a brand has no row, Load returns
// nil and the caller falls back to the engine's built-in constants.
package brandpolicy

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/jackc/pgx/v5"

	"github.com/Optiminastic/tensor-core/internal/db"
	"github.com/Optiminastic/tensor-core/internal/db/gen"
	"github.com/Optiminastic/tensor-core/internal/pricing"
)

// Policy is a brand's pricing policy in engine-ready form.
type Policy struct {
	Ladder            []int
	GreenMax          float64
	YellowMax         float64
	EntryMachineHours *float64
	EntryRung         *int
}

// Load reads the brand row and maps it to a Policy. It returns (nil, nil) when
// the brand is not configured, so pricing falls back to built-in defaults.
func Load(ctx context.Context, q *gen.Queries, brand pricing.Brand) (*Policy, error) {
	row, err := q.GetBrandByKey(ctx, string(brand))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}

	var ladder []int
	if err := json.Unmarshal(row.Ladder, &ladder); err != nil {
		return nil, err
	}

	policy := &Policy{
		Ladder:            ladder,
		GreenMax:          row.CpGreenMax,
		YellowMax:         row.CpYellowMax,
		EntryMachineHours: db.NumFloatPtr(row.EntryMachineHours),
	}
	if row.EntryRung != nil {
		v := int(*row.EntryRung)
		policy.EntryRung = &v
	}
	return policy, nil
}
