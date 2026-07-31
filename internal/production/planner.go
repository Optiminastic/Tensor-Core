package production

import (
	"math"
	"sort"
	"time"

	"github.com/Optiminastic/tensor-core/internal/bedpack"
)

// The batch planner groups queued jobs onto printer beds, ported from
// print-queue-be's batch_service. It is pure: jobs (with their footprints) in,
// planned batches out. Jobs are grouped by a hard key (material, colour, nozzle),
// clustered by due date, then packed with a small strategy search.

// similarDueDateWindow is how far two jobs' due dates may drift before they fall
// into separate batches.
const similarDueDateWindow = 3 * 24 * time.Hour

// packingStrategies are the job orderings tried per cluster; the best-scoring one
// wins.
const (
	strategyFCFS = "fcfs"
	strategyArea = "area_desc"
	strategyTime = "time_desc"
)

var packingStrategies = []string{strategyFCFS, strategyArea, strategyTime}

// PlanJob is one job as the planner sees it: its grouping key, quantity, timing,
// filament need, and per-unit footprint. Material/Colour/Nozzle are already
// normalised to "" when absent.
type PlanJob struct {
	ID               string
	JobNumber        string
	Material         string
	Colour           string
	Nozzle           string
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

type groupKey struct{ material, colour, nozzle string }

// groupByKey partitions jobs by (material, colour, nozzle), preserving input
// (FCFS) order within each group and iterating groups in a stable key order.
func groupByKey(jobs []PlanJob) [][]PlanJob {
	order := []groupKey{}
	byKey := map[groupKey][]PlanJob{}
	for _, j := range jobs {
		k := groupKey{j.Material, j.Colour, j.Nozzle}
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
		if a.colour != b.colour {
			return a.colour < b.colour
		}
		return a.nozzle < b.nozzle
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

// bestPartition tries each ordering strategy over a cluster and returns the
// best-scoring set of batches. Jobs too big for the bed on their own are the same
// under every strategy, so they are reported once.
func bestPartition(cluster []PlanJob) ([]PlannedBatch, []Unbatchable) {
	var best []PlannedBatch
	var bestUnb []Unbatchable
	haveBest := false

	for _, strategy := range packingStrategies {
		batches, unb := packJobs(orderJobs(cluster, strategy), strategy)
		if !haveBest || scoreLess(batches, best) {
			best, bestUnb, haveBest = batches, unb, true
		}
	}
	return best, bestUnb
}

// orderJobs returns the cluster in the given strategy's order (a copy).
func orderJobs(jobs []PlanJob, strategy string) []PlanJob {
	out := append([]PlanJob{}, jobs...)
	switch strategy {
	case strategyArea:
		sort.SliceStable(out, func(i, j int) bool { return unitArea(out[i]) > unitArea(out[j]) })
	case strategyTime:
		sort.SliceStable(out, func(i, j int) bool { return estMinutes(out[i]) > estMinutes(out[j]) })
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

// packJobs greedily fills beds: a job joins the current bed if the whole bed still
// packs, otherwise the bed is flushed and the job starts a fresh one. A job that
// cannot fit even alone is unbatchable.
func packJobs(jobs []PlanJob, strategy string) ([]PlannedBatch, []Unbatchable) {
	var batches []PlannedBatch
	var unb []Unbatchable
	var curJobs []PlanJob
	var curUnits []bedpack.UnitFootprint

	flush := func() {
		if len(curJobs) > 0 {
			batches = append(batches, finalise(curJobs, curUnits, strategy))
			curJobs, curUnits = nil, nil
		}
	}

	for _, j := range jobs {
		jobUnits := unitsFor(j)
		trial := append(append([]bedpack.UnitFootprint{}, curUnits...), jobUnits...)
		if _, rejected := bedpack.Pack(trial); len(rejected) == 0 {
			curJobs = append(curJobs, j)
			curUnits = trial
			continue
		}
		flush()
		if _, rejected := bedpack.Pack(jobUnits); len(rejected) > 0 {
			unb = append(unb, Unbatchable{
				JobID: j.ID, JobNumber: j.JobNumber,
				Reason: "Exceeds the print bed's capacity even on its own.",
			})
			continue
		}
		curJobs = []PlanJob{j}
		curUnits = jobUnits
	}
	flush()
	return batches, unb
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

// scoreLess reports whether partition a is better than b: fewer batches, then less
// total print time, then higher average utilisation.
func scoreLess(a, b []PlannedBatch) bool {
	an, bn := len(a), len(b)
	if an != bn {
		return an < bn
	}
	at, bt := totalTime(a), totalTime(b)
	if at != bt {
		return at < bt
	}
	return avgUtil(a) > avgUtil(b)
}

func totalTime(batches []PlannedBatch) int {
	sum := 0
	for _, b := range batches {
		if b.TotalPrintTimeMinutes != nil {
			sum += *b.TotalPrintTimeMinutes
		}
	}
	return sum
}

func avgUtil(batches []PlannedBatch) float64 {
	if len(batches) == 0 {
		return 0
	}
	var sum float64
	for _, b := range batches {
		sum += b.BedUtilisationPercent
	}
	return sum / float64(len(batches))
}
