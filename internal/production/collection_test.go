package production

import (
	"testing"
	"time"
)

// TestNextCollectionPicksTheRightVan covers the boundaries, which is where a
// step function goes wrong: exactly on a collection time counts as making it,
// and anything after the last van rolls to tomorrow morning.
func TestNextCollectionPicksTheRightVan(t *testing.T) {
	day := func(h, m int) time.Time {
		return time.Date(2026, 3, 4, h, m, 0, 0, time.UTC)
	}
	cases := []struct {
		name string
		at   time.Time
		want time.Time
	}{
		{"early morning", day(7, 0), day(12, 0)},
		{"exactly noon still makes noon", day(12, 0), day(12, 0)},
		{"just after noon waits for four", day(12, 1), day(16, 0)},
		{"exactly four still makes four", day(16, 0), day(16, 0)},
		{"evening rolls to tomorrow", day(18, 30), day(12, 0).Add(24 * time.Hour)},
	}
	for _, c := range cases {
		if got := NextCollection(c.at); !got.Equal(c.want) {
			t.Errorf("%s: NextCollection(%s) = %s, want %s", c.name, c.at, got, c.want)
		}
	}
}

// TestCollectionDeadlineForcesABatchOut is the behaviour that matters: a thin
// bed whose job must start now to make its van is created even though a fuller
// one might arrive later, and even with no idle machine to justify it.
func TestCollectionDeadlineForcesABatchOut(t *testing.T) {
	now := time.Date(2026, 3, 4, 10, 30, 0, 0, time.UTC) // 1.5h before the noon van
	due := time.Date(2026, 3, 4, 12, 0, 0, 0, time.UTC)

	job := smallJob("a", "PLA")
	job.Priority = 5 // routine: no priority override
	job.CreatedAt = now
	job.DueDate = &due

	// No idle machines, long max wait, no aging: without collections this bed
	// would be held.
	gate := BatchGate{MaxWait: 8 * time.Hour, IdleMachines: 0}
	batches, _, held := Plan([]PlanJob{job}, now, gate)
	if len(batches) != 1 || len(held) != 0 {
		t.Fatalf("batches=%d held=%d, want 1/0 - a job that must start now to make its collection cannot wait for a fuller bed",
			len(batches), len(held))
	}

	// The same job with a week to go is held as usual.
	far := now.Add(7 * 24 * time.Hour)
	relaxed := job
	relaxed.DueDate = &far
	batches, _, held = Plan([]PlanJob{relaxed}, now, gate)
	if len(batches) != 0 || len(held) != 1 {
		t.Errorf("batches=%d held=%d, want 0/1 - a job due next week has no collection pressure", len(batches), len(held))
	}
}
