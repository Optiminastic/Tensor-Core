package httpapi

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"github.com/google/uuid"

	"github.com/Optiminastic/tensor-core/internal/db/gen"
	"github.com/Optiminastic/tensor-core/internal/obs"
	"github.com/Optiminastic/tensor-core/internal/production"
)

// applyPersonalisationRules re-validates a job's personalisation against its
// product's rules and overrides the status, per-field confirmations, and notes.
// Malformed rules are ignored (the generic result stays), so a bad rule blob can
// never block order fulfilment.
func applyPersonalisationRules(
	p *gen.InsertProductionJobParams, rulesJSON []byte, li production.LineItem,
) {
	var rules production.PersonalisationRules
	if err := json.Unmarshal(rulesJSON, &rules); err != nil {
		return
	}
	status, confirms, issues := production.ValidateAgainstRules(rules, li)
	p.PersonalisationStatus = status
	p.NameConfirmed = confirms.Name
	p.PhotoConfirmed = confirms.Photo
	p.FontConfirmed = confirms.Font
	p.ColourConfirmed = confirms.Colour
	p.VariantConfirmed = confirms.Variant
	p.CustomerApprovalReceived = confirms.Approval
	if len(issues) > 0 {
		p.PersonalisationNotes = mergeNote(p.PersonalisationNotes, strings.Join(issues, "; "))
	}
}

// mergeNote appends an issue string to any existing personalisation note.
func mergeNote(existing *string, add string) *string {
	if existing != nil && strings.TrimSpace(*existing) != "" {
		merged := strings.TrimSpace(*existing) + " | " + add
		return &merged
	}
	return &add
}

// ensureTemplateFile returns the file_asset that stands in for the design's model
// in the production queue, creating it on first use from the design's stl_key
// (no copy - the file_asset points at the same storage object) and caching it on
// the design so every reprint reuses it.
func (s *Server) ensureTemplateFile(ctx context.Context, design gen.GetDesignBySkuRow) (uuid.UUID, error) {
	if design.TemplateFileID != nil {
		// Self-heal: templates minted before models were measured carry no
		// bounding box, and the batch planner rejects any job whose footprint is
		// zero. Repairing on read means existing designs recover without anyone
		// re-uploading them.
		s.ensureFileMeasured(ctx, *design.TemplateFileID, design.StlKey)
		return *design.TemplateFileID, nil
	}

	fileID := uuid.New()
	size, bx, by, bz := s.measureModel(ctx, design.StlKey)

	if _, err := s.store.Q.InsertFileAsset(ctx, gen.InsertFileAssetParams{
		ID:          fileID,
		Filename:    templateFilename(design.Name, design.StlKey),
		ContentType: modelContentType(design.StlKey),
		SizeBytes:   size,
		StorageKey:  design.StlKey,
		IsTemplate:  true,
		UploadedBy:  "system",
		// Measured, not just sized. Without these three numbers every job built
		// from this design is unbatchable ("No print file with measurable STL
		// dimensions"), which strands every order for it.
		BboxXMm: bx, BboxYMm: by, BboxZMm: bz,
	}); err != nil {
		return uuid.Nil, err
	}
	if err := s.store.Q.SetDesignTemplateFile(ctx, gen.SetDesignTemplateFileParams{
		ID: design.ID, TemplateFileID: &fileID,
	}); err != nil {
		return uuid.Nil, err
	}
	return fileID, nil
}

// templateFilename is a download-friendly name for a design's template model,
// keeping the model's own extension so it opens as what it is.
func templateFilename(designName, stlKey string) string {
	return safeName(designName) + strings.ToLower(filepath.Ext(stlKey))
}

// modelContentType maps a model object key to its MIME type; unknown extensions
// fall back to the generic octet-stream.
func modelContentType(stlKey string) string {
	switch strings.ToLower(filepath.Ext(stlKey)) {
	case ".stl":
		return "model/stl"
	case ".3mf":
		return "model/3mf"
	case ".step", ".stp":
		return "application/step"
	default:
		return "application/octet-stream"
	}
}

// measureModel downloads a stored model and returns its size and bounding box.
// Everything is best-effort: a model that cannot be fetched or parsed yields
// zero size and nil dimensions rather than failing job creation, because an
// unmeasured job is recoverable (see ensureFileMeasured) while a rejected order
// is not.
func (s *Server) measureModel(ctx context.Context, storageKey string) (size int64, x, y, z *float64) {
	if s.storage == nil || storageKey == "" {
		return 0, nil, nil, nil
	}
	ext := strings.ToLower(filepath.Ext(storageKey))
	tmp, err := os.CreateTemp("", "measure-*"+ext)
	if err != nil {
		return 0, nil, nil, nil
	}
	path := tmp.Name()
	_ = tmp.Close()
	defer func() { _ = os.Remove(path) }()

	if err := s.storage.Download(ctx, storageKey, path); err != nil {
		obs.FromContext(ctx).Warn("could not download model to measure", "key", storageKey, "error", err)
		return 0, nil, nil, nil
	}
	if info, err := os.Stat(path); err == nil {
		size = info.Size()
	}
	x, y, z = modelBBox(path, ext)
	return size, x, y, z
}

// ensureFileMeasured backfills a file asset's bounding box when it has none.
// A no-op once measured, so it is safe to call on every read path.
func (s *Server) ensureFileMeasured(ctx context.Context, fileID uuid.UUID, storageKey string) {
	file, err := s.store.Q.GetFileAsset(ctx, fileID)
	if err != nil {
		return
	}
	if file.BboxXMm.Valid && file.BboxYMm.Valid && file.BboxZMm.Valid {
		return
	}
	key := file.StorageKey
	if key == "" {
		key = storageKey
	}
	_, x, y, z := s.measureModel(ctx, key)
	if x == nil || y == nil || z == nil {
		return
	}
	if err := s.store.Q.UpdateFileAssetBBox(ctx, gen.UpdateFileAssetBBoxParams{
		ID: fileID, BboxXMm: x, BboxYMm: y, BboxZMm: z,
	}); err != nil {
		obs.FromContext(ctx).Warn("could not backfill model dimensions", "file", fileID, "error", err)
		return
	}
	obs.FromContext(ctx).Info("measured model", "file", fileID, "x", *x, "y", *y, "z", *z)
}
