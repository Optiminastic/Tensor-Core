package httpapi

// What the Queue button may and may not do, before anything is uploaded.
//
// The send half needs object storage and a BambuBuddy, neither of which exists
// in this environment (see testServer/NewServer - storage is always nil here).
// These cover the decisions QueueBatchForPrinting makes BEFORE that: which
// statuses it will act on, and who is allowed to lock a Draft on the way.

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/google/uuid"

	"github.com/Optiminastic/tensor-core/internal/auth"
	"github.com/Optiminastic/tensor-core/internal/production"
)

// A bed that is printing, or finished, is not something to queue.
//
// Checked on the batch's own status rather than on BambuBuddy's answer, so the
// refusal costs no upload and reads the same whether or not the printer host is
// reachable.
func TestIntegrationQueueRefusesAPrintingOrFinishedBatch(t *testing.T) {
	store := setupStore(t)
	seedAll(t, store)
	srv := testServerWithBatchQueue(t, store, nil, 1)
	ctx := context.Background()

	for _, c := range []struct {
		status string
		want   string
	}{
		{production.BatchInProgress, "This batch is already printing."},
		{production.BatchCompleted, "A completed batch cannot be queued."},
	} {
		t.Run(c.status, func(t *testing.T) {
			id := seedDraftBatch(t, store, "BATCH-Q-"+c.status, c.status, nil, "H2C")

			_, err := srv.QueueBatchForPrinting(ctx, id, "usr_test")
			if err == nil {
				t.Fatalf("a %s batch was queued; it must be refused", c.status)
			}
			var se *statusError
			if !errors.As(err, &se) {
				t.Fatalf("error = %v, want a status error", err)
			}
			if se.Status() != http.StatusConflict {
				t.Errorf("status = %d, want 409", se.Status())
			}
			if se.Error() != c.want {
				t.Errorf("detail = %q, want %q", se.Error(), c.want)
			}
		})
	}
}

// A batch that does not exist is a 404, not a 500.
func TestIntegrationQueueOnAMissingBatchIs404(t *testing.T) {
	store := setupStore(t)
	seedAll(t, store)
	srv := testServerWithBatchQueue(t, store, nil, 1)

	_, err := srv.QueueBatchForPrinting(context.Background(), uuid.New(), "usr_test")
	var se *statusError
	if !errors.As(err, &se) || se.Status() != http.StatusNotFound {
		t.Fatalf("error = %v, want 404", err)
	}
}

// Locking a Draft needs batch:manage as well as machine:manage.
//
// The route is guarded on machine:manage, because sending a plate is an
// operational act on a printer. Locking is not: it freezes a bed and reserves
// its filament, which is a batch edit. Before the Queue button existed the two
// could not meet, and letting machine:manage alone lock a bed would quietly
// widen a role - the separation internal/auth/catalog_test.go treats as spec.
//
// The check has to live in the handler rather than in middleware because it
// depends on the batch's status, which the router does not know.
func TestIntegrationQueueingADraftNeedsBatchManage(t *testing.T) {
	store := setupStore(t)
	seedAll(t, store)
	minter := newTokenMinter(t)
	router := testServer(t, store, auth.NewGuards(minter.verifier, ""))

	draft := seedDraftBatch(t, store, "BATCH-Q-DRAFT", production.BatchPendingApproval, nil, "H2C")
	locked := seedDraftBatch(t, store, "BATCH-Q-LOCKED", production.BatchOpen, nil, "H2C")

	machineOnly := minter.mint(t, []string{"machine:manage"})

	// The draft is refused for want of the batch permission, and refused
	// BEFORE storage is consulted - a 403 here rather than the 503 the locked
	// bed gets proves the order.
	if rr := doJSON(router, http.MethodPost, "/batches/"+draft.String()+"/print", machineOnly, nil); rr.Code != http.StatusForbidden {
		t.Errorf("draft with machine:manage only = %d body=%s, want 403",
			rr.Code, rr.Body.String())
	}

	// The same token on a locked bed passes the permission check and goes on to
	// fail for want of object storage, which is unconfigured here.
	if rr := doJSON(router, http.MethodPost, "/batches/"+locked.String()+"/print", machineOnly, nil); rr.Code == http.StatusForbidden {
		t.Errorf("locked bed with machine:manage = 403; sending a locked bed "+
			"must not need batch:manage (body=%s)", rr.Body.String())
	}

	// With both permissions the draft gets past the guard too.
	both := minter.mint(t, []string{"machine:manage", "batch:manage"})
	if rr := doJSON(router, http.MethodPost, "/batches/"+draft.String()+"/print", both, nil); rr.Code == http.StatusForbidden {
		t.Errorf("draft with both permissions = 403 (body=%s)", rr.Body.String())
	}
}
