package main

import (
	"context"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"unicode"

	"github.com/google/uuid"

	"github.com/Optiminastic/tensor-core/internal/db"
	"github.com/Optiminastic/tensor-core/internal/db/gen"
	"github.com/Optiminastic/tensor-core/internal/orientation"
	"github.com/Optiminastic/tensor-core/internal/production"
	"github.com/Optiminastic/tensor-core/internal/slicing"
	"github.com/Optiminastic/tensor-core/internal/storage"
)

var allowedModelExt = map[string]bool{".stl": true, ".3mf": true}

// colours rotates across new designs. Material is deliberately uniform (see
// designMaterial), so colour and quality are the axes that actually split
// designs into different beds.
var colours = []string{"Black", "White", "Red", "Blue", "Grey", "Orange"}

// designMaterial is PLA for every product. "PLA Basics" is the exact string the
// slicer profile table keys on (internal/slicing/profiles.go#materialFilament);
// a bare "PLA" resolves to no filament preset and the design can never slice.
const designMaterial = "PLA Basics"

// Quality is the one configuration axis deliberately varied. It is part of the
// batching group key (material + nozzles + quality), so a minority on a
// different quality produces a second machine-profile group to schedule
// against, while the majority stay compatible enough to actually fill beds.
//
// One in five, not half: the point of the fleet is to consolidate, and an even
// split would leave both groups too thin to reach the utilisation target.
const (
	majorityQuality = "0.20-standard"
	minorityQuality = "0.16-high"
	minorityEveryN  = 5
)

type seededDesign struct {
	ID   uuid.UUID
	Name string
	SKU  string
	// Unit dimensions in mm, measured from the model itself.
	X, Y, Z float64
	Minutes int
}

func modelFiles(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if allowedModelExt[strings.ToLower(filepath.Ext(e.Name()))] {
			out = append(out, filepath.Join(dir, e.Name()))
		}
	}
	return out, nil
}

// unitBounds measures ONE printable unit of a model.
//
// For a 3MF this is the largest build item, not the merged box of the whole
// file. Slicers export a project - the whole plate - so a file holding four
// arranged copies measures 428mm across, which fits no bed and describes no
// product. Each build item is one unit; the largest is the product.
func unitBounds(path string) (x, y, z float64, err error) {
	ext := strings.ToLower(filepath.Ext(path))
	if ext == ".3mf" {
		items, err := orientation.Load3MFItems(path)
		if err != nil {
			return 0, 0, 0, err
		}
		var best float64
		for _, it := range items {
			ix, iy, iz := it.Size()
			if v := ix * iy * iz; v > best {
				x, y, z, best = ix, iy, iz, v
			}
		}
		if best <= 0 {
			return 0, 0, 0, fmt.Errorf("no measurable build item")
		}
		return x, y, z, nil
	}
	mesh, err := orientation.LoadModel(path, ext)
	if err != nil {
		return 0, 0, 0, err
	}
	return mesh.Max.X - mesh.Min.X, mesh.Max.Y - mesh.Min.Y, mesh.Max.Z - mesh.Min.Z, nil
}

// createDesigns imports each model file as an approved, priced design.
//
// Idempotent by SKU: a file whose SKU already has a design is skipped and the
// existing design reused, so re-running against the same folder adds nothing
// and breaks no existing product or the orders referencing it.
//
// The pricing path is the real one. Rather than enqueue a slice and wait for a
// FAKE_SLICE worker to fabricate the same ~25 minutes for every model, metrics
// are derived from the model's own measured geometry and handed to
// slicing.ProcessSliceResult - the exact function the slice worker calls. The
// design goes through the real costing engine and lands "priced" the same way.
func createDesigns(
	ctx context.Context, store *db.Store, objects *storage.Client, files []string, profiles []uuid.UUID,
) ([]seededDesign, error) {
	out := make([]seededDesign, 0, len(files))
	for i, path := range files {
		name := productName(filepath.Base(path))
		sku := skuFor(name)

		if existing, err := store.Q.GetDesignBySKU(ctx, &sku); err == nil {
			fmt.Printf("  = %-42s already imported (%s), reusing\n", name, sku)
			x, y, z, _ := unitBounds(path)
			out = append(out, seededDesign{
				ID: existing.ID, Name: existing.Name, SKU: sku, X: x, Y: y, Z: z,
				Minutes: production.DefaultPrintTimeMinutes(x, y, z),
			})
			continue
		}

		x, y, z, err := unitBounds(path)
		if err != nil {
			fmt.Printf("  ! %-42s unreadable, skipped: %v\n", name, err)
			continue
		}
		minutes := production.DefaultPrintTimeMinutes(x, y, z)

		ext := strings.ToLower(filepath.Ext(path))
		designID := uuid.New()
		modelKey := fmt.Sprintf("designs/%s/model%s", designID, ext)
		if err := uploadFile(ctx, objects, path, modelKey); err != nil {
			return nil, fmt.Errorf("upload %s: %w", path, err)
		}

		colour := colours[i%len(colours)]
		quality := majorityQuality
		if i%minorityEveryN == minorityEveryN-1 {
			quality = minorityQuality
		}
		profileID := profiles[i%len(profiles)]

		if err := store.InTx(ctx, func(q *gen.Queries) error {
			if _, err := q.InsertDesign(ctx, gen.InsertDesignParams{
				ID: designID, BrandSlug: brandSlug, Name: name, CreatedBy: createdBy,
				Status: "queued", StlKey: modelKey, Material: designMaterial, Colour: &colour,
				Finish: "none", UnitsPerBed: 1, Quality: quality, InfillPct: 15,
				MachineID: &profileID,
			}); err != nil {
				return fmt.Errorf("insert design: %w", err)
			}
			// The bounding box the job-creation worker would otherwise
			// re-derive by downloading this file. Pre-creating it keyed on the
			// same storage key means getOrCreateFileAssetForDesign finds it and
			// every job carries the model's real measured dimensions.
			if _, err := q.InsertFileAsset(ctx, gen.InsertFileAssetParams{
				ID: uuid.New(), Filename: filepath.Base(path),
				ContentType: "application/octet-stream", SizeBytes: fileSize(path),
				StorageKey: modelKey, IsTemplate: false, UploadedBy: createdBy,
				BboxXMm: &x, BboxYMm: &y, BboxZMm: &z,
			}); err != nil {
				return fmt.Errorf("insert file asset: %w", err)
			}
			return nil
		}); err != nil {
			return nil, fmt.Errorf("insert design for %s: %w", path, err)
		}

		if err := priceFromGeometry(ctx, store, designID, x, y, z, minutes, quality); err != nil {
			return nil, fmt.Errorf("price %s: %w", name, err)
		}

		s := sku
		if _, err := store.Q.UpdateDesignSku(ctx, gen.UpdateDesignSkuParams{ID: designID, Sku: &s}); err != nil {
			return nil, fmt.Errorf("assign sku for %s: %w", name, err)
		}
		if err := store.Q.UpdateDesignStatus(ctx, gen.UpdateDesignStatusParams{
			ID: designID, Status: "approved",
		}); err != nil {
			return nil, fmt.Errorf("approve %s: %w", name, err)
		}

		fmt.Printf("  + %-42s %6.1f x %6.1f x %6.1f mm  %4d min  %s  %s\n",
			name, x, y, z, minutes, quality, sku)
		out = append(out, seededDesign{ID: designID, Name: name, SKU: sku, X: x, Y: y, Z: z, Minutes: minutes})
	}
	return out, nil
}

// priceFromGeometry writes the design's slice metrics and runs the real costing
// engine, through the same entry point the slice worker uses.
//
// Filament is estimated from bounding-box volume at the same solid fraction the
// planner's own default print-time model assumes, so weight and time tell a
// consistent story rather than being two unrelated guesses.
func priceFromGeometry(
	ctx context.Context, store *db.Store, designID uuid.UUID, x, y, z float64, minutes int, quality string,
) error {
	const (
		solidFraction = 0.25
		plaDensity    = 1.24 // g/cm^3, matches slicing.materialFilament
	)
	volumeCM3 := x * y * z / 1000 * solidFraction
	grams := volumeCM3 * plaDensity

	layer := 0.20
	if lh := slicing.LayerHeightMM(quality); lh != nil {
		layer = *lh
	}

	jobID := uuid.New()
	if _, err := store.Q.InsertSliceJob(ctx, gen.InsertSliceJobParams{
		ID: jobID, DesignID: designID, Status: "queued", Attempt: 1,
	}); err != nil {
		return fmt.Errorf("insert slice job: %w", err)
	}

	hours := float64(minutes) / 60
	return slicing.ProcessSliceResult(ctx, store, jobID, designID, brandSlug, slicing.PerUnitMetrics{
		PrintTimeHr: hours, EffectiveMachineTimeHr: hours, FilamentG: math.Round(grams*100) / 100,
		UnitsPerBed: 1, LayerHeightMm: layer, InfillDensityPct: 15, WallLoops: 2,
		FilamentLengthMm: grams / plaDensity * 1000 / 2.405, // 1.75mm filament cross-section
	})
}

func uploadFile(ctx context.Context, objects *storage.Client, path, key string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		return err
	}
	return objects.Put(ctx, key, f, info.Size(), "application/octet-stream")
}

func fileSize(path string) int64 {
	info, err := os.Stat(path)
	if err != nil {
		return 0
	}
	return info.Size()
}

// machineProfiles returns the printer profiles designs are spread across, most
// compatible first. A single profile is fine - quality still splits the groups.
func machineProfiles(ctx context.Context, store *db.Store) ([]uuid.UUID, error) {
	rows, err := store.Q.ListMachineProfilesFull(ctx)
	if err != nil {
		return nil, err
	}
	var out []uuid.UUID
	for _, p := range rows {
		if p.Family == "H2C" && p.Status == production.MachineOnline {
			out = append(out, p.ID)
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no online H2C machine profile found (run `go run ./cmd/seed` first)")
	}
	return out, nil
}

// productName turns a filename into a display name: strip the extension,
// replace separators with spaces, collapse whitespace, and title-case it.
func productName(filename string) string {
	base := strings.TrimSuffix(filename, filepath.Ext(filename))
	base = strings.Map(func(r rune) rune {
		if r == '_' || r == '-' {
			return ' '
		}
		return r
	}, base)
	words := strings.Fields(base)
	for i, w := range words {
		words[i] = titleCase(w)
	}
	name := strings.Join(words, " ")
	if name == "" {
		name = "Untitled model"
	}
	return name
}

func titleCase(word string) string {
	r := []rune(word)
	if len(r) == 0 {
		return word
	}
	r[0] = unicode.ToUpper(r[0])
	for i := 1; i < len(r); i++ {
		r[i] = unicode.ToLower(r[i])
	}
	return string(r)
}

// skuFor slugs a design name into a SKU. Derived purely from the name, with no
// run index: that is what makes re-importing the same folder find the existing
// design instead of minting a duplicate.
func skuFor(name string) string {
	var b strings.Builder
	for _, r := range strings.ToUpper(name) {
		switch {
		case r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == ' ':
			b.WriteRune('-')
		}
	}
	slug := strings.Trim(b.String(), "-")
	for strings.Contains(slug, "--") {
		slug = strings.ReplaceAll(slug, "--", "-")
	}
	if len(slug) > 60 {
		slug = strings.Trim(slug[:60], "-")
	}
	if slug == "" {
		slug = "MODEL"
	}
	return slug
}
