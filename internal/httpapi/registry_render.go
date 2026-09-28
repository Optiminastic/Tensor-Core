package httpapi

// Rendering a product the registry describes, rather than one Go knows about.
//
// Everything a render needs - which .scad, and which of the customer's answers
// feed which of its variables - has lived in Go: templateForHearts picks the
// file, ParamsFromProperties knows that "STEP 4-First Name-" means NAME_L, and
// generatedSKUSegments decides whether Tensor renders the product at all. That
// works for one family and puts a deploy in front of every product after it.
//
// This resolves the same three answers from the registry instead. A SKU finds
// a product, the product names a template and a field mapping, and the mapping
// becomes -D flags.
//
// It is deliberately ADDITIVE. A SKU the registry does not describe resolves
// nothing and the caller falls through to the path that has been printing
// planks all along, so configuring a new product cannot change an existing
// one. Retiring the hardcoded path is a later decision, taken once something
// real has printed through this one.

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/Optiminastic/tensor-core/internal/db/gen"
	"github.com/Optiminastic/tensor-core/internal/obs"
	"github.com/Optiminastic/tensor-core/internal/personalise"
	"github.com/Optiminastic/tensor-core/internal/production"
)

// renderPlan is everything one model generation needs, however it was decided.
//
// Both paths produce one of these, so renderColouredPlank and
// storeGeneratedModel no longer care which. That is the point: the alternative
// is those two growing a branch each, and a branch each is how the DNP path
// and its replacement drift apart while both look correct.
type renderPlan struct {
	// Template is the key the renderer resolves - an uploaded override where
	// there is one, the embedded .scad otherwise.
	Template string
	// Args are the -D flags, complete except for PART. PART changes between
	// the two passes over the same arguments, so the caller adds it.
	Args map[string]string
	// Label is the descriptive middle of the stored filename, so an operator
	// reading a file list can tell one model from another without opening it.
	Label string
	// Stored goes beside the file and is what "is this model still what the
	// order says?" is answered from later.
	Stored storedRenderParams
}

// argsForPart is Args with PART set, for one of the two coloured passes.
func (p renderPlan) argsForPart(part string) map[string]string {
	out := make(map[string]string, len(p.Args)+1)
	for k, v := range p.Args {
		out[k] = v
	}
	out["PART"] = personalise.Quote(part)
	return out
}

// describe is the one-line summary written to the job's history, so a
// generated model and an uploaded one are distinguishable months later.
//
// The plank form is kept verbatim - "AMENA / SALMAN, 2 heart(s), dnp_two_heart"
// is what every existing history entry reads like, and changing it would make
// the same event look like two different ones either side of this change.
func (p renderPlan) describe() string {
	if len(p.Stored.Args) == 0 {
		return fmt.Sprintf("%s / %s, %d heart(s), %s",
			p.Stored.NameLeft, p.Stored.NameRight, p.Stored.Hearts, p.Template)
	}
	names := make([]string, 0, len(p.Stored.Args))
	for name := range p.Stored.Args {
		names = append(names, name)
	}
	// Sorted, so the same render reads the same way twice rather than naming
	// whichever field map iteration reached first.
	sort.Strings(names)
	parts := make([]string, 0, len(names))
	for _, name := range names {
		parts = append(parts, name+"="+p.Stored.Args[name])
	}
	return fmt.Sprintf("%s from %s: %s", p.Label, p.Template, strings.Join(parts, ", "))
}

// errNoRegistryPlan says the registry does not describe this SKU.
//
// Not a failure: most SKUs are not in the registry and the caller falls back.
// Named so the fallback is a deliberate branch rather than an err == nil test
// that would also swallow a real database error.
var errNoRegistryPlan = errors.New("no registry configuration for this SKU")

// resolveRenderPlan builds a plan from the registry, or reports that it cannot.
//
// Every "cannot" is errNoRegistryPlan wrapping the reason, EXCEPT a mapping
// that exists and does not fit the order - that is a real failure and must
// hold the job. The distinction matters: falling back when a product IS
// configured would render it through the plank path, which would either
// produce a plank-shaped model for a keychain or fail with a message about
// names the product does not have.
func (s *Server) resolveRenderPlan(
	ctx context.Context, job gen.ProductionJob, props []production.LineProp,
) (renderPlan, error) {
	sku := strings.TrimSpace(deref(job.Sku))
	if sku == "" {
		return renderPlan{}, fmt.Errorf("%w: the job carries no SKU", errNoRegistryPlan)
	}

	product, err := s.store.Q.FindProductBySKU(ctx, sku)
	if isNoRows(err) {
		return renderPlan{}, fmt.Errorf("%w: %s", errNoRegistryPlan, sku)
	}
	if err != nil {
		// A database failure is not "this product is unconfigured". Falling
		// back here would render a configured product through the plank path
		// because the registry was briefly unreachable.
		return renderPlan{}, fmt.Errorf("look up %s in the registry: %w", sku, err)
	}

	role := partRoleOf(job)

	template, ok := s.templateKeyForRole(ctx, product.ID, role)
	if !ok {
		// Half-configured: a product with a mapping and no template cannot
		// render, and a product with neither is simply not this system's yet.
		return renderPlan{}, fmt.Errorf(
			"%w: %s names no template", errNoRegistryPlan, product.Code)
	}

	rows, err := s.store.Q.ListProductFieldMaps(ctx, gen.ListProductFieldMapsParams{
		ProductID: product.ID, Role: role,
	})
	if err != nil {
		return renderPlan{}, fmt.Errorf("read the field mapping for %s: %w", product.Code, err)
	}
	if len(rows) == 0 {
		return renderPlan{}, fmt.Errorf(
			"%w: %s maps no fields for %s", errNoRegistryPlan, product.Code, role)
	}

	maps := make([]personalise.FieldMap, 0, len(rows))
	for _, r := range rows {
		maps = append(maps, personalise.FieldMap{
			PropertyKey:  r.PropertyKey,
			ScadVariable: r.ScadVariable,
			Numeric:      r.ValueType == "number",
			Optional:     !r.Required,
			Fixed:        deref(r.FixedValue),
		})
	}

	// Past this point the product IS configured, so every failure is the
	// order's or the configuration's and none of them fall back.
	args, err := personalise.MappedArgs(props, maps)
	if err != nil {
		return renderPlan{}, err
	}

	obs.FromContext(ctx).Info("rendering from the registry",
		"job", job.JobNumber, "sku", sku, "product", product.Code,
		"part", role, "template", template, "fields", len(maps))

	return renderPlan{
		Template: template,
		Args:     args,
		Label:    product.Code,
		Stored: storedRenderParams{
			Template: template,
			// Which of the product's design files this model is. Without it,
			// a combo's three models all record the same product and a
			// "does this still match the order?" comparison could not tell
			// the rose's model from the keychain's.
			Role: role,
			// The mapped arguments themselves, because for a registry product
			// there is no NameLeft/Hearts to compare - the args ARE what the
			// model was built from, and they are what a later "does this still
			// match the order?" has to compare.
			Args: args,
		},
	}, nil
}

// rendersProduct reports whether Tensor builds this product's model itself.
//
// The registry first, then the hardcoded families. A product configured in the
// registry renders without anybody editing generatedSKUSegments and deploying
// - the cycle that ran twice in one day for PDNP, SC, SCWL and DNPWL - while
// every family printing today keeps working from the list it always used.
//
// Takes a context because the registry half needs the database. The pure
// IsGeneratedProduct stays, and stays the answer on the two paths that run
// per row of a list page, where a lookup each would be an N+1 for a column.
func (s *Server) rendersProduct(ctx context.Context, sku, productName string) bool {
	if IsGeneratedProduct(sku, productName) {
		return true
	}
	return s.registryRendersSKU(ctx, sku)
}

// registryRendersSKU reports whether the registry describes this SKU well
// enough to render it.
//
// The dispatch switch. IsGeneratedProduct consults it before its hardcoded
// segment list, which is what lets a product added through the registry render
// without anybody editing generatedSKUSegments and deploying - a cycle that
// ran twice in one day for PDNP, SC, SCWL and DNPWL.
//
// Cheap and fails CLOSED: an unreachable registry answers "no", and the
// hardcoded list still covers every product printing today. Answering "yes" on
// an error would route a live plank into a path that then could not find its
// configuration.
func (s *Server) registryRendersSKU(ctx context.Context, sku string) bool {
	sku = strings.TrimSpace(sku)
	if sku == "" {
		return false
	}
	product, err := s.store.Q.FindProductBySKU(ctx, sku)
	if err != nil {
		return false
	}
	if len(s.designRolesForProduct(ctx, product.ID)) == 0 {
		return false
	}
	rows, err := s.store.Q.ListAllProductFieldMaps(ctx, product.ID)
	return err == nil && len(rows) > 0
}

// designRoleBody is the design file a product has when it has only one.
//
// Every product configured before combos existed is a body, and the column
// defaults to it, so "one file" is the zero case of "several" rather than a
// separate path.
const designRoleBody = "body"

// partRoleOf is which of its product's design files this job prints.
func partRoleOf(job gen.ProductionJob) string {
	role := strings.TrimSpace(job.PartRole)
	if role == "" {
		return designRoleBody
	}
	return role
}
