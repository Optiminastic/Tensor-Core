package production

import (
	"fmt"
	"math/rand"
	"testing"
	"time"

	"github.com/Optiminastic/tensor-core/internal/bedpack"
)

// TestMeasurePartitioningEffect quantifies what widening the compatibility key
// actually bought, on one realistic job set.
//
// It plans the same jobs twice: once as the planner does now, and once through
// a partitioning that reproduces the old eight-field groupKey plus the 3-day
// due-date clustering. The comparison is the point - beds per job and mean
// utilisation are the numbers the whole change is for.
//
// Not an assertion about exact figures (they move with the fixture); the
// assertion is directional, because the mechanism is not in doubt: more
// partitions cannot produce fuller beds.
func TestMeasurePartitioningEffect(t *testing.T) {
	jobs := realisticJobSet(rand.New(rand.NewSource(7)), 120)
	gate := BatchGate{MaxWait: 4 * time.Hour, IdleMachines: 1}

	now := testNow
	newBatches, _, _ := Plan(jobs, now, gate)

	// The old boundary: material + both nozzles + quality + machineFamily +
	// supportUsed + infill bucket + priority tier, then due-date clusters
	// within each. Planning each old bucket separately is exactly what the
	// old code did.
	var oldBatches []PlannedBatch
	for _, bucket := range oldStylePartitions(jobs) {
		b, _, _ := Plan(bucket, now, gate)
		oldBatches = append(oldBatches, b...)
	}

	oldUtil, newUtil := meanUtil(oldBatches), meanUtil(newBatches)
	t.Logf("jobs=%d", len(jobs))
	t.Logf("  old boundary: %3d beds, mean utilisation %5.1f%%, %.2f jobs/bed",
		len(oldBatches), oldUtil, jobsPerBed(oldBatches))
	t.Logf("  new boundary: %3d beds, mean utilisation %5.1f%%, %.2f jobs/bed",
		len(newBatches), newUtil, jobsPerBed(newBatches))
	t.Logf("  => %d fewer beds for the same work, %+.1f pp mean utilisation",
		len(oldBatches)-len(newBatches), newUtil-oldUtil)

	if len(newBatches) > len(oldBatches) {
		t.Errorf("the widened boundary produced MORE beds (%d) than the old one (%d); fewer partitions cannot need more beds",
			len(newBatches), len(oldBatches))
	}
	if newUtil < oldUtil {
		t.Errorf("mean utilisation fell from %.1f%% to %.1f%%", oldUtil, newUtil)
	}
}

// realisticJobSet mirrors what the order generator produces: a handful of
// recurring products, mostly routine priority with some urgent, due dates
// spread over a few weeks, and the support/infill variety that used to
// fragment the pool.
func realisticJobSet(rng *rand.Rand, n int) []PlanJob {
	sizes := [][2]float64{{40, 40}, {60, 55}, {90, 70}, {35, 100}, {120, 80}}
	infills := []float64{15, 20, 25}
	out := make([]PlanJob, 0, n)
	for i := 0; i < n; i++ {
		size := sizes[rng.Intn(len(sizes))]
		j := PlanJob{
			ID: fmt.Sprint(i), JobNumber: fmt.Sprintf("JOB-%03d", i),
			Material: "PLA Basics", QualityMM: "0.2", NozzleLeft: "0.4", NozzleRight: "0.4",
			MachineFamily: "H2C", Quantity: 1 + rng.Intn(3),
			SupportUsed: rng.Intn(4) == 0,
			InfillPct:   infills[rng.Intn(len(infills))],
			Priority:    5, CreatedAt: testNow.Add(-time.Duration(rng.Intn(120)) * time.Minute),
			Footprint: bedpack.UnitFootprint{RefID: fmt.Sprint(i), XMM: size[0], YMM: size[1], ZMM: 20},
		}
		if rng.Intn(6) == 0 {
			j.Priority = 1
		}
		due := testNow.Add(time.Duration(rng.Intn(21)) * 24 * time.Hour)
		j.DueDate = &due
		out = append(out, j)
	}
	return out
}

// oldStylePartitions reproduces the pre-change bucketing: the eight-field key,
// then due-date clusters inside each bucket.
func oldStylePartitions(jobs []PlanJob) [][]PlanJob {
	type oldKey struct {
		material, nozzleL, nozzleR, quality, family, support, infill, tier string
	}
	buckets := map[oldKey][]PlanJob{}
	var order []oldKey
	for _, j := range jobs {
		tier := "normal"
		if j.Priority <= urgentPriority {
			tier = "urgent"
		}
		k := oldKey{
			j.Material, j.NozzleLeft, j.NozzleRight, j.QualityMM, j.MachineFamily,
			fmt.Sprint(j.SupportUsed), fmt.Sprintf("%.0f", j.InfillPct/5), tier,
		}
		if _, seen := buckets[k]; !seen {
			order = append(order, k)
		}
		buckets[k] = append(buckets[k], j)
	}
	var out [][]PlanJob
	for _, k := range order {
		out = append(out, oldDueDateClusters(buckets[k])...)
	}
	return out
}

// oldDueDateClusters is the removed dueDateClusters, kept here so the
// comparison reproduces the real previous behaviour rather than an
// approximation of it.
func oldDueDateClusters(jobs []PlanJob) [][]PlanJob {
	var clusters [][]PlanJob
	var current []PlanJob
	var anchor *time.Time
	for _, j := range jobs {
		if len(current) == 0 {
			current, anchor = []PlanJob{j}, j.DueDate
			continue
		}
		within := (anchor == nil) == (j.DueDate == nil)
		if within && anchor != nil {
			d := anchor.Sub(*j.DueDate)
			if d < 0 {
				d = -d
			}
			within = d <= dueUrgencyHorizon
		}
		if within {
			current = append(current, j)
			continue
		}
		clusters = append(clusters, current)
		current, anchor = []PlanJob{j}, j.DueDate
	}
	if len(current) > 0 {
		clusters = append(clusters, current)
	}
	return clusters
}

func meanUtil(batches []PlannedBatch) float64 {
	if len(batches) == 0 {
		return 0
	}
	var total float64
	for _, b := range batches {
		total += b.BedUtilisationPercent
	}
	return total / float64(len(batches))
}

func jobsPerBed(batches []PlannedBatch) float64 {
	if len(batches) == 0 {
		return 0
	}
	n := 0
	for _, b := range batches {
		n += len(b.Jobs)
	}
	return float64(n) / float64(len(batches))
}
