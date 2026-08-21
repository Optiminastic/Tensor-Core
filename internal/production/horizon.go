package production

import (
	"sort"
	"time"

	"github.com/Optiminastic/tensor-core/internal/bedpack"
)

// The bounded planning horizon, and the reasons a job was left out of a run.
//
// The packing search is superlinear in the size of the group it runs on:
// selectNext scans every remaining job and runs a full bed pack for each
// candidate, fillBed repeats that per placement, and bestPartition repeats the
// whole thing once per packing strategy. That is affordable on a group of
// dozens and ruinous on a group of thousands - and the pool grows whenever
// orders arrive faster than the printers consume them, which is the normal
// state of a busy shop.
//
// Capping the group the optimizer sees makes planning cost depend on the
// window, not on the backlog. What matters is choosing the window well: take
// the wrong jobs and the planner optimises a slice that never contained the
// good combination.

// DefaultHorizonJobs is how many jobs of one compatibility group a single run
// will consider when BatchGate.HorizonJobs is unset.
//
// 250 is comfortably above the number that can share a bed (a plate holds tens
// of parts, not hundreds), so the window still contains many alternative
// combinations, while keeping the quadratic term at a size that packs in
// milliseconds rather than minutes.
const DefaultHorizonJobs = 250

// Why a job is not on a bed this run. These are scheduling outcomes, not
// defects: every one of them is expected to resolve on a later run, which is
// what separates them from a job flagged with an issue_reason.
const (
	// ReasonNoFootprint - no print file, or one whose dimensions could not be
	// measured. The only one here that will not resolve on its own.
	ReasonNoFootprint = "No print file with measurable STL dimensions uploaded yet."

	// ReasonOversized - does not fit the bed even as a single unit, so no
	// combination of anything will ever place it.
	ReasonOversized = "Larger than the printer bed, so it cannot be placed even on its own."

	// ReasonOutsideWindow - real, batchable work that this run did not reach.
	// The pool held more compatible jobs than one run examines; older and more
	// urgent work was considered first and this will be picked up next run.
	ReasonOutsideWindow = "Not reached this planning run; older and more urgent compatible work was considered first."

	// ReasonWaitingForCompatible - packed onto a bed that came out under the
	// utilisation target, with no override applying. Its jobs stay queued so
	// the bed can keep absorbing compatible arrivals.
	ReasonWaitingForCompatible = "Waiting for more compatible work to fill the bed."

	// ReasonWaitingForBetterBatch - a printer is free and this bed could go
	// now, but it is thin enough that a short wait is likely to produce a
	// better plate. Bounded by BatchGate.IdleWaitWindow; never indefinite.
	ReasonWaitingForBetterBatch = "Briefly held for a fuller bed while a printer is free."

	// The three below are decided before the planner runs: the batchable
	// queries exclude these jobs, so the planner never sees them and could not
	// report on them. They are named here so every "why is this job not on a
	// bed?" answer comes from one vocabulary.

	// ReasonHeld - an operator put it on hold. Nothing automatic clears this.
	ReasonHeld = "On hold; an operator has to release it."

	// ReasonPersonalisationPending - personalisation details are unconfirmed,
	// so the job must not reach a plate in that state.
	ReasonPersonalisationPending = "Waiting for personalisation details to be confirmed."

	// ReasonConfigurationMissing - no printer profile, so the job has no
	// machine family and no batch containing it could ever be assigned to a
	// machine. Carried as an issue_reason on the job itself.
	ReasonConfigurationMissing = "The design has no printer profile, so no machine can be assigned."
)

// windowScore ranks a job's claim on a place in this run's planning window,
// 0-100.
//
// Deliberately the same signals the batch score uses, for the same reason:
// urgency and age decide who gets looked at first, and a job that has waited
// longer outranks a newer one that would merely pack better. Footprint carries
// a small weight so a window is not composed entirely of tiny parts that cannot
// fill a bed between them - but it stays small, because ranking the window by
// size is exactly how an old small job starves behind a stream of large new
// ones.
func windowScore(j PlanJob, now time.Time, gate BatchGate) float64 {
	urgency := dueUrgencyScore([]PlanJob{j}, now)
	if j.Priority <= urgentPriority {
		urgency = 100
	}
	waiting := waitingScore([]PlanJob{j}, now, gate.MaxWait)

	// Against a whole bed: a part covering the bed on its own scores 100.
	area := unitArea(j) / (bedpack.BedXMM * bedpack.BedYMM) * 100
	if area > 100 {
		area = 100
	}
	return urgency*0.50 + waiting*0.35 + area*0.15
}

// selectWindow splits one compatibility group into the jobs this run will plan
// and the jobs it will not reach.
//
// Jobs already sitting in a Draft are always in the window, whatever they
// score. Leaving one out would mean the run cannot reproduce that Draft's job
// set, so the Draft would be dissolved and rebuilt from whatever remained -
// churning batches purely because the pool grew, which is the opposite of what
// the horizon is for.
//
// Everything else is ranked by windowScore, ties broken by creation order so
// the selection is deterministic and FCFS still decides between equals.
func selectWindow(group []PlanJob, now time.Time, gate BatchGate) (window, deferred []PlanJob) {
	limit := gate.HorizonJobs
	if limit <= 0 {
		limit = DefaultHorizonJobs
	}
	if len(group) <= limit {
		return group, nil
	}

	var pinned, rest []PlanJob
	for _, j := range group {
		if j.InDraft {
			pinned = append(pinned, j)
		} else {
			rest = append(rest, j)
		}
	}

	// Already over budget on Draft members alone. Take them all regardless:
	// dropping one dissolves a Draft an operator can already see, which costs
	// more than the extra planning time.
	if len(pinned) >= limit {
		return pinned, rest
	}

	sort.SliceStable(rest, func(a, b int) bool {
		sa, sb := windowScore(rest[a], now, gate), windowScore(rest[b], now, gate)
		if sa != sb {
			return sa > sb
		}
		return rest[a].CreatedAt.Before(rest[b].CreatedAt)
	})

	room := limit - len(pinned)
	return append(pinned, rest[:room]...), rest[room:]
}

// deferredFor turns jobs left out of a run into reported outcomes, so nothing
// silently disappears from scheduling.
func deferredFor(jobs []PlanJob, reason string) []Unbatchable {
	out := make([]Unbatchable, 0, len(jobs))
	for _, j := range jobs {
		out = append(out, Unbatchable{JobID: j.ID, JobNumber: j.JobNumber, Reason: reason})
	}
	return out
}
