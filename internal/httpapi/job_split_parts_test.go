package httpapi

// A combo that arrived before its parts were set up becomes its parts on
// Rebuild, once and only once.

import (
	"context"
	"strings"
	"testing"

	"github.com/Optiminastic/tensor-core/internal/auth"
	"github.com/Optiminastic/tensor-core/internal/config"
)

func TestIntegrationAComboJobMadeBeforeSetupSplitsIntoItsParts(t *testing.T) {
	store := setupStore(t)
	seedAll(t, store)
	ctx := context.Background()
	if _, err := store.Pool.Exec(ctx, `TRUNCATE products CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	minter := newTokenMinter(t)
	s := NewServer(config.Settings{Environment: "development", AuthAudience: "tensor-core"},
		store, auth.NewGuards(minter.verifier, ""), nil)

	// The order lands while SC is not in the registry: one job, as on prod.
	orderID := seedOrder(t, store, 16633, []map[string]any{
		{"product_id": "SC", "sku": "SC-PNK", "product_name": "Soulmate COMBO",
			"quantity": 1, "material": "PLA"},
	})
	jobs, err := s.CreateJobsForOrder(ctx, orderID)
	if err != nil || len(jobs) != 1 {
		t.Fatalf("create jobs: %v (%d jobs), want the one job an unset-up combo got", err, len(jobs))
	}
	original := jobs[0]

	// Then SC is set up with three parts, and Rebuild is pressed.
	seedComboProduct(t, store, "SC", "SC-PNK", []string{"plank", "rose", "keychain"})
	job, made, err := s.splitIntoParts(ctx, original)
	if err != nil {
		t.Fatalf("split: %v", err)
	}
	if len(made) != 2 {
		t.Fatalf("made %d new jobs, want the two parts the order is missing", len(made))
	}
	if job.ID != original.ID || !strings.Contains(job.JobNumber, "-") {
		t.Fatalf("the original job should become the first part, got %s", job.JobNumber)
	}
	got := strings.Join(jobNumbersForOrder(t, store, orderID), " ")
	for _, want := range []string{"-PLANK", "-ROSE", "-KEYCHAIN"} {
		if !strings.Contains(got, want) {
			t.Errorf("no job named for %s; got %s", want, got)
		}
	}

	// Pressing Rebuild again makes nothing more.
	again, err := store.Q.GetProductionJobByID(ctx, original.ID)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if _, more, err := s.splitIntoParts(ctx, again); err != nil || len(more) != 0 {
		t.Fatalf("second split made %d jobs (err %v), want none", len(more), err)
	}
	if n := len(jobNumbersForOrder(t, store, orderID)); n != 3 {
		t.Fatalf("order has %d jobs, want 3", n)
	}
}
