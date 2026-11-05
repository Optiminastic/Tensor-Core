package httpapi

// What a product prints, file by file.
//
// A product used to print one thing, so "which .scad" and "which fields" were
// each one answer and the page could ask for them flatly. A Soulmate Combo is
// three printed things sold as one line - a plank, a rose and a keychain -
// and each has its own file, its own variables and its own name from the
// customer.
//
// This is the list those become. Derived from variant_designs rather than
// stored anywhere of its own: a part EXISTS because a file was assigned to it,
// and a second list would be a second thing to keep in step with the first.

import (
	"context"
	"net/http"
	"sort"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/Optiminastic/tensor-core/internal/db/gen"
)

type productPartResponse struct {
	// ID is the stored part's id, empty for one still derived from its files.
	//
	// Empty is not an error: every product configured before product_parts
	// existed has parts and no rows, and they go on working. It is what the
	// page uses to decide whether renaming and removing are offered yet.
	ID string `json:"id,omitempty"`
	// PerVariant is how this part is configured: a file per child SKU, or one
	// file for every colourway. It decides which of the two pickers the panel
	// shows - showing both is what it did before anybody could say.
	PerVariant bool `json:"per_variant"`
	// Role names the file: 'body' for a product that prints one thing, and
	// whatever somebody called it for a product that prints three.
	Role string `json:"role"`
	// TemplateKey is the .scad this part renders from.
	TemplateKey string `json:"template_key"`
	// Fields is how many of the customer's answers feed it, and Required how
	// many of those hold the job when an order does not carry them.
	Fields   int `json:"fields"`
	Required int `json:"required"`
	// Ready says this part can actually render: it has a file and at least one
	// mapped field. A part with a file and no mapping renders the template's
	// defaults - the same model for every customer - which is the failure that
	// looks most like success.
	Ready bool `json:"ready"`
	// Conflicting is the files this product's SKUs disagree about for this
	// part, when they disagree. Empty in the ordinary case.
	//
	// Reported rather than hidden. DNP's six variants each print from one of
	// three plank templates, and the page said "nothing prints this product
	// yet" - so the obvious next click, choosing a file, silently overwrote
	// all six. It did, on 28 September, and only the retired rows made it
	// recoverable.
	Conflicting []string `json:"conflicting,omitempty"`
}

// listProductParts answers "what does this product print, and can it".
func (s *Server) listProductParts(c *gin.Context) {
	product, ok := s.productByCode(c)
	if !ok {
		return
	}
	ctx := c.Request.Context()

	rows, err := s.store.Q.ListAllProductFieldMaps(ctx, product.ID)
	if err != nil {
		detail(c, http.StatusInternalServerError, "Could not read the field mapping.")
		return
	}
	fields := map[string]int{}
	required := map[string]int{}
	for _, r := range rows {
		role := strings.ToLower(roleOrBody(r.Role))
		fields[role]++
		if r.Required {
			required[role]++
		}
	}

	designs, err := s.store.Q.ListDesignsForProduct(ctx, product.ID)
	if err != nil {
		detail(c, http.StatusInternalServerError, "Could not read the product's designs.")
		return
	}
	// Every distinct file named per role, so a disagreement between SKUs is
	// something the page can show rather than something it flattens away.
	perRole := map[string]map[string]bool{}
	for _, d := range designs {
		if d.TemplateKey == nil || strings.TrimSpace(*d.TemplateKey) == "" {
			continue
		}
		role := strings.ToLower(roleOrBody(d.Role))
		if perRole[role] == nil {
			perRole[role] = map[string]bool{}
		}
		perRole[role][strings.TrimSpace(*d.TemplateKey)] = true
	}

	out := make([]productPartResponse, 0, 3)
	for _, role := range s.designRolesForProduct(ctx, product.ID) {
		key, _ := s.templateKeyForRole(ctx, product.ID, role)
		n := fields[strings.ToLower(role)]
		part := productPartResponse{
			Role: role, TemplateKey: key,
			Fields: n, Required: required[strings.ToLower(role)],
			Ready: key != "" && n > 0,
			// INFERRED, and inferred HERE rather than in the merge below,
			// which returns early when nothing has been named. Left to the
			// zero value, every product that predates named parts would come
			// back "one shared file" and the panel would hide the per-SKU
			// rows that are the only thing configuring PDHR and PWDH.
			//
			// No product-wide file means the SKUs carry their own - either
			// because they disagree, or because there is nothing yet.
			PerVariant: key == "",
		}
		if keys := perRole[strings.ToLower(role)]; len(keys) > 1 {
			part.Conflicting = sortedKeys(keys)
		}
		out = append(out, part)
	}

	// A mapping written before its file was uploaded is a normal half-finished
	// state, and leaving it out of this list would make the fields somebody
	// entered look lost. Shown with no template, which is what the page has to
	// tell them to fix.
	listed := map[string]bool{}
	for _, p := range out {
		listed[strings.ToLower(p.Role)] = true
	}
	for role, n := range fields {
		if listed[role] {
			continue
		}
		out = append(out, productPartResponse{
			Role: role, Fields: n, Required: required[role], Ready: false,
			PerVariant: true,
		})
	}

	c.JSON(http.StatusOK, s.withStoredParts(ctx, product.ID, out))
}

// withStoredParts merges what somebody NAMED over what the files imply.
//
// Additive in both directions. A derived part gains its id and its scope from
// a stored row when there is one; a stored part with no files yet is appended,
// which is the whole point - it is how a part can be named before anybody
// goes to find its drawings. A product configured before this table existed
// has no rows and comes back exactly as it did.
func (s *Server) withStoredParts(
	ctx context.Context, productID uuid.UUID, derived []productPartResponse,
) []productPartResponse {
	stored, err := s.store.Q.ListProductParts(ctx, productID)
	if err != nil || len(stored) == 0 {
		// An unreadable list is not a reason to hide the parts that have
		// files. They render; only renaming them is unavailable.
		return derived
	}
	byRole := make(map[string]gen.ProductPart, len(stored))
	for _, p := range stored {
		byRole[strings.ToLower(p.Role)] = p
	}
	seen := make(map[string]bool, len(derived))
	for i := range derived {
		role := strings.ToLower(derived[i].Role)
		seen[role] = true
		if p, ok := byRole[role]; ok {
			derived[i].ID = p.ID.String()
			derived[i].PerVariant = p.PerVariant
		}
		// No row means nobody has said, and the inference made when the part
		// was built already stands.
	}
	for _, p := range stored {
		if seen[strings.ToLower(p.Role)] {
			continue
		}
		derived = append(derived, productPartResponse{
			ID: p.ID.String(), Role: p.Role, PerVariant: p.PerVariant,
		})
	}
	return derived
}

// sortedKeys is a set as a stable list, so a disagreement reads the same way
// twice rather than in whichever order the map yielded.
func sortedKeys(set map[string]bool) []string {
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// partRolesForRender is the design files a job should be created for, in
// order.
//
// The job-creation half of the same question, without the HTTP. Empty means
// the registry does not describe this product, and the caller makes one job
// the way it always has.
//
// A part with no mapped fields is left OUT. It would render the template's
// defaults - one identical model for every customer - and a job that prints
// the wrong thing is worse than a job that was never made, because it reaches
// a bed looking finished.
//
// AND THE PREVIEW ROLE IS LEFT OUT BY NAME, whatever it maps.
//
// That file is the assembled product - a Lotus with its diya cup standing on
// it, a Royal with its foot and its cup - drawn so the customer can approve
// what they will receive. It is not a thing anybody prints: the cup is a
// separate print that pushes on afterwards, and the assembled model put on a
// bed would fuse it to the plate at ninety degrees.
//
// The mapping rule above happens to exclude it today, because the preview
// borrows the body's mapping and declares none of its own. That is an
// accident, not a guarantee - the day somebody maps a field to the preview
// role it would start making a job per order with nothing to say so. So it is
// refused here explicitly as well, and a test maps a field to it and asserts
// it still makes no job.
func (s *Server) partRolesForRender(ctx context.Context, productID uuid.UUID) []string {
	rows, err := s.store.Q.ListAllProductFieldMaps(ctx, productID)
	if err != nil {
		return nil
	}
	mapped := map[string]bool{}
	for _, r := range rows {
		mapped[strings.ToLower(roleOrBody(r.Role))] = true
	}

	roles := s.designRolesForProduct(ctx, productID)
	out := make([]string, 0, len(roles))
	for _, role := range roles {
		if isPreviewRole(role) {
			continue
		}
		if mapped[strings.ToLower(role)] {
			out = append(out, role)
		}
	}
	return out
}

// isPreviewRole reports whether this role is drawn for the customer rather
// than printed. See designRolePreview.
func isPreviewRole(role string) bool {
	return strings.EqualFold(strings.TrimSpace(role), designRolePreview)
}

// partRolesForSKU is partRolesForRender by the SKU an order carries.
//
// Fails CLOSED, like registryRendersSKU: an unreachable registry answers "no
// parts", and the caller makes the single job it always made. Answering with
// a partial list would split an order into two jobs and quietly drop the
// third.
func (s *Server) partRolesForSKU(ctx context.Context, sku string) []string {
	sku = strings.TrimSpace(sku)
	if sku == "" {
		return nil
	}
	product, err := s.store.Q.FindProductBySKU(ctx, sku)
	if err != nil {
		return nil
	}
	return s.partRolesForRenderSKU(ctx, product.ID, sku)
}

// partRolesForRenderSKU is partRolesForRender asked about ONE SKU.
//
// Identical in every rule - a role with no mapped field is left out, and the
// preview role is refused by name whatever it maps - and different in exactly
// one way: whether a role is mapped is answered by fieldMapsFor, which reads
// this SKU's own mapping first and the product's second.
//
// That difference is the whole reason this exists. partRolesForRender reads
// the product's own rows, so a product whose SKUs each carry their own
// mapping would come back with no roles at all, and a line that becomes no
// roles becomes no job - silently, on a paid order. The product-level twin
// stays for the product-level caller, which is asking a product-level
// question and has no SKU to ask about.
func (s *Server) partRolesForRenderSKU(
	ctx context.Context, productID uuid.UUID, sku string,
) []string {
	roles := s.designRolesForProduct(ctx, productID)
	out := make([]string, 0, len(roles))
	for _, role := range roles {
		if isPreviewRole(role) {
			continue
		}
		if len(s.fieldMapsFor(ctx, productID, sku, role)) > 0 {
			out = append(out, role)
		}
	}
	return out
}
