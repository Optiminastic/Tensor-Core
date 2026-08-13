package production

// Stage 9's "Earliest Available Machine Scheduling": given a fleet machine's
// current print (if any) and whatever is already queued behind it, compute
// when it will actually be free, then rank candidates by that time - not by
// which machine happens to be idle right now (a machine idle for the next 2
// minutes but with a 3-hour queue behind it is not "free").

import (
	"time"

	"github.com/google/uuid"
)

// FleetMachineState is the live print state of one physical fleet machine, as
// far as the scheduler needs to know it.
type FleetMachineState struct {
	MachineID             uuid.UUID
	PrintStartedAt        *time.Time
	BatchTotalTimeMinutes *int
}

// QueuedBatch is one batch already queued (open/in_progress) on a machine,
// ahead of whatever the scheduler is placing next.
type QueuedBatch struct {
	TotalPrintTimeMinutes int
}

// MachineFreeAt computes when a machine will next be free: now, plus whatever
// remains of its current print (if any), plus every already-queued batch's
// full time (queued batches run FCFS, one at a time, after the current print).
func MachineFreeAt(now time.Time, m FleetMachineState, queued []QueuedBatch) time.Time {
	freeAt := now
	if m.PrintStartedAt != nil && m.BatchTotalTimeMinutes != nil {
		remaining := time.Duration(*m.BatchTotalTimeMinutes)*time.Minute - now.Sub(*m.PrintStartedAt)
		if remaining > 0 {
			freeAt = now.Add(remaining)
		}
	}
	for _, b := range queued {
		freeAt = freeAt.Add(time.Duration(b.TotalPrintTimeMinutes) * time.Minute)
	}
	return freeAt
}

// MachineCandidate is one machine the scheduler is choosing between, already
// resolved to how soon it is free.
type MachineCandidate struct {
	MachineID uuid.UUID
	ProfileID uuid.UUID
	// FreeAt is when the machine's GUARANTEED workload ends: now plus the
	// remaining current print plus every Locked batch already queued on it.
	// Draft work is excluded on purpose - see MachineScoreInputs.DraftMinutes.
	FreeAt time.Time
	// Now is the instant FreeAt was computed against, so the idle-recency
	// tie-break has a clock without EffectiveFreeAt reading one itself.
	Now time.Time
}

// EarliestFreeMachine picks the candidate that is free soonest. Returns nil
// for an empty candidate list (nothing eligible - e.g. no fleet machine online
// for the required family). Superseded by BestMachine for actual batch
// assignment (see machine_scheduler.go), kept as the pure "soonest wins" rule
// BestMachine's EffectiveFreeAt-adjusted ranking builds on.
func EarliestFreeMachine(candidates []MachineCandidate) *MachineCandidate {
	if len(candidates) == 0 {
		return nil
	}
	best := candidates[0]
	for _, c := range candidates[1:] {
		if c.FreeAt.Before(best.FreeAt) {
			best = c
		}
	}
	return &best
}

// MachineScoreInputs is what BestMachine needs about one candidate beyond
// FreeAt: what's loaded/queued next on it, how many batches are already
// queued, and whether it's fully idle/ready vs busy. LoadedMaterial=="" means
// no signal (idle, empty queue) - never treated as a mismatch.
type MachineScoreInputs struct {
	LoadedMaterial string
	LoadedColours  []string
	QueueLength    int
	// DraftMinutes is how much flexible, not-yet-committed work is already
	// pointed at this machine.
	//
	// Deliberately separate from FreeAt, which carries only GUARANTEED load
	// (the remaining current print plus every Locked batch). A Draft may still
	// be re-planned onto another machine, so treating it as committed would
	// overstate the machine's real obligations. But ignoring it entirely is
	// what let one printer collect an entire planning run's worth of Drafts
	// while the rest stood idle - during a single run every machine's
	// committed queue is unchanged, so they all look identical.
	//
	// It is therefore counted at a discount (DraftLoadFraction) as a fleet
	// balance signal rather than as time the machine truly owes.
	DraftMinutes int
	// IdleSince is when this machine last changed state, used only to break a
	// tie between otherwise-equal candidates in favour of the one that has
	// been unused longest. Nil disables the tie-break for this machine.
	IdleSince *time.Time
	Healthy   bool
}

// MachineScoreWeights are the minutes-equivalent adjustments EffectiveFreeAt
// applies to a candidate's raw FreeAt before ranking - expressed as time
// rather than an abstract score so they stay directly comparable to (and
// combinable with) FreeAt itself, and are easy for an operator to reason
// about ("treated as if it were 30 minutes more free").
type MachineScoreWeights struct {
	MaterialMatchBonus    time.Duration
	ColourMatchBonus      time.Duration
	MaterialChangePenalty time.Duration
	QueueLengthPenalty    time.Duration
	// DraftLoadFraction is how much of a machine's Draft minutes count towards
	// its apparent load, 0..1. Below 1 because Draft work is not owed yet; above
	// 0 because a machine already carrying eight hours of proposals is a worse
	// home for a ninth than an empty one.
	DraftLoadFraction float64
	HealthBonus       time.Duration
	// IdleRecencyBonus is the most a long-idle machine can be favoured by,
	// reached after IdleRecencyWindow. It only ever separates candidates that
	// are otherwise close, so it cannot override a genuinely earlier finish.
	IdleRecencyBonus  time.Duration
	IdleRecencyWindow time.Duration
}

// EffectiveFreeAt ranks a machine for a batch: when it would realistically
// finish the work, adjusted for the things that make one machine a better home
// than another.
//
// The base is c.FreeAt - now plus the remaining current print plus every
// Locked batch queued on it. That is the machine's GUARANTEED workload, and it
// is what makes this a fleet scheduler rather than "whichever machine is free".
// A machine with 1h left and 2h locked finishes a new 2h batch at +5h; one with
// 1h left and 1h locked finishes it at +4h and should get the work, even though
// neither is idle.
//
// The new batch's own duration is added by the caller for reporting; it is the
// same on every candidate, so it never changes which one wins.
//
// On top of that: Draft load at a discount (fleet balance), material and colour
// changeover, health, and finally idle recency as a tie-break. Material match
// and the change penalty are mutually exclusive - a match applies only when the
// loaded material is known and equal, the penalty only when known and
// different. Unknown loaded material (idle, nothing queued) applies neither, so
// a genuinely available empty machine is never penalised for having nothing on
// it.
func EffectiveFreeAt(c MachineCandidate, in MachineScoreInputs, batchMaterial string, batchColours []string, w MachineScoreWeights) time.Time {
	adjusted := c.FreeAt
	switch {
	case in.LoadedMaterial != "" && in.LoadedMaterial == batchMaterial:
		adjusted = adjusted.Add(-w.MaterialMatchBonus)
		adjusted = adjusted.Add(-scaledColourBonus(in.LoadedColours, batchColours, w.ColourMatchBonus))
	case in.LoadedMaterial != "" && in.LoadedMaterial != batchMaterial:
		adjusted = adjusted.Add(w.MaterialChangePenalty)
	}
	adjusted = adjusted.Add(time.Duration(in.QueueLength) * w.QueueLengthPenalty)
	// Flexible work, discounted - see DraftMinutes.
	adjusted = adjusted.Add(time.Duration(float64(in.DraftMinutes)*w.DraftLoadFraction) * time.Minute)
	if in.Healthy {
		adjusted = adjusted.Add(-w.HealthBonus)
	}
	adjusted = adjusted.Add(-idleRecencyBonus(in.IdleSince, c.Now, w))
	return adjusted
}

// idleRecencyBonus favours the machine that has gone unused longest, ramping to
// IdleRecencyBonus over IdleRecencyWindow. Its only job is to stop an exact tie
// between equally-loaded machines always resolving to the same one, so it is
// capped well below the other terms.
func idleRecencyBonus(idleSince *time.Time, now time.Time, w MachineScoreWeights) time.Duration {
	if idleSince == nil || w.IdleRecencyBonus <= 0 || w.IdleRecencyWindow <= 0 {
		return 0
	}
	idle := now.Sub(*idleSince)
	if idle <= 0 {
		return 0
	}
	if idle >= w.IdleRecencyWindow {
		return w.IdleRecencyBonus
	}
	return time.Duration(float64(w.IdleRecencyBonus) * (float64(idle) / float64(w.IdleRecencyWindow)))
}

// scaledColourBonus scales bonus by what fraction of the batch's required
// colours the machine already has loaded - a full colour match is worth the
// whole bonus, a partial overlap proportionally less, no overlap none.
func scaledColourBonus(loaded, required []string, bonus time.Duration) time.Duration {
	if len(required) == 0 {
		return 0
	}
	have := make(map[string]bool, len(loaded))
	for _, c := range loaded {
		have[c] = true
	}
	matched := 0
	for _, c := range required {
		if have[c] {
			matched++
		}
	}
	return time.Duration(float64(bonus) * float64(matched) / float64(len(required)))
}

// BestMachine picks the candidate with the earliest EffectiveFreeAt -
// EarliestFreeMachine's weighted successor, ranking on a material/colour-
// match-adjusted free time instead of the raw one. Returns nil for an empty
// candidate list. inputs is keyed by MachineCandidate.MachineID; a candidate
// with no entry is treated as MachineScoreInputs{} (no signal, healthy).
func BestMachine(candidates []MachineCandidate, inputs map[uuid.UUID]MachineScoreInputs, batchMaterial string, batchColours []string, w MachineScoreWeights) *MachineCandidate {
	if len(candidates) == 0 {
		return nil
	}
	best := candidates[0]
	bestAt := EffectiveFreeAt(best, inputs[best.MachineID], batchMaterial, batchColours, w)
	for _, c := range candidates[1:] {
		at := EffectiveFreeAt(c, inputs[c.MachineID], batchMaterial, batchColours, w)
		if at.Before(bestAt) {
			best, bestAt = c, at
		}
	}
	return &best
}
