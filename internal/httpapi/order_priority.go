package httpapi

// Which orders jump the queue.
//
// The storefront sells a paid upgrade - "PRIORITY DISPATCH" - and the customer
// who bought it expects their plank made before the ones behind it. Nothing in
// the order carries a flag for that: the only record is the shipping option
// they chose, so that string is what this reads.
//
// There is history here, and the policy has now moved twice. Priority orders
// were once excluded from production altogether, on the instruction that they
// were handled outside Tensor; that rule went (see ShouldCreateJobs) and
// nineteen unfulfilled priority orders came back into the queue with it. They
// were then served FIRST - pulled to the front of the planning pool, locking
// beds under-full, jumping the dispatch queue.
//
// They are served in TURN as of this change, again at the shop's instruction:
// read as an ordinary order by the planner, the lock gate and the dispatcher
// alike. What survives is the record - the rank is still stamped on the job, so
// the shipping promise is still visible to whoever packs the parcel, and
// turning the behaviour back on is a matter of reading the column again.
//
// The three places that read it, now gone: sortPriorityFirst (below),
// carriesPriority (batch_lock.go) and the min(priority) term in
// ListBatchesToDispatch.

import (
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

// There was a sortPriorityFirst here, which pulled every expedited plank to the
// front of the planning pool. It is gone: BATCHING IS FIRST COME, FIRST SERVED
// at the shop's instruction, so a priority order is grouped, locked and sent in
// exactly the same order as any other.
//
// The rank below is still stamped, and still means what it says - it is simply
// not read by the planner any more. Keeping the column is what makes this
// reversible, and it is the only record anywhere that a customer paid for the
// upgrade; the fulfilment side still wants to know.
//
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
