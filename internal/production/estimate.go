package production

import "math"

// Fast print-time estimation - the cheap half of a two-level model.
//
// Level 1 (this file) answers "roughly how long?" from geometry alone, with no
// slicer involved. It is what batch planning and machine scheduling run on,
// because those questions are asked constantly: every planner pass scores many
// candidate combinations, and slicing each one through Bambu Studio would cost
// minutes of CPU to answer a question that only needs to be approximately
// right.
//
// Level 2 is the real slice of the merged plate, run once a batch is actually
// committed to a machine. That number replaces this one wherever it exists -
// see batchTimeFromJobs' callers, which prefer plate_sliced_at's measurement
// over anything here.
//
// Nothing in this file is a claim about physics. It is a deliberately simple
// model whose only job is to rank work sensibly until a measurement arrives.

const (
	// solidFraction is how much of a part's bounding box actually becomes
	// extruded plastic. A printed part is mostly air: sparse infill, hollow
	// interiors, and the box itself bounds an irregular shape. 0.25 is a
	// middling figure for typical shop parts at ~15-20% infill.
	solidFraction = 0.25

	// extrusionRateMM3PerSec is sustained volumetric throughput for a 0.4 mm
	// nozzle. Bambu's high-flow hot ends peak far above this, but peak flow is
	// not sustained flow once travel, retraction, layer changes and
	// acceleration limits are counted.
	extrusionRateMM3PerSec = 10.0

	// minDefaultPrintMinutes floors the estimate. Even a tiny part costs bed
	// preparation, purge, a first layer and cool-down; an estimate of "1
	// minute" would let the scheduler pack a machine with work that cannot
	// physically be done in the time claimed.
	minDefaultPrintMinutes = 5

	// maxDefaultPrintMinutes caps it at 24h. Beyond that the number is almost
	// certainly a bad bounding box rather than a real part, and letting it
	// through would poison a machine's whole projected queue.
	maxDefaultPrintMinutes = 24 * 60
)

// DefaultPrintTimeMinutes estimates one unit's print time from its bounding
// box, for a job whose design has no measured metrics yet.
//
// This exists because a job with no print time is not merely imprecise, it is
// invisible to scheduling: batchTimeFromJobs used to return nil for the whole
// batch if any single job lacked an estimate, so one unmeasured job stripped
// the time from every job beside it, and the machine scheduler then ranked that
// batch as free work.
//
// Deriving from the bounding box rather than using a flat constant matters: a
// constant makes every design identical, which is exactly the failure mode
// FAKE_SLICE produced - every batch 25 minutes, so bed occupancy, queue order
// and every countdown in the UI were equally fictional. Volume at least orders
// parts correctly against each other.
//
// Returns 0 when the bounding box is unusable, so the caller can tell "no
// estimate possible" from a real one.
func DefaultPrintTimeMinutes(bboxXMM, bboxYMM, bboxZMM float64) int {
	if bboxXMM <= 0 || bboxYMM <= 0 || bboxZMM <= 0 {
		return 0
	}
	volumeMM3 := bboxXMM * bboxYMM * bboxZMM
	seconds := (volumeMM3 * solidFraction) / extrusionRateMM3PerSec
	minutes := int(math.Ceil(seconds / 60))
	if minutes < minDefaultPrintMinutes {
		return minDefaultPrintMinutes
	}
	if minutes > maxDefaultPrintMinutes {
		return maxDefaultPrintMinutes
	}
	return minutes
}

// DefaultBatchTimeCorrection is the factor applied to the summed per-unit times
// of everything on a bed.
//
// Printing four parts together is cheaper than printing them one after another:
// the bed heats once, the purge tower is shared, and each layer change covers
// every part on the plate at once. Summing the individual times therefore
// overstates a batch, and by a margin that grows with the number of parts.
//
// 0.85 is a starting point, not a measurement, and it is deliberately
// conservative - an estimate that runs slightly long makes a machine look
// busier than it is, which delays work rather than promising a customer a
// slot that does not exist. Once enough batches carry both an estimate and a
// real plate slice, this should be derived from that history per batch shape
// rather than left as one global guess.
const DefaultBatchTimeCorrection = 0.85

// EstimateBatchMinutes is the fast batch estimate: the summed print time of
// every unit on the bed, scaled by the correction factor.
//
// Sum, not max. Max was the previous model and it is wrong in the direction
// that hurts most - a bed of eight 40-minute parts was scheduled as 40 minutes
// of machine time, so a machine could be handed a full day of work while
// appearing nearly free. Sum overstates (parts do share overhead), which the
// correction factor pulls back, and a real slice replaces entirely once the
// batch is committed.
//
// unitMinutes is per unit and quantities scale it, matching how
// filament_grams_required already treats its per-unit figure.
func EstimateBatchMinutes(unitMinutes []int, quantities []int, correction float64) int {
	if correction <= 0 {
		correction = DefaultBatchTimeCorrection
	}
	total := 0
	for i, minutes := range unitMinutes {
		qty := 1
		if i < len(quantities) && quantities[i] > 0 {
			qty = quantities[i]
		}
		total += minutes * qty
	}
	if total <= 0 {
		return 0
	}
	scaled := int(math.Round(float64(total) * correction))
	if scaled < 1 {
		return 1
	}
	return scaled
}
