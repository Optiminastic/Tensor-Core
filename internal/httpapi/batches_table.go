package httpapi

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/Optiminastic/tensor-core/internal/db/gen"
	"github.com/Optiminastic/tensor-core/internal/obs"
)

// The batch tables, one page at a time: GET /batches?page=N.
//
// The Batches page and Machine Management's Queued list both fetched every
// batch to show fifteen. Their tabs, the Material select, search and the
// period now run in CountBatchesTable before LIMIT/OFFSET.

const (
	batchesTabPending   = "pending"
	batchesTabCompleted = "completed"
	// batchesTabQueued is Machine Management's list: every bed yet to print.
	batchesTabQueued = "queued"
)

// queuedBatchStatuses are the beds yet to print, in the order work moves.
var queuedBatchStatuses = []string{"pending_approval", "open", "in_progress"}

// batchesTableFilter turns the request's tab and select into the query's
// filters. ok is false - with a 422 written - for a value it cannot read.
func batchesTableFilter(c *gin.Context, window timeRange) (gen.CountBatchesTableParams, bool, bool) {
	f := gen.CountBatchesTableParams{
		Search: searchParam(c), CreatedFrom: window.From, CreatedTo: window.To,
	}
	yes, no := true, false
	queueOrder := false
	switch c.Query("tab") {
	case "", batchesTabPending:
		// The Batches table opens on Pending, so an absent tab means it.
		f.Finished = &no
	case batchesTabCompleted:
		f.Finished = &yes
	case batchesTabQueued:
		f.Statuses, queueOrder = queuedBatchStatuses, true
	default:
		detail(c, http.StatusUnprocessableEntity, "'tab' must be pending, completed or queued.")
		return f, false, false
	}
	switch c.Query("shortage") {
	case "":
	case "yes":
		f.Shortage = &yes
	case "no":
		f.Shortage = &no
	default:
		detail(c, http.StatusUnprocessableEntity, "'shortage' must be yes or no.")
		return f, false, false
	}
	return f, queueOrder, true
}

func (s *Server) listBatchesTable(c *gin.Context, p offsetPage) {
	window, ok := parseTimeRange(c)
	if !ok {
		return
	}
	f, queueOrder, ok := batchesTableFilter(c, window)
	if !ok {
		return
	}
	ctx := c.Request.Context()
	total, err := s.store.Q.CountBatchesTable(ctx, f)
	if err != nil {
		obs.FromContext(ctx).Error("could not count batches (table)", "error", err)
		detail(c, http.StatusInternalServerError, "Could not list batches.")
		return
	}
	p = p.within(total)
	rows, err := s.store.Q.ListBatchesTable(ctx, gen.ListBatchesTableParams{
		Search: f.Search, CreatedFrom: f.CreatedFrom, CreatedTo: f.CreatedTo,
		Finished: f.Finished, Shortage: f.Shortage, Statuses: f.Statuses,
		QueueOrder: queueOrder, PageLimit: p.Size, PageOffset: p.Offset(),
	})
	if err != nil {
		obs.FromContext(ctx).Error("could not list batches (table)", "error", err)
		detail(c, http.StatusInternalServerError, "Could not list batches.")
		return
	}
	items := make([]batchResponse, 0, len(rows))
	for _, b := range rows {
		items = append(items, s.batchDTO(b))
	}
	// The order tags, colours and priority flags, for this page's beds only -
	// one batched read, as the whole list does it.
	out := newPageEnvelope(s.decorateBatches(ctx, items, batchIDsOf(rows)), total, p)
	if wantsCounts(c) {
		counts, err := s.store.Q.CountBatchesByTab(ctx)
		if err != nil {
			detail(c, http.StatusInternalServerError, "Could not count batches.")
			return
		}
		out.Counts = map[string]int64{
			batchesTabPending: counts.Pending, batchesTabCompleted: counts.Completed,
			batchesTabQueued: counts.Queued,
		}
	}
	c.JSON(http.StatusOK, out)
}
