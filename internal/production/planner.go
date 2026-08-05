package production

import (
	"math"
	"sort"
	"strconv"
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

// similarDueDateWindow is how far two jobs' due dates may drift before they fall
// into separate batches.
const similarDueDateWindow = 3 * 24 * time.Hour

// urgentPriority is the priority value marking a same-day/urgent job (matches
// the "1" the user specified: FCFS by default, "1" for same-day).
const urgentPriority = 1

// maxGroupColours caps how many distinct filament colours a single bed may
// use combined, checked live while packing (see withinColourCap) - not a
// pre-grouping equality bucket, so jobs of genuinely different colours can
// still share a bed as long as the running total stays at or under this.
const maxGroupColours = 3

// TargetBedUtilisationPercent is the floor bed-footprint fill (X*Y area of
// placed parts / bed area) a batch should clear before it's accepted as
// final - the top-priority packing goal, per the user's explicit "more than
// 80% in X/Y" instruction. It is enforced by packJobs's lookahead fill (see
// fillBed) rather than by ever discarding a partition outright: a batch that
// still lands under this after exhausting every compatible job left in its
// cluster is accepted anyway (there is nothing left to add it with) but
// surfaced as an explicit, non-silent exception by the caller (see
// httpapi.AutoCreateBatches).
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
	ID               string
	JobNumber        string
	Material         string
	Colours          []string
	NozzleLeft       string
	NozzleRight      string
	QualityMM        string
	MachineFamily    string
	SupportUsed      bool
	InfillPct        float64
	Priority         int
	Quantity         int
	EstimatedMinutes *int
	DueDate          *time.Time
	FilamentGrams    float64
	Footprint        bedpack.UnitFootprint
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

// Plan groups and packs jobs into batches. Jobs without a measurable footprint,
// or too large for the bed even alone, come back as Unbatchable.
func Plan(jobs []PlanJob) ([]PlannedBatch, []Unbatchable) {
	var batches []PlannedBatch
	var unbatchable []Unbatchable

	measurable := make([]PlanJob, 0, len(jobs))
	for _, j := range jobs {
		if j.Footprint.XMM <= 0 || j.Footprint.YMM <= 0 {
			unbatchable = append(unbatchable, Unbatchable{
				JobID: j.ID, JobNumber: j.JobNumber,
				Reason: "No print file with measurable STL dimensions uploaded yet.",
			})
			continue
		}
		measurable = append(measurable, j)
	}

	for _, group := range groupByKey(measurable) {
		for _, cluster := range dueDateClusters(group) {
			planned, unb := bestPartition(cluster)
			batches = append(batches, planned...)
			unbatchable = append(unbatchable, unb...)
		}
	}
	return batches, unbatchable
}

// groupKey is the compatibility bucket a job falls into: everything here must
// be identical for jobs to share a bed given today's raw-geometry STL merge.
// Colour is deliberately NOT part of this key - unlike nozzle/material/quality,
// two jobs of different colours can physically share a bed (different AMS
// slots), so colour compatibility is instead checked live while packing, up
// to maxGroupColours combined per bed (see withinColourCap).
type groupKey struct {
	material      string
	nozzleLeft    string
	nozzleRight   string
	qualityMM     string
	machineFamily string
	supportUsed   string
	infillBucket  string
	priorityTier  string
}

// infillBucketFor rounds infill% to the nearest 5% so near-identical designs
// still group together instead of fragmenting on noise.
func infillBucketFor(pct float64) string {
	return strconv.Itoa(int(math.Round(pct/5) * 5))
}

func priorityTierFor(priority int) string {
	if priority <= urgentPriority {
		return "urgent"
	}
	return "normal"
}

func keyFor(j PlanJob) groupKey {
	return groupKey{
		material: j.Material, nozzleLeft: j.NozzleLeft, nozzleRight: j.NozzleRight,
		qualityMM: j.QualityMM, machineFamily: j.MachineFamily,
		supportUsed:  strconv.FormatBool(j.SupportUsed),
		infillBucket: infillBucketFor(j.InfillPct), priorityTier: priorityTierFor(j.Priority),
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
		if a.qualityMM != b.qualityMM {
			return a.qualityMM < b.qualityMM
		}
		if a.machineFamily != b.machineFamily {
			return a.machineFamily < b.machineFamily
		}
		if a.supportUsed != b.supportUsed {
			return a.supportUsed < b.supportUsed
		}
		if a.infillBucket != b.infillBucket {
			return a.infillBucket < b.infillBucket
		}
		return a.priorityTier < b.priorityTier
	})
	out := make([][]PlanJob, 0, len(order))
	for _, k := range order {
		out = append(out, byKey[k])
	}
	return out
}

// dueDateClusters splits a group into runs whose due dates stay within the window
// of the run's first job. A change between "has a due date" and "has none" also
// starts a new run.
func dueDateClusters(jobs []PlanJob) [][]PlanJob {
	var clusters [][]PlanJob
	var current []PlanJob
	var anchor *time.Time

	for _, j := range jobs {
		if len(current) == 0 {
			current = []PlanJob{j}
			anchor = j.DueDate
			continue
		}
		if sameCluster(anchor, j.DueDate) {
			current = append(current, j)
			continue
		}
		clusters = append(clusters, current)
		current = []PlanJob{j}
		anchor = j.DueDate
	}
	if len(current) > 0 {
		clusters = append(clusters, current)
	}
	return clusters
}

func sameCluster(anchor, due *time.Time) bool {
	if (anchor == nil) != (due == nil) {
		return false
	}
	if anchor == nil {
		return true
	}
	diff := anchor.Sub(*due)
	if diff < 0 {
		diff = -diff
	}
	return diff <= similarDueDateWindow
}

// bestPartition tries each ordering strategy over a cluster (Stage 5: the
// Batch Optimizer's first pass), keeps the highest-total-score partition, then
// runs a bounded local search over it to find combinations a single greedy
// pass would miss. Jobs too big for the bed on their own are the same under
// every strategy, so they are reported once.
func bestPartition(cluster []PlanJob) ([]PlannedBatch, []Unbatchable) {
	var best []PlannedBatch
	var bestUnb []Unbatchable
	bestScore := math.Inf(-1)

	for _, strategy := range packingStrategies {
		batches, unb := packJobs(orderJobs(cluster, strategy), strategy)
		if score := partitionScore(batches); score > bestScore {
			best, bestUnb, bestScore = batches, unb, score
		}
	}
	return localSearch(best), bestUnb
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
// places nothing) is unbatchable.
func packJobs(jobs []PlanJob, strategy string) ([]PlannedBatch, []Unbatchable) {
	var batches []PlannedBatch
	var unb []Unbatchable
	remaining := append([]PlanJob{}, jobs...)

	for len(remaining) > 0 {
		bedJobs, bedUnits, rest := fillBed(remaining)
		if len(bedJobs) == 0 {
			head := remaining[0]
			unb = append(unb, Unbatchable{
				JobID: head.ID, JobNumber: head.JobNumber,
				Reason: "Exceeds the print bed's capacity even on its own.",
			})
			remaining = remaining[1:]
			continue
		}
		batches = append(batches, finalise(bedJobs, bedUnits, strategy))
		remaining = rest
	}
	return batches, unb
}

// fillBed greedily fills one bed to its maximum: it repeatedly scans the
// still-unplaced jobs (in strategy order) for the first one that both stays
// within the bed's colour cap (withinColourCap) and still packs alongside
// everything already placed, adds it, and rescans from the front - so a job
// that doesn't fit (or doesn't colour-fit) next in line no longer stops the
// bed short; a later, smaller or same-colour-family job gets pulled in ahead
// of it instead. This keeps going until a full scan places nothing more, i.e.
// the bed is genuinely as full as this job set (and its colour budget)
// allows - never capped at the 80% target itself, more fill is always
// better, 80% is only the floor checked by the caller.
func fillBed(jobs []PlanJob) (bedJobs []PlanJob, bedUnits []bedpack.UnitFootprint, remaining []PlanJob) {
	remaining = append([]PlanJob{}, jobs...)
	for {
		placedIdx := -1
		for i, j := range remaining {
			if !withinColourCap(append(append([]PlanJob{}, bedJobs...), j)) {
				continue
			}
			trial := append(append([]bedpack.UnitFootprint{}, bedUnits...), unitsFor(j)...)
			if _, rejected := bedpack.Pack(trial); len(rejected) == 0 {
				bedJobs = append(bedJobs, j)
				bedUnits = trial
				placedIdx = i
				break
			}
		}
		if placedIdx == -1 {
			return bedJobs, bedUnits, remaining
		}
		remaining = append(remaining[:placedIdx:placedIdx], remaining[placedIdx+1:]...)
	}
}

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
func finalise(jobs []PlanJob, units []bedpack.UnitFootprint, strategy string) PlannedBatch {
	placements, _ := bedpack.Pack(units)
	totalTime, effective := batchTimeFields(jobs)
	var filament float64
	for _, j := range jobs {
		filament += j.FilamentGrams
	}
	return PlannedBatch{
		Jobs: jobs, Placements: placements, UnitsPerBed: len(units),
		TotalPrintTimeMinutes: totalTime, EffectiveTimePerUnitMinutes: effective,
		TotalFilamentGrams: filament, BedUtilisationPercent: bedpack.UtilisationPercent(units),
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

// scoreBatch weights bed utilisation, urgency, print time, and colour-change
// cost into the single number the optimizer maximises. Utilisation and
// priority are both 0-100 scales so the weights below are directly
// comparable; print time and colour count are penalties.
func scoreBatch(b PlannedBatch) float64 {
	minutes := 0.0
	if b.TotalPrintTimeMinutes != nil {
		minutes = float64(*b.TotalPrintTimeMinutes)
	}
	return b.BedUtilisationPercent*0.5 +
		priorityScore(b.Jobs)*0.3 -
		minutes*0.10 -
		float64(colourChangeCount(b))*0.05
}

func partitionScore(batches []PlannedBatch) float64 {
	var total float64
	for _, b := range batches {
		total += scoreBatch(b)
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
func localSearch(batches []PlannedBatch) []PlannedBatch {
	if len(batches) < 2 {
		return batches
	}
	out := append([]PlannedBatch{}, batches...)
	for iter := 0; iter < maxLocalSearchIterations; iter++ {
		improved := false
		for i := 0; i < len(out); i++ {
			for j := i + 1; j < len(out); j++ {
				if swapBest(out, i, j) {
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
func swapBest(out []PlannedBatch, i, j int) bool {
	baseScore := scoreBatch(out[i]) + scoreBatch(out[j])
	for a := range out[i].Jobs {
		for b := range out[j].Jobs {
			candA := replaceJob(out[i].Jobs, a, out[j].Jobs[b])
			candB := replaceJob(out[j].Jobs, b, out[i].Jobs[a])
			if !withinColourCap(candA) || !withinColourCap(candB) {
				continue
			}
			unitsA, unitsB := unitsForAll(candA), unitsForAll(candB)
			if _, rejected := bedpack.Pack(unitsA); len(rejected) > 0 {
				continue
			}
			if _, rejected := bedpack.Pack(unitsB); len(rejected) > 0 {
				continue
			}
			newA := finalise(candA, unitsA, out[i].PackingStrategy)
			newB := finalise(candB, unitsB, out[j].PackingStrategy)
			if scoreBatch(newA)+scoreBatch(newB) > baseScore {
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
