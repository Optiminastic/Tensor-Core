package httpapi

import (
	"context"
	"os"
	"path/filepath"
	"strings"

	"github.com/google/uuid"

	"github.com/Optiminastic/tensor-core/internal/db"
	"github.com/Optiminastic/tensor-core/internal/db/gen"
	"github.com/Optiminastic/tensor-core/internal/production"
	"github.com/Optiminastic/tensor-core/internal/slicing"
)

// matchDesignForSKU is the Validation stage's SKU-matching step: it looks up
// an approved design by SKU and assembles the print facts a matched job
// should snapshot (material, colours, STL, printer profile, slice metrics).
// A nil Design comes back with a Reason from the Issue* taxonomy instead of
// an error - the caller creates the job anyway, flagged with that reason,
// never silently drops it.
func (s *Server) matchDesignForSKU(ctx context.Context, sku *string) production.MatchResult {
	if sku == nil || strings.TrimSpace(*sku) == "" {
		return production.MatchResult{Reason: production.IssueSKUMissing}
	}
	d, err := s.store.Q.GetDesignBySKU(ctx, sku)
	if err != nil {
		return production.MatchResult{Reason: production.IssueNoApprovedDesign}
	}

	fileID, bboxX, bboxY, bboxZ, ok := s.getOrCreateFileAssetForDesign(ctx, d)
	if !ok {
		return production.MatchResult{Reason: production.IssueSTLMissing}
	}

	facts := &production.DesignFacts{
		DesignID: d.ID, Material: d.Material, Colours: colourSetOf(d.Colour), PrintFileID: fileID,
		QualityMM: slicing.LayerHeightMM(d.Quality),
		BboxXMM:   bboxX, BboxYMM: bboxY, BboxZMM: bboxZ,
	}

	if m, err := s.store.Q.GetLatestMetricsForDesign(ctx, d.ID); err == nil {
		minutes := int(m.PrintTimeHr * 60)
		grams := m.FilamentG
		facts.PrintTimeMin, facts.FilamentGrams = &minutes, &grams
		facts.SupportWeightG, facts.PurgeWeightG = &m.SupportG, &m.PurgeG
		colourCount := int(m.ColourChanges)
		facts.ColourCount = &colourCount
		facts.SupportUsed, facts.InfillPct = &m.SupportUsed, &m.InfillDensityPct
	}

	if d.MachineID != nil {
		if p, err := s.store.Q.GetMachineProfileFull(ctx, *d.MachineID); err == nil {
			facts.LeftNozzleMM, facts.MachineFamily = &p.NozzleMm, p.Family
			facts.RightNozzleMM = db.NumFloatPtr(p.RightNozzleMm)
			flow := flowPercent(p.Flow)
			facts.FlowPct = &flow
		}
	}

	return production.MatchResult{Design: facts}
}

// colourSetOf wraps a design's single colour string into the one-element
// colours[] a job snapshots - designs don't yet have their own multi-colour
// concept (out of scope; see the plan's confirmed decisions).
func colourSetOf(colour *string) []string {
	if colour == nil || strings.TrimSpace(*colour) == "" {
		return []string{}
	}
	return []string{*colour}
}

// flowPercent converts machine_profiles.flow's categorical value into an
// approximate numeric percentage for the job snapshot. Bambu's "high flow"
// nozzles push roughly 30% more volume than standard - not a precise
// per-nozzle figure, but a reasonable placeholder until a real per-profile
// number is tracked.
func flowPercent(flow string) float64 {
	if flow == "high_flow" {
		return 130
	}
	return 100
}

// getOrCreateFileAssetForDesign resolves the file_assets row for a design's
// STL, downloading it and computing its bounding box on first use (an
// approved design is never re-created after upload, so the design's own
// stl_key is a stable cache key across jobs). Also surfaces the bbox it just
// measured or found cached, so matchDesignForSKU doesn't need a second
// lookup to snapshot it onto the job. Returns ok=false when the object can't
// be read or its bbox can't be measured.
func (s *Server) getOrCreateFileAssetForDesign(
	ctx context.Context, d gen.GetDesignBySKURow,
) (fileID *uuid.UUID, bboxX, bboxY, bboxZ *float64, ok bool) {
	if existing, err := s.store.Q.GetFileAssetByStorageKey(ctx, d.StlKey); err == nil {
		id := existing.ID
		return &id, db.NumFloatPtr(existing.BboxXMm), db.NumFloatPtr(existing.BboxYMm), db.NumFloatPtr(existing.BboxZMm), true
	}
	if s.storage == nil {
		return nil, nil, nil, nil, false
	}

	ext := filepath.Ext(d.StlKey)
	tmp, err := os.CreateTemp("", "tensor-design-*"+ext)
	if err != nil {
		return nil, nil, nil, nil, false
	}
	tmpPath := tmp.Name()
	_ = tmp.Close()
	defer func() { _ = os.Remove(tmpPath) }()

	if err := s.storage.Download(ctx, d.StlKey, tmpPath); err != nil {
		return nil, nil, nil, nil, false
	}
	bx, by, bz := modelBBox(tmpPath, ext)
	if bx == nil || by == nil || bz == nil {
		return nil, nil, nil, nil, false
	}

	row, err := s.store.Q.InsertFileAsset(ctx, gen.InsertFileAssetParams{
		ID: uuid.New(), Filename: filepath.Base(d.StlKey), ContentType: "application/octet-stream",
		SizeBytes: fileSize(tmpPath), StorageKey: d.StlKey, IsTemplate: false, UploadedBy: "system",
		BboxXMm: bx, BboxYMm: by, BboxZMm: bz,
	})
	if err != nil {
		return nil, nil, nil, nil, false
	}
	return &row.ID, bx, by, bz, true
}

func fileSize(path string) int64 {
	info, err := os.Stat(path)
	if err != nil {
		return 0
	}
	return info.Size()
}
