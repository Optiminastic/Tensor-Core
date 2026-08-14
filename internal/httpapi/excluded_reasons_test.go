package httpapi

import (
	"context"
	"testing"

	"github.com/Optiminastic/tensor-core/internal/production"
)

// TestExclusionReasonPrefersWhatAnOperatorCanAct'On is the ordering rule: a job
// can be held AND unvalidated AND flagged at once, and only one reason can be
// shown. A hold is a deliberate human decision, so it outranks the rest.
func TestExclusionReasonPrefersTheActionableCause(t *testing.T) {
	issue := production.IssueSTLMissing
	profile := production.IssueProfileMissing

	tests := []struct {
		name            string
		held            bool
		personalisation string
		issue           *string
		want            string
	}{
		{
			name: "held outranks everything", held: true,
			personalisation: production.PersonalisationPending, issue: &issue,
			want: production.ReasonHeld,
		},
		{
			name:            "personalisation outranks an issue",
			personalisation: production.PersonalisationPending, issue: &issue,
			want: production.ReasonPersonalisationPending,
		},
		{
			name:            "a missing printer profile reads as a configuration problem",
			personalisation: production.PersonalisationNotRequired, issue: &profile,
			want: production.ReasonConfigurationMissing,
		},
		{
			name:            "any other issue is surfaced as itself",
			personalisation: production.PersonalisationNotRequired, issue: &issue,
			want: issue,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := exclusionReason(tc.held, tc.personalisation, tc.issue); got != tc.want {
				t.Errorf("exclusionReason = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestExclusionReasonNeverReturnsEmpty: this string is the entire answer to
// "why is this job not on a bed?". An empty one puts the job back to looking
// lost, which is what the whole reporting path exists to prevent.
func TestExclusionReasonNeverReturnsEmpty(t *testing.T) {
	if got := exclusionReason(false, production.PersonalisationNotRequired, nil); got == "" {
		t.Error("exclusionReason returned empty for a job matching none of its arms")
	}
}

// TestIntegrationExcludedJobsAreReportedWithAReason covers the query itself:
// every queued job the planner's own predicate skips must come back here, and
// an eligible one must not.
func TestIntegrationExcludedJobsAreReportedWithAReason(t *testing.T) {
	store := setupStore(t)
	seedAll(t, store)
	ctx := context.Background()

	cfg := jobConfig{material: "PLA Basics", leftNozzleMm: 0.4, machineFamily: "H2C"}
	eligibleID := seedConfiguredJob(t, store, "JOB-OK", cfg)

	heldCfg := cfg
	heldCfg.held = true
	heldID := seedConfiguredJob(t, store, "JOB-HELD", heldCfg)

	rows, err := store.Q.ListExcludedQueuedJobs(ctx)
	if err != nil {
		t.Fatalf("ListExcludedQueuedJobs: %v", err)
	}

	var sawHeld bool
	for _, r := range rows {
		if r.ID == eligibleID {
			t.Error("an eligible job was reported as excluded; the query is not the complement of the batchable bar")
		}
		if r.ID == heldID {
			sawHeld = true
			if got := exclusionReason(r.Held, r.PersonalisationStatus, r.IssueReason); got != production.ReasonHeld {
				t.Errorf("held job reported as %q, want %q", got, production.ReasonHeld)
			}
		}
	}
	if !sawHeld {
		t.Error("the held job was not reported at all; it is excluded from batching AND from the explanation")
	}
}
