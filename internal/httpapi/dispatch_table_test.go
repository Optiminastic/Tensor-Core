package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"github.com/google/uuid"

	"github.com/Optiminastic/tensor-core/internal/auth"
	"github.com/Optiminastic/tensor-core/internal/db/gen"
	"github.com/Optiminastic/tensor-core/internal/production"
)

// The Dispatch tab: shipments paged in Postgres, and the orders ready to book
// found by one grouped query instead of a browser join of everything.

func seedPackedJob(t *testing.T, ctx context.Context, q *gen.Queries, orderID uuid.UUID, n int, packed bool) {
	t.Helper()
	status := production.PackagingPending
	if packed {
		status = "packaged"
	}
	if _, err := q.InsertProductionJob(ctx, gen.InsertProductionJobParams{
		ID: uuid.New(), JobNumber: fmt.Sprintf("DJ-%03d", n), OrderID: &orderID, Description: "Plank",
		Quantity: 1, Status: production.StatusCompleted, AssemblyStatus: "completed",
		QcStatus: "passed", PackagingStatus: status,
		PersonalisationStatus: production.PersonalisationNotRequired, Colours: []byte("[]"),
	}); err != nil {
		t.Fatalf("insert job: %v", err)
	}
}

func TestIntegrationDispatchTableAndReadyOrders(t *testing.T) {
	store := setupStore(t)
	seedAll(t, store)
	ctx := context.Background()
	if _, err := store.Pool.Exec(ctx, `TRUNCATE dispatch_orders`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	minter := newTokenMinter(t)
	router := testServer(t, store, auth.NewGuards(minter.verifier, ""))
	token := minter.mint(t, []string{auth.OrderRead.Key(), auth.DispatchRead.Key()})

	// ready: every job packed, nothing booked.
	ready := seedOrder(t, store, 7001, nil)
	seedPackedJob(t, ctx, store.Q, ready, 1, true)
	seedPackedJob(t, ctx, store.Q, ready, 2, true)
	// half: one job still to pack.
	half := seedOrder(t, store, 7002, nil)
	seedPackedJob(t, ctx, store.Q, half, 3, true)
	seedPackedJob(t, ctx, store.Q, half, 4, false)
	// booked: packed, but a shipment already exists.
	booked := seedOrder(t, store, 7003, nil)
	seedPackedJob(t, ctx, store.Q, booked, 5, true)
	// empty: no jobs at all - never ready.
	seedOrder(t, store, 7004, nil)

	// Twenty shipments against the booked order, for the paged table.
	for i := 0; i < 20; i++ {
		if _, err := store.Q.InsertDispatch(ctx, gen.InsertDispatchParams{
			ID: uuid.New(), OrderID: booked, Status: "pending",
		}); err != nil {
			t.Fatalf("insert dispatch: %v", err)
		}
	}

	rr := doJSON(router, http.MethodGet, "/orders?source=dummy&ready_to_dispatch=true", token, nil)
	var readyOrders []readyOrderResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &readyOrders); err != nil {
		t.Fatalf("decode ready orders (%d): %v %s", rr.Code, err, rr.Body.String())
	}
	if len(readyOrders) != 1 || readyOrders[0].ID != ready.String() || readyOrders[0].JobCount != 2 {
		t.Fatalf("ready orders = %+v, want only the fully packed, unbooked order with 2 jobs", readyOrders)
	}

	var p1, p2 struct {
		Items []struct {
			ID          string  `json:"id"`
			OrderNumber *string `json:"order_number"`
		} `json:"items"`
		Total      int64 `json:"total"`
		TotalPages int32 `json:"total_pages"`
	}
	rr = doJSON(router, http.MethodGet, "/dispatch-orders?page=1", token, nil)
	if err := json.Unmarshal(rr.Body.Bytes(), &p1); err != nil || len(p1.Items) != 15 || p1.Total != 20 || p1.TotalPages != 2 {
		t.Fatalf("dispatch page 1 = %d rows, total %d (%v) %s", len(p1.Items), p1.Total, err, rr.Body.String())
	}
	if p1.Items[0].OrderNumber == nil {
		t.Error("a dispatch row arrived without its order number")
	}
	rr = doJSON(router, http.MethodGet, "/dispatch-orders?page=2", token, nil)
	if err := json.Unmarshal(rr.Body.Bytes(), &p2); err != nil || len(p2.Items) != 5 {
		t.Fatalf("dispatch page 2 = %d rows (%v)", len(p2.Items), err)
	}

	// Unpaged, the bare array as before.
	var all []json.RawMessage
	rr = doJSON(router, http.MethodGet, "/dispatch-orders", token, nil)
	if err := json.Unmarshal(rr.Body.Bytes(), &all); err != nil || len(all) != 20 {
		t.Errorf("unpaged dispatch = %d rows (%v), want 20", len(all), err)
	}
}
