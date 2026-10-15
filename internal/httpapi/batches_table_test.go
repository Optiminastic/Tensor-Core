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

// The batch tables' numbered pages - Batches and Machine Management's Queued
// list - against a real Postgres, including both of their orderings.

type batchPage struct {
	Items []struct {
		ID          string `json:"id"`
		BatchNumber string `json:"batch_number"`
		Status      string `json:"status"`
		HasPriority bool   `json:"has_priority"`
	} `json:"items"`
	Total  int64            `json:"total"`
	Page   int32            `json:"page"`
	Counts map[string]int64 `json:"counts"`
}

func getBatchPage(t *testing.T, router http.Handler, token, query string) batchPage {
	t.Helper()
	rr := doJSON(router, http.MethodGet, "/batches?"+query, token, nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("GET /batches?%s = %d: %s", query, rr.Code, rr.Body.String())
	}
	var out batchPage
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode %s: %v", query, err)
	}
	return out
}

func TestIntegrationBatchesTablePages(t *testing.T) {
	store := setupStore(t)
	seedAll(t, store)
	ctx := context.Background()
	minter := newTokenMinter(t)
	router := testServer(t, store, auth.NewGuards(minter.verifier, ""))
	token := minter.mint(t, []string{auth.BatchRead.Key()})

	// 30 beds, numbered B-1001..B-1030. Index i decides everything:
	//   i < 10       completed
	//   10 <= i < 20 open
	//   20 <= i < 25 pending_approval (drafts)
	//   25 <= i      in_progress
	// every seventh bed carries a priority plank; every fourth is short.
	statusOf := func(i int) string {
		switch {
		case i < 10:
			return production.BatchCompleted
		case i < 20:
			return production.BatchOpen
		case i < 25:
			return "pending_approval"
		default:
			return production.BatchInProgress
		}
	}
	for i := 0; i < 30; i++ {
		b, err := store.Q.InsertBatch(ctx, gen.InsertBatchParams{
			ID: uuid.New(), BatchNumber: fmt.Sprintf("B-%d", 1001+i),
			Status: statusOf(i), MaterialShortage: i%4 == 0,
		})
		if err != nil {
			t.Fatalf("insert batch %d: %v", i, err)
		}
		priority := int32(NormalRank)
		if i%7 == 0 {
			priority = PriorityRank
		}
		j, err := store.Q.InsertProductionJob(ctx, gen.InsertProductionJobParams{
			ID: uuid.New(), JobNumber: fmt.Sprintf("BJ-%d", i), BatchID: &b.ID, Description: "Plank",
			Quantity: 1, Status: production.StatusQueued, AssemblyStatus: production.AssemblyPending,
			QcStatus: production.QcPending, PackagingStatus: production.PackagingPending,
			PersonalisationStatus: production.PersonalisationNotRequired, Colours: []byte("[]"),
		})
		if err != nil {
			t.Fatalf("insert job %d: %v", i, err)
		}
		if _, err := store.Pool.Exec(ctx, `UPDATE production_jobs SET priority = $2 WHERE id = $1`, j.ID, priority); err != nil {
			t.Fatalf("priority %d: %v", i, err)
		}
	}

	// Pending (the default tab): 20 beds, priority first, then newest number.
	p1 := getBatchPage(t, router, token, "page=1")
	if p1.Total != 20 || len(p1.Items) != 15 {
		t.Fatalf("pending page 1 = %d rows of %d", len(p1.Items), p1.Total)
	}
	// Pending priority beds are i = 14, 21, 28: they lead, newest number first.
	for k, want := range []string{"B-1029", "B-1022", "B-1015"} {
		if p1.Items[k].BatchNumber != want || !p1.Items[k].HasPriority {
			t.Errorf("pending row %d = %s (priority %v), want %s first", k, p1.Items[k].BatchNumber, p1.Items[k].HasPriority, want)
		}
	}
	if p1.Items[3].BatchNumber != "B-1030" {
		t.Errorf("after the priority beds, row 3 = %s, want the newest, B-1030", p1.Items[3].BatchNumber)
	}
	if p2 := getBatchPage(t, router, token, "page=2&counts=false"); len(p2.Items) != 5 {
		t.Errorf("pending page 2 = %d rows, want 5", len(p2.Items))
	}

	for tab, want := range map[string]int64{"pending": 20, "completed": 10, "queued": 20} {
		if p1.Counts[tab] != want {
			t.Errorf("count[%s] = %d, want %d", tab, p1.Counts[tab], want)
		}
	}
	if got := getBatchPage(t, router, token, "page=1&tab=completed"); got.Total != 10 {
		t.Errorf("completed = %d, want 10", got.Total)
	}
	// Short beds (i % 4 == 0) among the pending ones: 12, 16, 20, 24, 28.
	if got := getBatchPage(t, router, token, "page=1&shortage=yes"); got.Total != 5 {
		t.Errorf("pending and short = %d, want 5", got.Total)
	}
	if got := getBatchPage(t, router, token, "page=1&tab=completed&search=B-1003"); got.Total != 1 {
		t.Errorf("search for one completed bed found %d", got.Total)
	}

	// Queued: drafts, then locked, then printing; priority first within a
	// stage; oldest number first. Drafts are i = 20..24, priority i = 21.
	q := getBatchPage(t, router, token, "page=1&tab=queued&page_size=8")
	want := []string{"B-1022", "B-1021", "B-1023", "B-1024", "B-1025", "B-1015", "B-1011", "B-1012"}
	for k, w := range want {
		if q.Items[k].BatchNumber != w {
			t.Errorf("queued row %d = %s (%s), want %s", k, q.Items[k].BatchNumber, q.Items[k].Status, w)
		}
	}
	if q.Total != 20 {
		t.Errorf("queued total = %d, want 20 (completed beds excluded)", q.Total)
	}

	if rr := doJSON(router, http.MethodGet, "/batches?page=1&tab=nowhere", token, nil); rr.Code != http.StatusUnprocessableEntity {
		t.Errorf("an unknown tab answered %d, want 422", rr.Code)
	}
	var all []json.RawMessage
	rr := doJSON(router, http.MethodGet, "/batches", token, nil)
	if err := json.Unmarshal(rr.Body.Bytes(), &all); err != nil || len(all) != 30 {
		t.Errorf("unpaged /batches = %d rows (%v), want 30", len(all), err)
	}
}
