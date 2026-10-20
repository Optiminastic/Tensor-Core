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

// plankTemplate renders EVERY heart count.
//
// It replaced three templates, one per count, and that is why heartVariants no
// longer carries a template of its own: the count is an argument now
// (side_hearts), fed by the field mapping below, not a choice of file. A
// variant still exists per count because the storefront sells them as separate
// options and the bill of materials differs - but all six print from this.
const plankTemplate = "dnp_heart_joinery"

// heartVariants is the heart axis the storefront sells.
var heartVariants = []struct {
	code, label string
}{
	{"0", "No heart"},
	{"1", "1 heart"},
	{"2", "2 hearts"},
}

// plankFieldMap is which of the customer's answers feeds which variable in
// dnp_heart_joinery.scad - the registry's statement of what ParamsFromProperties
// knows in Go.
//
// Seeding it is what MOVES THE PLANK onto the registry path: resolveRenderPlan
// falls through to the Go path while a product maps no fields, and takes over
// the moment it maps any. That is why it is behind -map-fields rather than
// seeded by default, and why nothing past that point falls back - a configured
// product that cannot render holds its job instead of quietly printing from
// the other path.
//
// Keys are NORMALISED, as normalisePropKey stores them: "step 4 first name",
// never "STEP 4-First Name-:".
var plankFieldMap = []struct {
	key, variable, valueType, fixed, deflt string
	optional                               bool
}{
	// The two names. Required: a plank with one name is scrap, and holding
	// the job names the field a person can go and fill in.
	{key: "step 4 first name", variable: "name1", valueType: "string"},
	{key: "step 5 second name", variable: "name2", valueType: "string"},

	// The heart count, read out of the storefront's WORDING - "1 RED HEART"
	// is 1 - which is what value_type 'count' exists for.
	//
	// OPTIONAL WITH A DEFAULT OF ZERO, and the default is the point. The
	// 2-STEP plank sells no heart option at all, so nothing on those lines
	// names one; the Go path reads that as zero. Required, those orders would
	// be held for a choice the customer was never offered. Optional with no
	// default, the template's own side_hearts = 1 would stand and every one
	// of them would grow a heart nobody bought.
	{key: "step 3", variable: "side_hearts", valueType: "count",
		optional: true, deflt: "0"},

	// The finished product size. Fixed, not from the order: the customer does
	// not choose it, and without it the template would use its own defaults -
	// which happen to agree today and are one edit from not.
	{variable: "plank_length", valueType: "number", fixed: "200"},
	{variable: "plank_width", valueType: "number", fixed: "50"},
	{variable: "total_height", valueType: "number", fixed: "40"},
}

var lightVariants = []struct{ code, label string }{
	{"none", "No light"},
	{"wired", "With light"},
}

func main() {
	dry := flag.Bool("dry-run", false, "print what would be written and stop")
	// OFF by default, because seeding the mapping is not a description - it is
	// a SWITCH. A product that maps no fields renders through the Go path that
	// has printed every plank so far; one that maps any renders from the
	// registry, and past that point nothing falls back. Worth doing, worth
	// doing deliberately.
	mapFields := flag.Bool("map-fields", false,
		"also seed the field mapping, which MOVES the plank onto the registry render path")
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
		fmt.Printf("  every variant prints from %s\n", plankTemplate)
		fmt.Println("  parts on every lit variant:")
		for _, p := range lightKit {
			fmt.Printf("    %-14s %-22s x%g %s\n", p.code, p.name, p.qty, p.unit)
		}
		if *mapFields {
			fmt.Println("\n  field mapping - THIS MOVES THE PLANK ONTO THE REGISTRY RENDER PATH:")
			for _, m := range plankFieldMap {
				describeFieldMap(m.key, m.variable, m.valueType, m.fixed, m.deflt, m.optional)
			}
		} else {
			fmt.Println("\n  no field mapping (pass -map-fields to seed it)")
			fmt.Println("  without it the plank keeps rendering through the Go path, unchanged")
		}
		return
	}

	if err := seed(ctx, store, *mapFields); err != nil {
		log.Fatalf("seed registry: %v", err)
	}
	fmt.Println("registry seeded")
}

// describeFieldMap prints one mapped field the way the dry run shows it.
func describeFieldMap(key, variable, valueType, fixed, deflt string, optional bool) {
	source := fmt.Sprintf("order %q", key)
	if fixed != "" {
		source = fmt.Sprintf("fixed %s", fixed)
	}
	note := "required"
	if optional {
		note = "optional"
		if deflt != "" {
			note = fmt.Sprintf("optional, %s when unanswered", deflt)
		}
	}
	fmt.Printf("    %-14s <- %-28s %-8s (%s)\n", variable, source, valueType, note)
}

func seed(ctx context.Context, store *db.Store, mapFields bool) error {
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

			if err := ensureDesign(ctx, q, variantID, plankTemplate); err != nil {
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

	if mapFields {
		if err := ensureFieldMaps(ctx, store, product.ID); err != nil {
			return err
		}
	}
	return nil
}

// ensureFieldMaps writes the plank's mapping, whole and in one transaction.
//
// Written whole rather than row by row, like the editor does: the mapping IS
// the list, and a half-written one is a product that renders a model missing
// what the customer asked for and exits 0. Clearing and rewriting inside one
// transaction means a failure leaves the previous mapping - or none, which
// falls back to the Go path - rather than a partial one.
func ensureFieldMaps(ctx context.Context, store *db.Store, productID uuid.UUID) error {
	return store.InTx(ctx, func(q *gen.Queries) error {
		if err := q.ClearProductFieldMaps(ctx, gen.ClearProductFieldMapsParams{
			ProductID: productID, Role: "body",
		}); err != nil {
			return fmt.Errorf("clear the existing mapping: %w", err)
		}
		for i, m := range plankFieldMap {
			var fixed, deflt *string
			if m.fixed != "" {
				v := m.fixed
				fixed = &v
			}
			if m.deflt != "" {
				v := m.deflt
				deflt = &v
			}
			if err := q.InsertProductFieldMap(ctx, gen.InsertProductFieldMapParams{
				ID: uuid.New(), ProductID: productID, PropertyKey: m.key,
				ScadVariable: m.variable, ValueType: m.valueType, Role: "body",
				Required: !m.optional, FixedValue: fixed, DefaultValue: deflt,
				Position: int32(i),
			}); err != nil {
				return fmt.Errorf("map %s: %w", m.variable, err)
			}
			describeFieldMap(m.key, m.variable, m.valueType, m.fixed, m.deflt, m.optional)
		}
		return nil
	})
}

type valueSpec struct{ code, label string }

func optionValues(in []struct{ code, label string }) []valueSpec {
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

// ensureDesign makes `template` the file this variant's body prints from.
//
// It used to return as soon as a body design existed, which made this seeder
// create-only: the six variants were seeded against the three per-heart-count
// templates, and when one file replaced all three a second run left every
// variant still naming a file nothing renders from. The registry would then
// DESCRIBE the plank wrongly - and the moment a field mapping made it the
// authority, it would have rendered the old geometry.
//
// Superseded rather than updated in place, following the table's own rule:
// "which file printed the planks we shipped last Tuesday" has to stay
// answerable after somebody changes it.
func ensureDesign(ctx context.Context, q *gen.Queries, variantID uuid.UUID, template string) error {
	designs, err := q.ListVariantDesigns(ctx, variantID)
	if err != nil {
		return fmt.Errorf("list variant designs: %w", err)
	}
	for _, d := range designs {
		if d.Role != "body" || d.Status != "active" {
			continue
		}
		if d.TemplateKey != nil && *d.TemplateKey == template {
			return nil
		}
		// An uploaded design (no template key) is somebody's deliberate
		// override and is left alone - replacing it would throw away a file
		// this seeder cannot reproduce.
		if d.TemplateKey == nil {
			return nil
		}
		if err := q.SupersedeVariantDesign(ctx, gen.SupersedeVariantDesignParams{
			VariantID: variantID, Role: "body",
		}); err != nil {
			return fmt.Errorf("retire the previous design: %w", err)
		}
		fmt.Printf("  design %s -> %s\n", *d.TemplateKey, template)
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
