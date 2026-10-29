package httpapi

// Removing a design file from the registry.
//
// REFUSED WHILE ANYTHING PRINTS FROM IT, and that refusal is the whole reason
// this has a file of its own. A template key is not referenced by a foreign
// key - variant_designs stores the key as TEXT, deliberately, because a
// variant may be pointed at a file before the file is uploaded - so the
// database will happily let the last file a product renders from disappear.
// What follows is not an error on this screen: it is every order for that
// product holding, hours later, with a message about a template nobody can
// find.
//
// So the check is here, it names the products, and it is the only thing
// standing between a tidy-up and a stopped line.
//
// RETIRED, NOT DELETED. The file a key pointed at is what printed real orders,
// and "which file made the thing this customer is holding" has to stay
// answerable afterwards - see RetireTemplate. The key leaves the Designs tab;
// its history and the stored object both stay.

import (
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/Optiminastic/tensor-core/internal/db/gen"
)

func (s *Server) deleteDesignTemplate(c *gin.Context) {
	ctx := c.Request.Context()
	key := strings.TrimSpace(c.Param("key"))
	if key == "" {
		detail(c, http.StatusUnprocessableEntity, "Name the design file to remove.")
		return
	}

	// A built-in template is compiled into this binary. There is no row to
	// retire and no file to remove, and a delete that silently did nothing
	// would look like it had worked.
	for _, embedded := range embeddedTemplateKeys {
		if strings.EqualFold(embedded, key) {
			detail(c, http.StatusUnprocessableEntity, fmt.Sprintf(
				"%s is built into Tensor rather than uploaded, so there is nothing to "+
					"remove. Replacing it with your own file is the way to change what it "+
					"renders.", key))
			return
		}
	}

	rows, err := s.store.Q.ListProductsUsingTemplate(ctx, key)
	if err != nil {
		detail(c, http.StatusInternalServerError, "Could not check what uses this file.")
		return
	}
	if len(rows) > 0 {
		detail(c, http.StatusConflict, templateInUseDetail(key, rows))
		return
	}

	n, err := s.store.Q.RetireTemplate(ctx, key)
	if err != nil {
		detail(c, http.StatusInternalServerError, "Could not remove the design file.")
		return
	}
	if n == 0 {
		detail(c, http.StatusNotFound, "No uploaded design file under that key.")
		return
	}
	c.Status(http.StatusNoContent)
}

// templateInUseDetail names what would break, in the words of the screen.
//
// The products rather than the variants: "PDHR-P, PDHR-R, PDHR-G" is three
// ways of saying PDHR, and a refusal listing twenty SKUs is one nobody reads.
func templateInUseDetail(key string, rows []gen.ListProductsUsingTemplateRow) string {
	byProduct := map[string][]string{}
	order := []string{}
	for _, r := range rows {
		if _, seen := byProduct[r.Code]; !seen {
			order = append(order, r.Code)
		}
		byProduct[r.Code] = append(byProduct[r.Code], roleWords(r.Role))
	}
	sort.Strings(order)

	parts := make([]string, 0, len(order))
	for _, code := range order {
		parts = append(parts, fmt.Sprintf("%s (%s)", code, strings.Join(byProduct[code], " and ")))
	}
	return fmt.Sprintf(
		"%s still renders %s. Point those at another file first — removing it now would "+
			"hold every order for them.", key, strings.Join(parts, ", "))
}

// roleWords is a design role as somebody reading a refusal would say it.
func roleWords(role string) string {
	switch strings.ToLower(strings.TrimSpace(role)) {
	case designRoleBody, "":
		return "what prints"
	case "preview":
		return "the customer's preview"
	default:
		return role
	}
}
