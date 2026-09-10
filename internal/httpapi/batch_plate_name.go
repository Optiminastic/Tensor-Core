package httpapi

// Naming a merged plate after what is on it.
//
// A plate used to be called "BATCH-0042-preview.stl", which says nothing an
// operator needs. What they need, standing at a printer with a downloaded file,
// is which customers' orders are on this bed and which filament to load - so the
// name carries exactly that:
//
//	114556-114557-114558-BLUE.3mf
//
// Per the shop's own instruction. The order numbers are the ones the customer
// and the floor both quote, and the colour is last so it is the part the eye
// lands on when the files are sorted by name.

import (
	"sort"
	"strings"

	"github.com/Optiminastic/tensor-core/internal/db/gen"
)

// plateFileStem is the plate's filename without its extension.
//
// Falls back to the batch number when the bed yields no order numbers at all -
// a plate must still have a name, and a batch of manually-created jobs has no
// Shopify order behind it.
func plateFileStem(jobs []gen.ProductionJob, batchNumber string) string {
	orders := orderNumbersOnBed(jobs)
	colour := plateColourToken(jobs)

	switch {
	case len(orders) == 0 && colour == "":
		return batchNumber
	case len(orders) == 0:
		return batchNumber + "-" + colour
	case colour == "":
		return strings.Join(orders, "-")
	default:
		return strings.Join(orders, "-") + "-" + colour
	}
}

// orderNumbersOnBed is the distinct Shopify order numbers the bed's jobs came
// from, in ascending order.
//
// Read from the job number rather than joined back to orders: job_number is
// minted as "JOB-114556" precisely so it carries the order's own number, and a
// bed of four jobs would otherwise be four more queries on the plate-building
// path. Deduplicated, because one order with two planks on the same bed should
// appear once, not twice.
func orderNumbersOnBed(jobs []gen.ProductionJob) []string {
	seen := map[string]bool{}
	var out []string
	for _, j := range jobs {
		n := orderNumberFromJobNumber(j.JobNumber)
		if n == "" || seen[n] {
			continue
		}
		seen[n] = true
		out = append(out, n)
	}
	sortOrderNumbers(out)
	return out
}

// sortOrderNumbers puts order numbers in ascending numeric order, in place.
//
// By length first, then lexically: all live order numbers are the same width, so
// plain string order is already ascending, and the length comparison keeps that
// true if the store ever rolls over to another digit. Shared with the Batches
// table's Jobs column so a plate's filename and the row above it list the same
// orders in the same order.
func sortOrderNumbers(numbers []string) {
	sort.Slice(numbers, func(a, b int) bool {
		if len(numbers[a]) != len(numbers[b]) {
			return len(numbers[a]) < len(numbers[b])
		}
		return numbers[a] < numbers[b]
	})
}

// orderNumberFromJobNumber reads the order's number back out of a job number:
// "JOB-114556" and "JOB-114666-2" both give the order they belong to.
//
// The FIRST digit segment, deliberately - which is the opposite of what
// orderNumberDigits does, and the two must not be confused. orderNumberDigits
// reads an ORDER number ("T3DPS-114552"), where the prefix contains a digit of
// its own so the last segment is the right one. Here the string is already a job
// number, and its last segment is the per-order product index: reading it would
// name a plate "2-3-114556" instead of "114556".
//
// Returns "" for anything that is not a recognisable job number, including the
// sequential fallback numbers NextJobNumber mints for jobs with no order.
func orderNumberFromJobNumber(jobNumber string) string {
	rest := strings.TrimPrefix(strings.TrimSpace(jobNumber), jobNumberPrefix)
	if rest == jobNumber {
		// No "JOB-" prefix: not a number this function can read.
		return ""
	}
	first, _, _ := strings.Cut(rest, "-")
	if first == "" || !allDigits(first) {
		return ""
	}
	return first
}

// allDigits reports whether s is one or more ASCII digits.
func allDigits(s string) bool {
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return s != ""
}

// plateColourToken is the bed's colour as it appears in the filename.
//
// Under colour batching a bed has exactly one, which is the case this is for.
// More than one is still rendered rather than dropped - a plate built by the
// optimiser, or by a hand-assembled batch, should still say what is on it.
// Spaces become underscores so a colour stays a single token and "BABY PINK"
// cannot be mistaken for two orders.
func plateColourToken(jobs []gen.ProductionJob) string {
	colours := planColoursFromJobs(jobs)
	out := make([]string, 0, len(colours))
	for _, c := range colours {
		c = strings.ToUpper(strings.TrimSpace(c))
		c = strings.Join(strings.Fields(c), "_")
		if c != "" {
			out = append(out, c)
		}
	}
	sort.Strings(out)
	return strings.Join(out, "-")
}

// plateKey reduces a plate filename to the set of orders it carries, so a bed
// can be recognised in BambuBuddy's records.
//
// Deliberately order-INSENSITIVE and extension-insensitive, because the name
// does not survive the round trip unchanged. plateFileStem writes the colour
// LAST ("114556-114557-BLUE"), and the same plate comes back from the archive
// with the colour FIRST ("BLUE-114840-114873"), sometimes with ".stl" or
// "_plate_1.gcode.3mf" attached. Any prefix or positional match fails silently
// on that; a sorted set of order numbers survives it.
//
// Returns "" when the name yields no order numbers, and "" never matches
// anything. That is the point: a plate somebody ran straight from BambuBuddy,
// or one named after a batch number by plateFileStem's own fallback, must not
// be able to claim a bed and complete it.
func plateKey(filename string) string {
	name := strings.ToUpper(strings.TrimSpace(filename))
	// Strip the extensions BambuBuddy adds, longest first so ".gcode.3mf" is
	// not left as ".gcode".
	for _, ext := range []string{".GCODE.3MF", ".3MF", ".GCODE", ".STL"} {
		name = strings.TrimSuffix(name, ext)
	}
	// And the plate suffix a sliced file carries.
	if i := strings.LastIndex(name, "_PLATE_"); i > 0 && allDigits(name[i+len("_PLATE_"):]) {
		name = name[:i]
	}

	tokens := strings.FieldsFunc(name, func(r rune) bool {
		return r == '-' || r == '_' || r == ' '
	})

	var orders []string
	seen := map[string]bool{}
	for i, tok := range tokens {
		// The number after "BATCH" is a batch number, not an order number, and
		// it is long enough to pass every other test here. plateFileStem names
		// a bed with no Shopify order behind it exactly that way, so without
		// this such a plate would claim whichever bed shared its number.
		if i > 0 && tokens[i-1] == "BATCH" {
			continue
		}
		// Four digits or more, which is what an order number looks like and a
		// heart count or a plate index does not.
		if allDigits(tok) && len(tok) >= minOrderNumberDigits && !seen[tok] {
			seen[tok] = true
			orders = append(orders, tok)
		}
	}
	if len(orders) == 0 {
		return ""
	}
	sortOrderNumbers(orders)
	return strings.Join(orders, "-")
}

// minOrderNumberDigits is what tells an order number from a plate index.
//
// Shopify order numbers here are six digits ("114556"); the digits that are NOT
// order numbers in a plate name are a plate index ("1") and a split suffix
// ("-2"). Four is comfortably between the two.
const minOrderNumberDigits = 4
