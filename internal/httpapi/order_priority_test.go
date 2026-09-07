package httpapi

// Which orders jump the queue, and what that does to the planning pool.
//
// The rank convention is load-bearing in two places that sort opposite-looking
// ways - the planner here and min(j.priority) ASC in the machine scheduler - so
// these pin "lower is more urgent" rather than leaving it to a comment.

import (
	"context"
	"testing"
	"time"

	"github.com/Optiminastic/tensor-core/internal/auth"
	"github.com/Optiminastic/tensor-core/internal/db/gen"
	"github.com/Optiminastic/tensor-core/internal/production"
)

func TestOrderIsPriority(t *testing.T) {
	ship := func(s string) *string { return &s }

	for _, c := range []struct {
		name  string
		order gen.Order
		want  bool
	}{
		{"the store's current wording", gen.Order{ShippingTitle: ship("PRIORITY DISPATCH ⚡️")}, true},
		// The wording has already changed once, which is why this is a
		// substring match and not an equality check against the line above.
		{"reworded", gen.Order{ShippingTitle: ship("Priority Shipping")}, true},
		{"lower case", gen.Order{ShippingTitle: ship("priority dispatch")}, true},
		{"the standard option", gen.Order{ShippingTitle: ship("FREE DISPATCH - BEST DEAL 🎉")}, false},
		{"some other paid option", gen.Order{ShippingTitle: ship("Express Delivery")}, false},
		{"no shipping option at all", gen.Order{}, false},
		{"empty", gen.Order{ShippingTitle: ship("")}, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := OrderIsPriority(c.order); got != c.want {
				t.Errorf("OrderIsPriority(%v) = %v, want %v", c.order.ShippingTitle, got, c.want)
			}
		})
	}
}

// A priority order stamps the urgent rank; anything else keeps the schema
// default, so the millions of existing rows already mean "normal".
func TestJobPriorityRank(t *testing.T) {
	ship := func(s string) *string { return &s }
	priority := gen.Order{ShippingTitle: ship("PRIORITY DISPATCH ⚡️")}
	standard := gen.Order{ShippingTitle: ship("FREE DISPATCH")}

	if got := jobPriorityRank(priority, production.LineItem{}); got != PriorityRank {
		t.Errorf("a priority order ranked %d, want %d", got, PriorityRank)
	}
	if got := jobPriorityRank(standard, production.LineItem{}); got != NormalRank {
		t.Errorf("a standard order ranked %d, want %d", got, NormalRank)
	}
	// An explicitly escalated line is never demoted by a standard order.
	if got := jobPriorityRank(standard, production.LineItem{Priority: -5}); got != -5 {
		t.Errorf("an escalated line ranked %d, want -5 kept", got)
	}
	// ...nor is a more urgent line flattened to PriorityRank by a priority order.
	if got := jobPriorityRank(priority, production.LineItem{Priority: -5}); got != -5 {
		t.Errorf("an escalated line on a priority order ranked %d, want -5 kept", got)
	}
}

// Priority first, oldest first within each rank.
//
// The second half is the part worth protecting: the pool arrives ordered by the
// order's placed_at, and a non-stable sort would put priority planks first
// while shuffling everything behind them - silently discarding the fairness
// rule the rest of the planner is built on.
func TestSortPriorityFirstKeepsOldestFirstWithinARank(t *testing.T) {
	at := func(day int) time.Time { return time.Date(2026, 9, day, 0, 0, 0, 0, time.UTC) }

	jobs := []production.PlanJob{
		{JobNumber: "old-standard", Priority: NormalRank, CreatedAt: at(1)},
		{JobNumber: "mid-standard", Priority: NormalRank, CreatedAt: at(2)},
		{JobNumber: "old-priority", Priority: PriorityRank, CreatedAt: at(3)},
		{JobNumber: "new-standard", Priority: NormalRank, CreatedAt: at(4)},
		{JobNumber: "new-priority", Priority: PriorityRank, CreatedAt: at(5)},
	}
	sortPriorityFirst(jobs)

	want := []string{"old-priority", "new-priority", "old-standard", "mid-standard", "new-standard"}
	for i, w := range want {
		if jobs[i].JobNumber != w {
			t.Fatalf("position %d = %q, want %q (full order %v)", i, jobs[i].JobNumber, w, jobNames(jobs))
		}
	}
}

func jobNames(jobs []production.PlanJob) []string {
	out := make([]string, 0, len(jobs))
	for _, j := range jobs {
		out = append(out, j.JobNumber)
	}
	return out
}

// A priority order's jobs are ranked on every import, not only when they are
// created.
//
// This is the gap that let 21 of 32 priority jobs go unranked on the live
// database: jobPriorityRank runs at job CREATION, so a job that already existed
// when its order became known as priority kept the ordinary rank for ever, and
// the Priority tab showed six beds where there should have been many more. A
// one-off backfill repaired it once; this is what stops it happening again.
func TestIntegrationPriorityRankIsRepairedOnImport(t *testing.T) {
	store := setupStore(t)
	seedAll(t, store)
	minter := newTokenMinter(t)
	srv := testServerWithBatchQueue(t, store, auth.NewGuards(minter.verifier, ""), 1)
	ctx := context.Background()

	// An order that paid for priority, with a job already on the ordinary rank -
	// exactly the state a job created before the rule existed is left in.
	orderID := seedOrder(t, store, 9301, []map[string]any{
		{"product_id": "SKU1", "product_name": "Plank", "quantity": 1, "material": "PLA", "colour": "BLUE"},
	})
	if _, err := store.Pool.Exec(ctx,
		`UPDATE orders SET shipping_title = $1 WHERE id = $2`,
		"PRIORITY DISPATCH", orderID); err != nil {
		t.Fatalf("mark the order priority: %v", err)
	}
	jobID := seedConfiguredJob(t, store, "JOB-REPAIR-1", jobConfig{
		material: "PLA", colour: "BLUE", leftNozzleMm: 0.4, machineFamily: "A2L",
	})
	if _, err := store.Pool.Exec(ctx,
		`UPDATE production_jobs SET order_id = $1, priority = $2 WHERE id = $3`,
		orderID, NormalRank, jobID); err != nil {
		t.Fatalf("attach the job: %v", err)
	}

	order, err := store.Q.GetOrderByID(ctx, orderID)
	if err != nil {
		t.Fatalf("load the order: %v", err)
	}
	srv.rankPriorityJobs(ctx, order)

	job, err := store.Q.GetProductionJobByID(ctx, jobID)
	if err != nil {
		t.Fatalf("reload the job: %v", err)
	}
	if job.Priority != PriorityRank {
		t.Errorf("job rank = %d, want %d - a priority order's existing jobs must be "+
			"repaired on import, not left for a one-off command", job.Priority, PriorityRank)
	}

	// A standard order leaves its jobs alone.
	standardOrder := seedOrder(t, store, 9302, []map[string]any{
		{"product_id": "SKU1", "product_name": "Plank", "quantity": 1, "material": "PLA", "colour": "BLUE"},
	})
	standardJob := seedConfiguredJob(t, store, "JOB-REPAIR-2", jobConfig{
		material: "PLA", colour: "BLUE", leftNozzleMm: 0.4, machineFamily: "A2L",
	})
	if _, err := store.Pool.Exec(ctx,
		`UPDATE production_jobs SET order_id = $1 WHERE id = $2`, standardOrder, standardJob); err != nil {
		t.Fatalf("attach the standard job: %v", err)
	}
	std, err := store.Q.GetOrderByID(ctx, standardOrder)
	if err != nil {
		t.Fatalf("load the standard order: %v", err)
	}
	srv.rankPriorityJobs(ctx, std)

	job, err = store.Q.GetProductionJobByID(ctx, standardJob)
	if err != nil {
		t.Fatalf("reload the standard job: %v", err)
	}
	if job.Priority != NormalRank {
		t.Errorf("a standard order's job was ranked %d, want %d", job.Priority, NormalRank)
	}
}
