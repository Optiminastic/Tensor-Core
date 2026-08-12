package httpapi

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"

	"github.com/google/uuid"

	"github.com/Optiminastic/tensor-core/internal/db/gen"
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
		return *design.TemplateFileID, nil
	}

	fileID := uuid.New()
	var size int64
	if s.storage != nil {
		if obj, err := s.storage.Get(ctx, design.StlKey); err == nil {
			size = obj.Size
			_ = obj.Body.Close()
		}
	}

	if _, err := s.store.Q.InsertFileAsset(ctx, gen.InsertFileAssetParams{
		ID:          fileID,
		Filename:    templateFilename(design.Name, design.StlKey),
		ContentType: modelContentType(design.StlKey),
		SizeBytes:   size,
		StorageKey:  design.StlKey,
		IsTemplate:  true,
		UploadedBy:  "system",
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
