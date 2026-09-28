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
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

type productPartResponse struct {
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

	out := make([]productPartResponse, 0, 3)
	for _, role := range s.designRolesForProduct(ctx, product.ID) {
		key, _ := s.templateKeyForRole(ctx, product.ID, role)
		n := fields[strings.ToLower(role)]
		out = append(out, productPartResponse{
			Role: role, TemplateKey: key,
			Fields: n, Required: required[strings.ToLower(role)],
			Ready: key != "" && n > 0,
		})
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
		})
	}

	c.JSON(http.StatusOK, out)
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
		if mapped[strings.ToLower(role)] {
			out = append(out, role)
		}
	}
	return out
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
	return s.partRolesForRender(ctx, product.ID)
}
