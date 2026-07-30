package brandpolicy

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Optiminastic/tensor-core/internal/db"
	"github.com/Optiminastic/tensor-core/internal/db/gen"
	"github.com/Optiminastic/tensor-core/internal/pricing"
)

// SyncDefaultCostAssumptions seeds one global default cost-assumption set when
// none exists, so the selling price reflects real fixed costs and margins out of
// the box (the admin can then edit it through /config/cost-assumptions). The CP
// rates match the engine defaults so Design CP is unchanged; the fixed costs and
// margins mirror the spec's worked example (fixed total ₹170, team 12%).
// Idempotent: it never overwrites an existing default. Returns 1 if it seeded.
func SyncDefaultCostAssumptions(ctx context.Context, store *db.Store) (int, error) {
	seeded := 0
	err := store.InTx(ctx, func(q *gen.Queries) error {
		if _, err := q.GetDefaultCostAssumptionForBrand(ctx, nil); err == nil {
			return nil // a default already exists; leave admin edits alone
		} else if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}

		fixed, err := json.Marshal(pricing.FixedVariableCosts{
			Packaging: 60, Shipping: 50, RtoCod: 20, PaymentGateway: 40,
		})
		if err != nil {
			return err
		}
		margins, err := json.Marshal(pricing.MarginAssumptions{
			AdSpendPct: 0.30, TeamPct: 0.12, OverheadPct: 0.05, TargetProfitPct: 0.15,
		})
		if err != nil {
			return err
		}

		if _, err := q.InsertCostAssumption(ctx, gen.InsertCostAssumptionParams{
			ID:   uuid.New(),
			Name: "Default cost assumptions",
			// Brand nil: applies to every brand until a brand-specific default is added.
			FilamentCostPerKg:      1000,
			ElectricityCostPerUnit: 12,
			MachineHourCost:        45,
			FinishingLabour:        30,
			Consumables:            15,
			FailurePct:             0.06,
			FixedCosts:             fixed,
			Margins:                margins,
			IsDefault:              true,
		}); err != nil {
			return err
		}
		seeded = 1
		return nil
	})
	return seeded, err
}
