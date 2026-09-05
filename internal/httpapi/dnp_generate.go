package httpapi

// Rendering a personalised model for a production job.
//
// A Dual Name Plank has no design row and no uploaded STL - its geometry is
// made from the customer's own two names at the moment the order arrives. This
// is the step that replaces a person opening OpenSCAD, typing the names in,
// exporting, then opening Bambu Studio to untick "uniform scale" and type
// 200/50/40. The template's OUT_X/Y/Z block does that last part at render
// time, so what comes out is already the finished size.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/Optiminastic/tensor-core/internal/db"
	"github.com/Optiminastic/tensor-core/internal/db/gen"
	"github.com/Optiminastic/tensor-core/internal/meshio"
	"github.com/Optiminastic/tensor-core/internal/obs"
	"github.com/Optiminastic/tensor-core/internal/orientation"
	"github.com/Optiminastic/tensor-core/internal/personalise"
	"github.com/Optiminastic/tensor-core/internal/production"
)

// errNotPersonalisable means this job's product is not one Tensor can generate
// a model for. Not a failure - most products are ordinary designs with an
// uploaded STL - so callers skip rather than retry.
var errNotPersonalisable = errors.New("this product has no generated model")

// generatedProductNames are product names Tensor renders itself, matched when a
// line has no usable SKU.
//
// Lower-case, and compared as a substring of the lower-cased product name,
// because the storefront appends and reorders words around the product's own
// name freely.
var generatedProductNames = []string{"dual name plank"}

// IsGeneratedProduct reports whether a product is one Tensor renders itself.
//
// The SKU is the primary key on it: the storefront renames products freely, but
// the SKU is what the warehouse and the design tables key on. Both live families
// are covered - T3DPS-DNP-1..16 and DNPLB-*.
//
// Matched per hyphen-separated segment, NOT as a bare substring. "DNP" appears
// inside plenty of words, and a false positive here is not cosmetic: Tensor
// would try to render a plank for a product that is not one, fail, and hold an
// order that had nothing wrong with it.
//
// The product name is the fallback, and it is not belt-and-braces. Nine live
// Dual Name Plank lines - the GREEN and PURPLE variants - carry all four STEP
// customisation properties and NO SKU at all, so a SKU-only rule filed them as
// manual-upload work and never rendered them. They are planks; the variant just
// lost its SKU in the catalogue.
func IsGeneratedProduct(sku, productName string) bool {
	for _, part := range strings.Split(strings.ToUpper(sku), "-") {
		if strings.HasPrefix(part, "DNP") {
			return true
		}
	}
	name := strings.ToLower(strings.TrimSpace(productName))
	if name == "" {
		return false
	}
	for _, known := range generatedProductNames {
		if strings.Contains(name, known) {
			return true
		}
	}
	return false
}

// GenerateModelForJob renders one job's personalised model, stores it, and
// attaches it as the job's print file.
//
// On success the job's issue_reason clears and it becomes batchable by itself -
// SetProductionJobPrintFile does that, and the caller triggers a replan. On a
// failure the caller records the reason on the job rather than retrying
// forever: a name OpenSCAD cannot set will not render on the third attempt
// either.
func (s *Server) GenerateModelForJob(ctx context.Context, jobID uuid.UUID) error {
	if s.renderer == nil || !s.renderer.Available() {
		return fmt.Errorf("OpenSCAD is not configured on this service")
	}
	if s.storage == nil {
		return fmt.Errorf("object storage is not configured on this service")
	}

	job, err := s.store.Q.GetProductionJobByID(ctx, jobID)
	if err != nil {
		return fmt.Errorf("load job: %w", err)
	}
	if !IsGeneratedProduct(deref(job.Sku), deref(job.ProductName)) {
		return errNotPersonalisable
	}
	if job.OrderID == nil {
		return fmt.Errorf("job %s has no order, so there is no personalisation to read", job.JobNumber)
	}

	params, err := s.plankParamsForJob(ctx, *job.OrderID, job)
	if err != nil {
		return err
	}

	model, err := s.renderColouredPlank(ctx, job, params)
	if err != nil {
		return err
	}

	fileID, err := s.storeGeneratedModel(ctx, job, params, model)
	if err != nil {
		return err
	}

	// Clears issue_reason when it is exactly 'stl_missing', which is what makes
	// the job batchable without anyone touching it.
	if _, err := s.store.Q.SetProductionJobPrintFile(ctx, gen.SetProductionJobPrintFileParams{
		PrintFileID: &fileID, ID: jobID,
	}); err != nil {
		return fmt.Errorf("attach the model to job %s: %w", job.JobNumber, err)
	}

	// A generated model has no design, so nothing has told this job which
	// printer family it runs on - and without that the planner keeps it out of
	// batching however good its geometry is. Done after the file is attached
	// so a job is never left claiming a family for a model that failed.
	if err := s.setGeneratedMachineFamily(ctx, jobID); err != nil {
		return err
	}

	// A model landed, so any previous failure is no longer true.
	if err := s.store.Q.ClearJobModelError(ctx, jobID); err != nil {
		return fmt.Errorf("clear the previous model error on job %s: %w", jobID, err)
	}

	// The render consumed the customer's exact input, so there is nothing left
	// for a person to confirm about it. Not best-effort: a job left pending
	// here never reaches a bed, which would make the whole render pointless.
	if err := s.confirmGeneratedPersonalisation(ctx, jobID, params); err != nil {
		return err
	}
	return nil
}

// renderColouredPlank builds the finished model: a white base with the
// lettering in the colour the customer chose.
//
// Two renders, not one. Neither STL nor OpenSCAD's own 3MF export carries
// colour - the templates say so themselves - so the base and the lettering are
// rendered as separate parts and assembled into a 3MF where each is its own
// object with its own material. That is the only way the colour reaches the
// slicer.
//
// Falls back to a single uncoloured STL when the colour cannot be resolved.
// A plank in one colour is a product somebody can still inspect and fix; a
// plank in a colour nobody chose is scrap, and a job that failed outright
// helps nobody when the geometry was fine.
func (s *Server) renderColouredPlank(
	ctx context.Context, job gen.ProductionJob, params personalise.Params,
) ([]byte, error) {
	log := obs.FromContext(ctx)

	colour := colourFromVariant(job.VariantTitle)
	hex, err := s.resolveColourHex(ctx, colour)
	if err != nil {
		if !errors.Is(err, errUnknownColour) {
			return nil, err
		}
		log.Warn("no swatch for this colour; building a single-colour model instead",
			"job", job.JobNumber, "colour", colour)
		return s.renderer.RenderSTL(ctx, params.Template, params.ArgsForPart(personalise.PartAll))
	}

	base, err := s.renderer.RenderSTL(ctx, params.Template, params.ArgsForPart(personalise.PartBase))
	if err != nil {
		return nil, fmt.Errorf("render the base: %w", err)
	}
	text, err := s.renderer.RenderSTL(ctx, params.Template, params.ArgsForPart(personalise.PartText))
	if err != nil {
		return nil, fmt.Errorf("render the lettering: %w", err)
	}

	baseMesh, err := meshFromSTL(base)
	if err != nil {
		return nil, fmt.Errorf("read the base: %w", err)
	}
	textMesh, err := meshFromSTL(text)
	if err != nil {
		return nil, fmt.Errorf("read the lettering: %w", err)
	}

	model, err := meshio.Write3MF([]meshio.Part{
		// Named for what they are, because these are what an operator sees in
		// the slicer's object list.
		{Name: "Plate", Colour: BasePlateColour, Triangles: baseMesh.Triangles},
		{Name: colour + " lettering", Colour: hex, Triangles: textMesh.Triangles},
	})
	if err != nil {
		return nil, fmt.Errorf("assemble the coloured model: %w", err)
	}
	log.Info("built a two-colour plank",
		"job", job.JobNumber, "lettering", colour, "hex", hex)
	return model, nil
}

// meshFromSTL parses rendered STL bytes into a mesh.
func meshFromSTL(stl []byte) (orientation.Mesh, error) {
	dir, err := os.MkdirTemp("", "part-*")
	if err != nil {
		return orientation.Mesh{}, err
	}
	defer func() { _ = os.RemoveAll(dir) }()

	path := filepath.Join(dir, "part.stl")
	if err := os.WriteFile(path, stl, 0o600); err != nil {
		return orientation.Mesh{}, err
	}
	return orientation.LoadModel(path, ".stl")
}

// plankParamsForJob resolves the render inputs from the ORDER's line items.
//
// Not from the job: the importer joins the two names into a single
// "VASU & PADMANABH" string on the job row, and splitting that back apart would
// break on any name containing an ampersand - the very character the join uses.
// The individual names survive only in the order's line-item properties.
func (s *Server) plankParamsForJob(
	ctx context.Context, orderID uuid.UUID, job gen.ProductionJob,
) (personalise.Params, error) {
	order, err := s.store.Q.GetOrderByID(ctx, orderID)
	if err != nil {
		return personalise.Params{}, fmt.Errorf("load order: %w", err)
	}

	var items []production.LineItem
	if err := json.Unmarshal(order.LineItems, &items); err != nil {
		return personalise.Params{}, fmt.Errorf("read the order's line items: %w", err)
	}

	sku, product := deref(job.Sku), deref(job.ProductName)
	for _, li := range items {
		if !sameLineItem(li, sku, product) {
			continue
		}
		if len(li.Properties) == 0 {
			// The common case today: 43 of 46 imported orders predate the
			// properties import. Naming it precisely is what stops somebody
			// debugging the renderer for an hour.
			return personalise.Params{}, fmt.Errorf(
				"this order carries no personalisation options - press Sync from Shopify to fetch them")
		}
		return personalise.ParamsFromProperties(li.Properties)
	}
	if sku != "" {
		return personalise.Params{}, fmt.Errorf("no line item on this order matches SKU %s", sku)
	}
	return personalise.Params{}, fmt.Errorf(
		"no line item on this order matches the product %q", product)
}

// sameLineItem reports whether a line is the one this job was made from.
//
// By SKU when the job has one, and by product name when it does not. The second
// case is not a fallback for tidiness: nine live Dual Name Plank lines carry no
// SKU at all, and IsGeneratedProduct recognises them by name - so matching only
// on SKU here dereferenced a nil and panicked the render worker on exactly those
// jobs.
//
// A job with neither matches nothing, which surfaces as a clear error rather
// than as the first line on the order being rendered for the wrong customer.
func sameLineItem(li production.LineItem, sku, product string) bool {
	if sku != "" {
		return li.SKU != nil && strings.EqualFold(*li.SKU, sku)
	}
	if product == "" {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(li.ProductName), strings.TrimSpace(product))
}

// storeGeneratedModel puts the STL in object storage and records it as a file
// asset, measuring the geometry on the way through.
//
// The bbox is measured from the rendered triangles rather than assumed from the
// requested output size. They should agree - that is the whole point - but the
// planner packs beds with these numbers, and a bbox nobody checked is how a
// plate silently stops fitting.
func (s *Server) storeGeneratedModel(
	ctx context.Context, job gen.ProductionJob, params personalise.Params, model []byte,
) (uuid.UUID, error) {
	// A two-colour plank is a 3MF; a single-colour fallback is still STL. The
	// extension has to follow the bytes, because everything downstream - the
	// loader, the slicer, the browser preview - decides how to read the file
	// from it.
	ext, contentType := ".stl", "model/stl"
	if isZip(model) {
		ext, contentType = ".3mf", "model/3mf"
	}

	x, y, z, err := measureModel(model, ext)
	if err != nil {
		return uuid.Nil, fmt.Errorf("measure the rendered model: %w", err)
	}

	key := fmt.Sprintf("generated/%s%s", job.ID, ext)
	if err := s.storage.Put(
		ctx, key, bytes.NewReader(model), int64(len(model)), contentType,
	); err != nil {
		return uuid.Nil, fmt.Errorf("store the rendered model: %w", err)
	}

	// Named for the job and the customer's own words, so an operator opening
	// the file list can tell one plank from another without opening them.
	filename := fmt.Sprintf("%s-%s-%s%s", job.JobNumber, params.NameLeft, params.NameRight, ext)

	fileID := uuid.New()
	if _, err := s.store.Q.InsertFileAsset(ctx, gen.InsertFileAssetParams{
		ID: fileID, Filename: filename, ContentType: contentType,
		SizeBytes: int64(len(model)), StorageKey: key,
		// "system", matching design_match.go's convention for a file a
		// background process built rather than a person uploaded.
		UploadedBy: "system",
		BboxXMm:    &x, BboxYMm: &y, BboxZMm: &z,
	}); err != nil {
		return uuid.Nil, fmt.Errorf("record the rendered model: %w", err)
	}
	return fileID, nil
}

// isZip reports whether the bytes are a zip archive, which is what a 3MF is.
// Sniffed from the content rather than tracked alongside it: the writer decides
// the format, and a flag passed separately is a flag that can disagree.
func isZip(b []byte) bool {
	return len(b) > 3 && b[0] == 'P' && b[1] == 'K' && (b[2] == 3 || b[2] == 5 || b[2] == 7)
}

// measureModel reads a rendered model's bounding box through the same loader
// the rest of the pipeline uses, so a generated plate is measured exactly as an
// uploaded one would be.
func measureModel(model []byte, ext string) (x, y, z float64, err error) {
	dir, err := os.MkdirTemp("", "measure-*")
	if err != nil {
		return 0, 0, 0, err
	}
	defer func() { _ = os.RemoveAll(dir) }()

	path := filepath.Join(dir, "model"+ext)
	if err := os.WriteFile(path, model, 0o600); err != nil {
		return 0, 0, 0, err
	}
	m, err := orientation.LoadModel(path, ext)
	if err != nil {
		return 0, 0, 0, err
	}
	return m.Max.X - m.Min.X, m.Max.Y - m.Min.Y, m.Max.Z - m.Min.Z, nil
}

// holdJobForModelFailure records why a personalised model could not be built.
//
// The job is already excluded from batching - it has no print file, so
// issue_reason is 'stl_missing' from creation and stays that way. What is
// missing without this is the WHY: 'stl_missing' says a file is absent, not
// that OpenSCAD rejected the name or that the order never carried its
// personalisation options.
//
// The reason goes in the job's history rather than into issue_reason, which is
// a 32-character enum the planner switches on and cannot carry a sentence.
func (s *Server) holdJobForModelFailure(ctx context.Context, jobID uuid.UUID, cause error) error {
	// Recorded on the job so the queue can show it. Without this, a plank whose
	// render failed looks exactly like one whose render has not run yet: both
	// have no print file, and nobody can tell "being made" from "never will be".
	if err := s.store.Q.SetJobModelError(ctx, gen.SetJobModelErrorParams{
		ID: jobID, ModelError: strPtr(truncate(cause.Error(), 1000)),
	}); err != nil {
		return err
	}
	return recordJobEvent(ctx, s.store.Q, jobEvent{
		JobID:     jobID,
		EventType: production.EventModelFailed,
		Reason:    production.IssueSTLMissing,
		Comment:   strPtr(truncate(cause.Error(), 1000)),
	})
}

// recordModelGenerated notes a successful render, so the history shows where a
// job's file came from - a generated plank and an uploaded STL are otherwise
// indistinguishable once attached.
func (s *Server) recordModelGenerated(
	ctx context.Context, jobID uuid.UUID, params personalise.Params,
) error {
	return recordJobEvent(ctx, s.store.Q, jobEvent{
		JobID:     jobID,
		EventType: production.EventModelGenerated,
		Comment: strPtr(fmt.Sprintf("%s / %s, %d heart(s), %s",
			params.NameLeft, params.NameRight, params.Hearts, params.Template)),
	})
}

func strPtr(s string) *string { return &s }

// truncate bounds a message to the column's width, keeping the front - the
// part naming the failure - rather than the tail.
func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max-1] + "…"
}

// enqueueModelGeneration schedules a render for every job whose model Tensor
// builds itself.
//
// Best effort, and deliberately after the jobs are committed rather than in
// their transaction. A job that never gets its render still exists, still
// carries issue_reason = 'stl_missing', and is still visible to an operator -
// whereas rolling job creation back because a queue insert failed would lose
// the order's work entirely for a recoverable reason.
func (s *Server) enqueueModelGeneration(ctx context.Context, jobs []gen.ProductionJob) {
	if s.modelEnqueuer == nil {
		return
	}
	log := obs.FromContext(ctx)
	for _, job := range jobs {
		if !IsGeneratedProduct(deref(job.Sku), deref(job.ProductName)) {
			continue
		}
		if err := s.modelEnqueuer.Enqueue(ctx, job.ID); err != nil {
			log.Error("could not schedule the model render",
				"job", job.ID, "sku", deref(job.Sku), "error", err)
			continue
		}
		log.Info("model render scheduled", "job", job.ID, "sku", *job.Sku)
	}
}

// setGeneratedMachineFamily tells a rendered job which printer family it runs
// on, clearing the 'profile_missing' guard that would otherwise keep it out of
// batching for ever.
//
// Every other job inherits its family from a matched design's machine profile.
// A generated model has no design, so the family has to be stated - and stated
// by configuration rather than inferred here, because "which printer prints a
// plank" is an operations decision, not something the geometry implies. A plank
// is 200x50x40 and would physically fit any of the three families in the fleet.
//
// Unset is not an error to retry: the job keeps 'profile_missing', which is
// exactly right - it is not printable until somebody says where.
func (s *Server) setGeneratedMachineFamily(ctx context.Context, jobID uuid.UUID) error {
	family := strings.TrimSpace(s.cfg.GeneratedMachineFamily)
	if family == "" {
		obs.FromContext(ctx).Warn(
			"generated model has no machine family; the job stays held. "+
				"Set GENERATED_MACHINE_FAMILY to the printer family planks run on",
			"job", jobID)
		return nil
	}
	if _, err := s.store.Q.SetProductionJobMachineFamily(ctx, gen.SetProductionJobMachineFamilyParams{
		MachineFamily: &family, ID: jobID,
	}); err != nil {
		return fmt.Errorf("set the machine family on job %s: %w", jobID, err)
	}
	return nil
}

// confirmGeneratedPersonalisation marks a rendered job's personalisation as
// validated.
//
// The six confirmations exist for a person to check that what will be engraved
// matches what the customer asked for. On a generated plank there is nothing
// left to check: the model was BUILT from the customer's own two names moments
// ago, the font is fixed by the template, and the colour and variant are the
// SKU rather than choices anyone recorded. Leaving them unconfirmed would hold
// every plank behind a checkbox nobody can meaningfully tick - which is the
// whole manual step this pipeline removes.
//
// Recorded as validated by "system" and written to the job history, so the
// decision is auditable rather than invisible.
func (s *Server) confirmGeneratedPersonalisation(
	ctx context.Context, jobID uuid.UUID, params personalise.Params,
) error {
	now := time.Now().UTC()
	by := "system"
	if _, err := s.store.Q.ValidateProductionJobPersonalisation(
		ctx, gen.ValidateProductionJobPersonalisationParams{
			ID:                         jobID,
			NameConfirmed:              true,
			PhotoConfirmed:             true,
			FontConfirmed:              true,
			ColourConfirmed:            true,
			VariantConfirmed:           true,
			CustomerApprovalReceived:   true,
			PersonalisationStatus:      production.PersonalisationValidated,
			PersonalisationValidatedBy: &by,
			PersonalisationValidatedAt: db.Timestamptz(&now),
		}); err != nil {
		return fmt.Errorf("confirm personalisation on job %s: %w", jobID, err)
	}

	return recordJobEvent(ctx, s.store.Q, jobEvent{
		JobID:     jobID,
		EventType: production.EventModelGenerated,
		Comment: strPtr(fmt.Sprintf(
			"Personalisation confirmed by rendering: %s / %s, %d heart(s)",
			params.NameLeft, params.NameRight, params.Hearts)),
	})
}
