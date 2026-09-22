package httpapi

import (
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/Optiminastic/tensor-core/internal/db/gen"
	"github.com/Optiminastic/tensor-core/internal/production"
)

func batchableJobRow(number string, colours string) gen.ProductionJob {
	material := "PLA"
	// A model, because a plank without one has nothing to put on a plate and is
	// refused before any other rule is reached.
	model := uuid.New()
	return gen.ProductionJob{
		PrintFileID: &model,
		ID:          uuid.New(), JobNumber: number,
		Status:                production.StatusQueued,
		Quantity:              1,
		PersonalisationStatus: production.PersonalisationNotRequired,
		Material:              &material,
		Colours:               []byte(colours),
	}
}

// A bed is sliced once against one filament load, so two colours on it describe
// a print that cannot happen. The rule does not soften because a person did the
// choosing - that is the whole reason it is checked here and not only in the
// planner.
func TestCheckOneBedWorthRefusesTwoColours(t *testing.T) {
	blue := batchableJobRow("JOB-1", `["BLUE"]`)
	gold := batchableJobRow("JOB-2", `["GOLD"]`)

	err := checkOneBedWorth([]gen.ProductionJob{blue, gold}, 4)
	if err == nil {
		t.Fatal("a bed mixing BLUE and GOLD was accepted")
	}
	if !strings.Contains(err.Error(), "JOB-2") || !strings.Contains(err.Error(), "JOB-1") {
		t.Errorf("message %q names neither job; an operator has to know WHICH one clashed",
			err.Error())
	}
}

func TestCheckOneBedWorthAcceptsOneColour(t *testing.T) {
	jobs := []gen.ProductionJob{
		batchableJobRow("JOB-1", `["BLUE"]`),
		batchableJobRow("JOB-2", `["BLUE"]`),
	}
	if err := checkOneBedWorth(jobs, 4); err != nil {
		t.Fatalf("two BLUE planks were refused: %v", err)
	}
}

// Four 200x50 planks cover 37.9% of the bed, so an area test can never fire.
// The cap is the only thing standing between a hand-built bed and a plate with
// more planks than it has places.
func TestCheckOneBedWorthRefusesMoreThanTheBedHolds(t *testing.T) {
	jobs := make([]gen.ProductionJob, 0, 5)
	for i := 0; i < 5; i++ {
		jobs = append(jobs, batchableJobRow("JOB-X", `["BLUE"]`))
	}
	err := checkOneBedWorth(jobs, 4)
	if err == nil {
		t.Fatal("five products were accepted onto a four-place bed")
	}
	if !strings.Contains(err.Error(), "4") || !strings.Contains(err.Error(), "5") {
		t.Errorf("message %q says neither the cap nor the count", err.Error())
	}
}

// Quantity, not row count. One job of four planks fills the bed by itself, and
// counting rows would have let four such jobs through as "four products".
func TestCheckOneBedWorthCountsProductsNotJobs(t *testing.T) {
	big := batchableJobRow("JOB-1", `["BLUE"]`)
	big.Quantity = 5

	if err := checkOneBedWorth([]gen.ProductionJob{big}, 4); err == nil {
		t.Fatal("one job of five planks was accepted onto a four-place bed")
	}
}

// A job with no colour has nothing to match against, so it would accept
// anything beside it - the planner's ReasonNoColour, at this end.
func TestCheckOneBedWorthRefusesAJobWithNoColour(t *testing.T) {
	if err := checkOneBedWorth([]gen.ProductionJob{batchableJobRow("JOB-1", `[]`)}, 4); err == nil {
		t.Fatal("a bed whose first job records no colour was accepted")
	}
}

// Each refusal has to name the job and say what is wrong, because the operator
// is looking at a list of twenty and "not eligible" tells them nothing.
func TestBatchableNowRefusesWhatThePlannerWouldRefuse(t *testing.T) {
	held := batchableJobRow("JOB-HELD", `["BLUE"]`)
	held.Held = true

	flagged := batchableJobRow("JOB-FLAG", `["BLUE"]`)
	reason := "stl_missing"
	flagged.IssueReason = &reason

	spent := batchableJobRow("JOB-SPENT", `["BLUE"]`)
	spent.Quantity = 0

	unchecked := batchableJobRow("JOB-PERS", `["BLUE"]`)
	unchecked.PersonalisationStatus = "pending"

	for _, tc := range []struct {
		job  gen.ProductionJob
		says string
	}{
		{held, "on hold"},
		{flagged, "stl_missing"},
		{spent, "nothing left"},
		{unchecked, "personalisation"},
	} {
		err := batchableNow(tc.job)
		if err == nil {
			t.Errorf("%s was accepted", tc.job.JobNumber)
			continue
		}
		if !strings.Contains(err.Error(), tc.job.JobNumber) {
			t.Errorf("message %q does not name the job", err.Error())
		}
		if !strings.Contains(err.Error(), tc.says) {
			t.Errorf("message %q does not say %q", err.Error(), tc.says)
		}
	}
}

func TestBatchableNowAcceptsAnOrdinaryQueuedJob(t *testing.T) {
	if err := batchableNow(batchableJobRow("JOB-1", `["BLUE"]`)); err != nil {
		t.Fatalf("an ordinary queued job was refused: %v", err)
	}
}

// Being on a bed is deliberately NOT a refusal here. On a floor that plans
// continuously almost everything queued is on some Draft within minutes, and a
// Draft is a proposal - its planks can be rearranged. Which beds are still
// proposals is decided by refuseCommittedBeds, which can read their status;
// this row carries a batch id and not the status, so it cannot.
func TestBatchableNowAcceptsAJobSittingOnABed(t *testing.T) {
	onABed := batchableJobRow("JOB-DRAFT", `["BLUE"]`)
	batchID := uuid.New()
	onABed.BatchID = &batchID

	if err := batchableNow(onABed); err != nil {
		t.Fatalf("a job on a bed was refused outright: %v - a Draft's planks can be moved", err)
	}
}

// The browser compares these strings to decide what to offer next, so two jobs
// that may share a bed must produce the same one and two that may not must not.
func TestCompatibilityKeyStringSeparatesWhatCannotShareABed(t *testing.T) {
	blue := batchableJobRow("JOB-1", `["BLUE"]`)
	alsoBlue := batchableJobRow("JOB-2", `["BLUE"]`)
	gold := batchableJobRow("JOB-3", `["GOLD"]`)

	if compatibilityKeyString(blue) != compatibilityKeyString(alsoBlue) {
		t.Error("two BLUE planks produced different keys, so the dialog would not offer the second")
	}
	if compatibilityKeyString(blue) == compatibilityKeyString(gold) {
		t.Error("BLUE and GOLD produced the same key, so the dialog would offer a plate that cannot print")
	}

	petg := batchableJobRow("JOB-4", `["BLUE"]`)
	material := "PETG"
	petg.Material = &material
	if compatibilityKeyString(blue) == compatibilityKeyString(petg) {
		t.Error("PLA and PETG produced the same key")
	}
}

func bedRow(status string, number string, manual bool) gen.ListBatchIdentityForIDsRow {
	return gen.ListBatchIdentityForIDsRow{ID: uuid.New(), Status: status, BatchNumber: number, Manual: manual}
}

// A planner-made Draft is a proposal, so its planks are still free to move.
func TestAProductOnAPlannedDraftIsStillOffered(t *testing.T) {
	job := batchableJobRow("JOB-1", `["BLUE"]`)
	batchID := uuid.New()
	job.BatchID = &batchID

	if got := unavailableBecause(job, bedRow(production.BatchPendingApproval, "BATCH-1000501", false)); got != "" {
		t.Fatalf("reason = %q, want a planned Draft's plank to stay available", got)
	}
}

// A locked bed can still give a plank up - editing one gives its filament back
// and takes its plate out of the queue first. Only printing and done cannot.
func TestALockedBedsProductIsStillOffered(t *testing.T) {
	job := batchableJobRow("JOB-1", `["BLUE"]`)
	batchID := uuid.New()
	job.BatchID = &batchID

	if got := unavailableBecause(job, bedRow(production.BatchOpen, "BATCH-1000502", false)); got != "" {
		t.Fatalf("reason = %q, want a locked bed's plank to remain movable", got)
	}
	for _, settled := range []string{production.BatchInProgress, production.BatchCompleted} {
		if got := unavailableBecause(job, bedRow(settled, "BATCH-1000503", false)); got == "" {
			t.Errorf("a %s bed's plank was offered as movable", settled)
		}
	}
}

// Four printed planks on finished beds could not be reprinted together,
// because each bed was judged for editability and a finished bed cannot be
// edited - beds nothing was going to change.
func TestPlanksBeingMovedExcludesOnesThatWillBeReprinted(t *testing.T) {
	printed := batchableJobRow("JOB-115082", `["BLUE"]`)
	printed.Status = production.StatusCompleted
	finishedBed := uuid.New()
	printed.BatchID = &finishedBed

	queued := batchableJobRow("JOB-115090", `["BLUE"]`)
	draftBed := uuid.New()
	queued.BatchID = &draftBed

	got := planksBeingMoved([]gen.ProductionJob{printed, queued})
	if len(got) != 1 {
		t.Fatalf("planksBeingMoved returned %d, want only the queued one", len(got))
	}
	if got[0].JobNumber != "JOB-115090" {
		t.Errorf("kept %s, want the queued plank", got[0].JobNumber)
	}
}

func TestPlanksBeingMovedKeepsEveryUnprintedPlank(t *testing.T) {
	jobs := []gen.ProductionJob{
		batchableJobRow("JOB-1", `["BLUE"]`),
		batchableJobRow("JOB-2", `["BLUE"]`),
	}
	if got := planksBeingMoved(jobs); len(got) != 2 {
		t.Fatalf("planksBeingMoved dropped a queued plank: %d of 2", len(got))
	}
}

// The model is what goes on the plate. Without one there is nothing to
// arrange, and a bed built around it would fail at plate-merge time with a
// message about a missing file rather than about this order.
func TestAPlankWithNoModelIsNotOffered(t *testing.T) {
	job := batchableJobRow("JOB-1", `["BLUE"]`)
	job.PrintFileID = nil

	got := unavailableBecause(job, gen.ListBatchIdentityForIDsRow{})
	if got != "no 3D model yet" {
		t.Fatalf("reason = %q, want it refused for having no model", got)
	}
}

// An order somebody has just built a bed for is a question they have already
// answered. Leaving it on screen invited answering it twice, and a list that
// keeps offering back what you just chose is tiresome to work through.
func TestPlanksOnAHandBuiltBedLeaveThePool(t *testing.T) {
	custom, planned := uuid.New(), uuid.New()
	onCustom := batchableJobRow("JOB-1", `["BLUE"]`)
	onCustom.BatchID = &custom
	onPlanned := batchableJobRow("JOB-2", `["BLUE"]`)
	onPlanned.BatchID = &planned
	loose := batchableJobRow("JOB-3", `["BLUE"]`)

	beds := map[uuid.UUID]gen.ListBatchIdentityForIDsRow{
		custom:  {ID: custom, Status: production.BatchPendingApproval, Manual: true},
		planned: {ID: planned, Status: production.BatchPendingApproval},
	}

	got := stillToPlace([]gen.ProductionJob{onCustom, onPlanned, loose}, beds)
	if len(got) != 2 {
		t.Fatalf("stillToPlace kept %d planks, want 2", len(got))
	}
	for _, j := range got {
		if j.JobNumber == "JOB-1" {
			t.Error("a plank already on a hand-built bed was offered again")
		}
	}
}

// A planner-made Draft is a proposal, so its planks stay in the pool.
func TestPlanksOnAPlannedDraftStayInThePool(t *testing.T) {
	planned := uuid.New()
	job := batchableJobRow("JOB-1", `["BLUE"]`)
	job.BatchID = &planned

	beds := map[uuid.UUID]gen.ListBatchIdentityForIDsRow{
		planned: {ID: planned, Status: production.BatchPendingApproval},
	}
	if got := stillToPlace([]gen.ProductionJob{job}, beds); len(got) != 1 {
		t.Fatal("a plank on a planned Draft was dropped from the pool")
	}
}
