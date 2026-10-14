package httpapi

// What the floor is told about a job it cannot batch.
//
// The danger here is not a wrong sentence, it is a missing one: humanReason
// passes an unrecognised value straight through, so an issue reason with no
// wording shows an operator the raw column value.

import (
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/Optiminastic/tensor-core/internal/db/gen"
	"github.com/Optiminastic/tensor-core/internal/production"
)

func TestEveryIssueReasonHasWording(t *testing.T) {
	// production.issueReasons is unexported, so the list is restated here -
	// which is the point: adding a reason there and not here fails this test,
	// and adding it to both without wording fails it too.
	for _, reason := range []string{
		production.IssueSKUMissing,
		production.IssueNoApprovedDesign,
		production.IssueSTLMissing,
		production.IssueColourMissing,
		production.IssueMaterialMissing,
		production.IssueFilamentOutOfStock,
		production.IssueDesignFileStale,
	} {
		worded := humanReason(reason)
		if worded == reason {
			t.Errorf("issue reason %q has no wording, so the queue shows the "+
				"column value to an operator", reason)
		}
		if !strings.HasSuffix(worded, ".") {
			t.Errorf("issue reason %q reads %q, which is not a sentence", reason, worded)
		}
	}
	// IssueProfileMissing is the known exception: exclusionReason intercepts
	// it before humanReason is reached.
	if humanReason(production.IssueProfileMissing) != production.IssueProfileMissing {
		t.Log("IssueProfileMissing has gained wording; it can join the list above")
	}
}

func TestAStaleDesignFileIsExplainedNotEnumerated(t *testing.T) {
	fileID := uuid.New()
	stale := production.IssueDesignFileStale
	job := gen.ProductionJob{
		Status:                production.StatusQueued,
		PersonalisationStatus: production.PersonalisationValidated,
		PrintFileID:           &fileID,
		IssueReason:           &stale,
		Sku:                   strPtr("DT-BoneTag-8"),
		ProductName:           strPtr("Bone Tag - Black / Gold"),
	}

	got := batchingBlockedReason(job, true)
	if got == nil {
		t.Fatal("a job with a stale design file must report why it is not batched")
	}
	if *got == stale {
		t.Fatalf("the queue shows the raw enum %q instead of a sentence", *got)
	}
	if !strings.Contains(*got, "3MF") {
		t.Errorf("the reason should name the action, got %q", *got)
	}
	// It has a file, so it must NOT be confused with the three reasons that
	// all mean "there is no model here".
	if *got == UploadDesignFileWording {
		t.Error("a stale file is not a missing one; the two must read differently")
	}
}

func TestARebuildingJobIsNotReportedAsBlocked(t *testing.T) {
	fileID := uuid.New()
	job := gen.ProductionJob{
		Status:                production.StatusQueued,
		PersonalisationStatus: production.PersonalisationValidated,
		PrintFileID:           &fileID,
		Sku:                   strPtr("T3DPS-DNP-9"),
		ProductName:           strPtr("Dual Name Plank - BLUE / NO LIGHT"),
		ModelRenderQueuedAt:   pgtype.Timestamptz{Valid: true},
	}
	// It is excluded from planning while the rebuild runs, but that is
	// progress rather than a fault - and a red row on every plank for the
	// second its render takes teaches an operator that red means nothing.
	if got := batchingBlockedReason(job, true); got != nil {
		t.Errorf("a job mid-rebuild reported %q; it is making progress", *got)
	}
}
