package httpapi

// Which jobs a locked bed will take.
//
// topUpCandidates is pure, so the rules that matter can be stated here without
// a database, a printer or object storage. Each test is one rule, and each rule
// exists because the alternative costs something concrete.

import (
	"testing"

	"github.com/google/uuid"

	"github.com/Optiminastic/tensor-core/internal/db/gen"
	"github.com/Optiminastic/tensor-core/internal/production"
)

// candidate builds a job row with just the fields compatibility and capacity
// read.
func candidate(colour string, priority, quantity int32) gen.ProductionJob {
	material, family := "PLA", "A2L"
	c := colour
	return gen.ProductionJob{
		ID: uuid.New(), JobNumber: colour, Material: &material, MachineFamily: &family,
		Colour: &c, Colours: []byte(`["` + colour + `"]`),
		Priority: priority, Quantity: quantity,
	}
}

func blueBedKey() production.CompatibilityKey {
	return compatibilityKeyOf(candidate("BLUE", NormalRank, 1))
}

// The headline bug this whole change exists to stop: a RED plank on a BLUE bed.
// One plate is sliced once against one filament load.
func TestTopUpCandidatesRefusesAColourMismatch(t *testing.T) {
	pool := []gen.ProductionJob{
		candidate("RED", PriorityRank, 1),
		candidate("GOLD", PriorityRank, 1),
	}
	chosen, priority := topUpCandidates(blueBedKey(), 3, pool, map[uuid.UUID]bool{})
	if len(chosen) != 0 || priority != 0 {
		t.Errorf("a BLUE bed took %d job(s) of another colour", len(chosen))
	}
}

// A locked bed is a committed plate. Opening one costs a withdrawal from the
// printer's queue and a re-plate, and that is only worth paying for expedited
// work - standard work can wait for a Draft, which costs nothing.
func TestTopUpCandidatesNeverOpensABedForStandardWorkAlone(t *testing.T) {
	pool := []gen.ProductionJob{
		candidate("BLUE", NormalRank, 1),
		candidate("BLUE", NormalRank, 1),
	}
	chosen, priority := topUpCandidates(blueBedKey(), 3, pool, map[uuid.UUID]bool{})
	if len(chosen) != 0 || priority != 0 {
		t.Errorf("a locked bed was opened for %d standard job(s) and no expedited work", len(chosen))
	}
}

// Once one expedited plank is aboard, the withdrawal and the re-plate are
// already paid for - so the rest of the bed is filled in the SAME pass rather
// than withdrawing the same plate from the same queue again next run.
func TestTopUpCandidatesFillsTheRestWithStandardWork(t *testing.T) {
	priorityJob := candidate("BLUE", PriorityRank, 1)
	pool := []gen.ProductionJob{
		priorityJob,
		candidate("BLUE", NormalRank, 1),
		candidate("BLUE", NormalRank, 1),
		candidate("BLUE", NormalRank, 1), // one too many for a room of 3
	}
	chosen, priority := topUpCandidates(blueBedKey(), 3, pool, map[uuid.UUID]bool{})
	if priority != 1 {
		t.Errorf("expedited jobs placed = %d, want 1", priority)
	}
	if len(chosen) != 3 {
		t.Fatalf("jobs placed = %d, want 3 - the free places should be filled once the bed is open",
			len(chosen))
	}
	if chosen[0].ID != priorityJob.ID {
		t.Error("the expedited job must be taken first")
	}
}

// Quantity counts products, not jobs. A job of two fills two places.
func TestTopUpCandidatesNeverOvershootsTheRoom(t *testing.T) {
	// Room for one; the only expedited job needs two.
	pool := []gen.ProductionJob{candidate("BLUE", PriorityRank, 2)}
	chosen, _ := topUpCandidates(blueBedKey(), 1, pool, map[uuid.UUID]bool{})
	if len(chosen) != 0 {
		t.Errorf("a job of quantity 2 was placed in a single free place")
	}

	// With room for two it fits exactly.
	chosen, priority := topUpCandidates(blueBedKey(), 2, pool, map[uuid.UUID]bool{})
	if len(chosen) != 1 || priority != 1 {
		t.Errorf("a job of quantity 2 did not fit two free places (chosen=%d)", len(chosen))
	}
}

// A full bed is not a candidate at all.
func TestTopUpCandidatesTakesNothingWithNoRoom(t *testing.T) {
	pool := []gen.ProductionJob{candidate("BLUE", PriorityRank, 1)}
	if chosen, _ := topUpCandidates(blueBedKey(), 0, pool, map[uuid.UUID]bool{}); len(chosen) != 0 {
		t.Errorf("a bed with no room took %d job(s)", len(chosen))
	}
}

// One pass fills several beds, and must not promise the same job to two of
// them - the second bed would then write nothing and be re-plated for nothing.
func TestTopUpCandidatesSkipsJobsAlreadyClaimed(t *testing.T) {
	job := candidate("BLUE", PriorityRank, 1)
	pool := []gen.ProductionJob{job}
	taken := map[uuid.UUID]bool{job.ID: true}

	if chosen, _ := topUpCandidates(blueBedKey(), 3, pool, taken); len(chosen) != 0 {
		t.Error("a job already claimed by an earlier bed in the same pass was taken again")
	}
}

// A colourless job is not a wildcard. The planner will not bed it at all, and
// it must not slip onto a coloured bed here either.
func TestTopUpCandidatesRefusesAColourlessJob(t *testing.T) {
	material, family := "PLA", "A2L"
	colourless := gen.ProductionJob{
		ID: uuid.New(), JobNumber: "NOCOLOUR", Material: &material, MachineFamily: &family,
		Colours: []byte("[]"), Priority: PriorityRank, Quantity: 1,
	}
	chosen, _ := topUpCandidates(blueBedKey(), 3, []gen.ProductionJob{colourless}, map[uuid.UUID]bool{})
	if len(chosen) != 0 {
		t.Error("a job recording no colour was placed on a BLUE bed")
	}
}
