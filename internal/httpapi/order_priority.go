package httpapi

// Which orders jump the queue.
//
// The storefront sells a paid upgrade - "PRIORITY DISPATCH" - and the customer
// who bought it expects their plank made before the ones behind it. Nothing in
// the order carries a flag for that: the only record is the shipping option
// they chose, so that string is what this reads.
//
// There is history here. Priority orders used to be excluded from production
// altogether, on the instruction that they were handled outside Tensor; that
// rule is gone (see ShouldCreateJobs) and nineteen unfulfilled priority orders
// came back into the queue with it. This is the opposite policy: they are not
// merely included, they are served first.

import (
	"sort"
	"strings"

	"github.com/Optiminastic/tensor-core/internal/db/gen"
	"github.com/Optiminastic/tensor-core/internal/production"
)

// priorityShippingMarker is what a paid-upgrade shipping option contains.
//
// Matched as a case-insensitive substring rather than against the exact
// "PRIORITY DISPATCH ⚡️" the store sends today, because the wording has already
// changed once and an emoji is a poor thing to hinge a customer's promise on.
// The word is specific enough not to appear in a standard option: the others
// read "FREE DISPATCH - BEST DEAL 🎉".
const priorityShippingMarker = "priority"

// Rank values for production_jobs.priority.
//
// LOWER IS MORE URGENT. That is not arbitrary - the machine scheduler already
// orders candidate work by min(j.priority) ASC (see ListMachinesForScheduling),
// so a rank that sorted the other way would put priority planks last on a
// printer while putting them first on a bed.
//
// PriorityRank is negative so the schema's DEFAULT 0 keeps its meaning for
// every job that already exists: normal. A backfill is therefore only needed
// for the priority ones, not for the whole table.
const (
	PriorityRank = -1
	NormalRank   = 0
)

// OrderIsPriority reports whether the customer paid for priority dispatch.
func OrderIsPriority(order gen.Order) bool {
	if order.ShippingTitle == nil {
		return false
	}
	return strings.Contains(strings.ToLower(*order.ShippingTitle), priorityShippingMarker)
}

// sortPriorityFirst brings priority work to the front of a planning pool.
//
// GroupByColour never reorders - "oldest order first" is a property of the list
// it is handed, not something it computes - so the serving order is decided
// here, which is the seam that contract leaves for exactly this.
//
// Stable, and that is the whole point: the pool arrives ordered by the order's
// placed_at, so a stable sort by rank alone yields priority-first AND
// oldest-first within each rank. A non-stable sort would put priority planks
// first and shuffle everything behind them, quietly discarding the fairness
// rule the rest of the planner is built on.
func sortPriorityFirst(jobs []production.PlanJob) {
	sort.SliceStable(jobs, func(i, j int) bool {
		return jobs[i].Priority < jobs[j].Priority
	})
}

// jobPriorityRank is the rank to stamp on a new job.
//
// A line item may carry its own priority (the import can set one, and a reprint
// copies its source's). That wins when it is more urgent than the order's, so
// an explicitly escalated line is never demoted by an order that merely shipped
// standard.
func jobPriorityRank(order gen.Order, li production.LineItem) int32 {
	rank := int32(li.Priority)
	if OrderIsPriority(order) && rank > PriorityRank {
		return PriorityRank
	}
	return rank
}
