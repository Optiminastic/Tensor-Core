package httpapi

// Filing a design file under a heading on the Designs tab.
//
// A LABEL, AND NOTHING ELSE READS IT. What a file is actually FOR is decided
// by variant_designs - which product prints from which key, in which role -
// and that is what the renderer resolves. This decides which accordion the row
// appears in, so a shelf of .scad files can be told apart at a glance.
// Setting it changes no model, no job and no order.
//
// That separation is the whole point of this file existing. The first attempt
// let the Designs tab assign a file to a product, which made "tidy the list"
// and "change what prints" the same gesture - and the second is not something
// anybody should do by accident from a file browser.

import (
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/Optiminastic/tensor-core/internal/db/gen"
)

// templateSections are the headings a file may be filed under.
//
// "other" rather than "unsorted": a file can belong to neither of the first
// two on purpose - a part's mesh, a test piece - and a heading that reads as
// "not done yet" invites somebody to tidy away something that was already
// where it belonged.
var templateSections = map[string]bool{"print": true, "preview": true, "other": true}

func templateSectionNames() []string {
	out := make([]string, 0, len(templateSections))
	for name := range templateSections {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

type templateSectionRequest struct {
	// Section is one of templateSections, or empty to unfile it - which
	// returns the row to the heading its actual use implies.
	Section string `json:"section"`
}

func (s *Server) setTemplateSection(c *gin.Context) {
	ctx := c.Request.Context()
	key := strings.TrimSpace(c.Param("key"))
	if key == "" {
		detail(c, http.StatusUnprocessableEntity, "Name the design file.")
		return
	}

	var req templateSectionRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		detail(c, http.StatusUnprocessableEntity, "Say which section to file it under.")
		return
	}
	section := strings.ToLower(strings.TrimSpace(req.Section))

	// Empty unfiles it. A real choice, so it is spelled rather than left to
	// absence: a file nobody has filed and a file somebody deliberately
	// unfiled are the same state, and both fall back to what its use implies.
	if section == "" {
		if err := s.store.Q.ClearTemplateSection(ctx, key); err != nil {
			detail(c, http.StatusInternalServerError, "Could not unfile that design file.")
			return
		}
		c.Status(http.StatusNoContent)
		return
	}

	if !templateSections[section] {
		detail(c, http.StatusUnprocessableEntity, fmt.Sprintf(
			"A design file is filed under %s.", strings.Join(templateSectionNames(), ", ")))
		return
	}

	// NOT CHECKED AGAINST THE TEMPLATE LIST, deliberately. A key filed before
	// its file is uploaded is harmless - the label describes a shelf, and a
	// label for a file that never arrives sorts nothing - whereas refusing it
	// would impose an order of operations on somebody tidying a screen.
	if err := s.store.Q.SetTemplateSection(ctx, gen.SetTemplateSectionParams{
		TemplateKey: key, Section: section, UpdatedBy: currentUserID(c),
	}); err != nil {
		detail(c, http.StatusInternalServerError, "Could not file that design file.")
		return
	}
	c.Status(http.StatusNoContent)
}

// templateSectionsByKey is every filed label, by lower-cased key.
func (s *Server) templateSectionsByKey(c *gin.Context) map[string]string {
	rows, err := s.store.Q.ListTemplateSections(c.Request.Context())
	if err != nil {
		// A missing label sorts a row under what its use implies, which is
		// the behaviour before anybody filed anything. Not worth failing the
		// page over.
		return map[string]string{}
	}
	out := make(map[string]string, len(rows))
	for _, r := range rows {
		out[strings.ToLower(r.TemplateKey)] = r.Section
	}
	return out
}
