package httpapi

// The file the customer is shown, which is not the file that prints.
//
// A product is rarely one printed thing. A Royal diya holder is an arch plate
// with a name on it, a foot and a cup; a Lotus is a plate and a diya cup that
// pushes onto it with two pegs. Only the plate carries the personalisation, so
// only the plate goes on a bed - and for a long time only the plate reached
// the customer's thirty-minute link, which asked them to approve a diya holder
// with no holder.
//
// So a product may name a SECOND template: an OpenSCAD file of the whole
// assembled product, rendered for the picture and for nothing else.
//
// THREE RULES HOLD THIS SAFE, and each exists because its absence is a bed of
// scrap or a customer approving the wrong thing:
//
//   - It is a design ROLE, "preview", and partRolesForRender refuses that role
//     by name. A preview template can never become a production job, so it can
//     never be batched, sliced or printed.
//   - It is driven by the PRINT file's field mapping, never its own. One
//     mapping means the name on the picture and the name on the plate are the
//     same string by construction, rather than by two people keeping two
//     mappings in step.
//   - Its pieces are its own (design_colour_parts, role "preview"), because an
//     assembled product has pieces the plate does not - a foot, a cup - and
//     colours the plate's scheme has no opinion about.

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/google/uuid"

	"github.com/Optiminastic/tensor-core/internal/db/gen"
	"github.com/Optiminastic/tensor-core/internal/meshio"
	"github.com/Optiminastic/tensor-core/internal/obs"
	"github.com/Optiminastic/tensor-core/internal/personalise"
)

// renderPreviewModel is the assembled picture of this job's product, or nil.
//
// NIL IS THE ORDINARY ANSWER. Every product configured before this existed has
// no preview template, and the caller stores one file: the picture is then a
// picture of what prints, exactly as it always was.
//
// Failures here are NOT best-effort, unlike the component assembly beside it.
// A preview template that cannot render is a configuration error - a piece
// misspelled, a file replaced by one that does not branch on PART - and the
// person who can fix it is the person who uploaded it. Showing the plate
// instead would hide that until somebody noticed a cup missing from a
// photograph.
func (s *Server) renderPreviewModel(
	ctx context.Context, job gen.ProductionJob, plan renderPlan, colour, hex string,
) ([]byte, error) {
	sku := strings.TrimSpace(deref(job.Sku))
	if sku == "" {
		return nil, nil
	}
	product, err := s.store.Q.FindProductBySKU(ctx, sku)
	if err != nil {
		// Not in the registry, so it has no preview file by definition. A
		// lookup failure is the same answer as "no row" here because both
		// mean this job renders the way it rendered yesterday.
		return nil, nil
	}
	template, ok := s.previewTemplateFor(ctx, product.ID, sku, job)
	if !ok {
		return nil, nil
	}

	parts := s.previewColourParts(ctx, product.ID, sku, job)
	// THE FILE'S OWN PIECES, when it states them - before the refusal below.
	//
	// A flagged file names its pieces and paints them in its own source, so
	// nothing is configured for it by design. Asked after the registry so an
	// existing scheme is what gets used while a shop is mid-migration, and
	// before the error so a correctly flagged file is not refused for having
	// exactly the configuration it was flagged to avoid.
	//
	// That error matters more than it looks: it is returned to
	// renderColouredPlank, which fails the WHOLE job - the printed model
	// included - over a picture.
	if len(parts) == 0 {
		parts = s.selfColouredParts(ctx, template)
	}
	if len(parts) == 0 {
		// A template with no pieces cannot be rendered: the renderer asks for
		// a PART by name and there is no name to ask for. Refused rather than
		// skipped, because somebody uploaded a preview file and would
		// otherwise never learn it is doing nothing.
		return nil, fmt.Errorf(
			"%s names a personalisation preview file (%s) but no pieces for it - "+
				"add its pieces and their colours in the registry, or mark the file as "+
				"stating its own colours", product.Code, template)
	}

	// WHICH VARIABLES THIS FILE IS DRIVEN BY.
	//
	// Its own mapping when it has one, and the printed design's when it does
	// not. The second is the common case and the reason it is the fallback:
	// an assembled file usually declares the same TEXT the plate does, and
	// making somebody re-map it would be a second place to keep in step.
	//
	// The first exists because the file is somebody else's drawing. An
	// assembled Royal holder may call the name NAME while the plate calls it
	// TEXT, and refusing it for that would be refusing a correct file over a
	// spelling. Both mappings read the SAME order property, so the two models
	// still carry the same string whichever variable it lands in - that is
	// what keeps them from drifting, not the variable's name.
	previewPlan := plan
	previewPlan.Template = template
	if args, ok := s.previewArgs(ctx, product.ID, job); ok {
		previewPlan.Args = args
	}

	// THE FILE'S OWN COLOURS, where it states them.
	//
	// The design declares BACK_COLOR, OUTLINE_COLOR and the rest, and those
	// are the colours the designer chose. Painting the picture from values
	// re-typed into the registry means two records of one decision, and the
	// customer approving whichever is stale. A piece the file says nothing
	// about keeps the registry's colour.
	//
	// PICTURE ONLY. The print model is still painted from the registry,
	// because those hexes are matched against the filament shelf.
	//
	// ONLY WHEN THE FILE IS THIS COLOURWAY'S OWN. A file shared by every
	// colourway carries one set of defaults, and painting from them would
	// show a red plate in the blue the file happens to declare.
	if _, ownsFile, _ := s.templateForVariantDetail(
		ctx, product.ID, sku, designRolePreview,
	); ownsFile {
		parts = s.withDeclaredColours(ctx, template, parts)
	}

	// The customer's colour is resolved ONCE, for both models, by the caller.
	// Passing it in rather than resolving again means the picture and the
	// plate cannot land on two different spools of the same colour name.
	meshes, err := s.renderPieces(ctx, template, previewPlan, parts, colour, hex, deref(job.Material))
	if err != nil {
		return nil, fmt.Errorf("render the personalisation preview: %w", err)
	}
	model, err := meshio.Write3MF(meshes)
	if err != nil {
		return nil, fmt.Errorf("assemble the personalisation preview: %w", err)
	}
	return model, nil
}

// previewArgs builds the preview file's arguments from its OWN field mapping.
//
// ok is false when the product maps nothing for the preview role, which means
// "use the printed design's arguments" - see renderPreviewModel.
//
// A mapping here does NOT make a production job. partRolesForRender refuses
// the preview role by name, which is exactly why that refusal is explicit
// rather than relying on this role having no mapping: this is the mapping it
// was protecting against.
func (s *Server) previewArgs(
	ctx context.Context, productID uuid.UUID, job gen.ProductionJob,
) (map[string]string, bool) {
	rows := s.fieldMapsFor(ctx, productID, strings.TrimSpace(deref(job.Sku)), designRolePreview)
	if len(rows) == 0 {
		return nil, false
	}
	var orderID uuid.UUID
	if job.OrderID != nil {
		orderID = *job.OrderID
	}
	props, err := s.linePropertiesForJob(ctx, orderID, job)
	if err != nil {
		return nil, false
	}
	args, err := personalise.MappedArgs(props, toFieldMaps(rows))
	if err != nil {
		// The picture falls back to the plate's arguments rather than failing
		// the job. A mapping that cannot be satisfied is a configuration
		// problem worth fixing, and holding a paid order over the PICTURE
		// while the plate is renderable is the wrong trade.
		obs.FromContext(ctx).Warn(
			"personalisation preview: its own mapping could not be filled, using the printed design's",
			"job", job.JobNumber, "error", err)
		return nil, false
	}
	return args, true
}

// previewTemplateFor is the assembled file for the colourway this job will be
// made in, or the product's.
//
// THE COLOUR THEY CHOSE, NOT THE ONE THEY BOUGHT, exactly as
// previewColourParts resolves the palette and as the printed model resolves
// its own file. A Royal holder's colourways are separate files, each already
// in its own colours, so looking the file up by the SKU on the order line
// showed a customer who picked red the purple holder they had just changed
// their mind about - and said underneath that this is exactly how it will be
// made.
func (s *Server) previewTemplateFor(
	ctx context.Context, productID uuid.UUID, sku string, job gen.ProductionJob,
) (string, bool) {
	return s.templateForVariant(ctx, productID,
		s.skuForColourway(ctx, productID, sku, job), designRolePreview)
}

// templateForVariant is the design file one SKU uses for one role.
//
// PER VARIANT FIRST, the product second. A product's colourways may be drawn
// as separate files - a red lotus and a white one, each already in its own
// colours - and they may equally be one file that every colourway shares.
// Uploading one file three times is three chances to update two of them, so
// sharing is expressed by assigning the same key rather than by copying.
//
// Deliberately NOT templateKeyForRole, which refuses to answer when two
// variants of a product name different keys. That rule made sense while a
// product printed from exactly one file; it is now the thing that makes a
// file per SKU impossible, and it fails silently - the product simply stops
// rendering. That function is left alone because other callers want its
// product-wide answer, and this is the one the renderer asks.
func (s *Server) templateForVariant(
	ctx context.Context, productID uuid.UUID, sku, role string,
) (string, bool) {
	key, _, ok := s.templateForVariantDetail(ctx, productID, sku, role)
	return key, ok
}

// templateForVariantDetail also says whether the file was found on THIS
// variant or inherited from the product.
//
// The difference decides whose colours a picture is painted in. A file
// assigned to one colourway is that colourway's drawing, already in its own
// colours, and the registry is only repeating them. A file shared by every
// colourway has exactly one set of defaults, and trusting them would paint
// the red variant and the white one in whatever the purple one happens to
// declare - which is what TNP-RED did, in blue, the first time this ran.
func (s *Server) templateForVariantDetail(
	ctx context.Context, productID uuid.UUID, sku, role string,
) (key string, ownedByVariant, ok bool) {
	rows, err := s.store.Q.ListDesignsForProduct(ctx, productID)
	if err != nil {
		return "", false, false
	}
	var fallback string
	for _, r := range rows {
		if !strings.EqualFold(strings.TrimSpace(r.Role), role) {
			continue
		}
		if r.TemplateKey == nil || strings.TrimSpace(*r.TemplateKey) == "" {
			continue
		}
		k := strings.TrimSpace(*r.TemplateKey)
		if strings.EqualFold(strings.TrimSpace(deref(r.Sku)), sku) {
			// ITS OWN only when no sibling names the same file. Three
			// colourways each assigned "pwdh" are three rows that match a
			// SKU, and they are still one shared drawing.
			return k, !sharedWithSiblings(rows, role, sku, k), true
		}
		if fallback == "" {
			fallback = k
		}
	}
	return fallback, false, fallback != ""
}

// sharedWithSiblings reports whether another colourway names the same file.
func sharedWithSiblings(
	rows []gen.ListDesignsForProductRow, role, sku, key string,
) bool {
	for _, r := range rows {
		if !strings.EqualFold(strings.TrimSpace(r.Role), role) {
			continue
		}
		if r.TemplateKey == nil || !strings.EqualFold(strings.TrimSpace(*r.TemplateKey), key) {
			continue
		}
		if !strings.EqualFold(strings.TrimSpace(deref(r.Sku)), sku) {
			return true
		}
	}
	return false
}

// withDeclaredColours repaints the pieces from the template's own variables.
//
// Best effort in both directions: a template whose source cannot be read, or
// which declares no colours, leaves every piece exactly as the registry
// configured it - which is how this behaved before the file was consulted.
//
// A piece that FOLLOWS THE CUSTOMER is left alone on purpose. Its colour is
// the one they chose on the link, and overriding that with the file's default
// would show them a product in a colour they did not pick.
func (s *Server) withDeclaredColours(
	ctx context.Context, template string, parts []colourPart,
) []colourPart {
	if s.renderer == nil {
		return parts
	}
	source, err := s.renderer.Source(ctx, template)
	if err != nil || len(source) == 0 {
		return parts
	}
	// NAMES AS WELL AS HEXES, the same as selfColouredParts reads them.
	//
	// This read PartColours, which keeps only well-formed "#RRGGBB", while
	// the sibling path that builds a flagged file's print scheme reads
	// PartColourValues and resolves a name against the shelf. So one file was
	// being read two ways: the Royal holder declares background_color =
	// "purple" beside text_color = "#FFBF00", and the picture kept the gold
	// and dropped the purple - a holder drawn entirely in its text colour.
	// The Lotus is worse: every one of its colours is a name, so the picture
	// took none of them and the flag appeared to do nothing at all.
	//
	// PER PIECE, where selfColouredParts is all-or-nothing. That difference
	// is deliberate and stays: a print scheme missing a piece prints a product
	// missing it, while a picture missing one is a picture with one piece
	// still in the colour the registry chose.
	declared := personalise.PartColourValues(source)
	if len(declared) == 0 {
		return parts
	}
	out := make([]colourPart, len(parts))
	copy(out, parts)
	for i := range out {
		if out[i].FollowsCustomer() {
			continue
		}
		written, ok := declared[strings.ToLower(strings.TrimSpace(out[i].Name))]
		if !ok {
			continue
		}
		if hex, ok := s.declaredColourHex(ctx, written); ok {
			out[i].Hex = hex
		}
	}
	return out
}

// previewColourParts is the pieces the assembled file draws, and their colours.
//
// The variant's own scheme first and the product's second, the same order and
// the same all-or-nothing rule colourPartsForJob applies to the printed
// pieces - see resolveScheme for why merging the two would build a palette
// that exists in neither.
//
// THE COLOURWAY IS RESOLVED BY variantForColourway, exactly as the printed
// pieces resolve it, and that is the whole care in this function. Looking the
// variant up by SKU instead is the obvious thing and it is wrong: a customer
// who bought PWDH-P and changed their mind to WHITE on the link gets a plate
// in the white scheme and - measured on JOB-990043, which is how this was
// found - a PICTURE in the purple one. They would then approve a purple lotus
// and receive a white one, with nothing anywhere disagreeing.
func (s *Server) previewColourParts(
	ctx context.Context, productID uuid.UUID, sku string, job gen.ProductionJob,
) []colourPart {
	var own []colourPart
	if variantID := s.variantForColourway(ctx, productID, sku, job); variantID != nil {
		rows, err := s.store.Q.ListColourPartsForVariant(ctx,
			gen.ListColourPartsForVariantParams{
				VariantID: variantID, Role: designRolePreview,
			})
		if err == nil {
			own = toColourParts(rows)
		}
	}
	rows, err := s.store.Q.ListDesignColourParts(ctx, gen.ListDesignColourPartsParams{
		ProductID: productID, Role: designRolePreview,
	})
	var shared []colourPart
	if err == nil {
		shared = toColourParts(rows)
	}
	return resolveScheme(own, shared)
}

// renderPieces renders one model's pieces, in parallel, into coloured meshes.
//
// THE PIECES RENDER AT THE SAME TIME, not one after the other.
//
// They are independent OpenSCAD subprocesses - the renderer says so, and each
// works in its own temp directory - and neither reads the other's output. Run
// in sequence, a two-piece plank costs the sum of two CGAL passes; run
// together it costs the longer of them. Measured on this plank, from the
// customer pressing Save to the new picture arriving: 34 seconds became 21.
//
// It cannot be avoided by rendering less. The base is
// difference() { base_plate(); letters(); }, so a name change really does
// change both pieces - there is no half of this work to skip, only time to
// overlap.
//
// Bounded by the number of pieces a product has, which is two to four. The
// queue's own MaxWorkers is what bounds how many jobs render at once, and that
// is where to turn this down if a box starts thrashing.
func (s *Server) renderPieces(
	ctx context.Context, template string, plan renderPlan,
	parts []colourPart, colour, hex, material string,
) ([]meshio.Part, error) {
	meshes := make([]meshio.Part, len(parts))
	failures := make([]error, len(parts))

	var wg sync.WaitGroup
	for i, part := range parts {
		wg.Add(1)
		go func(i int, part colourPart) {
			defer wg.Done()

			stl, err := s.renderer.RenderSTL(ctx, template, plan.argsForPart(part.Name))
			if err != nil {
				failures[i] = fmt.Errorf("render the %s: %w", part.Name, err)
				return
			}
			mesh, err := meshFromSTL(stl)
			if err != nil {
				failures[i] = fmt.Errorf("read the %s: %w", part.Name, err)
				return
			}
			// An empty piece is the silent failure this whole mechanism is
			// exposed to: a template that declares PART and never branches on
			// it answers every request with the same solid, and one that
			// branches on names that do not match the configured pieces
			// answers some with nothing. Both slice, both print, and both are
			// wrong - so a piece that came back with no geometry holds the job
			// and names itself.
			if len(mesh.Triangles) == 0 {
				failures[i] = fmt.Errorf(
					"the template rendered nothing for part %q - check that %s branches on "+
						"PART and spells this piece the same way",
					part.Name, template)
				return
			}

			partColour, partName := part.Hex, part.Name
			if part.FollowsCustomer() {
				partColour = hex
				partName = colour + " " + part.Name
			}
			// Written to its own slot, so the pieces keep the order the design
			// lists them in. Appending from goroutines would shuffle the
			// slicer's object list between renders of the same plank.
			meshes[i] = meshio.Part{
				// Named for what it is, because this is what an operator sees
				// in the slicer's object list.
				Name: partName, Colour: partColour, Material: material,
				Triangles: mesh.Triangles,
			}
		}(i, part)
	}
	wg.Wait()

	// The FIRST failure in part order, not whichever goroutine happened to
	// finish last - the same plank must fail the same way twice.
	for _, err := range failures {
		if err != nil {
			return nil, err
		}
	}
	return meshes, nil
}
