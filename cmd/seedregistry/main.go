// Command seedregistry puts the Dual Name Plank into the product registry.
//
// The registry's first real content, and a worked example of the shape: one
// product, two option axes, the six variants they make, the template each one
// renders from, and the parts the lit ones carry.
//
// Idempotent throughout - every insert is guarded by a lookup, so running it
// twice changes nothing and running it after a partial failure finishes the job.
// That matters because this is meant to be run against production once the
// parts have prices, not only against a fresh development database.
//
// What it deliberately does NOT do is invent prices. Every part is created with
// a null unit_price, which the UI renders as an em dash rather than as free.
// A guessed price is worse than a missing one: it looks like an answer.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"time"

	"github.com/google/uuid"
	"github.com/joho/godotenv"

	"github.com/Optiminastic/tensor-core/internal/config"
	"github.com/Optiminastic/tensor-core/internal/db"
	"github.com/Optiminastic/tensor-core/internal/db/gen"
)

// The parts a lit plank carries, as the shop describes them.
//
// The base box is 3D printed rather than bought, so it will eventually be a
// variant_designs row with role 'base' - but no base-box template exists yet, so
// today it is tracked as a part like any other. That is not a fudge: until
// Tensor can render one, the boxes are made or bought outside it and sit on a
// shelf, which is exactly what a part is.
var lightKit = []struct {
	code, name, unit string
	qty              float64
}{
	{"DNP-BASE-BOX", "DNP base box (black)", "piece", 1},
	{"DNP-LIGHT", "LED light", "piece", 1},
	{"DNP-SWITCH", "Switch", "piece", 1},
	{"DNP-WIRE", "Wire", "piece", 2},
}

// heartVariants pairs each heart count with the template that renders it,
// mirroring templateForHearts in internal/personalise/dnp.go. Seeding them here
// is the first step in that switch statement becoming data.
var heartVariants = []struct {
	code, label, template string
}{
	{"0", "No heart", "dnp_with_no_heart"},
	{"1", "1 heart", "dual_one_heart"},
	{"2", "2 hearts", "dnp_two_heart"},
}

var lightVariants = []struct{ code, label string }{
	{"none", "No light"},
	{"wired", "With light"},
}

func main() {
	dry := flag.Bool("dry-run", false, "print what would be written and stop")
	flag.Parse()

	_ = godotenv.Load("env/local.env")
	cfg := config.Load()
	if cfg.DatabaseURL == "" {
		log.Fatal("DATABASE_URL is not set")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	store, err := db.Open(ctx, cfg.DatabaseURL, db.Options{})
	if err != nil {
		log.Fatalf("open database: %v", err)
	}
	defer store.Pool.Close()

	if *dry {
		fmt.Println("would seed product DNP - Dual Name Plank (generated)")
		fmt.Printf("  options: heart_count (%d values), light (%d values)\n",
			len(heartVariants), len(lightVariants))
		fmt.Printf("  variants: %d\n", len(heartVariants)*len(lightVariants))
		fmt.Println("  parts on every lit variant:")
		for _, p := range lightKit {
			fmt.Printf("    %-14s %-22s x%g %s\n", p.code, p.name, p.qty, p.unit)
		}
		return
	}

	if err := seed(ctx, store); err != nil {
		log.Fatalf("seed registry: %v", err)
	}
	fmt.Println("registry seeded")
}

func seed(ctx context.Context, store *db.Store) error {
	q := store.Q

	product, err := q.GetProductByCode(ctx, "DNP")
	if err != nil {
		product, err = q.InsertProduct(ctx, gen.InsertProductParams{
			ID: uuid.New(), Code: "DNP", Name: "Dual Name Plank",
			Kind: "generated", Status: "active",
		})
		if err != nil {
			return fmt.Errorf("insert product: %w", err)
		}
		fmt.Println("created product DNP")
	}

	hearts, heartValues, err := ensureOption(ctx, q, product.ID, "heart_count", "Hearts", 0,
		optionValues(heartVariants))
	if err != nil {
		return err
	}
	_ = hearts
	light, lightValues, err := ensureOption(ctx, q, product.ID, "light", "Light", 1,
		lightValueSpecs())
	if err != nil {
		return err
	}
	_ = light

	parts, err := ensureParts(ctx, q)
	if err != nil {
		return err
	}

	existing, err := q.ListVariantsForProduct(ctx, product.ID)
	if err != nil {
		return fmt.Errorf("list variants: %w", err)
	}
	byName := map[string]gen.ListVariantsForProductRow{}
	for _, v := range existing {
		byName[v.Name] = v
	}

	for _, h := range heartVariants {
		for _, l := range lightVariants {
			name := fmt.Sprintf("%s, %s", h.label, l.label)
			row, seen := byName[name]
			var variantID uuid.UUID
			if seen {
				variantID = row.ID
			} else {
				// No SKU. The storefront sells this plank as colour x light
				// under names like "Dual Name Plank - BLUE / NO LIGHT", and the
				// SKU it carries (T3DPS-DNP-2) does not encode the heart count
				// at all - the hearts come from the customer's own properties.
				// Guessing a mapping here would be inventing data.
				v, err := q.InsertVariant(ctx, gen.InsertVariantParams{
					ID: uuid.New(), ProductID: product.ID, Name: name, Status: "active",
				})
				if err != nil {
					return fmt.Errorf("insert variant %q: %w", name, err)
				}
				variantID = v.ID
				fmt.Printf("created variant %s\n", name)
			}

			for _, id := range []uuid.UUID{heartValues[h.code], lightValues[l.code]} {
				if err := q.AddVariantOptionValue(ctx, gen.AddVariantOptionValueParams{
					VariantID: variantID, OptionValueID: id,
				}); err != nil {
					return fmt.Errorf("tie variant %q to its options: %w", name, err)
				}
			}

			if err := ensureDesign(ctx, q, variantID, h.template); err != nil {
				return err
			}
			// Only a lit plank carries parts. A plain one is the printed plank
			// and nothing else, which is why its bill of materials is empty
			// rather than absent.
			if l.code == "wired" {
				if err := ensureBom(ctx, store, variantID, parts); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

type valueSpec struct{ code, label string }

func optionValues(in []struct{ code, label, template string }) []valueSpec {
	out := make([]valueSpec, 0, len(in))
	for _, v := range in {
		out = append(out, valueSpec{code: v.code, label: v.label})
	}
	return out
}

func lightValueSpecs() []valueSpec {
	out := make([]valueSpec, 0, len(lightVariants))
	for _, v := range lightVariants {
		out = append(out, valueSpec{code: v.code, label: v.label})
	}
	return out
}

// ensureOption creates an axis and its values if they are not already there,
// returning the value ids by code.
func ensureOption(
	ctx context.Context, q *gen.Queries, productID uuid.UUID,
	code, label string, position int32, values []valueSpec,
) (uuid.UUID, map[string]uuid.UUID, error) {
	options, err := q.ListProductOptions(ctx, productID)
	if err != nil {
		return uuid.Nil, nil, fmt.Errorf("list options: %w", err)
	}
	var optionID uuid.UUID
	for _, o := range options {
		if o.Code == code {
			optionID = o.ID
		}
	}
	if optionID == uuid.Nil {
		o, err := q.InsertProductOption(ctx, gen.InsertProductOptionParams{
			ID: uuid.New(), ProductID: productID, Code: code, Label: label, Position: position,
		})
		if err != nil {
			return uuid.Nil, nil, fmt.Errorf("insert option %q: %w", code, err)
		}
		optionID = o.ID
		fmt.Printf("created option %s\n", code)
	}

	all, err := q.ListOptionValues(ctx, productID)
	if err != nil {
		return uuid.Nil, nil, fmt.Errorf("list option values: %w", err)
	}
	byCode := map[string]uuid.UUID{}
	for _, v := range all {
		if v.OptionID == optionID {
			byCode[v.Code] = v.ID
		}
	}
	for i, spec := range values {
		if _, ok := byCode[spec.code]; ok {
			continue
		}
		v, err := q.InsertOptionValue(ctx, gen.InsertOptionValueParams{
			ID: uuid.New(), OptionID: optionID, Code: spec.code,
			Label: spec.label, Position: int32(i),
		})
		if err != nil {
			return uuid.Nil, nil, fmt.Errorf("insert option value %q: %w", spec.code, err)
		}
		byCode[spec.code] = v.ID
		fmt.Printf("created option value %s=%s\n", code, spec.code)
	}
	return optionID, byCode, nil
}

// ensureParts puts the light kit on the shelf, without inventing prices.
func ensureParts(ctx context.Context, q *gen.Queries) (map[string]uuid.UUID, error) {
	out := map[string]uuid.UUID{}
	for _, p := range lightKit {
		item, err := q.GetInventoryItemByCode(ctx, p.code)
		if err == nil {
			out[p.code] = item.ID
			continue
		}
		// Quantity zero, price null: the shop counts its own shelf, and a
		// seeded stock level would be a lie that reads as a fact.
		code := p.code
		created, err := q.UpsertInventoryItem(ctx, gen.UpsertInventoryItemParams{
			ID: uuid.New(), Name: p.name, Unit: p.unit, Quantity: 0, Code: &code,
		})
		if err != nil {
			return nil, fmt.Errorf("create part %q: %w", p.code, err)
		}
		out[p.code] = created.ID
		fmt.Printf("created part %s (%s)\n", p.code, p.name)
	}
	return out, nil
}

func ensureDesign(ctx context.Context, q *gen.Queries, variantID uuid.UUID, template string) error {
	designs, err := q.ListVariantDesigns(ctx, variantID)
	if err != nil {
		return fmt.Errorf("list variant designs: %w", err)
	}
	for _, d := range designs {
		if d.Role == "body" {
			return nil
		}
	}
	key := template
	if _, err := q.InsertVariantDesign(ctx, gen.InsertVariantDesignParams{
		ID: uuid.New(), VariantID: variantID, Role: "body",
		TemplateKey: &key, Version: 1, Status: "active",
	}); err != nil {
		return fmt.Errorf("attach design %q: %w", template, err)
	}
	return nil
}

// ensureBom writes the light kit onto a variant, whole and in one transaction.
func ensureBom(
	ctx context.Context, store *db.Store, variantID uuid.UUID, parts map[string]uuid.UUID,
) error {
	existing, err := store.Q.ListVariantBom(ctx, variantID)
	if err != nil {
		return fmt.Errorf("list bom: %w", err)
	}
	if len(existing) == len(lightKit) {
		return nil
	}
	return store.InTx(ctx, func(q *gen.Queries) error {
		if err := q.ClearVariantBom(ctx, variantID); err != nil {
			return err
		}
		for _, p := range lightKit {
			if err := q.ReplaceVariantBomItem(ctx, gen.ReplaceVariantBomItemParams{
				ID: uuid.New(), VariantID: variantID,
				InventoryItemID: parts[p.code], Quantity: p.qty,
			}); err != nil {
				return err
			}
		}
		return nil
	})
}
