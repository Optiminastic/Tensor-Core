package production

import "time"

// Courier collections. Finished work leaves the building at these two times,
// so a job's real deadline is not its due date - it is the last collection at
// or before that date. A job due at 14:30 that misses the 12:00 collection is
// not "half a day late", it is a full day late, because the next van is
// tomorrow noon.
//
// This is why collections are scheduled on rather than left to the due date
// alone: the cost of slipping is a step function, and a scheduler that sees a
// smooth ramp will happily trade a 20-minute delay that crosses a collection
// for a slightly fuller bed.
const (
	morningCollectionHour   = 12
	afternoonCollectionHour = 16
)

// collectionLeadTime is how long before a collection a batch must START to make
// it: printing, then assembly, finishing, QC and packing. Deliberately generous
// - the point is to stop the planner committing work that cannot physically
// reach the van, not to model the stations precisely.
const collectionLeadTime = 2 * time.Hour

// collectionUrgencyWindow is how far ahead of a deadline urgency begins to
// climb. Beyond this a job contributes nothing; at the deadline it contributes
// everything.
const collectionUrgencyWindow = 6 * time.Hour

// NextCollection is the first courier collection at or after t.
func NextCollection(t time.Time) time.Time {
	day := time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, t.Location())
	morning := day.Add(morningCollectionHour * time.Hour)
	afternoon := day.Add(afternoonCollectionHour * time.Hour)
	switch {
	case !t.After(morning):
		return morning
	case !t.After(afternoon):
		return afternoon
	default:
		// Past today's last van: tomorrow morning.
		return day.Add(24 * time.Hour).Add(morningCollectionHour * time.Hour)
	}
}

// CollectionDeadline is the latest moment work for this due date can still
// start and make its van. A job with no due date has no collection pressure.
func CollectionDeadline(due *time.Time) *time.Time {
	if due == nil {
		return nil
	}
	// The van that actually carries it is the last one at or before the due
	// date; NextCollection gives the first at or after, which is the same
	// instant when the due date IS a collection time.
	deadline := NextCollection(*due).Add(-collectionLeadTime)
	return &deadline
}

// collectionUrgency is how close the most pressing job on a bed is to missing
// its collection, 0-100. 100 means at or past the point where it must start
// now; 0 means no job on the bed has a collection within the window.
//
// Scored per bed rather than per job because a bed prints as one unit: the
// whole plate inherits the tightest deadline on it.
func collectionUrgency(jobs []PlanJob, now time.Time) float64 {
	worst := 0.0
	for _, j := range jobs {
		deadline := CollectionDeadline(j.DueDate)
		if deadline == nil {
			continue
		}
		until := deadline.Sub(now)
		switch {
		case until <= 0:
			return 100
		case until >= collectionUrgencyWindow:
			continue
		}
		if s := (1 - float64(until)/float64(collectionUrgencyWindow)) * 100; s > worst {
			worst = s
		}
	}
	return worst
}

// wouldMissCollection reports whether any job on this bed is already at or past
// the point where the plate must start to make its van.
//
// This is a lock condition, not a preference. Holding such a batch back for a
// fuller one does not trade a little efficiency for a little lateness - it
// misses the van, and the work waits until the next one.
func wouldMissCollection(jobs []PlanJob, now time.Time) bool {
	for _, j := range jobs {
		if d := CollectionDeadline(j.DueDate); d != nil && !now.Before(*d) {
			return true
		}
	}
	return false
}
