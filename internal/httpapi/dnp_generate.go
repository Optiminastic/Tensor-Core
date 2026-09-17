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
var generatedProductNames = []string{"dual name plank", "dual name & photo frame"}

// generatedSKUSegments are the hyphen-separated SKU segments that name a
// product Tensor renders itself.
//
// Three live families: T3DPS-DNP-1..16 carries "DNP", the with-light range
// carries "DNPLB", and the Dual Name & Photo Frame carries "DNPF".
//
// Matched as whole hyphen-separated segments, never as a prefix. The rule used
// to accept anything starting with "DNP" and that cost real orders, which is
// why DNPF is listed here explicitly rather than falling in by accident.
//
// DNPF renders from the same no-heart template as the plank and differs only in
// finished size - see personalise.Params.ForProduct. It used to need a design
// file uploaded per order; it no longer does.
var generatedSKUSegments = map[string]bool{
	"DNP":   true,
	"DNPLB": true,
	"DNPF":  true,
}

// IsGeneratedProduct reports whether a product is one Tensor renders itself.
//
// The SKU is the primary key on it: the storefront renames products freely, but
// the SKU is what the warehouse and the design tables key on. Both live families
// are covered - T3DPS-DNP-1..16 and DNPLB-*.
//
// Matched against a NAMED SET of segments, not a prefix. That distinction cost
// real orders: the rule used to accept any segment starting with "DNP", which
// swallowed DNPF - the Dual Name & PHOTO FRAME. Tensor rendered a plank for a
// photo frame, failed with "a plank needs both names", and held four orders
// that had nothing wrong with them behind an error about a product they are
// not. The families it must match are known and short, so listing them is both
// safer and more honest than a prefix that happens to fit today.
//
// The product name is the fallback, and it is not belt-and-braces. Nine live
// Dual Name Plank lines - the GREEN and PURPLE variants - carry all four STEP
// customisation properties and NO SKU at all, so a SKU-only rule filed them as
// manual-upload work and never rendered them. They are planks; the variant just
// lost its SKU in the catalogue.
func IsGeneratedProduct(sku, productName string) bool {
	for _, part := range strings.Split(strings.ToUpper(sku), "-") {
		if generatedSKUSegments[part] {
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

	// No printer family is stamped here any more.
	//
	// It used to be, because the planner refuses to bed a job with no family.
	// But the value came from one setting - GENERATED_MACHINE_FAMILY, "A2L" in
	// production - so every plank in the shop claimed the same class, five of
	// thirteen printers did all the plank work, and eight stood idle.
	//
	// A plank is 200x50x40 on a 330mm bed: it fits any printer in the fleet,
	// and which one runs it is BambuBuddy's decision, made against real AMS
	// trays at the moment of slicing. An unstamped family now means "any
	// machine" rather than "no machine" - see assignMachineForBatch.

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
// A colour that cannot be resolved fails the job rather than building it
// anyway. It used to fall back to a single uncoloured STL, which was worse than
// it sounds: that render is PartAll - one mesh - so the plank came out with no
// white base at all, and the job still reported success, cleared its issue and
// went to a bed. One such job then took every plank beside it down to a
// colourless plate. A job held with a named colour is a five-second fix; a bed
// of planks in the wrong colour is scrap.
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
		// An unresolvable colour fails the job rather than building it plain.
		//
		// This used to log a warning and return a single UNCOLOURED model, and
		// report success: the job then cleared its issue, reached a bed, and
		// took every other plank on that bed down with it, because one part
		// with no colour makes the whole plate a colourless STL that declares
		// no AMS slots at all. That is how 103 of 141 planks printed with no
		// colour (see fallbackColours).
		//
		// A job held with a named reason is recoverable - add the colour to
		// fallbackColours, or sync the filament shelf - and it is visible on
		// the issues board rather than silent. A plank in a colour nobody
		// chose is scrap, and a whole bed of them is four times the scrap.
		return nil, fmt.Errorf(
			"no swatch for %q, so this plank cannot be built in colour; "+
				"add the colour to the filament shelf or the built-in table", colour)
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

	// The job's own material, not a default. It reaches BambuBuddy as
	// filament_type in the plate's slot declaration, and a bed of PETG that
	// declares PLA asks the AMS for the wrong spool.
	material := deref(job.Material)
	model, err := meshio.Write3MF([]meshio.Part{
		// Named for what they are, because these are what an operator sees in
		// the slicer's object list.
		{Name: "Plate", Colour: BasePlateColour, Material: material, Triangles: baseMesh.Triangles},
		{Name: colour + " lettering", Colour: hex, Material: material, Triangles: textMesh.Triangles},
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
	sku, product := deref(job.Sku), deref(job.ProductName)

	// The job's OWN line first.
	//
	// An order carrying five planks carries five lines of the same SKU, each
	// with its own two names - and the scan below, which matches on SKU or
	// product name, cannot tell them apart: it returns the first every time. On
	// T3DPS-115059 that printed NAVYA & KRISHNA four times while APRAJITA,
	// FARAH and CHANDNI never reached a plate at all.
	//
	// The names were never lost. buildJobsForOrder snapshots each line's
	// properties onto the job it creates, precisely so a job shows what it was
	// made from; this reads that snapshot back. The scan stays as a fallback
	// for jobs created before the column existed.
	if props, ok := jobLineProperties(job); ok {
		params, err := personalise.ParamsFromProperties(props)
		if err != nil {
			return personalise.Params{}, err
		}
		return params.ForProduct(sku, product), nil
	}

	order, err := s.store.Q.GetOrderByID(ctx, orderID)
	if err != nil {
		return personalise.Params{}, fmt.Errorf("load order: %w", err)
	}

	var items []production.LineItem
	if err := json.Unmarshal(order.LineItems, &items); err != nil {
		return personalise.Params{}, fmt.Errorf("read the order's line items: %w", err)
	}

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
		params, err := personalise.ParamsFromProperties(li.Properties)
		if err != nil {
			return personalise.Params{}, err
		}
		// The names come from the line's properties; the product decides what
		// they are scaled into.
		return params.ForProduct(sku, product), nil
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

	// And the inputs themselves, beside the file.
	//
	// The filename is for reading, not for checking: it carries no heart count,
	// and the names cannot be parsed back out of it reliably because a customer's
	// name may hold the same hyphen it separates on. Recording the arguments the
	// render was actually given turns "is this model still what the order says?"
	// into a comparison instead of a guess - which is what let a bed print NAVYA
	// & KRISHNA four times against three other customers' orders without
	// anything in the database disagreeing with itself.
	renderParams, err := json.Marshal(storedRenderParams{
		Template: params.Template, Hearts: params.Hearts,
		NameLeft: params.NameLeft, NameRight: params.NameRight,
	})
	if err != nil {
		return uuid.Nil, fmt.Errorf("record what this model was built from: %w", err)
	}

	fileID := uuid.New()
	if _, err := s.store.Q.InsertFileAsset(ctx, gen.InsertFileAssetParams{
		ID: fileID, Filename: filename, ContentType: contentType,
		SizeBytes: int64(len(model)), StorageKey: key,
		// "system", matching design_match.go's convention for a file a
		// background process built rather than a person uploaded.
		UploadedBy: "system",
		BboxXMm:    &x, BboxYMm: &y, BboxZMm: &z,
		RenderParams: renderParams,
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
		// deref, not *job.Sku. IsGeneratedProduct matches the GREEN and PURPLE
		// plank variants by PRODUCT NAME because those nine lines carry no SKU
		// at all, so this line dereferenced nil on exactly the jobs the name
		// fallback exists to catch. The panic aborted the loop, which meant
		// every job after it on the same order never had a render enqueued -
		// permanently, because River retries job creation and creation
		// short-circuits on errJobsAlreadyCreated without ever reaching here
		// again. Twenty such jobs exist on the live database.
		log.Info("model render scheduled", "job", job.ID, "sku", deref(job.Sku))
	}
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

// jobLineProperties is the personalisation the job was CREATED from, as stored
// on the job itself.
//
// Reports false for a job that carries none - one created before the column
// existed, or a line Shopify sent no properties for - so the caller can fall
// back to reading the order rather than failing a job that is renderable.
func jobLineProperties(job gen.ProductionJob) ([]production.LineProp, bool) {
	if len(job.PersonalisationProperties) == 0 {
		return nil, false
	}
	var props []production.LineProp
	if err := json.Unmarshal(job.PersonalisationProperties, &props); err != nil {
		return nil, false
	}
	if len(props) == 0 {
		return nil, false
	}
	return props, true
}

// storedRenderParams is what a generated model was built from, written beside
// the file as file_assets.render_params.
//
// Deliberately its own small struct rather than personalise.Params: that type
// carries a Shape and grows with the renderer, and this is a record on disk that
// other code compares against. Adding a field here is a decision about what
// "the same model" means, which is worth making on purpose.
type storedRenderParams struct {
	Template  string `json:"template"`
	Hearts    int    `json:"hearts"`
	NameLeft  string `json:"name_left"`
	NameRight string `json:"name_right"`
}
