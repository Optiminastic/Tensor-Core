package httpapi

// The one search box, read the same way on every list.

import (
	"strings"

	"github.com/gin-gonic/gin"
)

// searchParam is ?search=, trimmed, or nil when there is nothing to search for.
//
// Nil rather than an empty string, because the queries treat NULL as "no filter"
// and an empty pattern would otherwise become '%%' - which matches every row
// with a non-null column and silently DROPS every row where the column is null.
// A blank box must return everything, not almost everything.
//
// The pattern's own wildcards are left alone deliberately: an operator typing %
// is vanishingly rare next to one typing a customer's name, and ILIKE cannot
// escape a LIKE pattern without also breaking the names that legitimately
// contain an underscore.
func searchParam(c *gin.Context) *string {
	q := strings.TrimSpace(c.Query("search"))
	if q == "" {
		return nil
	}
	return &q
}
