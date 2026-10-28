package httpapi

// The bulk catch-up, and the things it must refuse to do.
//
// This is the one action in Tensor that declares work finished which nobody
// performed. Every test here is about a way that could go wrong quietly.

import (
	"testing"

	"github.com/Optiminastic/tensor-core/internal/db/gen"
	"github.com/Optiminastic/tensor-core/internal/production"
)

// The admin-only assertion lives in internal/auth's catalog_test.go, beside
// the other separation-of-duties rules and inside the package that can see
// the grants. See TestProjectAndBrandManageAreAdminOnly.

func TestWaitingStationNamesTheFirstBlockedStep(t *testing.T) {
	// The preview has to read the way the board reads, or somebody checks a
	// list that does not match the tabs they are looking at.
	cases := []struct {
		name string
		row  gen.ListJobsAwaitingStationsRow
		want string
	}{
		{"assembly first", gen.ListJobsAwaitingStationsRow{
			AssemblyStatus: production.AssemblyPending, FinishingStatus: production.FinishingPending,
			QcStatus: production.QcPending, PackagingStatus: production.PackagingPending,
		}, "Assembly"},
		{"finishing next", gen.ListJobsAwaitingStationsRow{
			AssemblyStatus: production.AssemblyCompleted, FinishingStatus: production.FinishingPending,
			QcStatus: production.QcPending, PackagingStatus: production.PackagingPending,
		}, "Finishing"},
		{"then qc", gen.ListJobsAwaitingStationsRow{
			AssemblyStatus: production.AssemblyCompleted, FinishingStatus: production.FinishingCompleted,
			QcStatus: production.QcPending, PackagingStatus: production.PackagingPending,
		}, "Quality Check"},
		{"then packaging", gen.ListJobsAwaitingStationsRow{
			AssemblyStatus: production.AssemblyCompleted, FinishingStatus: production.FinishingCompleted,
			QcStatus: production.QcPassed, PackagingStatus: production.PackagingPending,
		}, "Packaging"},
	}
	for _, c := range cases {
		if got := waitingStation(c.row); got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}

func TestTheReasonIsBoundedButKept(t *testing.T) {
	// It is written to every job's permanent record, so it is a sentence, not
	// a report - but truncating it to nothing would leave the record silent
	// about why a board was declared finished.
	long := ""
	for i := 0; i < 500; i++ {
		long += "x"
	}
	got := trimTo(long, catchUpReasonMax)
	if len(got) != catchUpReasonMax {
		t.Errorf("reason truncated to %d, want %d", len(got), catchUpReasonMax)
	}
	if trimTo("   shipped last week   ", catchUpReasonMax) != "shipped last week" {
		t.Error("the reason was not trimmed of surrounding space")
	}
	if trimTo("    ", catchUpReasonMax) != "" {
		t.Error("whitespace should read as no reason given, and be refused upstream")
	}
}
