package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/Optiminastic/tensor-core/internal/auth"
	"github.com/Optiminastic/tensor-core/internal/db/gen"
	"github.com/Optiminastic/tensor-core/internal/production"
)

// The job tables' numbered pages - Production Jobs and the packaging stations -
// against a real Postgres. Each station is checked against the filter the
// packaging page used to apply to the whole job list in the browser.

type jobPage struct {
	Items []struct {
		ID          string  `json:"id"`
		JobNumber   string  `json:"job_number"`
		BatchNumber *string `json:"batch_number"`
	} `json:"items"`
	Total      int64            `json:"total"`
	Page       int32            `json:"page"`
	TotalPages int32            `json:"total_pages"`
	Counts     map[string]int64 `json:"counts"`
}

func getJobPage(t *testing.T, router http.Handler, token, query string) jobPage {
	t.Helper()
	rr := doJSON(router, http.MethodGet, "/production-jobs?"+query, token, nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("GET /production-jobs?%s = %d: %s", query, rr.Code, rr.Body.String())
	}
	var out jobPage
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode %s: %v", query, err)
	}
	return out
}

// tableJob is one fixture job's shape.
type tableJob struct {
	status, assembly, finishing, qc, packaging string
	held                                       bool
	sku, product                               string
	batch                                      *uuid.UUID
}

func seedTableJob(t *testing.T, ctx context.Context, store interface {
	InsertProductionJob(context.Context, gen.InsertProductionJobParams) (gen.ProductionJob, error)
}, i int, j tableJob) uuid.UUID {
	t.Helper()
	sku, product := j.sku, j.product
	row, err := store.InsertProductionJob(ctx, gen.InsertProductionJobParams{
		ID: uuid.New(), JobNumber: fmt.Sprintf("JOB-%03d", i), Description: "Plank " + fmt.Sprint(i),
		Quantity: 1, Status: j.status, AssemblyStatus: j.assembly,
		QcStatus: j.qc, PackagingStatus: j.packaging, BatchID: j.batch,
		PersonalisationStatus: production.PersonalisationNotRequired, Colours: []byte("[]"),
		Sku: &sku, ProductName: &product,
	})
	if err != nil {
		t.Fatalf("insert job %d: %v", i, err)
	}
	return row.ID
}

func TestIntegrationJobsTablePages(t *testing.T) {
	store := setupStore(t)
	seedAll(t, store)
	ctx := context.Background()
	minter := newTokenMinter(t)
	router := testServer(t, store, auth.NewGuards(minter.verifier, ""))
	token := minter.mint(t, []string{auth.ProductionRead.Key()})

	done, err := store.Q.InsertBatch(ctx, gen.InsertBatchParams{
		ID: uuid.New(), BatchNumber: "B-DONE", Status: production.BatchCompleted,
	})
	if err != nil {
		t.Fatalf("insert batch: %v", err)
	}

	// 30 jobs. Index i decides each field, so every expectation below is
	// arithmetic on i rather than a hand-kept list.
	const n = 30
	for i := 0; i < n; i++ {
		j := tableJob{
			status: production.StatusQueued, assembly: "pending", finishing: "pending",
			qc: "pending", packaging: "pending", sku: "DNP-RED", product: "Dual Name Plank",
		}
		switch {
		case i < 10: // completed, assembly pending -> Assembly
			j.status = production.StatusCompleted
		case i < 14: // completed, assembled, finishing pending -> Finishing
			j.status, j.assembly = production.StatusCompleted, "completed"
		case i < 17: // completed, assembled, finished, qc pending -> QC
			j.status, j.assembly, j.finishing = production.StatusCompleted, "completed", "completed"
		case i < 19: // qc passed, packaging pending -> Packaging
			j.status, j.assembly, j.finishing, j.qc = production.StatusCompleted, "completed", "completed", "passed"
		case i == 19: // queued, but on a completed batch -> Assembly
			j.batch = &done.ID
		}
		if i%6 == 0 {
			j.sku, j.product = "KC-01", "Keychain" // not generated -> Non-DNP
		}
		id := seedTableJob(t, ctx, store.Q, i, j)
		if _, err := store.Pool.Exec(ctx,
			`UPDATE production_jobs SET finishing_status = $2, held = $3, created_at = $4 WHERE id = $1`,
			id, j.finishing, i == 25, time.Date(2026, 9, 1, i, 0, 0, 0, time.UTC)); err != nil {
			t.Fatalf("shape job %d: %v", i, err)
		}
	}

	p1 := getJobPage(t, router, token, "page=1")
	if len(p1.Items) != 15 || p1.Total != n || p1.TotalPages != 2 {
		t.Fatalf("page 1 = %d rows, total %d, %d pages", len(p1.Items), p1.Total, p1.TotalPages)
	}
	p2 := getJobPage(t, router, token, "page=2&counts=false")
	seen := map[string]bool{}
	for _, it := range p1.Items {
		seen[it.ID] = true
	}
	for _, it := range p2.Items {
		if seen[it.ID] {
			t.Errorf("job %s is on both pages", it.JobNumber)
		}
	}
	if len(p2.Items) != 15 || p2.Counts != nil {
		t.Errorf("page 2 = %d rows, counts %v", len(p2.Items), p2.Counts)
	}

	// Badges. Completed excludes the held one, as the table's status does.
	want := map[string]int64{
		"": 30, "non-dnp": 5, "completed": 19, "assembly": 11, "finishing": 4, "qc": 3, "packaging": 2,
	}
	for tab, w := range want {
		if got := p1.Counts[tab]; got != w {
			t.Errorf("count[%q] = %d, want %d", tab, got, w)
		}
	}

	// Each tab and station returns exactly its badge's rows.
	for query, w := range map[string]int64{
		"tab=completed": 19, "tab=non-dnp": 5, "tab=on_hold": 1, "tab=queued": 10,
		"station=assembly": 11, "station=finishing": 4, "station=qc": 3, "station=packaging": 2,
		"packaging=packed": 0, "packaging=pending": 30,
	} {
		if got := getJobPage(t, router, token, "page=1&"+query); got.Total != w {
			t.Errorf("%s: total %d, want %d", query, got.Total, w)
		}
	}

	// The batch number rides on the row; search and the period run in SQL.
	assembly := getJobPage(t, router, token, "page=1&station=assembly&search=JOB-019")
	if assembly.Total != 1 || assembly.Items[0].BatchNumber == nil || *assembly.Items[0].BatchNumber != "B-DONE" {
		t.Errorf("JOB-019 on the assembly queue = %+v, want it labelled B-DONE", assembly)
	}
	from := time.Date(2026, 9, 1, 5, 0, 0, 0, time.UTC).Format(time.RFC3339)
	to := time.Date(2026, 9, 1, 9, 0, 0, 0, time.UTC).Format(time.RFC3339)
	if got := getJobPage(t, router, token, "page=1&from="+from+"&to="+to); got.Total != 5 {
		t.Errorf("a five-hour window found %d jobs, want 5", got.Total)
	}

	if rr := doJSON(router, http.MethodGet, "/production-jobs?page=1&station=nowhere", token, nil); rr.Code != http.StatusUnprocessableEntity {
		t.Errorf("an unknown station answered %d, want 422", rr.Code)
	}

	// Without page, the whole bare list, as every other consumer expects.
	var all []json.RawMessage
	rr := doJSON(router, http.MethodGet, "/production-jobs", token, nil)
	if err := json.Unmarshal(rr.Body.Bytes(), &all); err != nil || len(all) != n {
		t.Errorf("unpaged /production-jobs = %d rows (%v), want all %d", len(all), err, n)
	}
}
