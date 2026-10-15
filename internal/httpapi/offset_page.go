package httpapi

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgtype"
)

// Numbered pages for the dashboard's tables.
//
// The keyset pages in pagination.go answer "the next N after this row", which
// is right for an infinite scroll and wrong for a table with "Page 3 of 41"
// and a last-page button: a keyset cannot jump, and it cannot say how many
// pages there are. These can.
//
// OPT-IN, like the keyset pages: without `page` a handler answers exactly as it
// always has - the bare array every other consumer (the dashboard home, the
// overview, the sales report, the packaging joins) reads. With `page` it
// answers one page inside an envelope carrying the total.
const (
	defaultTablePageSize = 15
	maxTablePageSize     = 200
)

// offsetPage is a requested page, 1-based.
type offsetPage struct {
	Page int32
	Size int32
}

// parseOffsetPage reads `page` and `page_size`. paged is false when `page` is
// absent, and the caller serves its unpaged list. A value that is not a number
// is a 422; a page below 1 is read as 1, and one past the end is clamped by
// within() once the total is known - a bookmarked page 40 of a list that has
// since shrunk to 3 pages shows page 3, not an empty table.
func parseOffsetPage(c *gin.Context) (p offsetPage, paged, ok bool) {
	raw := strings.TrimSpace(c.Query("page"))
	if raw == "" {
		return offsetPage{}, false, true
	}
	page, err := strconv.Atoi(raw)
	if err != nil {
		detail(c, http.StatusUnprocessableEntity, "'page' must be a whole number.")
		return offsetPage{}, false, false
	}
	size := defaultTablePageSize
	if s := strings.TrimSpace(c.Query("page_size")); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil || n < 1 || n > maxTablePageSize {
			detail(c, http.StatusUnprocessableEntity,
				fmt.Sprintf("'page_size' must be a whole number between 1 and %d.", maxTablePageSize))
			return offsetPage{}, false, false
		}
		size = n
	}
	if page < 1 {
		page = 1
	}
	return offsetPage{Page: int32(page), Size: int32(size)}, true, true
}

// within clamps the page to the ones that exist for this many rows. An empty
// list still has a page 1.
func (p offsetPage) within(total int64) offsetPage {
	if last := pageCount(total, p.Size); p.Page > last {
		p.Page = last
	}
	return p
}

// Offset is the number of rows before this page.
func (p offsetPage) Offset() int32 { return (p.Page - 1) * p.Size }

func pageCount(total int64, size int32) int32 {
	if total <= 0 {
		return 1
	}
	return int32((total + int64(size) - 1) / int64(size))
}

// pageEnvelope is one page and what the table needs to draw its footer.
//
// Counts are the tab badges. They count the whole list - not the search, not
// the page - because that is what the tabs have always shown: "Unfulfilled
// 214" is how many there are, whatever is typed in the box.
type pageEnvelope[T any] struct {
	Items      []T              `json:"items"`
	Total      int64            `json:"total"`
	Page       int32            `json:"page"`
	PageSize   int32            `json:"page_size"`
	TotalPages int32            `json:"total_pages"`
	Counts     map[string]int64 `json:"counts,omitempty"`
}

func newPageEnvelope[T any](items []T, total int64, p offsetPage) pageEnvelope[T] {
	if items == nil {
		items = []T{}
	}
	return pageEnvelope[T]{
		Items: items, Total: total, Page: p.Page, PageSize: p.Size,
		TotalPages: pageCount(total, p.Size),
	}
}

// timeRange is an optional window from `from` and `to`, both inclusive and
// both RFC 3339. The browser resolves "this week" in the operator's own time
// zone and sends the instants, so the server never has to guess one.
type timeRange struct {
	From pgtype.Timestamptz
	To   pgtype.Timestamptz
}

func parseTimeRange(c *gin.Context) (timeRange, bool) {
	var out timeRange
	for _, side := range []struct {
		key string
		dst *pgtype.Timestamptz
	}{{"from", &out.From}, {"to", &out.To}} {
		raw := strings.TrimSpace(c.Query(side.key))
		if raw == "" {
			continue
		}
		t, err := time.Parse(time.RFC3339Nano, raw)
		if err != nil {
			detail(c, http.StatusUnprocessableEntity,
				fmt.Sprintf("'%s' must be an RFC 3339 timestamp.", side.key))
			return timeRange{}, false
		}
		*side.dst = pgtype.Timestamptz{Time: t, Valid: true}
	}
	return out, true
}

// slicePage cuts a list that did not come from a query of ours - an outside
// service's answer (Shopify, BambuBuddy), or one assembled in Go from several
// places - to one page, so the browser is sent that page and not the list.
// Database tables never come through here: they are cut by LIMIT/OFFSET.
func slicePage[T any](items []T, p offsetPage) pageEnvelope[T] {
	total := int64(len(items))
	p = p.within(total)
	start := min(int(p.Offset()), len(items))
	end := min(start+int(p.Size), len(items))
	return newPageEnvelope(items[start:end], total, p)
}
