// Command scadorders renders real orders with whatever OpenSCAD it is given,
// to local files.
//
// The point is to see what a renderer change does to ACTUAL customer names
// rather than to invented ones: scadcompare proved the geometry moves, this
// shows what it moves on. Real names are the test that matters, because the
// templates auto-fit from a glyph table and the cases that bite are the long
// ones nobody would think to type.
//
// Writes to a DIRECTORY, never to object storage and never to the database. The
// production path puts a model at generated/{job_id} in a shared S3 bucket, and
// regenerating there would replace the file a printer prints from with one from
// a renderer nobody has approved. That is the whole reason this command exists
// separately rather than as a flag on cmd/rerender.
//
//	go run ./cmd/scadorders -n 10 -out ./out
//	go run ./cmd/scadorders -n 10 -match "photo frame" -out ./out
//
// Throwaway, like cmd/scadcompare: it answers a one-off question and should go
// when the question is settled.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/joho/godotenv"

	"github.com/Optiminastic/tensor-core/internal/config"
	"github.com/Optiminastic/tensor-core/internal/db"
	"github.com/Optiminastic/tensor-core/internal/meshio"
	"github.com/Optiminastic/tensor-core/internal/orientation"
	"github.com/Optiminastic/tensor-core/internal/personalise"
	"github.com/Optiminastic/tensor-core/internal/production"
)

// plank is one job to render, read straight from the row.
type plank struct {
	JobNumber   string
	OrderNumber string
	SKU         string
	ProductName string
	Material    string
	ColourHex   string
	ColourName  string
	Props       []production.LineProp
}

func main() {
	n := flag.Int("n", 10, "how many of the most recent personalised jobs to render")
	match := flag.String("match", "",
		`only jobs whose SKU or product name contains this, case-insensitive (e.g. "photo frame")`)
	out := flag.String("out", "./scadorders-out", "directory to write the models into")
	timeout := flag.Duration("timeout", 4*time.Minute, "per-render timeout")
	flag.Parse()

	_ = godotenv.Load("env/local.env")
	cfg := config.Load()
	ctx := context.Background()

	r := personalise.NewRenderer(cfg.OpenSCADBin, cfg.OpenSCADAssetDir, *timeout)
	if !r.Available() {
		log.Fatalf("no OpenSCAD at %q; set OPENSCAD_BIN", cfg.OpenSCADBin)
	}
	log.Printf("renderer: %s", cfg.OpenSCADBin)

	store, err := db.Open(ctx, cfg.DatabaseURL, db.Options{})
	if err != nil {
		log.Fatalf("connect to database: %v", err)
	}
	defer store.Close()

	planks, err := recentPlanks(ctx, store, *n, *match)
	if err != nil {
		log.Fatalf("read jobs: %v", err)
	}
	if len(planks) == 0 {
		log.Fatal("no personalised jobs carry line properties; nothing to render")
	}
	if err := os.MkdirAll(*out, 0o755); err != nil {
		log.Fatalf("create output directory: %v", err)
	}

	var ok, failed int
	for _, p := range planks {
		if err := renderOne(ctx, r, p, *out); err != nil {
			failed++
			log.Printf("  %-18s %-28s FAILED: %v", p.JobNumber, p.ProductName, err)
			continue
		}
		ok++
	}
	log.Printf("\n%d rendered, %d failed, written to %s", ok, failed, *out)
	log.Printf("nothing was written to S3 or to the database")
}

// renderOne reproduces renderColouredPlank: two renders, then one 3MF carrying
// both parts. Deliberately the same shape as production rather than a
// shortcut - a harness that assembled the model differently would not be
// showing what the pipeline would produce.
func renderOne(ctx context.Context, r *personalise.Renderer, p plank, outDir string) error {
	params, err := personalise.ParamsFromProperties(p.Props)
	if err != nil {
		return fmt.Errorf("read personalisation: %w", err)
	}
	params = params.ForProduct(p.SKU, p.ProductName)

	base, err := r.RenderSTL(ctx, params.Template, params.ArgsForPart(personalise.PartBase))
	if err != nil {
		return fmt.Errorf("render the base: %w", err)
	}
	text, err := r.RenderSTL(ctx, params.Template, params.ArgsForPart(personalise.PartText))
	if err != nil {
		return fmt.Errorf("render the lettering: %w", err)
	}

	baseMesh, err := meshFromSTL(base)
	if err != nil {
		return fmt.Errorf("read the base: %w", err)
	}
	textMesh, err := meshFromSTL(text)
	if err != nil {
		return fmt.Errorf("read the lettering: %w", err)
	}

	// basePlateColour, matching httpapi's own constant. Named here rather than
	// imported so this command does not pull a whole HTTP package in for one
	// string.
	const basePlateColour = "#FFFFFF"
	hex := p.ColourHex
	if hex == "" {
		// The row carries no confirmed hex. The geometry is what is being
		// inspected and a material entry cannot change it, so this renders
		// rather than refuses - but the filename says so, because a model that
		// silently invented a colour is exactly the failure mode the
		// production path refuses outright.
		hex = "#FF00FF"
	}
	model, err := meshio.Write3MF([]meshio.Part{
		{Name: "Plate", Colour: basePlateColour, Material: p.Material, Triangles: baseMesh},
		{Name: "Lettering", Colour: hex, Material: p.Material, Triangles: textMesh},
	})
	if err != nil {
		return fmt.Errorf("assemble the coloured model: %w", err)
	}

	name := fmt.Sprintf("%s_%s_%s_%s", safe(p.OrderNumber), safe(p.JobNumber),
		params.Template, safe(p.ColourName))
	if p.ColourHex == "" {
		name += "_NOCOLOUR"
	}
	path := filepath.Join(outDir, name+".3mf")
	if err := os.WriteFile(path, model, 0o644); err != nil {
		return err
	}
	log.Printf("  %-18s %-28s %-20s %q + %q -> %s",
		p.JobNumber, truncate(p.ProductName, 28), params.Template,
		params.NameLeft, params.NameRight, filepath.Base(path))
	return nil
}

// meshFromSTL parses rendered STL bytes. production's own helper goes via a
// temp file and orientation.LoadModel; LoadSTL is what that ends up calling, so
// this skips the file without changing the parser.
func meshFromSTL(data []byte) ([]orientation.Triangle, error) {
	mesh, err := orientation.LoadSTL(data)
	if err != nil {
		return nil, err
	}
	if len(mesh.Triangles) == 0 {
		return nil, fmt.Errorf("no triangles")
	}
	return mesh.Triangles, nil
}

// recentPlanks reads the most recent jobs that carry their line properties.
//
// personalisation_properties, not the order's items: the importer snapshots each line onto
// the job it creates precisely so a job shows what it was made from, and the
// order-level scan cannot tell five planks of one SKU apart.
func recentPlanks(ctx context.Context, store *db.Store, n int, match string) ([]plank, error) {
	where := "j.personalisation_properties IS NOT NULL"
	args := []any{n}
	if match != "" {
		where += " AND (lower(coalesce(j.sku,'')) LIKE lower($2)" +
			" OR lower(coalesce(j.product_name,'')) LIKE lower($2))"
		args = append(args, "%"+match+"%")
	}
	rows, err := store.Pool.Query(ctx, `
        SELECT j.job_number, coalesce(o.order_number, ''), coalesce(j.sku, ''),
               coalesce(j.product_name, ''), coalesce(j.material, 'PLA'),
               coalesce(cm.hex, ''), coalesce(j.colour, ''), j.personalisation_properties
        FROM production_jobs j
        LEFT JOIN orders o ON o.id = j.order_id
        -- The job stores a colour NAME; the hex lives in the colour map, which
        -- is the same table resolveColourHex consults. is_primary first so a
        -- colour confirmed against several spools resolves the same way twice.
        LEFT JOIN LATERAL (
            SELECT hex FROM colour_map
            WHERE upper(colour_name) = upper(coalesce(j.colour, ''))
            ORDER BY is_primary DESC, hex
            LIMIT 1
        ) cm ON true
        WHERE `+where+`
        ORDER BY j.created_at DESC
        LIMIT $1`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []plank
	for rows.Next() {
		var p plank
		var props []byte
		if err := rows.Scan(&p.JobNumber, &p.OrderNumber, &p.SKU,
			&p.ProductName, &p.Material, &p.ColourHex, &p.ColourName, &props); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(props, &p.Props); err != nil {
			return nil, fmt.Errorf("%s: read line properties: %w", p.JobNumber, err)
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func safe(s string) string {
	if s == "" {
		return "unknown"
	}
	return strings.Map(func(r rune) rune {
		if strings.ContainsRune(`\/:*?"<>| `, r) {
			return '-'
		}
		return r
	}, s)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}
