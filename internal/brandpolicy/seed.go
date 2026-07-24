package brandpolicy

import (
	"context"
	"encoding/json"

	"github.com/google/uuid"

	"github.com/Optiminastic/tensor-core/internal/db"
	"github.com/Optiminastic/tensor-core/internal/db/gen"
	"github.com/Optiminastic/tensor-core/internal/pricing"
)

type brandDefault struct {
	slug              pricing.Brand
	name              string
	startingPrice     float64
	description       string
	ladder            []int
	greenMax          float64
	yellowMax         float64
	entryMachineHours *float64
	entryRung         *int32
}

func fptr(v float64) *float64 { return &v }
func i32(v int32) *int32      { return &v }

// brandDefaults reproduce the spec's two brands, copied from the engine
// constants so the database and the built-in fallback agree.
var brandDefaults = []brandDefault{
	{
		slug: pricing.BrandGifting, name: "Personalised Gifting", startingPrice: 999,
		description: "Personalised 3D-printed gifting. Starts at ₹999.",
		ladder:      pricing.GiftingLadder, greenMax: 0.25, yellowMax: 0.30,
		entryMachineHours: fptr(pricing.GiftingEntryMachineHours), entryRung: i32(pricing.GiftingEntryRung),
	},
	{
		slug: pricing.BrandDecor, name: "Decor", startingPrice: 3999,
		description: "Home, office and desk decor. Starts at ₹3,999.",
		ladder:      pricing.DecorLadder, greenMax: 0.30, yellowMax: 0.33,
		entryMachineHours: nil, entryRung: nil,
	},
}

// SyncBrands inserts the two default brands if absent, never overwriting admin
// edits. It always reports the number of brands the system defines (2).
func SyncBrands(ctx context.Context, store *db.Store) (int, error) {
	err := store.InTx(ctx, func(q *gen.Queries) error {
		for _, d := range brandDefaults {
			exists, err := q.BrandExists(ctx, string(d.slug))
			if err != nil {
				return err
			}
			if exists {
				continue
			}
			ladderJSON, err := json.Marshal(d.ladder)
			if err != nil {
				return err
			}
			description := d.description
			if _, err := q.InsertBrand(ctx, gen.InsertBrandParams{
				ID:                uuid.New(),
				Slug:              string(d.slug),
				Name:              d.name,
				LogoUrl:           nil,
				StartingPrice:     d.startingPrice,
				ShopifyUrl:        nil,
				Description:       &description,
				IsActive:          true,
				Ladder:            ladderJSON,
				CpGreenMax:        d.greenMax,
				CpYellowMax:       d.yellowMax,
				EntryMachineHours: d.entryMachineHours,
				EntryRung:         d.entryRung,
			}); err != nil {
				return err
			}
		}
		return nil
	})
	return len(brandDefaults), err
}
