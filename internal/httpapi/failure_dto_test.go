package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/google/uuid"

	"github.com/Optiminastic/tensor-core/internal/auth"
	"github.com/Optiminastic/tensor-core/internal/db/gen"
	"github.com/Optiminastic/tensor-core/internal/production"
)

// The frontend parses these responses with Zod, so a key that silently stops
// being emitted is a runtime parse failure on a page rather than a compile
// error here. These assert the exact wire keys the failure-visibility work
// added, against real seeded rows - a shape test that skips when nothing is
// seeded asserts nothing at all.

// requireKeys fails naming every missing key, so one run shows the whole gap.
func requireKeys(t *testing.T, what string, row map[string]json.RawMessage, keys ...string) {
	t.Helper()
	for _, key := range keys {
		if _, ok := row[key]; !ok {
			t.Errorf("%s response is missing %q", what, key)
		}
	}
}

func decodeRows(t *testing.T, body []byte) []map[string]json.RawMessage {
	t.Helper()
	var rows []map[string]json.RawMessage
	if err := json.Unmarshal(body, &rows); err != nil {
		t.Fatalf("decode: %v (body %s)", err, body)
	}
	if len(rows) == 0 {
		t.Fatalf("expected at least one seeded row, got none: %s", body)
	}
	return rows
}

func TestBatchListCarriesFailureKeys(t *testing.T) {
	store := setupStore(t)
	seedAll(t, store)
	minter := newTokenMinter(t)
	router := testServer(t, store, auth.NewGuards(minter.verifier, ""))

	manage := minter.mint(t, []string{"batch:manage", "batch:read"})
	if rr := doJSON(router, http.MethodPost, "/batches", manage, map[string]any{}); rr.Code != http.StatusCreated {
		t.Fatalf("create batch = %d body=%s", rr.Code, rr.Body.String())
	}

	rr := doJSON(router, http.MethodGet, "/batches", manage, nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("GET /batches = %d body=%s", rr.Code, rr.Body.String())
	}
	row := decodeRows(t, rr.Body.Bytes())[0]
	requireKeys(t, "batch", row, "plate_slice_error", "print_error", "queue_item_id")

	// Round-trip the failure. printBatch's own failure paths need a BambuBuddy
	// that refuses on demand, which there is no way to arrange, so the storage
	// and DTO halves are proven here and the handler wiring is a one-line call
	// to this same query.
	var id string
	_ = json.Unmarshal(row["id"], &id)
	batchID := uuid.MustParse(id)
	reason := "BambuBuddy rejected the plate: no printer of that model is online."
	if err := store.Q.SetBatchPrintError(context.Background(), gen.SetBatchPrintErrorParams{
		ID: batchID, PrintError: &reason,
	}); err != nil {
		t.Fatalf("set print error: %v", err)
	}

	rr = doJSON(router, http.MethodGet, "/batches", manage, nil)
	var got *string
	_ = json.Unmarshal(decodeRows(t, rr.Body.Bytes())[0]["print_error"], &got)
	if got == nil || *got != reason {
		t.Fatalf("print_error = %v, want %q", got, reason)
	}

	// A successful send clears it and records what to follow. A batch that
	// still showed its last failure after being sent would send an operator
	// chasing a problem that no longer exists.
	queueItem := int32(4242)
	if err := store.Q.ClearBatchPrintError(context.Background(), gen.ClearBatchPrintErrorParams{
		ID: batchID, QueueItemID: &queueItem,
	}); err != nil {
		t.Fatalf("clear print error: %v", err)
	}

	rr = doJSON(router, http.MethodGet, "/batches", manage, nil)
	row = decodeRows(t, rr.Body.Bytes())[0]
	var cleared *string
	_ = json.Unmarshal(row["print_error"], &cleared)
	if cleared != nil {
		t.Errorf("print_error = %q after a successful send, want null", *cleared)
	}
	var item *int32
	_ = json.Unmarshal(row["queue_item_id"], &item)
	if item == nil || *item != queueItem {
		t.Errorf("queue_item_id = %v, want %d", item, queueItem)
	}
}

func TestFleetListCarriesStatusReason(t *testing.T) {
	store := setupStore(t)
	seedAll(t, store)
	minter := newTokenMinter(t)
	router := testServer(t, store, auth.NewGuards(minter.verifier, ""))

	reason := "Printer fault 0500_0200_0002_0003. Look the code up on Bambu's HMS list."
	if _, err := store.Q.UpsertFleetMachineFromSource(context.Background(),
		gen.UpsertFleetMachineFromSourceParams{
			ID: uuid.New(), MachineID: "SERIAL-TEST-1", Name: "Test printer",
			Status: production.FleetMachineError, StatusReason: &reason,
			Filaments: []byte("[]"),
		}); err != nil {
		t.Fatalf("seed fleet machine: %v", err)
	}

	rr := doJSON(router, http.MethodGet, "/machine-fleet", minter.mint(t, []string{"machine:read"}), nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("GET /machine-fleet = %d body=%s", rr.Code, rr.Body.String())
	}
	row := decodeRows(t, rr.Body.Bytes())[0]
	requireKeys(t, "fleet machine", row, "status", "status_reason")

	// Round-tripped, not merely present: the column is new, so a mapping that
	// dropped it would still emit the key as null and look correct.
	var got string
	if err := json.Unmarshal(row["status_reason"], &got); err != nil || got != reason {
		t.Errorf("status_reason = %s, want %q", row["status_reason"], reason)
	}
	var status string
	_ = json.Unmarshal(row["status"], &status)
	if status != production.FleetMachineError {
		t.Errorf("status = %q, want %q", status, production.FleetMachineError)
	}
}

func TestJobQueueCarriesBatchingBlockedReason(t *testing.T) {
	store := setupStore(t)
	seedAll(t, store)
	minter := newTokenMinter(t)
	router := testServer(t, store, auth.NewGuards(minter.verifier, ""))

	seedBatchableJob(t, store)
	read := minter.mint(t, []string{"production:read", "production:update"})

	rr := doJSON(router, http.MethodGet, "/production-jobs", read, nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("GET /production-jobs = %d body=%s", rr.Code, rr.Body.String())
	}
	rows := decodeRows(t, rr.Body.Bytes())
	requireKeys(t, "job", rows[0], "batching_blocked_reason")

	// A batchable job is not blocked, so the reason must be absent rather than
	// a default sentence - the field's whole value is that it means something.
	var before *string
	_ = json.Unmarshal(rows[0]["batching_blocked_reason"], &before)
	if before != nil {
		t.Errorf("a batchable job reported %q, want null", *before)
	}

	var id string
	_ = json.Unmarshal(rows[0]["id"], &id)
	if rr := doJSON(router, http.MethodPatch, "/production-jobs/"+id, read,
		map[string]any{"held": true}); rr.Code != http.StatusOK {
		t.Fatalf("hold job = %d body=%s", rr.Code, rr.Body.String())
	}

	rr = doJSON(router, http.MethodGet, "/production-jobs", read, nil)
	var after *string
	_ = json.Unmarshal(decodeRows(t, rr.Body.Bytes())[0]["batching_blocked_reason"], &after)
	if after == nil {
		t.Fatal("a held job must explain why it will not be batched")
	}
	t.Logf("held job reports: %q", *after)
}
