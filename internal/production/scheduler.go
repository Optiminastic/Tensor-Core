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
	FreeAt    time.Time
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
	Healthy        bool
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
	HealthBonus           time.Duration
}

// EffectiveFreeAt adjusts a candidate's raw FreeAt by the material/colour
// match, queue length, and health signals. Material match and the material-
// change penalty are mutually exclusive: match applies only when the loaded
// material is known and equal to the batch's; the penalty applies only when
// it's known and different. Unknown loaded material (idle, nothing queued)
// applies neither - a genuinely available empty machine is never penalised
// just for not currently having anything loaded.
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
	if in.Healthy {
		adjusted = adjusted.Add(-w.HealthBonus)
	}
	return adjusted
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
