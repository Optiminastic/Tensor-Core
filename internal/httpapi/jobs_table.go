package httpapi

import (
	"context"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/Optiminastic/tensor-core/internal/db/gen"
)

// The job tables, one page at a time: GET /production-jobs?page=N.
//
// Two pages read jobs as tables - Production Jobs, and the four station queues
// on Packaging - and both used to fetch every job in the system to show
// fifteen. Each filter they applied in the browser is now a clause of
// CountProductionJobsTable, applied before LIMIT/OFFSET.

const (
	jobsTabNonDnp = "non-dnp"
	jobsTabAll    = ""
)

// The packaging page's station queues, as the `station` parameter names them.
var jobStations = map[string]bool{"assembly": true, "finishing": true, "qc": true, "packaging": true}

// generatedKeySep joins a job's sku and product name into one key. chr(31),
// the unit separator, cannot appear in either.
const generatedKeySep = "\x1f"

// nonGeneratedKeys is every (sku, product name) pair on a job that
// IsGeneratedProduct says Tensor does not make, as sku+sep+name keys.
//
// Read from the distinct pairs, a few dozen, rather than from every job: the
// rule runs in Go, where it lives, and only its verdicts reach the query.
func (s *Server) nonGeneratedKeys(ctx context.Context) ([]string, error) {
	pairs, err := s.store.Q.ListJobProductKeys(ctx)
	if err != nil {
		return nil, err
	}
	out := []string{}
	for _, p := range pairs {
		if !IsGeneratedProduct(p.Sku, p.ProductName) {
			out = append(out, p.Sku+generatedKeySep+p.ProductName)
		}
	}
	return out, nil
}

func (s *Server) listJobsTable(c *gin.Context, p offsetPage) {
	window, ok := parseTimeRange(c)
	if !ok {
		return
	}
	station := nilIfEmpty(c.Query("station"))
	if station != nil && !jobStations[*station] {
		detail(c, http.StatusUnprocessableEntity, "'station' must be one of assembly, finishing, qc or packaging.")
		return
	}
	ctx := c.Request.Context()
	tab := c.Query("tab")
	withCounts := wantsCounts(c)

	var keys []string
	if tab == jobsTabNonDnp || withCounts {
		var err error
		if keys, err = s.nonGeneratedKeys(ctx); err != nil {
			detail(c, http.StatusInternalServerError, "Could not list production jobs.")
			return
		}
	}

	f := gen.CountProductionJobsTableParams{
		Search: searchParam(c), CreatedFrom: window.From, CreatedTo: window.To,
		Personalisation: nilIfEmpty(c.Query("personalisation")),
		Packaging:       nilIfEmpty(c.Query("packaging")),
		Station:         station,
		NgKeys:          []string{},
	}
	switch tab {
	case jobsTabAll:
	case jobsTabNonDnp:
		yes := true
		f.NonGeneratedOnly, f.NgKeys = &yes, keys
	default:
		// Any other tab is one of the table's status values (completed,
		// failed, on_hold, in_progress, queued), compared to the derived
		// status - an unknown one matches nothing, as it did in the browser.
		f.QueueStatus = &tab
	}

	out, err := s.jobsTablePage(ctx, f, p)
	if err != nil {
		detail(c, http.StatusInternalServerError, "Could not list production jobs.")
		return
	}
	if withCounts {
		counts, err := s.store.Q.CountProductionJobsByTab(ctx, keys)
		if err != nil {
			detail(c, http.StatusInternalServerError, "Could not count production jobs.")
			return
		}
		out.Counts = map[string]int64{
			jobsTabAll: counts.AllJobs, jobsTabNonDnp: counts.NonDnp, "completed": counts.Completed,
			"assembly": counts.Assembly, "finishing": counts.Finishing, "qc": counts.Qc,
			"packaging": counts.Packaging,
		}
	}
	c.JSON(http.StatusOK, out)
}

// jobsTablePage counts the filtered jobs, clamps the page, reads it, and
// shapes it with the same DTO the whole list uses - whose order, batch and
// dispatch lookups are batched per page, not per row.
func (s *Server) jobsTablePage(
	ctx context.Context, f gen.CountProductionJobsTableParams, p offsetPage,
) (pageEnvelope[productionJobResponse], error) {
	total, err := s.store.Q.CountProductionJobsTable(ctx, f)
	if err != nil {
		return pageEnvelope[productionJobResponse]{}, err
	}
	p = p.within(total)
	rows, err := s.store.Q.ListProductionJobsTable(ctx, gen.ListProductionJobsTableParams{
		Search: f.Search, CreatedFrom: f.CreatedFrom, CreatedTo: f.CreatedTo,
		QueueStatus: f.QueueStatus, NonGeneratedOnly: f.NonGeneratedOnly, NgKeys: f.NgKeys,
		Personalisation: f.Personalisation, Packaging: f.Packaging, Station: f.Station,
		PageLimit: p.Size, PageOffset: p.Offset(),
	})
	if err != nil {
		return pageEnvelope[productionJobResponse]{}, err
	}
	jobs := make([]gen.ProductionJob, len(rows))
	for i, r := range rows {
		jobs[i] = gen.ProductionJob(r)
	}
	return newPageEnvelope(s.productionJobsDTO(ctx, jobs), total, p), nil
}
