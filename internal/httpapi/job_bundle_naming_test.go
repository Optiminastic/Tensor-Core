package httpapi

// A combo sold as a Shopify bundle arrives as one line per item, each its own
// registry product. Its jobs are named for the item, under the order's number.

import (
	"context"
	"strings"
	"testing"

	"github.com/Optiminastic/tensor-core/internal/auth"
	"github.com/Optiminastic/tensor-core/internal/config"
)

func TestIntegrationABundlesItemsAreNamedForWhatTheyPrint(t *testing.T) {
	store := setupStore(t)
	seedAll(t, store)
	ctx := context.Background()
	if _, err := store.Pool.Exec(ctx, `TRUNCATE products CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	// Each item is a single product with one named part.
	seedComboProduct(t, store, "SCPLANK", "SC-PLANK-RED", []string{"plank"})
	seedComboProduct(t, store, "3DRN", "3DRN", []string{"rose"})
	seedComboProduct(t, store, "SCKEY", "SC-KEY", []string{"keychain"})
	// And one still on the default part, which takes its product's code.
	seedComboProduct(t, store, "SCCARD", "SC-CARD", []string{"body"})

	s := NewServer(config.Settings{Environment: "development", AuthAudience: "tensor-core"},
		store, auth.NewGuards(newTokenMinter(t).verifier, ""), nil)

	line := func(sku string, bundle bool) map[string]any {
		l := map[string]any{"product_id": sku, "sku": sku, "product_name": sku, "quantity": 1, "material": "PLA"}
		if bundle {
			l["bundle"] = "Soulmate COMBO"
		}
		return l
	}
	combo := seedOrder(t, store, 16755, []map[string]any{
		line("SC-PLANK-RED", true), line("3DRN", true), line("SC-KEY", true), line("SC-CARD", true),
	})
	if _, err := s.CreateJobsForOrder(ctx, combo); err != nil {
		t.Fatalf("create jobs: %v", err)
	}
	got := strings.Join(jobNumbersForOrder(t, store, combo), " ")
	for _, want := range []string{"-PLANK", "-ROSE", "-KEYCHAIN", "-SCCARD"} {
		if !strings.Contains(got, want) {
			t.Errorf("no bundle job named %s; got %s", want, got)
		}
	}
	if strings.Contains(got, "-2") || strings.Contains(got, "-3") {
		t.Errorf("a bundle item was numbered instead of named: %s", got)
	}

	// The same rose bought on its own keeps its plain number.
	alone := seedOrder(t, store, 16756, []map[string]any{line("3DRN", false)})
	if _, err := s.CreateJobsForOrder(ctx, alone); err != nil {
		t.Fatalf("create jobs: %v", err)
	}
	if got := jobNumbersForOrder(t, store, alone); len(got) != 1 || strings.Contains(got[0], "ROSE") {
		t.Errorf("a rose sold on its own was renamed: %v", got)
	}
}

// A Soulmate COMBO sold as ONE line (an order from before the bundle, or the
// with-LIGHT combo) is made as its three items, like the bundle it is now.
func TestIntegrationAWholeComboLineIsMadeAsItsThreeItems(t *testing.T) {
	store := setupStore(t)
	seedAll(t, store)
	ctx := context.Background()
	if _, err := store.Pool.Exec(ctx, `TRUNCATE products CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	seedComboProduct(t, store, "SCPLANK", "SC-PLANK-RED", []string{"plank"})
	seedComboProduct(t, store, "3DRN", "3DRN", []string{"rose"})
	seedComboProduct(t, store, "SCKEY", "SC-KEY", []string{"keychain"})

	s := NewServer(config.Settings{Environment: "development", AuthAudience: "tensor-core"},
		store, auth.NewGuards(newTokenMinter(t).verifier, ""), nil)

	for i, sku := range []string{"SC-RED", "SCWL-RED"} {
		order := seedOrder(t, store, int64(16590+i), []map[string]any{{
			"product_id": sku, "sku": sku, "product_name": "Soulmate COMBO", "quantity": 1, "material": "PLA",
			"properties": []map[string]any{{"name": "name", "value": "INDU"}},
		}})
		if _, err := s.CreateJobsForOrder(ctx, order); err != nil {
			t.Fatalf("%s: create jobs: %v", sku, err)
		}
		got := strings.Join(jobNumbersForOrder(t, store, order), " ")
		for _, want := range []string{"-PLANK", "-ROSE", "-KEYCHAIN"} {
			if !strings.Contains(got, want) {
				t.Errorf("%s: no job named %s; got %s", sku, want, got)
			}
		}
	}

	// The keychain item is not mistaken for a whole combo.
	if _, ok := s.legacyComboItems(ctx, lineFor("SC-KEY", "Heart Keychain")); ok {
		t.Error("SC-KEY was read as a whole combo")
	}
}
