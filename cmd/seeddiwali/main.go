// Command seeddiwali configures the two Diwali products in the registry.
//
// Both arrived the same way: imported from Shopify with their SKUs, their
// .scad uploaded and assigned to every variant, and then nothing - no field
// mapping, so no order can fill TEXT, and no colour scheme, so the renderer
// falls back to the plank's pair and asks each template for a PART="base" it
// does not draw. A product in that state renders an empty mesh and holds.
//
// This writes the two things that were missing. It is a SEED, idempotent and
// meant to be run again: it replaces exactly the rows it owns and leaves
// everything else alone, so the same command brings production to the same
// state as this laptop rather than somebody re-typing fifteen colours into a
// form and getting one wrong.
//
// Everything here is editable afterwards from the registry page. This is the
// first load, not the source of truth.
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
	"github.com/Optiminastic/tensor-core/internal/httpapi"
	"github.com/Optiminastic/tensor-core/internal/personalise"
)

// designRole is the role both products' single design file carries. Neither is
// a multi-part product - one file, one body.
const designRole = "body"

// previewRole is the role of the file drawn for the customer and never
// printed - see httpapi's designRolePreview, which this must match.
const previewRole = "preview"

// nameField is the personalisation property both products carry.
//
// "Name" is what the storefront's product-options app actually sends, read off
// order T3DPS-115961: {"name": "Name", "value": "Tushar ji"}. NOT "Name on
// Diya Holder", which is the label painted on the page - the two differ, and
// mapping the label would hold every order.
//
// Normalised on the way in by the same function the importer uses, so the
// mapping matches what was stored rather than what was typed.
const nameField = "Name"

// scheme is one colourway's palette: the pieces the template draws, and the
// colour each prints in. An empty colour means the piece follows whatever the
// customer bought.
type scheme struct {
	// variantSKU scopes it. Empty is the product's own scheme, which every
	// variant that declares nothing of its own falls back to.
	variantSKU string
	// role is which design file these pieces belong to. Empty is "body", the
	// file that prints; "preview" is the assembled picture, which is drawn
	// for the customer and never put on a bed.
	role  string
	parts []part
}

// roleOf is a scheme's design role, defaulting to the one that prints.
func (s scheme) roleOf() string {
	if s.role == "" {
		return designRole
	}
	return s.role
}

type part struct {
	name string
	// colour is the shop's word for it, resolved against this shop's spools.
	// Empty means the colour on the order.
	colour string
}

// product is everything this command writes for one registry product.
type product struct {
	code string
	// scadVariable is the template variable the customer's name fills.
	scadVariable string
	// maxLetters is how many characters this product's text box fits, and it
	// is WRITTEN, not decoration: it becomes product_field_maps.max_runes, so
	// the personalisation page refuses a longer name in the form rather than
	// in a render half a minute later.
	//
	// Read off the .scad's own text box. The lotus holder's is 100 x 30 mm
	// over two lines and its console says "about 16 fit"; the Royal's single
	// line of raised text takes twelve.
	maxLetters int
	schemes    []scheme
	// previewTemplate is the assembled file the personalisation link draws.
	// Empty means the product has none, and the picture stays a picture of
	// what prints.
	previewTemplate string
}

// pdhr is the arch plate: a coloured plate with raised text above it.
//
// arch_plate.scad draws PART="plate" and PART="text". The plate is whichever
// colour was bought; the text is gold on all three.
var pdhr = product{
	code: "PDHR", scadVariable: "TEXT", maxLetters: 12,
	schemes: []scheme{{
		parts: []part{
			{name: "plate"},
			{name: "text", colour: "gold"},
		},
	}},
}

// pwdh is the lotus holder: four pieces, and a palette chosen per colourway.
//
// The template is the shop's own complete lotus_diya_holder.scad, which draws
// PART="back", "outline", "text" and "diya" - the whole product, diya cup
// included, rather than the plate alone. The back is the colour bought in all
// three schemes - which alone would have fitted the old product-wide model -
// but the outline, the text and the cup are picked per variant and are nothing
// like the customer's choice, which is what per-variant schemes exist for.
//
// THE CUP TAKES THE OUTLINE'S COLOUR. Stated for the purple scheme by the shop
// (DIYA_COLOR and OUTLINE_COLOR are the same pink there) and carried to the
// other two, which is the only reading the one stated case supports. Worth
// confirming by eye on the red and the white before they print.
//
// THE CUP IS NOT PART OF THE PLATE. It is "diya_placed", the file's own name
// for the cup standing on the plate, and it is preview-only: the picture
// shows the product the customer receives, and the model that goes to the bed
// is the plate alone. The cup is a separate print - the .scad says so, and
// exposes it as PART="diya", floor down, for exactly that.
var pwdh = product{
	code: "PWDH", scadVariable: "TEXT", maxLetters: 16,
	schemes: []scheme{
		// No product-level scheme is written: every variant declares its own,
		// and a product scheme nothing reaches would be a fourth palette to
		// keep in step with the three that print.
		// What PRINTS: the plate, and nothing else. The diya cup is a
		// separate print that pushes onto it with two pegs - the .scad says
		// so, and exposes it as PART="diya", floor down, for exactly that.
		{variantSKU: "PWDH-P", parts: []part{
			{name: "back", colour: "purple"},
			{name: "outline", colour: "pink"},
			{name: "text", colour: "gold"},
		}},
		{variantSKU: "PWDH-R", parts: []part{
			{name: "back", colour: "red"},
			{name: "outline", colour: "gold"},
			{name: "text", colour: "yellow"},
		}},
		{variantSKU: "PWDH-W", parts: []part{
			{name: "back", colour: "white"},
			{name: "outline", colour: "gold"},
			{name: "text", colour: "red"},
		}},

		// What the CUSTOMER SEES: the same three pieces with the cup standing
		// on the plate, which is the product they will receive.
		//
		// The same template as the plate, because this one .scad happens to
		// draw both - PART="diya_placed" is the cup in position. A product
		// whose assembled form needs geometry its print file does not have,
		// like the Royal with its foot, points this role at a second file
		// instead. Either way the picture is built from the SAME field
		// mapping as the plate, so the two cannot carry different names.
		{variantSKU: "PWDH-P", role: previewRole, parts: []part{
			{name: "back", colour: "purple"},
			{name: "outline", colour: "pink"},
			{name: "text", colour: "gold"},
			{name: "diya_placed", colour: "pink"},
		}},
		{variantSKU: "PWDH-R", role: previewRole, parts: []part{
			{name: "back", colour: "red"},
			{name: "outline", colour: "gold"},
			{name: "text", colour: "yellow"},
			{name: "diya_placed", colour: "gold"},
		}},
		{variantSKU: "PWDH-W", role: previewRole, parts: []part{
			{name: "back", colour: "white"},
			{name: "outline", colour: "gold"},
			{name: "text", colour: "red"},
			{name: "diya_placed", colour: "gold"},
		}},
	},
	// The assembled file, per variant. The same key as the plate here; a
	// product needing a different file names a different key.
	previewTemplate: "pwdh",
}

var products = []product{pdhr, pwdh}

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

	for _, p := range products {
		if err := seed(ctx, store.Q, p, *dry); err != nil {
			log.Fatalf("%s: %v", p.code, err)
		}
	}
	if *dry {
		fmt.Println("\nnothing written (dry run)")
	}
}

func seed(ctx context.Context, q *gen.Queries, p product, dry bool) error {
	row, err := q.GetProductByCode(ctx, p.code)
	if err != nil {
		return fmt.Errorf("not in the registry - import it from Shopify first: %w", err)
	}
	fmt.Printf("\n%s - %s\n", p.code, row.Name)

	// The mapping first, because it is the switch: a product that maps no
	// fields renders through the Go path, and one that maps any renders from
	// the registry. Colours configured on a product nothing routes here would
	// sit unused.
	key := personalise.NormaliseKey(nameField)
	fmt.Printf("  field mapping: %q -> %s (string, required, max %d characters)\n",
		key, p.scadVariable, p.maxLetters)
	if !dry {
		if err := writeFieldMap(ctx, q, row.ID, key, p.scadVariable, p.maxLetters); err != nil {
			return err
		}
	}

	for _, sc := range p.schemes {
		if err := seedScheme(ctx, q, row.ID, sc, dry); err != nil {
			return err
		}
	}

	if p.previewTemplate != "" {
		if err := seedPreviewDesign(ctx, q, row.ID, p.previewTemplate, dry); err != nil {
			return err
		}
	}
	return nil
}

// seedPreviewDesign points every variant at the assembled file.
//
// Per variant because variant_designs is per variant, and all of them because
// these three colourways are one shape: the picture differs only in colour,
// which the preview scheme already says. A product whose colourways are
// genuinely different shapes gets a different key per variant, which
// previewTemplateFor reads before it falls back to the product's.
func seedPreviewDesign(
	ctx context.Context, q *gen.Queries, productID uuid.UUID, template string, dry bool,
) error {
	variants, err := q.ListVariantsForProduct(ctx, productID)
	if err != nil {
		return fmt.Errorf("read the variants: %w", err)
	}
	fmt.Printf("  personalisation preview: %s (drawn for the customer, never printed)\n", template)
	if dry {
		return nil
	}
	key := template
	for i := range variants {
		if variants[i].Status == "retired" {
			continue
		}
		// Replaced rather than added to: a second active preview row for one
		// variant would make previewTemplateFor answer whichever the database
		// returned first.
		if err := q.SupersedeVariantDesign(ctx, gen.SupersedeVariantDesignParams{
			VariantID: variants[i].ID, Role: previewRole,
		}); err != nil {
			return fmt.Errorf("clear the preview design for %v: %w", variants[i].Sku, err)
		}
		if _, err := q.InsertVariantDesign(ctx, gen.InsertVariantDesignParams{
			ID: uuid.New(), VariantID: variants[i].ID, Role: previewRole,
			TemplateKey: &key, Version: 1, Status: "active",
		}); err != nil {
			return fmt.Errorf("write the preview design for %v: %w", variants[i].Sku, err)
		}
	}
	return nil
}

// writeFieldMap replaces this product's mapping for the body role.
//
// Required rather than optional, and with no default: a diya holder with no
// name on it is not the product somebody bought, so an order that fails to
// carry one should hold for a person rather than print blank.
func writeFieldMap(
	ctx context.Context, q *gen.Queries, productID uuid.UUID,
	key, variable string, maxLetters int,
) error {
	if err := q.ClearProductFieldMaps(ctx, gen.ClearProductFieldMapsParams{
		ProductID: productID, Role: designRole,
	}); err != nil {
		return fmt.Errorf("clear the field mapping: %w", err)
	}
	var maxRunes *int32
	if maxLetters > 0 {
		v := int32(maxLetters)
		maxRunes = &v
	}
	if err := q.InsertProductFieldMap(ctx, gen.InsertProductFieldMapParams{
		ID: uuid.New(), ProductID: productID, Role: designRole,
		PropertyKey: key, ScadVariable: variable,
		ValueType: "string", Required: true, Position: 0,
		MaxRunes: maxRunes,
	}); err != nil {
		return fmt.Errorf("write the field mapping: %w", err)
	}
	return nil
}

func seedScheme(ctx context.Context, q *gen.Queries, productID uuid.UUID, sc scheme, dry bool) error {
	var variantID *uuid.UUID
	label := "every variant (the product's own scheme)"
	if sc.variantSKU != "" {
		variant, err := q.FindVariantBySKU(ctx, sc.variantSKU)
		if err != nil {
			return fmt.Errorf("no active variant %s - re-import from Shopify: %w", sc.variantSKU, err)
		}
		if variant.ProductID != productID {
			return fmt.Errorf("%s belongs to another product", sc.variantSKU)
		}
		variantID = &variant.ID
		label = sc.variantSKU
	}

	// Resolved BEFORE anything is cleared, so a colour the shop cannot print
	// leaves the previous scheme standing rather than half-replacing it.
	hexes := make([]string, len(sc.parts))
	for i, pt := range sc.parts {
		if pt.colour == "" {
			continue
		}
		hex, err := httpapi.ResolveColourHex(ctx, q, pt.colour)
		if err != nil {
			return fmt.Errorf("no filament matches %q for the piece %q: %w", pt.colour, pt.name, err)
		}
		hexes[i] = hex
	}

	fmt.Printf("  %s colours for %s:\n", sc.roleOf(), label)
	for i, pt := range sc.parts {
		if pt.colour == "" {
			fmt.Printf("    %-9s the colour the customer chose\n", pt.name)
			continue
		}
		fmt.Printf("    %-9s %-7s %s\n", pt.name, pt.colour, hexes[i])
	}
	if dry {
		return nil
	}

	if err := q.ClearDesignColourParts(ctx, gen.ClearDesignColourPartsParams{
		ProductID: productID, Role: sc.roleOf(), VariantID: variantID,
	}); err != nil {
		return fmt.Errorf("clear the colour parts for %s: %w", label, err)
	}
	for i, pt := range sc.parts {
		var hex *string
		if hexes[i] != "" {
			v := hexes[i]
			hex = &v
		}
		if _, err := q.InsertDesignColourPart(ctx, gen.InsertDesignColourPartParams{
			ID: uuid.New(), ProductID: productID, Role: sc.roleOf(),
			PartName: pt.name, ColourHex: hex, Position: int32(i),
			VariantID: variantID,
		}); err != nil {
			return fmt.Errorf("write the piece %q for %s: %w", pt.name, label, err)
		}
	}
	return nil
}
