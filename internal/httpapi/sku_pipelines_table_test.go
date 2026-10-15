package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"github.com/google/uuid"

	"github.com/Optiminastic/tensor-core/internal/auth"
	"github.com/Optiminastic/tensor-core/internal/db/gen"
	"github.com/Optiminastic/tensor-core/internal/production"
)

// SKU Pipelines, paged over SKUs in Postgres: a SKU's family mappings arrive
// together, never split across two pages.
func TestIntegrationSKUPipelinesTablePages(t *testing.T) {
	store := setupStore(t)
	seedAll(t, store)
	ctx := context.Background()
	if _, err := store.Pool.Exec(ctx, `TRUNCATE sku_slicer_pipelines`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	minter := newTokenMinter(t)
	router := testServer(t, store, auth.NewGuards(minter.verifier, ""))
	token := minter.mint(t, []string{auth.RegistryRead.Key()})

	// 20 SKUs on jobs, SKU-00..SKU-19; the even ones mapped for all three
	// families.
	for i := 0; i < 20; i++ {
		sku := fmt.Sprintf("SKU-%02d", i)
		if _, err := store.Q.InsertProductionJob(ctx, gen.InsertProductionJobParams{
			ID: uuid.New(), JobNumber: fmt.Sprintf("SJ-%d", i), Description: "Plank", Quantity: 1,
			Status: production.StatusQueued, AssemblyStatus: production.AssemblyPending,
			QcStatus: production.QcPending, PackagingStatus: production.PackagingPending,
			PersonalisationStatus: production.PersonalisationNotRequired, Colours: []byte("[]"),
			Sku: &sku,
		}); err != nil {
			t.Fatalf("insert job: %v", err)
		}
		if i%2 != 0 {
			continue
		}
		for k, family := range machineFamilies() {
			if _, err := store.Q.UpsertSKUPipeline(ctx, gen.UpsertSKUPipelineParams{
				ID: uuid.New(), Sku: sku, MachineFamily: family, PipelineID: int32(100 + k), PipelineName: "p",
			}); err != nil {
				t.Fatalf("map %s: %v", sku, err)
			}
		}
	}

	type page struct {
		Items []struct {
			SKU       string                       `json:"sku"`
			Pipelines map[string]skuPipelineChoice `json:"pipelines"`
		} `json:"items"`
		Total    int64    `json:"total"`
		Families []string `json:"families"`
	}
	get := func(q string) page {
		rr := doJSON(router, http.MethodGet, "/registry/sku-pipelines?"+q, token, nil)
		var out page
		if rr.Code != http.StatusOK || json.Unmarshal(rr.Body.Bytes(), &out) != nil {
			t.Fatalf("GET %s = %d: %s", q, rr.Code, rr.Body.String())
		}
		return out
	}

	p1 := get("page=1")
	if len(p1.Items) != 15 || p1.Total != 20 || len(p1.Families) != 3 {
		t.Fatalf("page 1 = %d SKUs of %d, %d families", len(p1.Items), p1.Total, len(p1.Families))
	}
	for _, it := range p1.Items {
		var n int
		fmt.Sscanf(it.SKU, "SKU-%d", &n)
		if want := map[bool]int{true: 3, false: 0}[n%2 == 0]; len(it.Pipelines) != want {
			t.Errorf("%s carries %d mappings, want all %d on its one page", it.SKU, len(it.Pipelines), want)
		}
	}
	if p2 := get("page=2"); len(p2.Items) != 5 || p2.Items[0].SKU != "SKU-15" {
		t.Errorf("page 2 = %d SKUs starting %v, want the last 5 from SKU-15", len(p2.Items), p2.Items)
	}
	if got := get("page=1&search=sku-1"); got.Total != 10 {
		t.Errorf("search sku-1 found %d, want 10 (SKU-10..SKU-19)", got.Total)
	}
}
