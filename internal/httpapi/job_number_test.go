package httpapi

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/Optiminastic/tensor-core/internal/auth"
)

// The job number is the thing an operator reads out to a customer, so it has to
// be the order's number - and it has to stay unique, because job_number carries
// a UNIQUE index and 10 of the 199 live orders hold more than one product.
func TestJobNumberForOrder(t *testing.T) {
	for _, c := range []struct {
		name        string
		orderNumber string
		index       int
		want        string
	}{
		{"single product", "T3DPS-114552", 0, "JOB-114552"},
		{"another", "T3DPS-114734", 0, "JOB-114734"},
		// One order, four products: they cannot all be JOB-114666.
		{"second of four", "T3DPS-114666", 1, "JOB-114666-2"},
		{"third of four", "T3DPS-114666", 2, "JOB-114666-3"},
		{"fourth of four", "T3DPS-114666", 3, "JOB-114666-4"},
		// The store's prefix is not stable - one live order is numbered
		// "T3PS-114743", missing a letter - so only the digits are read.
		{"malformed prefix", "T3PS-114743", 0, "JOB-114743"},
		{"no prefix at all", "114743", 0, "JOB-114743"},
		{"leading hash", "#1006", 0, "JOB-1006"},
		{"surrounding space", "  T3DPS-114552  ", 0, "JOB-114552"},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := jobNumberForOrder(c.orderNumber, c.index); got != c.want {
				t.Errorf("jobNumberForOrder(%q, %d) = %q, want %q",
					c.orderNumber, c.index, got, c.want)
			}
		})
	}
}

// An order number with nothing to borrow falls back to the sequence rather than
// producing "JOB-" or an empty number. A job nobody can trace is still better
// than an order whose jobs were never created.
func TestJobNumberFallsBackWithoutDigits(t *testing.T) {
	for _, orderNumber := range []string{"", "   ", "DRAFT", "T3DPS-"} {
		if got := jobNumberForOrder(orderNumber, 0); got != "" {
			t.Errorf("jobNumberForOrder(%q) = %q, want \"\" so the caller mints one",
				orderNumber, got)
		}
	}
}

// Every product on one order must get a distinct number, or the insert fails on
// the unique index and the whole order's job creation rolls back.
func TestJobNumbersAreDistinctWithinAnOrder(t *testing.T) {
	seen := map[string]bool{}
	for i := range 4 {
		n := jobNumberForOrder("T3DPS-114666", i)
		if seen[n] {
			t.Fatalf("product %d reused job number %q", i, n)
		}
		seen[n] = true
	}
}

// A job detail URL should carry the number a person reads off a plank, not a
// uuid. Both must resolve, because every internal link still uses the uuid.
func TestFindProductionJobAcceptsEitherIdentifier(t *testing.T) {
	store := setupStore(t)
	seedAll(t, store)
	minter := newTokenMinter(t)
	router := testServer(t, store, auth.NewGuards(minter.verifier, ""))
	read := minter.mint(t, []string{"production:read"})

	seedBatchableJob(t, store)
	rr := doJSON(router, http.MethodGet, "/production-jobs", read, nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("list = %d body=%s", rr.Code, rr.Body.String())
	}
	var rows []struct {
		ID        string `json:"id"`
		JobNumber string `json:"job_number"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &rows); err != nil || len(rows) == 0 {
		t.Fatalf("decode list: %v (body %s)", err, rr.Body.String())
	}
	job := rows[0]

	for _, ref := range []string{job.ID, job.JobNumber} {
		got := doJSON(router, http.MethodGet, "/production-jobs/"+ref, read, nil)
		if got.Code != http.StatusOK {
			t.Fatalf("GET by %q = %d body=%s", ref, got.Code, got.Body.String())
		}
		var one struct {
			ID string `json:"id"`
		}
		_ = json.Unmarshal(got.Body.Bytes(), &one)
		if one.ID != job.ID {
			t.Errorf("GET by %q returned job %s, want %s", ref, one.ID, job.ID)
		}
	}

	// An identifier matching neither must 404 rather than 500.
	if rr := doJSON(router, http.MethodGet, "/production-jobs/JOB-999999", read, nil); rr.Code != http.StatusNotFound {
		t.Errorf("GET an unknown job number = %d, want 404", rr.Code)
	}
}
