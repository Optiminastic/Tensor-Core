package httpapi

// Which orders jump the queue, and what that does to the planning pool.
//
// The rank convention is load-bearing in two places that sort opposite-looking
// ways - the planner here and min(j.priority) ASC in the machine scheduler - so
// these pin "lower is more urgent" rather than leaving it to a comment.

import (
	"testing"
	"time"

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
