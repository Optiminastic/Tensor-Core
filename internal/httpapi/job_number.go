package httpapi

// Job numbers that name the order they came from.
//
// The sequence-minted "JOB-1000615" is unique and says nothing: an operator
// holding a job, an order, and a customer on the phone had three identifiers
// and no way to line them up without a lookup. "JOB-114552" is the Shopify
// order the product came from, which is the number every other system - the
// storefront, the packing slip, the customer's email - already prints.

import (
	"fmt"
	"regexp"
	"strings"
)

// orderDigits matches a run of digits.
var orderDigits = regexp.MustCompile(`\d+`)

// orderNumberDigits pulls the order's own number out of a storefront order
// number: "T3DPS-114552" -> "114552".
//
// Read from the LAST hyphen-separated segment, not by scanning the whole
// string. The store's prefix contains a digit of its own - the "3" in "T3DPS" -
// so taking the first run it finds turns every order into "JOB-3". A test
// caught that; it would otherwise have given all 199 orders the same number and
// failed on the unique index.
//
// The prefix is also not stable: one live order is numbered "T3PS-114743",
// missing a letter. Only the trailing digits can be relied on.
func orderNumberDigits(orderNumber string) string {
	trimmed := strings.TrimSpace(orderNumber)
	if trimmed == "" {
		return ""
	}
	segments := strings.Split(trimmed, "-")
	return orderDigits.FindString(segments[len(segments)-1])
}

// jobNumberPrefix marks a job number as carrying an order's own number, which
// is what lets orderNumberFromJobNumber read it back out when naming a plate.
const jobNumberPrefix = "JOB-"

// jobNumberForOrder builds the job number for the index-th product on an order.
//
// A single-product order - 189 of the 199 live orders - gets the plain
// "JOB-114552". Where an order carries several products they cannot all be
// that, because job_number is UNIQUE, so the second onward take a suffix:
// JOB-114666, JOB-114666-2, JOB-114666-3, JOB-114666-4.
//
// Safe without a uniqueness check because of where it is called from:
// CreateJobsForOrder holds a per-order advisory lock and refuses outright if
// the order already has jobs, so an order's numbers are minted exactly once,
// all together, from a contiguous index.
//
// Returns "" when the order number carries no digits, which the caller reads as
// "fall back to the sequence" - a job with no traceable order is better than no
// job at all.
func jobNumberForOrder(orderNumber string, index int) string {
	digits := orderNumberDigits(orderNumber)
	if digits == "" {
		return ""
	}
	if index <= 0 {
		return jobNumberPrefix + digits
	}
	// 1-based for a human: the second product on the order reads as "-2".
	return fmt.Sprintf("%s%s-%d", jobNumberPrefix, digits, index+1)
}
