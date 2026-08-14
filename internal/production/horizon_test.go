package production

import (
	"fmt"
	"testing"
	"time"

	"github.com/Optiminastic/tensor-core/internal/bedpack"
)

// agedJob is a small compatible job queued minutesAgo minutes ago.
//
// Priority is set explicitly to a routine value. The zero value is NOT neutral:
// shouldCreateBatch treats Priority <= urgentPriority (1) as urgent, so a
// fixture left at 0 is an urgent job and bypasses every gate under test.
func agedJob(id string, minutesAgo int) PlanJob {
	j := smallJob(id, "PLA Basics")
	j.CreatedAt = testNow.Add(-time.Duration(minutesAgo) * time.Minute)
	j.Priority = 5
	return j
}

// TestSelectWindowBoundsALargePool is the point of the horizon: planning cost
// must depend on the window, not on the backlog. The packing search runs a full
// bed pack per candidate per placement per strategy, so an unbounded group is
// what turns a growing queue into a planner that never finishes.
func TestSelectWindowBoundsALargePool(t *testing.T) {
	group := make([]PlanJob, 0, 2000)
	for i := range 2000 {
		group = append(group, agedJob(fmt.Sprintf("j%04d", i), i))
	}
	gate := BatchGate{HorizonJobs: 100, MaxWait: 4 * time.Hour}

	window, deferred := selectWindow(group, testNow, gate)

	if len(window) != 100 {
		t.Errorf("window = %d jobs, want the 100 the horizon allows", len(window))
	}
	if len(window)+len(deferred) != len(group) {
		t.Errorf("window %d + deferred %d != pool %d; a job was lost rather than deferred",
			len(window), len(deferred), len(group))
	}
}

// TestSelectWindowProtectsTheOldest: FCFS must survive the horizon. If the
// window were chosen by what packs best, an old small job would sit behind a
// stream of newer ones for ever - starvation introduced by the optimisation
// meant to bound it.
func TestSelectWindowProtectsTheOldest(t *testing.T) {
	var group []PlanJob
	group = append(group, agedJob("ancient", 600)) // 10 hours old
	for i := range 50 {
		group = append(group, agedJob(fmt.Sprintf("new%02d", i), 1))
	}
	gate := BatchGate{HorizonJobs: 5, MaxWait: 4 * time.Hour}

	window, _ := selectWindow(group, testNow, gate)

	for _, j := range window {
		if j.ID == "ancient" {
			return
		}
	}
	t.Error("the oldest job was left outside the planning window; FCFS must outrank a better-packing newcomer")
}

// TestSelectWindowPrefersUrgentWork: an urgent job must be looked at this run,
// not whenever the queue drains down to it.
func TestSelectWindowPrefersUrgentWork(t *testing.T) {
	var group []PlanJob
	for i := range 50 {
		group = append(group, agedJob(fmt.Sprintf("routine%02d", i), 30))
	}
	urgent := agedJob("urgent", 1)
	urgent.Priority = UrgentPriority
	group = append(group, urgent)

	window, _ := selectWindow(group, testNow, BatchGate{HorizonJobs: 5, MaxWait: 4 * time.Hour})

	for _, j := range window {
		if j.ID == "urgent" {
			return
		}
	}
	t.Error("an urgent job was left outside the planning window")
}

// TestSelectWindowAlwaysKeepsDraftMembers is what stops the horizon fighting
// the stability threshold. A Draft whose jobs fall outside the window cannot be
// reproduced by the run, so it would be dissolved and rebuilt purely because
// the backlog grew.
func TestSelectWindowAlwaysKeepsDraftMembers(t *testing.T) {
	var group []PlanJob
	drafted := agedJob("drafted", 1) // newest, lowest claim on the window
	drafted.InDraft = true
	group = append(group, drafted)
	for i := range 50 {
		group = append(group, agedJob(fmt.Sprintf("older%02d", i), 100+i))
	}

	window, deferred := selectWindow(group, testNow, BatchGate{HorizonJobs: 3, MaxWait: 4 * time.Hour})

	for _, j := range window {
		if j.ID == "drafted" {
			return
		}
	}
	t.Errorf("a Draft member was deferred (%d deferred); its batch would be dissolved for no reason", len(deferred))
}

// TestSelectWindowIsAPassthroughUnderTheLimit keeps the common case free: a
// shop whose queue fits the horizon must behave exactly as it did before.
func TestSelectWindowIsAPassthroughUnderTheLimit(t *testing.T) {
	group := []PlanJob{agedJob("a", 5), agedJob("b", 4)}
	window, deferred := selectWindow(group, testNow, BatchGate{HorizonJobs: 100})

	if len(window) != 2 || len(deferred) != 0 {
		t.Errorf("window=%d deferred=%d, want 2/0 for a pool under the horizon", len(window), len(deferred))
	}
}

// TestPlanReportsEveryDeferredJob: a job left out must say why. One that
// silently vanishes from scheduling is indistinguishable from one the planner
// has lost.
func TestPlanReportsEveryDeferredJob(t *testing.T) {
	group := make([]PlanJob, 0, 300)
	for i := range 300 {
		group = append(group, agedJob(fmt.Sprintf("j%03d", i), i))
	}

	_, _, _, deferred := PlanWithReasons(group, testNow, BatchGate{HorizonJobs: 20}, DefaultNester)

	if len(deferred) == 0 {
		t.Fatal("300 jobs against a horizon of 20 deferred nothing")
	}
	for _, d := range deferred {
		if d.Reason == "" {
			t.Errorf("job %s deferred with no reason", d.JobNumber)
		}
		if d.JobNumber == "" {
			t.Error("a deferred entry has no job number, so it cannot be shown to an operator")
		}
	}
}

// TestWorthWaitingForFullerIsBounded is the wait-or-print decision. Waiting has
// to end: a machine held idle for a plate that never arrives is worse than a
// thin plate printed now.
func TestWorthWaitingForFullerIsBounded(t *testing.T) {
	thin := PlannedBatch{
		BedUtilisationPercent: 30,
		Jobs:                  []PlanJob{agedJob("a", 2)},
	}
	gate := BatchGate{IdleMachines: 1, IdleWaitWindow: 10 * time.Minute, AgingFloorPercent: 60}

	t.Run("waits briefly for a thin fresh bed", func(t *testing.T) {
		if !worthWaitingForFuller(thin, testNow, gate) {
			t.Error("a 30 percent full bed two minutes old went straight to a printer; a short wait may fill it")
		}
	})

	t.Run("stops waiting once the window expires", func(t *testing.T) {
		stale := thin
		stale.Jobs = []PlanJob{agedJob("a", 30)} // past the 10-minute window
		if worthWaitingForFuller(stale, testNow, gate) {
			t.Error("still waiting after the window expired; the wait must be bounded")
		}
	})

	t.Run("does not wait on an already-respectable bed", func(t *testing.T) {
		full := thin
		full.BedUtilisationPercent = 75 // above the 60 floor
		if worthWaitingForFuller(full, testNow, gate) {
			t.Error("held a 75 percent full bed back for a marginal improvement while a printer idled")
		}
	})

	t.Run("a zero window disables waiting entirely", func(t *testing.T) {
		off := gate
		off.IdleWaitWindow = 0
		if worthWaitingForFuller(thin, testNow, off) {
			t.Error("waited with IdleWaitWindow=0; zero must restore print-immediately")
		}
	})
}

// TestIdleMachinePrintsRatherThanStarve: with no wait configured, an idle
// machine takes the best available bed at once. This is the behaviour every
// existing caller gets from a zero-value BatchGate, and it must not regress.
func TestIdleMachinePrintsRatherThanStarve(t *testing.T) {
	j := smallJob("solo", "PLA Basics")
	j.CreatedAt = testNow // brand new: no max-wait override
	j.Priority = 5        // routine; 0 would be urgent and bypass the gate
	j.Footprint = bedpack.UnitFootprint{RefID: "solo", XMM: 60, YMM: 60, ZMM: 20}

	gate := BatchGate{IdleMachines: 1, MaxWait: 24 * time.Hour}
	batches, _, held, _ := PlanWithReasons([]PlanJob{j}, testNow, gate, DefaultNester)

	if len(batches) != 1 {
		t.Errorf("batches=%d held=%d; an idle machine with one thin job should print it, not idle",
			len(batches), len(held))
	}
}

// TestHeldJobsCarryAWaitingReason: a bed held under target reports why, and the
// reason distinguishes "nothing else compatible exists yet" from "briefly
// holding for a fuller plate while a printer is free".
func TestHeldJobsCarryAWaitingReason(t *testing.T) {
	j := smallJob("solo", "PLA Basics")
	j.CreatedAt = testNow
	j.Priority = 5 // routine; 0 would be urgent and bypass the gate
	j.Footprint = bedpack.UnitFootprint{RefID: "solo", XMM: 60, YMM: 60, ZMM: 20}

	// No idle machine, nothing compatible waiting: held for compatible volume.
	_, _, held, deferred := PlanWithReasons(
		[]PlanJob{j}, testNow, BatchGate{MaxWait: 24 * time.Hour}, DefaultNester)
	if len(held) != 1 {
		t.Fatalf("held=%d, want 1 thin partition held below target", len(held))
	}
	if len(deferred) != 1 || deferred[0].Reason != ReasonWaitingForCompatible {
		t.Errorf("deferred = %+v, want one job reported as %q", deferred, ReasonWaitingForCompatible)
	}
}
