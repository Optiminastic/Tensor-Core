package httpapi

// Is this SKU ready to be made?
//
// A product reaches production through about six pieces of configuration -
// a file per role, a mapping, a colour scheme, a SKU to match orders on - and
// until now the only way to find out whether they were all there was to let an
// order arrive and see what happened. What happened was usually a job holding
// with a message written for whoever wrote the renderer:
//
//	PWDH names no template for body
//	PWDH maps no fields for body
//
// Both are true, neither says what to go and do, and both arrive hours after
// the configuration was saved, attached to a customer's order.
//
// So the same questions are asked up front, per SKU, and answered in the words
// of the thing that is missing. READ-ONLY on purpose: it reports, it does not
// refuse. A product half-configured on a Friday is a normal state, and a page
// that blocked saving until everything was perfect would mean configuring it
// in one sitting or not at all.

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/Optiminastic/tensor-core/internal/db/gen"
	"github.com/Optiminastic/tensor-core/internal/production"

	"github.com/Optiminastic/tensor-core/internal/personalise"
)

// skuReadiness is one child SKU and what stands between it and an order.
type skuReadiness struct {
	VariantID string `json:"variant_id"`
	SKU       string `json:"sku"`
	Name      string `json:"name"`
	Status    string `json:"status"`
	// Ready is true when Problems is empty: this SKU can take an order today.
	Ready bool `json:"ready"`
	// Problems are in the order they must be fixed - a mapping cannot be
	// checked against a file that is not there yet.
	Problems []string `json:"problems"`
	// Notes are things worth knowing that do not stop an order: no preview
	// file, no colour scheme. Separate from Problems because a page that
	// shows both as failures teaches people to ignore it.
	Notes []string `json:"notes"`
	// Rendering is how many of this SKU's jobs are building their model now.
	//
	// The rest of this struct says whether a SKU is CONFIGURED. This says
	// whether anything configured is MOVING - which is the question somebody
	// has right after setting a product up, and which had no answer short of
	// going to the production queue and reading it by eye.
	Rendering int64 `json:"rendering"`
	// RenderFailed is how many stopped with an error and no model.
	RenderFailed int64 `json:"render_failed"`
	// LastRenderError is the most recent of those, verbatim from the renderer.
	LastRenderError string `json:"last_render_error,omitempty"`
}

// productReadiness lists a product's SKUs and what each still needs.
func (s *Server) productReadiness(c *gin.Context) {
	ctx := c.Request.Context()
	product, ok := s.productByCode(c)
	if !ok {
		return
	}
	variants, err := s.store.Q.ListVariantsForProduct(ctx, product.ID)
	if err != nil {
		detail(c, http.StatusInternalServerError, "Could not read this product's SKUs.")
		return
	}

	// ONE READ FOR THE WHOLE PRODUCT, not one per SKU. A product has a handful
	// of colourways and this page is opened often; a query per row would make
	// the cost of the panel grow with the catalogue.
	renders := s.renderStateBySKU(ctx, variants)

	out := make([]skuReadiness, 0, len(variants))
	for i := range variants {
		if variants[i].Status == "retired" {
			continue
		}
		row := s.readinessOf(ctx, product.ID, product.Code, variants[i])
		if state, ok := renders[strings.ToLower(strings.TrimSpace(row.SKU))]; ok {
			row.Rendering = state.Rendering
			row.RenderFailed = state.Failed
			row.LastRenderError = state.LastError
			// A STOPPED RENDER IS A PROBLEM; one in flight is a NOTE.
			//
			// That split is the contract this panel states for itself: a
			// page that shows progress as a failure teaches people to ignore
			// it, and a held job is work somebody has to do.
			if state.Failed > 0 {
				row.Problems = append(row.Problems, renderFailedWording(state))
				row.Ready = false
			}
			if state.Rendering > 0 {
				row.Notes = append(row.Notes, renderingWording(state.Rendering))
			}
		}
		out = append(out, row)
	}
	c.JSON(http.StatusOK, gin.H{"items": out})
}

// renderStateBySKU is how each of these SKUs' models are getting on.
//
// Keyed lower-cased, as the query groups them: an order's SKU arrives however
// the storefront wrote it. An unreadable table answers "nothing known", which
// renders the panel exactly as it read before this existed - configuration
// only. Render state is worth showing and not worth failing a page over.
func (s *Server) renderStateBySKU(
	ctx context.Context, variants []gen.ListVariantsForProductRow,
) map[string]gen.SummariseJobRendersForSKUsRow {
	skus := make([]string, 0, len(variants))
	for i := range variants {
		if sku := strings.ToLower(strings.TrimSpace(deref(variants[i].Sku))); sku != "" {
			skus = append(skus, sku)
		}
	}
	if len(skus) == 0 {
		return nil
	}
	rows, err := s.store.Q.SummariseJobRendersForSKUs(ctx, skus)
	if err != nil {
		return nil
	}
	out := make(map[string]gen.SummariseJobRendersForSKUsRow, len(rows))
	for _, r := range rows {
		out[r.Sku] = r
	}
	return out
}

// renderFailedWording says how many stopped, and what the renderer said.
//
// The renderer's own words rather than a summary: "a plank needs both names"
// names the fault and the fix, where "a render failed" sends somebody to the
// logs.
func renderFailedWording(state gen.SummariseJobRendersForSKUsRow) string {
	what := fmt.Sprintf("%d job", state.Failed)
	if state.Failed != 1 {
		what += "s"
	}
	if detail := strings.TrimSpace(state.LastError); detail != "" {
		return what + " stopped while building a model: " + detail
	}
	return what + " stopped while building a model."
}

func renderingWording(n int64) string {
	if n == 1 {
		return "One job is building its model now."
	}
	return fmt.Sprintf("%d jobs are building their models now.", n)
}

// readinessOf answers for one SKU.
func (s *Server) readinessOf(
	ctx context.Context, productID uuid.UUID, code string, v gen.ListVariantsForProductRow,
) skuReadiness {
	sku := strings.TrimSpace(deref(v.Sku))
	out := skuReadiness{
		VariantID: v.ID.String(), SKU: sku, Name: v.Name, Status: v.Status,
		Problems: []string{}, Notes: []string{},
	}

	// 1. SOMETHING TO MATCH AN ORDER ON. A variant with no SKU is one no
	//    order line can ever be resolved to, and the fix is in Shopify rather
	//    than here - so it is named as such.
	if sku == "" {
		out.Problems = append(out.Problems,
			"This SKU is blank in Shopify, so no order line can ever be matched to it.")
		return out
	}

	// 2. A FILE TO PRINT FROM.
	template, hasTemplate := s.templateForVariant(ctx, productID, sku, designRoleBody)
	if !hasTemplate {
		out.Problems = append(out.Problems,
			"No design file. Assign one on the Design & fields panel.")
	}

	// 3. SOMETHING TO PUT IN IT.
	maps := s.fieldMapsFor(ctx, productID, sku, designRoleBody)
	if len(maps) == 0 {
		out.Problems = append(out.Problems,
			fmt.Sprintf(
				"No field mapping, so %s would render the same model for every customer.", code))
	}

	// 4. AND THE FILE HAS TO DECLARE WHAT THE MAPPING SENDS IT.
	//
	// Only askable once both exist. OpenSCAD accepts an unknown -D without
	// complaint, so a mapping pointing at a variable the file does not have
	// renders a model with the name missing - successfully.
	if hasTemplate && len(maps) > 0 {
		if missing := s.undeclaredVariables(ctx, template, maps); len(missing) > 0 {
			out.Problems = append(out.Problems, fmt.Sprintf(
				"%s does not declare %s, so the customer's text would not reach the model.",
				template, strings.Join(missing, ", ")))
		}
	}

	// HOW MANY FILAMENTS THIS DESIGN ACTUALLY NEEDS.
	//
	// A job declares the colour the customer bought - one - and the planner
	// caps a bed on that. A design whose plate is purple, pink and gold in
	// three bands still declares one, so a bed of them is planned as a
	// single-filament bed and arrives at a printer needing three trays.
	//
	// Said here rather than fixed in the planner, because the fix is a
	// decision about what a bed may carry rather than a bug: counting the
	// real colours would put every such design over the cap and stop it
	// batching at all. Until somebody makes that decision, the person
	// configuring the product is the one who can see it coming.
	if n := s.filamentsNeeded(ctx, productID, sku); n > production.MaxBedColours {
		out.Notes = append(out.Notes, fmt.Sprintf(
			"This design prints in %d colours, but a bed is planned for at most %d. "+
				"Tensor will batch it as if it needed one filament, so the operator has "+
				"to change filament %d times on the plate.",
			n, production.MaxBedColours, n-1))
	}

	// Notes: real, and not reasons to stop an order.
	if _, ok := s.templateForVariant(ctx, productID, sku, designRolePreview); !ok {
		out.Notes = append(out.Notes,
			"No personalisation preview file. The customer's link will show the model that prints.")
	}
	if !v.PersonalisationLinkEnabled {
		out.Notes = append(out.Notes,
			"The customer is not asked to confirm: this SKU prints as soon as its model is built.")
	}

	out.Ready = len(out.Problems) == 0
	return out
}

// filamentsNeeded is how many distinct colours this SKU's printed design uses.
//
// Read from the colour scheme, which is what the renderer paints the model
// from, so the count is the one that will actually reach a plate. Zero when
// the product configures no scheme, which means it renders as the plank's two
// pieces and has nothing to warn about.
func (s *Server) filamentsNeeded(ctx context.Context, productID uuid.UUID, sku string) int {
	parts := s.colourPartsForSKU(ctx, productID, sku, designRoleBody)
	seen := map[string]bool{}
	for _, p := range parts {
		// A piece following the customer's choice is the colour they bought,
		// which the job already declares. Counting it again would report a
		// plain two-colour plank as needing two filaments beyond its one.
		if p.FollowsCustomer() {
			continue
		}
		seen[strings.ToUpper(strings.TrimSpace(p.Hex))] = true
	}
	if len(seen) == 0 {
		return 0
	}
	// Plus the customer's own colour, when any piece follows it.
	for _, p := range parts {
		if p.FollowsCustomer() {
			return len(seen) + 1
		}
	}
	return len(seen)
}

// colourPartsForSKU is the printed scheme for one SKU, variant first.
func (s *Server) colourPartsForSKU(
	ctx context.Context, productID uuid.UUID, sku, role string,
) []colourPart {
	var own []colourPart
	if variant, err := s.store.Q.FindVariantBySKU(ctx, sku); err == nil {
		rows, err := s.store.Q.ListColourPartsForVariant(ctx,
			gen.ListColourPartsForVariantParams{VariantID: &variant.ID, Role: role})
		if err == nil {
			own = toColourParts(rows)
		}
	}
	rows, err := s.store.Q.ListDesignColourParts(ctx, gen.ListDesignColourPartsParams{
		ProductID: productID, Role: role,
	})
	var shared []colourPart
	if err == nil {
		shared = toColourParts(rows)
	}
	return resolveScheme(own, shared)
}

// undeclaredVariables names the mapped variables a template does not have.
//
// Empty when the source cannot be read - an embedded template, or storage
// having a bad moment. A check that cannot see the file must not report it as
// broken, because the person reading that would go looking for a fault that
// is not there.
func (s *Server) undeclaredVariables(
	ctx context.Context, template string, maps []gen.ProductFieldMap,
) []string {
	if s.renderer == nil {
		return nil
	}
	source, err := s.renderer.Source(ctx, template)
	if err != nil || len(source) == 0 {
		return nil
	}
	wanted := make([]string, 0, len(maps))
	for _, m := range maps {
		// A fixed value never comes off the order, so the file is free not to
		// read it.
		if strings.TrimSpace(deref(m.FixedValue)) != "" {
			continue
		}
		wanted = append(wanted, m.ScadVariable)
	}
	return personalise.DeclaresAll(source, wanted)
}
