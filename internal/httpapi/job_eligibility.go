package httpapi

// Which orders become production jobs.
//
// Not every imported order is work for the print floor. Tensor pulls the whole
// recent history from Shopify so the Orders page is complete, but the queue
// should only hold what this run is actually printing - otherwise the floor is
// looking at hundreds of jobs nobody intends to make, and the ones that matter
// are lost among them.

import (
	"strings"

	"github.com/Optiminastic/tensor-core/internal/db/gen"
)

// firstProductionOrderNumber is the first order this production run covers.
//
// T3DPS-114599 was placed at 02:32 on 27 August 2026; everything below it
// belongs to the previous way of working. It sits just after the 26 August
// import floor (see shopifyImportSince), so the two together mean: import the
// history, print from the run's own first order.
//
// A number rather than a date because the store's own numbering is what the
// floor and the customer both quote, and it is unambiguous at the boundary -
// two orders placed a minute apart on the cutover day are separated cleanly.
const firstProductionOrderNumber = 114599

// ShouldCreateJobs reports whether an order is work for the print floor.
//
// Two rules, both deliberate:
//
//   - Orders below firstProductionOrderNumber predate this run.
//   - An order Shopify already reports as FULFILLED has shipped. Its plank was
//     made and posted, whether Tensor printed it or somebody made it by hand,
//     so creating jobs for it would put work on the floor that is already out
//     the door. 141 of 288 orders were in that state when this rule was added -
//     half the queue was work nobody was going to do.
//
// Priority dispatch used to be a third rule: an order whose shipping option
// said "PRIORITY DISPATCH" was excluded from the queue entirely, on the
// instruction that those were handled outside it. That is no longer how the
// floor works - priority orders print like any other - so the rule is gone.
// Nineteen unfulfilled priority orders were being held out of production by it.
//
// An order that fails a test gets no NEW jobs. Nothing is deleted: jobs that
// already exist for a fulfilled order are completed by reconcileFulfilledOrder,
// which is a record of work done rather than work removed. The order stays
// visible on the Orders page either way - not printing something is different
// from not having received it.
func ShouldCreateJobs(order gen.Order) bool {
	if orderSequence(order.OrderNumber) < firstProductionOrderNumber {
		return false
	}
	return !orderIsFulfilled(order)
}

// orderIsFulfilled reports whether Shopify says the order has shipped.
//
// Matched exactly, like reconcileFulfilledOrder: "partially_fulfilled" is NOT
// this. Part of an order having shipped says nothing about the plank still to
// be made, and treating it as done would drop work somebody is waiting for.
func orderIsFulfilled(order gen.Order) bool {
	if order.FulfillmentStatus == nil {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(*order.FulfillmentStatus), fulfilledStatus)
}

// orderSequence is the store's own number for an order: "T3DPS-114599" ->
// 114599.
//
// Read from the last hyphen-separated segment, for the reason jobNumberForOrder
// gives: the prefix contains a digit of its own, so scanning the whole string
// finds the "3" in "T3DPS". Returns 0 when there is nothing to read, which
// fails the cutoff - an order whose number cannot be understood is not
// something to start printing on a guess.
func orderSequence(orderNumber string) int {
	digits := orderNumberDigits(orderNumber)
	if digits == "" {
		return 0
	}
	n := 0
	for _, r := range digits {
		n = n*10 + int(r-'0')
	}
	return n
}
