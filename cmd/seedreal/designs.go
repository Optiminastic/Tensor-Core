package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Optiminastic/tensor-core/internal/db"
	"github.com/Optiminastic/tensor-core/internal/db/gen"
	"github.com/Optiminastic/tensor-core/internal/slicing"
	"github.com/Optiminastic/tensor-core/internal/storage"
)

var allowedModelExt = map[string]bool{".stl": true, ".3mf": true}

// colours rotates across new designs for realistic variety - everything else
// (material mostly, quality, nozzle config) is shared, so colour is the axis
// that actually splits designs into different batches.
var colours = []string{"Black", "White", "Red", "Blue", "Grey", "Orange"}

type seededDesign struct {
	ID   uuid.UUID
	Name string
	SKU  string
}

func stlFiles(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		ext := strings.ToLower(filepath.Ext(e.Name()))
		if allowedModelExt[ext] {
			out = append(out, filepath.Join(dir, e.Name()))
		}
	}
	return out, nil
}

// createDesigns uploads each file to object storage and inserts a design +
// slice job + River slice enqueue, exactly like a real upload (POST
// /designs) does - see internal/httpapi/designs_create.go#insertDesignAndJob.
func createDesigns(
	ctx context.Context, store *db.Store, objects *storage.Client, files []string, machineID uuid.UUID,
) ([]seededDesign, error) {
	riverClient, err := slicing.NewInsertOnlyClient(store.Pool)
	if err != nil {
		return nil, fmt.Errorf("build river client: %w", err)
	}
	enqueuer := slicing.NewEnqueuer(riverClient)

	out := make([]seededDesign, 0, len(files))
	for i, path := range files {
		name := productName(filepath.Base(path))
		sku := skuFor(name, i+1)
		ext := strings.ToLower(filepath.Ext(path))
		designID := uuid.New()
		stlKey := fmt.Sprintf("designs/%s/model%s", designID, ext)

		if err := uploadFile(ctx, objects, path, stlKey); err != nil {
			return nil, fmt.Errorf("upload %s: %w", path, err)
		}

		colour := colours[i%len(colours)]
		material := "PLA Basics"
		if i%4 == 3 {
			material = "PA-CF"
		}
		const quality = "0.20-standard"

		jobID := uuid.New()
		err = store.InTxWith(ctx, func(q *gen.Queries, tx pgx.Tx) error {
			if _, err := q.InsertDesign(ctx, gen.InsertDesignParams{
				ID: designID, BrandSlug: brandSlug, Name: name, CreatedBy: createdBy,
				Status: "queued", StlKey: stlKey, Material: material, Colour: &colour,
				Finish: "none", UnitsPerBed: 1, Quality: quality, InfillPct: 15,
				MachineID: &machineID,
			}); err != nil {
				return fmt.Errorf("insert design: %w", err)
			}
			if _, err := q.InsertSliceJob(ctx, gen.InsertSliceJobParams{
				ID: jobID, DesignID: designID, Status: "queued", Attempt: 1,
			}); err != nil {
				return fmt.Errorf("insert slice job: %w", err)
			}
			return enqueuer.EnqueueTx(ctx, tx, slicing.SliceArgs{
				JobID: jobID, DesignID: designID, BrandSlug: brandSlug, StlKey: stlKey,
				Material: material, Quality: quality, UnitsPerBed: 1, InfillPct: 15,
			})
		})
		if err != nil {
			return nil, fmt.Errorf("insert design for %s: %w", path, err)
		}
		out = append(out, seededDesign{ID: designID, Name: name, SKU: sku})
	}
	return out, nil
}

func uploadFile(ctx context.Context, objects *storage.Client, path, key string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return err
	}
	return objects.Put(ctx, key, f, info.Size(), "application/octet-stream")
}

// waitForSlicing polls until every design leaves queued/slicing, returning
// only the ones that reached "priced" (a "failed" design is skipped, logged,
// and never gets a SKU or an order referencing it).
func waitForSlicing(ctx context.Context, store *db.Store, designs []seededDesign, timeout time.Duration) ([]seededDesign, error) {
	deadline := time.Now().Add(timeout)
	pending := make(map[uuid.UUID]seededDesign, len(designs))
	for _, d := range designs {
		pending[d.ID] = d
	}
	var done []seededDesign

	for len(pending) > 0 {
		if time.Now().After(deadline) {
			for _, d := range pending {
				fmt.Printf("  timed out waiting for design %q (%s) to slice\n", d.Name, d.ID)
			}
			break
		}
		for id, d := range pending {
			row, err := store.Q.GetDesignByID(ctx, id)
			if err != nil {
				return nil, fmt.Errorf("check design %s: %w", id, err)
			}
			switch row.Status {
			case "priced", "approved", "published":
				done = append(done, d)
				delete(pending, id)
			case "failed":
				fmt.Printf("  design %q (%s) failed to slice\n", d.Name, id)
				delete(pending, id)
			}
		}
		if len(pending) > 0 {
			time.Sleep(2 * time.Second)
		}
	}
	return done, nil
}

func approveDesigns(ctx context.Context, store *db.Store, designs []seededDesign) error {
	for _, d := range designs {
		sku := d.SKU
		if _, err := store.Q.UpdateDesignSku(ctx, gen.UpdateDesignSkuParams{ID: d.ID, Sku: &sku}); err != nil {
			return fmt.Errorf("assign sku for %s: %w", d.Name, err)
		}
		if err := store.Q.UpdateDesignStatus(ctx, gen.UpdateDesignStatusParams{ID: d.ID, Status: "approved"}); err != nil {
			return fmt.Errorf("approve %s: %w", d.Name, err)
		}
	}
	return nil
}

func findH2CProfile(ctx context.Context, store *db.Store) (uuid.UUID, error) {
	rightNozzle := 0.4
	rightFlow := "standard"
	p, err := store.Q.FindMachineProfileByNozzleFlow(ctx, gen.FindMachineProfileByNozzleFlowParams{
		Family: "H2C", NozzleMm: 0.4, RightNozzleMm: &rightNozzle, Flow: "standard", RightFlow: &rightFlow,
	})
	if err != nil {
		return uuid.UUID{}, err
	}
	return p.ID, nil
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

// skuFor slugs a design name into a SKU (letters/digits/hyphen only, <=64
// chars), suffixed with a 2-digit index for guaranteed uniqueness this run.
func skuFor(name string, index int) string {
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
	if len(slug) > 40 {
		slug = strings.Trim(slug[:40], "-")
	}
	if slug == "" {
		slug = "MODEL"
	}
	return fmt.Sprintf("%s-%02d", slug, index)
}
