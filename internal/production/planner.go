package production

import (
	"math"
	"sort"
	"time"

	"github.com/Optiminastic/tensor-core/internal/bedpack"
)

// The batch planner groups queued jobs onto printer beds, ported from
// print-queue-be's batch_service and extended into two explicit stages:
// Grouping (groupKey/groupByKey) partitions jobs into buckets that are
// physically COMPATIBLE - everything that must be identical across a whole
// bed given today's raw-geometry STL merge (no per-object slicer settings
// survive it). Optimization (bestPartition/scoreBatch/localSearch) then finds
// the best-SCORING way to split one compatible bucket into batches, not just
// the first one that fits.

// dueUrgencyHorizon is how far ahead a due date still earns urgency in the
// batch score: at or past due scores 100, this far out scores 0, linear in
// between.
//
// This was similarDueDateWindow, a HARD partition that kept jobs whose due
// dates differed by more than this off the same bed entirely - so a job due
// Friday could not share a plate with one due the following Tuesday even when
// both fitted and the bed was half empty. Expressing the same preference as a
// score costs no bed space and says the same thing.
const dueUrgencyHorizon = 3 * 24 * time.Hour

// urgentPriority is the priority value marking a same-day/urgent job (matches
// the "1" the user specified: FCFS by default, "1" for same-day).
const urgentPriority = 1

// UrgentPriority is urgentPriority, exported for callers outside this
// package that need to force a job to the top of the queue - e.g. a reprint,
// which should always jump ahead regardless of its source job's priority.
const UrgentPriority = urgentPriority

// maxGroupColours caps how many distinct filament colours a single bed may
// use combined, checked live while packing (see withinColourCap) - not a
// pre-grouping equality bucket, so jobs of genuinely different colours can
// still share a bed as long as the running total stays at or under this.
const maxGroupColours = 2

// TargetBedUtilisationPercent is the floor bed-footprint fill (X*Y area of
// placed parts / bed area) a batch should clear before it's accepted as
// final - the top-priority packing goal, per the user's explicit "more than
// 80% in X/Y" instruction. It is chased two ways: packJobs's lookahead fill
// (see fillBed) tries to reach it by pulling in whatever compatible jobs
// still fit; and a partition that still lands under it after exhausting
// every compatible job in its cluster is not created at all - see
// shouldCreateBatch/BatchGate - unless an override condition (urgent
// priority, due soon, or max queue wait) applies, so genuinely low-volume
// periods still ship instead of waiting forever.
const TargetBedUtilisationPercent = 80.0

// packingStrategies are the job orderings tried per cluster; the best-scoring one
// wins. priority_desc places same-day/urgent jobs first so they never wait
// behind routine ones for bed space.
const (
	strategyFCFS     = "fcfs"
	strategyArea     = "area_desc"
	strategyTime     = "time_desc"
	strategyPriority = "priority_desc"
)

var packingStrategies = []string{strategyFCFS, strategyArea, strategyTime, strategyPriority}

// PlanJob is one job as the planner sees it: its grouping key, quantity, timing,
// filament need, and per-unit footprint. String fields are already normalised
// to "" when absent. Colours is a set (order doesn't matter for grouping).
type PlanJob struct {
	ID        string
	JobNumber string
	// ShopifyCustomerID is nil when the job has no linked order or the
	// order had no customer object (guest checkout) - never treated as
	// matching another nil, so unset-customer jobs never spuriously
	// cluster together (see sameCustomerOnBed/customerCohesionCount).
	ShopifyCustomerID *int64
	Material          string
	Colours           []string
	NozzleLeft        string
	NozzleRight       string
	QualityMM         string
	MachineFamily     string
	SupportUsed       bool
	InfillPct         float64
	Priority          int
	Quantity          int
	EstimatedMinutes  *int
	DueDate           *time.Time
	CreatedAt         time.Time
	FilamentGrams     float64
	Footprint         bedpack.UnitFootprint
	// InDraft marks a job that is already sitting in a pending_approval batch.
	// The planning window always keeps these (see selectWindow): dropping one
	// would stop the run reproducing that Draft's job set, dissolving a batch
	// an operator can already see purely because the backlog grew.
	InDraft bool
}

// PlannedBatch is a proposed batch: its jobs, the placement result, and the
// snapshot metrics the batch row will store.
type PlannedBatch struct {
	Jobs                        []PlanJob
	Placements                  []bedpack.Placement
	UnitsPerBed                 int
	TotalPrintTimeMinutes       *int
	EffectiveTimePerUnitMinutes *float64
	TotalFilamentGrams          float64
	BedUtilisationPercent       float64
	PackingStrategy             string
}

// Unbatchable is a job that could not be placed, with the reason.
type Unbatchable struct {
	JobID     string
	JobNumber string
	Reason    string
}

// BatchGate configures the override conditions that let a partition become a
// real batch even when it hasn't cleared TargetBedUtilisationPercent -
// otherwise it's held (Plan's third return value) so its jobs stay queued,
// waiting for more compatible volume to arrive rather than printing an
// under-filled bed.
type BatchGate struct {
	// MaxWait forces a batch once any of its jobs has been queued this long,
	// however low the utilisation - a low-volume period must not leave
	// customers waiting forever for more orders to arrive. Stays in force
	// even with aging configured below: a gradually-relaxing floor alone
	// can't guarantee a genuinely isolated, incompatible-with-everything job
	// ever ships, so this remains the final unconditional backstop.
	MaxWait time.Duration
	// DueSoonWindow forces a batch if any of its jobs is due within this
	// window of now (including already overdue).
	DueSoonWindow time.Duration

	// AgingWindow is how long a partition ages before its effective
	// acceptance bar bottoms out at AgingFloorPercent, relaxing linearly
	// from TargetBedUtilisationPercent in between. AgingWindow<=0 disables
	// aging entirely (the zero-value BatchGate{} used by existing callers
	// and tests is unaffected).
	AgingWindow time.Duration
	// AgingFloorPercent is the lowest the effective bar relaxes to before
	// MaxWait's unconditional override takes over. Should be less than
	// TargetBedUtilisationPercent for aging to have any effect.
	AgingFloorPercent float64

	// IdleMachines is how many printers are sitting idle and able to take
	// work right now. A partition under target is created anyway when this is
	// non-zero, because by the time shouldCreateBatch sees it fillBed has
	// already exhausted every compatible job in the group - so waiting cannot
	// improve this bed with anything that currently exists, and an idle
	// machine is capacity being thrown away for a fuller bed that may never
	// arrive.
	//
	// This is the condition that unsticks a floor where 80% is not reachable:
	// measured against the real packer the ceiling is ~69% for parts up to
	// 100mm and ~74% up to 150mm, so without it batches wait out MaxWait -
	// four hours by default - while printers stand still.
	IdleMachines int

	// IdleWaitWindow is how long a thin bed may be held back while a printer
	// is free, in the hope that a short wait produces a fuller plate.
	//
	// This is the "wait or print?" decision. Releasing every thin partition
	// the instant a machine frees up converts a backlog into single-job beds,
	// because a created batch never returns to the pool to be consolidated.
	// Waiting indefinitely is worse - it leaves capacity idle for a plate that
	// may never arrive. So the wait is bounded: once a bed's oldest job has
	// waited this long, or the bed is already reasonably full, it goes.
	//
	// Zero disables the wait entirely and restores the previous behaviour
	// (idle machine means print now), which is what the zero-value
	// BatchGate{} used by existing callers and tests gets.
	IdleWaitWindow time.Duration

	// HorizonJobs caps how many jobs of one compatibility group a single run
	// considers - see selectWindow. Zero uses DefaultHorizonJobs.
	HorizonJobs int
}

// Nester places print units onto a bed and reports what fit vs what didn't -
// the seam between Stage 2 (Candidate Batch Builder: which jobs go on this
// bed) and Stage 3 (2D Nesting/Bed Layout: where they physically sit).
// bedpack.Pack (guillotine Best-Area-Fit) is today's only implementation and
// exactly matches this signature; a future no-fit-polygon nester could
// satisfy the same signature without any other function in this file
// changing (see PlanWithNester).
type Nester func(units []bedpack.UnitFootprint) (placements []bedpack.Placement, rejected []bedpack.UnitFootprint)

// DefaultNester is bedpack's guillotine Best-Area-Fit packer - today's only
// Nester, and what Plan uses.
var DefaultNester Nester = bedpack.Pack

// BedNester is a Nester that is told which bed it is packing.
//
// Separate from Nester rather than replacing it because the optimiser path has
// one bed by construction (see groupKey's comment) while the colour path has
// three - a P2S's, an A2L's and an H2C's - and packing a six-up A2L plate
// against a P2S bed would silently drop two planks.
type BedNester func(bedpack.Bed, []bedpack.UnitFootprint) ([]bedpack.Placement, []bedpack.UnitFootprint)

// DefaultBedNester is the real packer: 2D, parts turned 90 degrees where that
// fits more on. The single-column layout (bedpack.PackColumnOn) is what held
// every bed to four planks whatever the machine, so it is no longer the default.
var DefaultBedNester BedNester = bedpack.PackOn

// Plan groups and packs jobs into batches using DefaultNester. See
// PlanWithNester for the nesting-algorithm-injectable version and the full
// behaviour description.
func Plan(jobs []PlanJob, now time.Time, gate BatchGate) (batches []PlannedBatch, unbatchable []Unbatchable, held []PlannedBatch) {
	return PlanWithNester(jobs, now, gate, DefaultNester)
}

// PlanWithNester groups and packs jobs into batches. Jobs without a
// measurable footprint, or too large for the bed even alone, come back as
// Unbatchable. A partition that doesn't clear TargetBedUtilisationPercent is
// only accepted as a real batch if it qualifies for an override under gate
// (an urgent-priority job, a job due soon, or a job that's been queued past
// the max wait) - otherwise it's returned as held, not created, so the next
// run reconsiders its jobs alongside whatever newer orders have arrived by
// then. Grouping (Stage 1: Compatibility Filter), scoring (Stage 4: Batch
// Scoring), and this gate (Stage 5: Batch Approval) are all nesting-
// algorithm-agnostic and unaffected by which Nester is passed - only Stage
// 2/3 (candidate building + nesting, both inside bestPartition) consult it.
func PlanWithNester(jobs []PlanJob, now time.Time, gate BatchGate, nest Nester) (batches []PlannedBatch, unbatchable []Unbatchable, held []PlannedBatch) {
	batches, unbatchable, held, _ = PlanWithReasons(jobs, now, gate, nest)
	return batches, unbatchable, held
}

// PlanWithReasons is PlanWithNester plus the fourth outcome: jobs this run did
// not reach, each with the reason it was left out.
//
// Split from PlanWithNester rather than folded into it so every existing caller
// and test keeps its signature. The extra return exists because a bounded
// planning window (see selectWindow) means a perfectly batchable job can now be
// left out of a run for no fault of its own, and a job that silently vanishes
// from scheduling is indistinguishable from one the planner has lost.
func PlanWithReasons(jobs []PlanJob, now time.Time, gate BatchGate, nest Nester) (
	batches []PlannedBatch, unbatchable []Unbatchable, held []PlannedBatch, deferred []Unbatchable,
) {
	measurable := make([]PlanJob, 0, len(jobs))
	for _, j := range jobs {
		if j.Footprint.XMM <= 0 || j.Footprint.YMM <= 0 {
			unbatchable = append(unbatchable, Unbatchable{
				JobID: j.ID, JobNumber: j.JobNumber, Reason: ReasonNoFootprint,
			})
			continue
		}
		measurable = append(measurable, j)
	}

	// One pass per compatibility group. Due dates used to sub-partition each
	// group again on a 3-day window, so a job due Friday could not share a bed
	// with one due the following Tuesday even when both fitted and the bed was
	// half empty. Urgency is a scoring term and a lock condition now (see
	// scoreBatch's dueUrgency and shouldCreateBatch), which expresses the same
	// intent without costing bed space.
	// idleBudget is how many more under-target partitions the idle-machine rule
	// may release this run. It is a budget rather than a flag because idle
	// capacity is finite: with three printers free and forty thin partitions
	// available, releasing all forty does not use the capacity any better - it
	// just converts the whole backlog into single-job beds that can never be
	// consolidated, because a created batch never returns to the pool.
	//
	// Releasing exactly as many as there are free machines fills every idle
	// printer now and leaves the rest to keep accumulating compatible volume,
	// which is the entire point of holding a partition back.
	idleBudget := gate.IdleMachines

	for _, group := range groupByKey(measurable) {
		// Bounded window first: the packing search is superlinear in group
		// size, so an unbounded group is what turns a growing backlog into a
		// planner that never finishes. See selectWindow for how the window is
		// chosen and why Draft members are always in it.
		window, out := selectWindow(group, now, gate)
		deferred = append(deferred, deferredFor(out, ReasonOutsideWindow)...)

		planned, unb := bestPartition(window, now, gate, nest)
		unbatchable = append(unbatchable, unb...)
		for _, p := range planned {
			create, usedIdle := shouldCreateBatch(p, now, gate, idleBudget)
			if usedIdle {
				idleBudget--
			}
			if create {
				batches = append(batches, p)
				continue
			}
			held = append(held, p)
			deferred = append(deferred, deferredFor(p.Jobs, holdReason(p, now, gate))...)
		}
	}
	return batches, unbatchable, held, deferred
}

// shouldCreateBatch reports whether a partition should become a real batch
// now rather than stay held waiting for more compatible volume.
// shouldCreateBatch reports whether a partition becomes a real batch now, and
// whether it consumed one of the run's idle-machine allowances (see Plan's
// idleBudget - the caller decrements on a true second return).
func shouldCreateBatch(b PlannedBatch, now time.Time, gate BatchGate, idleBudget int) (create, usedIdle bool) {
	if b.BedUtilisationPercent >= TargetBedUtilisationPercent {
		return true, false
	}
	// Every unconditional override is checked BEFORE the idle-machine budget,
	// so a batch that would ship anyway never spends an allowance meant for a
	// partition that has no other reason to go.
	for _, j := range b.Jobs {
		if j.Priority <= urgentPriority {
			return true, false
		}
		if j.DueDate != nil && !j.DueDate.After(now.Add(gate.DueSoonWindow)) {
			return true, false
		}
		if !j.CreatedAt.IsZero() && now.Sub(j.CreatedAt) >= gate.MaxWait {
			return true, false
		}
	}
	if wait := longestJobWait(b.Jobs, now); wait > 0 {
		if b.BedUtilisationPercent >= effectiveUtilisationThreshold(wait, gate) {
			return true, false
		}
	}
	// Last: a printer is free. Holding the bed back costs machine time; sending
	// it costs the chance of a fuller plate. Decide rather than assume - see
	// worthWaitingForFuller. Bounded by how many printers are actually free.
	if idleBudget > 0 {
		if worthWaitingForFuller(b, now, gate) {
			return false, false
		}
		return true, true
	}
	return false, false
}

// worthWaitingForFuller answers "wait, or print?" for a thin bed while a
// printer stands idle.
//
// By the time this runs, fillBed has already pulled in every compatible job in
// the window, so waiting cannot improve this bed with anything that exists
// right now - only with something that has not arrived yet. That makes the
// question a bet, and the answer has to be bounded on both sides: print
// everything the moment a machine frees up and a backlog becomes a run of
// single-job beds that can never be consolidated, because a created batch never
// returns to the pool; wait for the perfect plate and the machine idles for
// work that may never come.
//
// So the wait is short and expires. It ends when either:
//   - the bed is already at or above the aging floor, i.e. respectable enough
//     that a marginal improvement is not worth idle machine time, or
//   - its oldest job has already waited out IdleWaitWindow.
//
// IdleWaitWindow of zero disables waiting entirely, which is the behaviour
// every existing caller and test gets from a zero-value BatchGate.
func worthWaitingForFuller(b PlannedBatch, now time.Time, gate BatchGate) bool {
	if gate.IdleWaitWindow <= 0 {
		return false
	}
	if gate.AgingFloorPercent > 0 && b.BedUtilisationPercent >= gate.AgingFloorPercent {
		return false
	}
	return longestJobWait(b.Jobs, now) < gate.IdleWaitWindow
}

// holdReason names why a packed partition was not created, so its jobs can be
// reported individually rather than vanishing from the board.
func holdReason(b PlannedBatch, now time.Time, gate BatchGate) string {
	if gate.IdleMachines > 0 && worthWaitingForFuller(b, now, gate) {
		return ReasonWaitingForBetterBatch
	}
	return ReasonWaitingForCompatible
}

// longestJobWait is how long the longest-queued job in the batch has been
// waiting, the single scalar effectiveUtilisationThreshold needs - the same
// "how long has this batch been sitting" subject as the MaxWait loop above,
// reduced to one value instead of a per-job early-exit.
func longestJobWait(jobs []PlanJob, now time.Time) time.Duration {
	var longest time.Duration
	for _, j := range jobs {
		if j.CreatedAt.IsZero() {
			continue
		}
		if w := now.Sub(j.CreatedAt); w > longest {
			longest = w
		}
	}
	return longest
}

// effectiveUtilisationThreshold linearly relaxes the acceptance bar from
// TargetBedUtilisationPercent at wait=0 down to gate.AgingFloorPercent at
// wait>=gate.AgingWindow, then holds flat at the floor - MaxWait's separate,
// unconditional override is what eventually ships a batch that never clears
// even the floor. AgingWindow<=0 disables aging, returning the unchanged
// target (today's behaviour).
func effectiveUtilisationThreshold(wait time.Duration, gate BatchGate) float64 {
	if gate.AgingWindow <= 0 {
		return TargetBedUtilisationPercent
	}
	t := float64(wait) / float64(gate.AgingWindow)
	if t >= 1 {
		return gate.AgingFloorPercent
	}
	if t <= 0 {
		return TargetBedUtilisationPercent
	}
	return TargetBedUtilisationPercent - (TargetBedUtilisationPercent-gate.AgingFloorPercent)*t
}

// groupKey is the hard compatibility boundary: the nozzle, quality and
// material a bed is physically set up for. Two jobs that agree on all of it
// are candidates for the same plate; two that differ on any of it can never
// share one.
//
// Everything else is a preference, not a wall, and belongs in scoreBatch or
// the lock gate instead. This key used to carry five more fields, and each one
// silently halved the pool a bed could draw from:
//
//   - priorityTier: an urgent job could never share a bed with a routine one,
//     which is backwards - priority decides what gets attention first, not
//     what can physically print together. It is a score term (priorityScore).
//   - infillBucket, supportUsed: slicing-process settings, not bed
//     compatibility. A mixed-infill bed is legitimate; it just has to be
//     sliced conservatively - see plateSliceSpecFor.
//   - machineFamily: implied by nozzle and material today, because the fleet
//     is one family and bedpack has one bed size. THE DAY A SECOND BED SIZE
//     JOINS THE FLEET THIS MUST COME BACK, because two families' plates are
//     not interchangeable. Until then assignMachineForBatch refuses to guess
//     for a batch whose jobs span families.
//
// Colour was already correctly absent: two colours can share a bed via
// different AMS slots, so it is a live cap while packing (maxGroupColours,
// withinColourCap) plus a changeover cost in the score.
type groupKey struct {
	material    string
	nozzleLeft  string
	nozzleRight string
	qualityMM   string
}

func keyFor(j PlanJob) groupKey {
	return groupKey{
		material: j.Material, nozzleLeft: j.NozzleLeft,
		nozzleRight: j.NozzleRight, qualityMM: j.QualityMM,
	}
}

// groupByKey partitions jobs into compatibility buckets (Stage 4: Grouping),
// preserving input (FCFS) order within each group and iterating groups in a
// stable key order. It decides nothing about bed layout or batch membership -
// that's bestPartition's job (Stage 5: Optimization), run per group below.
func groupByKey(jobs []PlanJob) [][]PlanJob {
	order := []groupKey{}
	byKey := map[groupKey][]PlanJob{}
	for _, j := range jobs {
		k := keyFor(j)
		if _, seen := byKey[k]; !seen {
			order = append(order, k)
		}
		byKey[k] = append(byKey[k], j)
	}
	sort.Slice(order, func(i, j int) bool {
		a, b := order[i], order[j]
		if a.material != b.material {
			return a.material < b.material
		}
		if a.nozzleLeft != b.nozzleLeft {
			return a.nozzleLeft < b.nozzleLeft
		}
		if a.nozzleRight != b.nozzleRight {
			return a.nozzleRight < b.nozzleRight
		}
		return a.qualityMM < b.qualityMM
	})
	out := make([][]PlanJob, 0, len(order))
	for _, k := range order {
		out = append(out, byKey[k])
	}
	return out
}

// bestPartition tries each ordering strategy over a cluster (Stage 5: the
// Batch Optimizer's first pass), keeps the highest-total-score partition, then
// runs a bounded local search over it to find combinations a single greedy
// pass would miss. Jobs too big for the bed on their own are the same under
// every strategy, so they are reported once.
func bestPartition(cluster []PlanJob, now time.Time, gate BatchGate, nest Nester) ([]PlannedBatch, []Unbatchable) {
	sc := scoreCtx{now: now, gate: gate}
	var best []PlannedBatch
	var bestUnb []Unbatchable
	bestScore := math.Inf(-1)

	for _, strategy := range packingStrategies {
		batches, unb := packJobs(orderJobs(cluster, strategy), strategy, nest)
		if score := partitionScore(batches, sc); score > bestScore {
			best, bestUnb, bestScore = batches, unb, score
		}
	}
	return localSearch(best, sc, nest), bestUnb
}

// orderJobs returns the cluster in the given strategy's order (a copy).
func orderJobs(jobs []PlanJob, strategy string) []PlanJob {
	out := append([]PlanJob{}, jobs...)
	switch strategy {
	case strategyArea:
		sort.SliceStable(out, func(i, j int) bool { return unitArea(out[i]) > unitArea(out[j]) })
	case strategyTime:
		sort.SliceStable(out, func(i, j int) bool { return estMinutes(out[i]) > estMinutes(out[j]) })
	case strategyPriority:
		// Urgent (priority <= urgentPriority) jobs first, FCFS within each tier.
		sort.SliceStable(out, func(i, j int) bool {
			iu, ju := out[i].Priority <= urgentPriority, out[j].Priority <= urgentPriority
			return iu && !ju
		})
	}
	return out
}

func unitArea(j PlanJob) float64 {
	return j.Footprint.XMM * j.Footprint.YMM * float64(quantityOf(j))
}

func estMinutes(j PlanJob) int {
	if j.EstimatedMinutes == nil {
		return -1
	}
	return *j.EstimatedMinutes
}

func quantityOf(j PlanJob) int {
	if j.Quantity < 1 {
		return 1
	}
	return j.Quantity
}

// packJobs fills beds one at a time via fillBed, which pulls in whatever
// compatible jobs still fit - not just the next one in strategy order - so
// each bed is packed toward TargetBedUtilisationPercent before it's closed
// out. A job that cannot fit even alone (the only jobs left when fillBed
// places nothing) is unbatchable - unless splitJobToFit finds that a smaller
// quantity of it does fit alone, in which case that quantity is retried
// through the normal fillBed path (so it can still combine with other
// compatible jobs, same as anything else) and the rest is carried forward to
// split further on a later bed, until either the whole original quantity is
// placed across however many beds it took, or a single unit genuinely
// doesn't fit at all.
func packJobs(jobs []PlanJob, strategy string, nest Nester) ([]PlannedBatch, []Unbatchable) {
	var batches []PlannedBatch
	var unb []Unbatchable
	remaining := append([]PlanJob{}, jobs...)

	for len(remaining) > 0 {
		bedJobs, bedUnits, rest := fillBed(remaining, nest)
		if len(bedJobs) == 0 {
			head := remaining[0]
			fragment, leftover, ok := splitJobToFit(head, nest)
			if !ok {
				unb = append(unb, Unbatchable{
					JobID: head.ID, JobNumber: head.JobNumber,
					Reason: ReasonOversized,
				})
				remaining = remaining[1:]
				continue
			}
			remaining = append(append([]PlanJob{fragment}, remaining[1:]...), leftover)
			continue
		}
		batches = append(batches, finalise(bedJobs, bedUnits, strategy, nest))
		remaining = rest
	}
	return batches, unb
}

// splitJobToFit reports the largest quantity of j (starting from 1) that
// nest fits alone on an empty bed, as (fragment, leftover, ok=true) -
// fragment carries that quantity, leftover carries whatever's left over,
// both otherwise identical copies of j. ok is false only when not even a
// single unit fits alone (genuinely oversized geometry, unrelated to
// quantity), or j's quantity is already 1 (nothing left to split) - the
// caller's existing unbatchable path handles both.
func splitJobToFit(j PlanJob, nest Nester) (fragment, leftover PlanJob, ok bool) {
	qty := quantityOf(j)
	if qty <= 1 {
		return PlanJob{}, PlanJob{}, false
	}
	placements, _ := nest(unitsFor(j))
	fits := len(placements)
	if fits <= 0 || fits >= qty {
		return PlanJob{}, PlanJob{}, false
	}
	fragment, leftover = j, j
	fragment.Quantity = fits
	leftover.Quantity = qty - fits
	return fragment, leftover, true
}

// fillBed greedily fills one bed to its maximum: it repeatedly scans the
// still-unplaced jobs for one that both stays within the bed's colour cap
// (withinColourCap) and still packs alongside everything already placed,
// adds it, and rescans from the front - so a job that doesn't fit (or
// doesn't colour-fit) next in line no longer stops the bed short; a later,
// smaller or same-colour-family job gets pulled in ahead of it instead.
// Within that, each round prefers (soft, never overriding fit or the colour
// cap) a candidate sharing a known customer with something already on this
// bed - see selectNext/sameCustomerOnBed - falling back to strategy order
// when no such candidate is eligible. This keeps going until a full scan
// places nothing more, i.e. the bed is genuinely as full as this job set
// (and its colour budget) allows - never capped at the 80% target itself,
// more fill is always better, 80% is only the floor checked by the caller.
func fillBed(jobs []PlanJob, nest Nester) (bedJobs []PlanJob, bedUnits []bedpack.UnitFootprint, remaining []PlanJob) {
	remaining = append([]PlanJob{}, jobs...)
	for {
		idx, trial, ok := selectNext(remaining, bedJobs, bedUnits, sameCustomerOnBed, nest)
		if !ok {
			idx, trial, ok = selectNext(remaining, bedJobs, bedUnits, anyJob, nest)
		}
		if !ok {
			return bedJobs, bedUnits, remaining
		}
		bedJobs = append(bedJobs, remaining[idx])
		bedUnits = trial
		remaining = append(remaining[:idx:idx], remaining[idx+1:]...)
	}
}

// selectNext scans remaining for the first job that passes match, stays
// within the colour cap, and still packs (per nest) alongside everything
// already on the bed - the single eligibility check fillBed always used, now
// reused across two candidate-ordering passes (see fillBed) so a preference
// can only ever change which eligible job is tried first, never which jobs
// are eligible at all.
func selectNext(remaining, bedJobs []PlanJob, bedUnits []bedpack.UnitFootprint, match func([]PlanJob, PlanJob) bool, nest Nester) (int, []bedpack.UnitFootprint, bool) {
	for i, j := range remaining {
		if !match(bedJobs, j) {
			continue
		}
		if !withinColourCap(append(append([]PlanJob{}, bedJobs...), j)) {
			continue
		}
		trial := append(append([]bedpack.UnitFootprint{}, bedUnits...), unitsFor(j)...)
		if _, rejected := nest(trial); len(rejected) == 0 {
			return i, trial, true
		}
	}
	return -1, nil, false
}

// sameCustomerOnBed reports whether cand shares a known Shopify customer
// with something already placed on this bed. A nil ShopifyCustomerID never
// matches anything, including another nil - two jobs with no known customer
// are not "the same customer." Also false while the bed is still empty,
// since anyJob picks the first job on any bed regardless of customer.
func sameCustomerOnBed(bedJobs []PlanJob, cand PlanJob) bool {
	if cand.ShopifyCustomerID == nil {
		return false
	}
	for _, bj := range bedJobs {
		if bj.ShopifyCustomerID != nil && *bj.ShopifyCustomerID == *cand.ShopifyCustomerID {
			return true
		}
	}
	return false
}

// anyJob is fillBed's fallback match: every candidate is eligible, same as
// its behaviour before the same-customer preference existed.
func anyJob([]PlanJob, PlanJob) bool { return true }

// colourUnionSize is how many distinct filament colours a set of jobs uses
// combined.
func colourUnionSize(jobs []PlanJob) int {
	seen := map[string]bool{}
	for _, j := range jobs {
		for _, c := range j.Colours {
			seen[c] = true
		}
	}
	return len(seen)
}

// withinColourCap reports whether a bed's jobs stay at or under
// maxGroupColours combined. A single job is always allowed regardless of its
// own colour count - how many colours one part needs is a fact about that
// part, not a batching choice; the cap only ever blocks COMBINING jobs whose
// colours together would exceed it.
func withinColourCap(jobs []PlanJob) bool {
	return len(jobs) <= 1 || colourUnionSize(jobs) <= maxGroupColours
}

// unitsFor expands a job into one footprint per unit, tagged with the job id.
func unitsFor(j PlanJob) []bedpack.UnitFootprint {
	n := quantityOf(j)
	units := make([]bedpack.UnitFootprint, n)
	for i := range n {
		units[i] = bedpack.UnitFootprint{RefID: j.ID, XMM: j.Footprint.XMM, YMM: j.Footprint.YMM, ZMM: j.Footprint.ZMM}
	}
	return units
}

// finalise computes a batch's placements and snapshot metrics.
func finalise(jobs []PlanJob, units []bedpack.UnitFootprint, strategy string, nest Nester) PlannedBatch {
	return finaliseWith(jobs, units, strategy, bedpack.DefaultBed, func(units []bedpack.UnitFootprint) []bedpack.Placement {
		placements, _ := nest(units)
		return placements
	})
}

// finaliseOn is finalise for a batch built on a named bed - the colour strategy,
// where a bed is a P2S's or an H2C's and its utilisation only means anything
// measured against that one.
func finaliseOn(
	bed bedpack.Bed, jobs []PlanJob, units []bedpack.UnitFootprint,
	strategy string, nest BedNester,
) PlannedBatch {
	return finaliseWith(jobs, units, strategy, bed, func(units []bedpack.UnitFootprint) []bedpack.Placement {
		placements, _ := nest(bed, units)
		return placements
	})
}

// finaliseWith is the shared body: pack, then fill in the metrics the
// orchestrator commits.
func finaliseWith(
	jobs []PlanJob, units []bedpack.UnitFootprint, strategy string,
	bed bedpack.Bed, pack func([]bedpack.UnitFootprint) []bedpack.Placement,
) PlannedBatch {
	placements := pack(units)
	totalTime, effective := batchTimeFields(jobs)
	var filament float64
	for _, j := range jobs {
		filament += j.FilamentGrams
	}
	return PlannedBatch{
		Jobs: jobs, Placements: placements, UnitsPerBed: len(units),
		TotalPrintTimeMinutes: totalTime, EffectiveTimePerUnitMinutes: effective,
		TotalFilamentGrams: filament, BedUtilisationPercent: bedpack.UtilisationPercentOn(bed, units),
		PackingStrategy: strategy,
	}
}

// batchTimeFields returns the batch's total print time (the max job time, since a
// bed prints in parallel) and the per-unit effective time. Both are nil if any
// job has no estimated time.
func batchTimeFields(jobs []PlanJob) (*int, *float64) {
	units := 0
	maxTime := 0
	for _, j := range jobs {
		units += quantityOf(j)
		if j.EstimatedMinutes == nil {
			return nil, nil
		}
		if *j.EstimatedMinutes > maxTime {
			maxTime = *j.EstimatedMinutes
		}
	}
	total := maxTime
	var eff *float64
	if units > 0 {
		v := math.Round(float64(total)/float64(units)*100) / 100
		eff = &v
	}
	return &total, eff
}

// scoreCtx is what a score needs beyond the batch itself: the clock, and the
// windows that make "due soon" and "waited long enough" mean anything. Passed
// explicitly rather than held as package state so scoring stays pure and a
// test can score the same batch at two different instants.
type scoreCtx struct {
	now  time.Time
	gate BatchGate
}

// referencePlateMinutes is the print time a batch is penalised against: a
// plate at or above this scores the full time penalty, one at zero scores
// none. Twelve hours is roughly a full overnight plate on this fleet.
//
// A reference is what makes the term safe. It used to be `minutes * 0.10` on
// RAW minutes, so a 400-minute plate scored -40 against a maximum of +50 from
// utilisation - print time quietly outweighed everything the optimizer was
// supposed to be balancing, and the effect grew without bound as plates got
// longer. Normalising caps its influence at its weight, which is the whole
// point of having weights.
const referencePlateMinutes = 12 * 60

// referenceThroughput is the utilisation-per-machine-hour a batch must reach to
// score the full throughput term. 40 points/hour is a bed half full in 75
// minutes, or nearly full in two and a half - brisk for this fleet without
// being unreachable, so the term discriminates across the range real plates
// actually occupy instead of saturating.
const referenceThroughput = 40.0

// ScoreWeights are the multipliers scoreBatch applies to its normalised terms.
// Gathered into one struct so the trade-offs are visible together and tunable
// in one place, rather than as literals scattered through an expression.
//
// Every term feeding these is normalised to 0-100 first, which is what makes
// the weights comparable at all: on raw values the biggest-unit term silently
// wins regardless of intent, which is exactly how print time (raw minutes) came
// to outweigh everything before it was normalised.
type ScoreWeights struct {
	Utilisation  float64 // fill the bed
	Throughput   float64 // utilisation delivered per machine-hour
	Priority     float64 // urgent work first
	DueUrgency   float64 // due-soon work first
	Waiting      float64 // nothing starves
	PrintTime    float64 // subtracted: a fuller bed is not worth an unreasonable one
	ColourChange float64 // subtracted: fewer AMS changeovers
	Cohesion     float64 // one customer's units ship together
}

// DefaultScoreWeights is the tuning in use.
//
// Utilisation and print time are set against each other deliberately, and the
// ratio settles a specific case: a 96%-full 7-hour plate versus an 86%-full
// 3-hour one. The fuller plate looks better and is worse - it delivers 13.7
// utilisation-points per machine-hour against the leaner plate's 28.7. At these
// weights the 3-hour plate wins, which TestScoreBalancesUtilisationAgainstPrintTime
// locks in. Raising Utilisation (or lowering PrintTime/Throughput) reverses it
// and quietly trades throughput for a better-looking number.
//
// Throughput is deliberately a modest term, not a replacement for utilisation.
// It expresses the same preference the utilisation/print-time pair already
// encodes, stated directly - so the ranking degrades gracefully rather than
// flipping on a metric that goes to infinity as print time approaches zero.
//
// Waiting carries less weight than the others because it is not the mechanism
// that prevents starvation - BatchGate.MaxWait is, unconditionally, in
// shouldCreateBatch. This term only tilts the ranking on the way there.
//
// The two raw-count terms (colours, cohesion) stay small and unnormalised on
// purpose: both are bounded by how many jobs fit on one bed, so they act as
// tie-breakers between similarly-scoring partitions and can never outweigh a
// meaningful utilisation difference.
var DefaultScoreWeights = ScoreWeights{
	Utilisation:  0.34,
	Throughput:   0.10,
	Priority:     0.20,
	DueUrgency:   0.15,
	Waiting:      0.10,
	PrintTime:    0.13,
	ColourChange: 0.05,
	Cohesion:     0.05,
}

// throughputScore is how much bed utilisation a batch delivers per machine-hour,
// normalised 0-100 against referenceThroughput.
//
// This is the "good bed utilisation is not the same thing as good production
// efficiency" term. A 96%/7h plate and an 86%/3h plate are within ten
// utilisation points of each other and nowhere near within ten points of equal
// value: the second frees the machine four hours sooner for a fraction of the
// fill.
//
// Zero when the print time is unknown or non-positive rather than infinite -
// an unestimated batch must not score as infinitely efficient and displace
// every measured one.
func throughputScore(b PlannedBatch) float64 {
	if b.TotalPrintTimeMinutes == nil || *b.TotalPrintTimeMinutes <= 0 {
		return 0
	}
	perHour := b.BedUtilisationPercent / (float64(*b.TotalPrintTimeMinutes) / 60)
	if s := perHour / referenceThroughput * 100; s < 100 {
		return s
	}
	return 100
}

// scoreBatch is the single number the optimizer maximises - a weighted sum of
// normalised terms. See ScoreWeights/DefaultScoreWeights for what each term is
// worth and why.
func scoreBatch(b PlannedBatch, sc scoreCtx) float64 {
	w := DefaultScoreWeights
	return b.BedUtilisationPercent*w.Utilisation +
		throughputScore(b)*w.Throughput +
		priorityScore(b.Jobs)*w.Priority +
		dueUrgencyScore(b.Jobs, sc.now)*w.DueUrgency +
		waitingScore(b.Jobs, sc.now, sc.gate.MaxWait)*w.Waiting -
		printTimeScore(b)*w.PrintTime -
		float64(colourChangeCount(b))*w.ColourChange +
		float64(customerCohesionCount(b))*w.Cohesion
}

// EvaluateBatch builds a PlannedBatch from a fixed set of jobs, packing and
// measuring it exactly as the planner would one of its own proposals. ok is
// false when the set does not physically fit a single bed.
//
// This exists so an existing Draft can be scored on precisely the same terms as
// the proposal that would replace it. Comparing a stored batch row's utilisation
// against a freshly-scored candidate would compare two different measurements
// and make the stability threshold meaningless.
func EvaluateBatch(jobs []PlanJob) (PlannedBatch, bool) {
	if len(jobs) == 0 {
		return PlannedBatch{}, false
	}
	var units []bedpack.UnitFootprint
	for _, j := range jobs {
		units = append(units, unitsFor(j)...)
	}
	if _, rejected := DefaultNester(units); len(rejected) > 0 {
		return PlannedBatch{}, false
	}
	return finalise(jobs, units, strategyFCFS, DefaultNester), true
}

// ScorePlan is partitionScore for callers outside this package: the summed
// quality of a whole set of batches, on the same scale scoreBatch uses.
//
// Exported so the orchestrator can compare a freshly-planned partition against
// the Drafts already on the floor and decide whether the improvement is worth
// churning them - see the plan-stability check in AutoCreateBatches. Comparing
// through the planner's own scoring rather than a separate rule of thumb is the
// point: "better" has to mean the same thing to both.
func ScorePlan(batches []PlannedBatch, now time.Time, gate BatchGate) float64 {
	return partitionScore(batches, scoreCtx{now: now, gate: gate})
}

// dueUrgencyScore is how pressing the most urgent due date on the bed is, 0-100:
// 100 at or past due, 0 at dueUrgencyHorizon or beyond, linear between. A batch
// whose jobs all have no due date scores 0 - absent is not urgent.
func dueUrgencyScore(jobs []PlanJob, now time.Time) float64 {
	worst := 0.0
	for _, j := range jobs {
		if j.DueDate == nil {
			continue
		}
		until := j.DueDate.Sub(now)
		switch {
		case until <= 0:
			return 100
		case until >= dueUrgencyHorizon:
			continue
		}
		if s := (1 - float64(until)/float64(dueUrgencyHorizon)) * 100; s > worst {
			worst = s
		}
	}
	return worst
}

// waitingScore is how long the longest-queued job on the bed has waited, as a
// fraction of MaxWait, 0-100. This is what stops a job that is neither urgent
// nor due soon from starving behind fuller beds forever: its score climbs the
// longer it sits, until MaxWait's unconditional override in shouldCreateBatch
// takes over regardless. Zero when MaxWait is unset.
func waitingScore(jobs []PlanJob, now time.Time, maxWait time.Duration) float64 {
	if maxWait <= 0 {
		return 0
	}
	longest := longestJobWait(jobs, now)
	if longest <= 0 {
		return 0
	}
	if longest >= maxWait {
		return 100
	}
	return float64(longest) / float64(maxWait) * 100
}

// printTimeScore is the batch's print time as a 0-100 penalty against
// referencePlateMinutes. A batch with no estimate scores 0 rather than being
// treated as instant or infinite: an unknown time is not evidence either way,
// and the utilisation and urgency terms still rank it.
func printTimeScore(b PlannedBatch) float64 {
	if b.TotalPrintTimeMinutes == nil {
		return 0
	}
	minutes := float64(*b.TotalPrintTimeMinutes)
	if minutes <= 0 {
		return 0
	}
	if minutes >= referencePlateMinutes {
		return 100
	}
	return minutes / referencePlateMinutes * 100
}

// customerCohesionCount is how many jobs in the batch are "redundant" with
// an already-represented customer: total jobs minus distinct identity
// buckets, where every known ShopifyCustomerID is its own bucket and every
// job with no known customer gets its own unique bucket - so unset-customer
// jobs never cluster with each other or inflate this count. A cohesive
// same-customer batch scores higher than an otherwise-identical scattered
// one, without ever letting cohesion alone beat a meaningfully better-
// utilised partition (see scoreBatch's doc comment).
func customerCohesionCount(b PlannedBatch) int {
	seen := map[int64]bool{}
	buckets := 0
	for _, j := range b.Jobs {
		if j.ShopifyCustomerID == nil {
			buckets++
			continue
		}
		if !seen[*j.ShopifyCustomerID] {
			seen[*j.ShopifyCustomerID] = true
			buckets++
		}
	}
	return len(b.Jobs) - buckets
}

func partitionScore(batches []PlannedBatch, sc scoreCtx) float64 {
	var total float64
	for _, b := range batches {
		total += scoreBatch(b, sc)
	}
	return total
}

// priorityScore is the share of urgent (same-day, priority <= urgentPriority)
// jobs in the batch, 0-100.
func priorityScore(jobs []PlanJob) float64 {
	if len(jobs) == 0 {
		return 0
	}
	urgent := 0
	for _, j := range jobs {
		if j.Priority <= urgentPriority {
			urgent++
		}
	}
	return float64(urgent) / float64(len(jobs)) * 100
}

// colourChangeCount is how many distinct colours the batch's jobs use
// combined - always <= maxGroupColours per withinColourCap, but still
// scored as a penalty so the optimizer prefers fewer colour changes (less
// AMS/filament-swap overhead) when it has a choice between otherwise
// similar-scoring partitions.
func colourChangeCount(b PlannedBatch) int {
	return colourUnionSize(b.Jobs)
}

// maxLocalSearchIterations bounds the 2-opt-style improvement pass below -
// tractable at realistic group sizes (tens of jobs) without a solver
// dependency, per-cluster since it only ever swaps within one already-grouped,
// already-compatible bestPartition() call.
const maxLocalSearchIterations = 50

// localSearch tries pairwise single-job swaps between every pair of batches in
// a partition, keeping any swap that improves the pair's combined score while
// both batches still fit their beds. This is what finds a partition like
// A+B+D=90% instead of settling for whichever greedy strategy found A+B=60%
// first - bestPartition's strategy loop alone cannot discover it, since no
// single fixed ordering produces that grouping.
func localSearch(batches []PlannedBatch, sc scoreCtx, nest Nester) []PlannedBatch {
	if len(batches) < 2 {
		return batches
	}
	out := append([]PlannedBatch{}, batches...)
	for iter := 0; iter < maxLocalSearchIterations; iter++ {
		improved := false
		for i := 0; i < len(out); i++ {
			for j := i + 1; j < len(out); j++ {
				if swapBest(out, i, j, sc, nest) {
					improved = true
				}
			}
		}
		if !improved {
			break
		}
	}
	return out
}

// swapBest tries every single-job exchange between out[i] and out[j], applying
// the first one that improves their combined score while both batches still
// pack onto a bed. Reports whether it made a swap.
func swapBest(out []PlannedBatch, i, j int, sc scoreCtx, nest Nester) bool {
	baseScore := scoreBatch(out[i], sc) + scoreBatch(out[j], sc)
	for a := range out[i].Jobs {
		for b := range out[j].Jobs {
			candA := replaceJob(out[i].Jobs, a, out[j].Jobs[b])
			candB := replaceJob(out[j].Jobs, b, out[i].Jobs[a])
			if !withinColourCap(candA) || !withinColourCap(candB) {
				continue
			}
			unitsA, unitsB := unitsForAll(candA), unitsForAll(candB)
			if _, rejected := nest(unitsA); len(rejected) > 0 {
				continue
			}
			if _, rejected := nest(unitsB); len(rejected) > 0 {
				continue
			}
			newA := finalise(candA, unitsA, out[i].PackingStrategy, nest)
			newB := finalise(candB, unitsB, out[j].PackingStrategy, nest)
			if scoreBatch(newA, sc)+scoreBatch(newB, sc) > baseScore {
				out[i], out[j] = newA, newB
				return true
			}
		}
	}
	return false
}

func replaceJob(jobs []PlanJob, idx int, with PlanJob) []PlanJob {
	out := append([]PlanJob{}, jobs...)
	out[idx] = with
	return out
}

func unitsForAll(jobs []PlanJob) []bedpack.UnitFootprint {
	var units []bedpack.UnitFootprint
	for _, j := range jobs {
		units = append(units, unitsFor(j)...)
	}
	return units
}
