package httpapi

import (
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/Optiminastic/tensor-core/internal/db/gen"
)

func issue(s string) *string { return &s }

// The Desk Name Plank job: created before its product was set up, so it was
// stamped no_approved_design and never queued. Once the setup was finished
// the queue called it "Creating" - for ever, since nothing was running.
func TestAJobJudgedBeforeSetupSaysSoInsteadOfCreating(t *testing.T) {
	j := gen.ProductionJob{IssueReason: issue("no_approved_design")}

	if got := modelStatusOf(j, true); got != ModelFailed {
		t.Fatalf("status = %q, want %q - it is not being made", got, ModelFailed)
	}
	why := reportableModelError(j, true)
	if why == nil || *why != setUpAfterOrderWording {
		t.Fatalf("reason = %v, want the set-up-after-this-order sentence", why)
	}
}

// A job created against the finished setup is genuinely waiting on its
// render and must still read as Creating.
func TestAGeneratedJobAwaitingItsRenderIsStillCreating(t *testing.T) {
	j := gen.ProductionJob{IssueReason: issue("stl_missing")}
	if got := modelStatusOf(j, true); got != ModelGenerating {
		t.Fatalf("status = %q, want %q", got, ModelGenerating)
	}
	if why := reportableModelError(j, true); why != nil {
		t.Fatalf("a render in flight has no error to report, got %q", *why)
	}
}

// A product Tensor does not make - a photo frame - carries the same stamp
// and must keep asking for a file, not be told to rebuild.
func TestAProductTensorDoesNotMakeStillAsksForAFile(t *testing.T) {
	j := gen.ProductionJob{IssueReason: issue("no_approved_design")}
	if got := modelStatusOf(j, false); got != ModelApprovalRequired {
		t.Fatalf("status = %q, want %q", got, ModelApprovalRequired)
	}
}

// A render actually queued outranks everything, exactly as before.
func TestAQueuedRenderIsCreatingWhateverTheStamp(t *testing.T) {
	j := gen.ProductionJob{
		IssueReason:         issue("no_approved_design"),
		ModelRenderQueuedAt: pgtype.Timestamptz{Time: time.Now(), Valid: true},
	}
	if got := modelStatusOf(j, true); got != ModelGenerating {
		t.Fatalf("status = %q, want %q", got, ModelGenerating)
	}
}

// A real renderer complaint wins over the set-up sentence: if something DID
// run and fail, the operator needs that, not a guess.
func TestARealRenderErrorIsNotReplaced(t *testing.T) {
	msg := "render the base: openscad produced an empty model"
	j := gen.ProductionJob{IssueReason: issue("no_approved_design"), ModelError: &msg}
	why := reportableModelError(j, true)
	if why == nil || *why != msg {
		t.Fatalf("reason = %v, want the renderer's own %q", why, msg)
	}
}
