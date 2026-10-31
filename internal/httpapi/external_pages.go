package httpapi

import (
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/Optiminastic/tensor-core/internal/integrations/shopify"
)

// Numbered pages for the lists whose rows are not a query of ours: Shopify's
// abandoned checkouts and fulfilments, BambuBuddy's queue and archive, and the
// design templates Go assembles from the binary and the database.
//
// The source cannot be paged - Shopify and BambuBuddy are read whole, and the
// templates are a merge - so each is filtered, searched and sorted here exactly
// as its table did in the browser, then cut with slicePage. What changes is
// that the browser is sent fifteen rows rather than every one.

// matchesNeedle reports whether any field contains the needle, ignoring case.
// needle is already lower-cased; an empty one matches everything.
func matchesNeedle(needle string, fields ...string) bool {
	if needle == "" {
		return true
	}
	return strings.Contains(strings.ToLower(strings.Join(fields, " ")), needle)
}

func lowerSearch(c *gin.Context) string { return strings.ToLower(strings.TrimSpace(c.Query("search"))) }

// --- Abandoned checkouts ---

const (
	abandonedTabPhone     = "phone"
	abandonedTabNoPhone   = "no-phone"
	abandonedTabRecovered = "recovered"
)

type abandonedCheckoutsPage struct {
	pageEnvelope[shopify.AbandonedCheckout]
	Shop string `json:"shop"`
	// Attribution says, per checkout id, whether a recovery was ours - see
	// winback_attribution.go. A MAP BESIDE THE ROWS rather than a field on
	// them: the rows are shopify.AbandonedCheckout, the integration's own
	// type, and Tensor's judgement about a sale does not belong inside
	// Shopify's description of a cart.
	Attribution map[string]string `json:"attribution,omitempty"`
}

// pageAbandonedCheckouts filters by the reachability tab, searches the fields
// the table searched, sorts as its column header asked (sort=key:dir, default
// newest first), counts the tabs over everything, and cuts one page.
func pageAbandonedCheckouts(
	c *gin.Context, items []shopify.AbandonedCheckout, shop string,
	p offsetPage, sentCodes map[string]string,
) {
	tab, needle := c.Query("tab"), lowerSearch(c)
	counts := map[string]int64{"": int64(len(items))}
	out := make([]shopify.AbandonedCheckout, 0, len(items))
	for _, ck := range items {
		hasPhone := strings.TrimSpace(ck.Phone) != ""
		if hasPhone {
			counts[abandonedTabPhone]++
		} else {
			counts[abandonedTabNoPhone]++
		}
		if ck.CompletedAt != nil {
			counts[abandonedTabRecovered]++
		}
		switch {
		case tab == abandonedTabPhone && !hasPhone,
			tab == abandonedTabNoPhone && hasPhone,
			tab == abandonedTabRecovered && ck.CompletedAt == nil:
			continue
		}
		skus := make([]string, 0, len(ck.LineItems))
		for _, li := range ck.LineItems {
			skus = append(skus, li.SKU)
		}
		if !matchesNeedle(needle, append([]string{ck.Name, ck.CustomerName, ck.Phone, ck.City, ck.Province}, skus...)...) {
			continue
		}
		out = append(out, ck)
	}
	sortAbandoned(out, c.DefaultQuery("sort", "abandoned:desc"))
	page := slicePage(out, p)
	page.Counts = counts
	// Only for the rows actually sent: the map exists to colour an arrow on
	// screen, and judging four hundred rows to render fifteen is work nobody
	// asked for.
	attribution := make(map[string]string, len(page.Items))
	for _, ck := range page.Items {
		attribution[ck.ID] = attributeRecovery(sentCodes[ck.ID], ck.DiscountCodes)
	}
	c.JSON(http.StatusOK, abandonedCheckoutsPage{
		pageEnvelope: page, Shop: shop, Attribution: attribution})
}

// sortAbandoned orders by the table's sortable columns. An empty or unknown
// sort keeps Shopify's order, as the table did with no column chosen.
func sortAbandoned(rows []shopify.AbandonedCheckout, spec string) {
	key, dir, _ := strings.Cut(spec, ":")
	var less func(a, b shopify.AbandonedCheckout) bool
	switch key {
	case "abandoned":
		less = func(a, b shopify.AbandonedCheckout) bool { return a.CreatedAt.Before(b.CreatedAt) }
	case "customer":
		less = func(a, b shopify.AbandonedCheckout) bool {
			return strings.ToLower(a.CustomerName) < strings.ToLower(b.CustomerName)
		}
	case "value":
		less = func(a, b shopify.AbandonedCheckout) bool { return amount(a.TotalAmount) < amount(b.TotalAmount) }
	default:
		return
	}
	sort.SliceStable(rows, func(i, j int) bool {
		if dir == "desc" {
			return less(rows[j], rows[i])
		}
		return less(rows[i], rows[j])
	})
}

func amount(raw string) float64 {
	v, _ := strconv.ParseFloat(strings.TrimSpace(raw), 64)
	return v
}

// --- Deliveries ---

type shipmentsPage struct {
	pageEnvelope[shipmentRowResponse]
	shipmentsListMeta
}

// shipmentsListMeta is everything the deliveries page shows besides the rows.
type shipmentsListMeta struct {
	Shop          string         `json:"shop"`
	Actionable    int            `json:"actionable"`
	Shipped       int            `json:"shipped"`
	Tracked       int            `json:"tracked"`
	OtherCarriers map[string]int `json:"other_carriers"`
	WindowDays    int            `json:"window_days"`
	GeneratedAt   string         `json:"generated_at"`
}

// reasonCounts is the reason buckets as tab badges, over the whole window.
func reasonCounts(counts map[string]int) map[string]int64 {
	out := make(map[string]int64, len(counts))
	for k, v := range counts {
		out[k] = int64(v)
	}
	return out
}

// shipmentMatches is the deliveries page's wide search: everything about a
// parcel an operator with a customer on the phone might type.
func shipmentMatches(row shipmentRowResponse, needle string) bool {
	titles := make([]string, 0, len(row.LineItems))
	for _, li := range row.LineItems {
		titles = append(titles, li.Title)
	}
	return matchesNeedle(needle, append([]string{
		row.OrderName, row.Waybill, row.CustomerName, row.Phone, row.City, row.State,
		row.Pincode, row.Instruction, row.ReasonLabel,
	}, titles...)...)
}

// --- BambuBuddy ---

// liveQueueOnly keeps the queue items still to run - what the Queued tab
// shows - and counts them.
func liveQueueOnly(items []queueItemResponse) []queueItemResponse {
	out := make([]queueItemResponse, 0, len(items))
	for _, it := range items {
		if it.Status == "pending" || it.Status == "printing" {
			out = append(out, it)
		}
	}
	return out
}
