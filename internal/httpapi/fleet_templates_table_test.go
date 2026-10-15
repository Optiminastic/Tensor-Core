package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"github.com/google/uuid"

	"github.com/Optiminastic/tensor-core/internal/auth"
)

// The Machines tab paged in Postgres - errored machines first, so the
// per-machine live-filament work runs for one page only - and the Designs tab
// cut from the templates Go assembles.
func TestIntegrationFleetAndTemplatesTablePages(t *testing.T) {
	store := setupStore(t)
	seedAll(t, store)
	ctx := context.Background()
	if _, err := store.Pool.Exec(ctx, `TRUNCATE machines CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	minter := newTokenMinter(t)
	router := testServer(t, store, auth.NewGuards(minter.verifier, ""))
	token := minter.mint(t, []string{auth.MachineRead.Key(), auth.ConfigRead.Key()})

	// 18 machines M-00..M-17; M-12 is in error.
	for i := 0; i < 18; i++ {
		status := "idle"
		if i == 12 {
			status = "error"
		}
		if _, err := store.Pool.Exec(ctx, `INSERT INTO machines (id, machine_id, status) VALUES ($1, $2, $3)`,
			uuid.New(), fmt.Sprintf("M-%02d", i), status); err != nil {
			t.Fatalf("insert machine %d: %v", i, err)
		}
	}

	var fleet struct {
		Items []struct {
			MachineID string `json:"machine_id"`
			Status    string `json:"status"`
		} `json:"items"`
		Total int64 `json:"total"`
	}
	rr := doJSON(router, http.MethodGet, "/machine-fleet?page=1", token, nil)
	if err := json.Unmarshal(rr.Body.Bytes(), &fleet); err != nil || rr.Code != http.StatusOK {
		t.Fatalf("fleet page = %d (%v): %s", rr.Code, err, rr.Body.String())
	}
	if len(fleet.Items) != 15 || fleet.Total != 18 {
		t.Fatalf("fleet page 1 = %d of %d", len(fleet.Items), fleet.Total)
	}
	if fleet.Items[0].MachineID != "M-12" || fleet.Items[1].MachineID != "M-00" {
		t.Errorf("fleet starts %s, %s; want the errored M-12, then M-00", fleet.Items[0].MachineID, fleet.Items[1].MachineID)
	}

	var templates struct {
		Items []json.RawMessage `json:"items"`
		Total int64             `json:"total"`
	}
	rr = doJSON(router, http.MethodGet, "/registry/templates?page=1&page_size=2", token, nil)
	if err := json.Unmarshal(rr.Body.Bytes(), &templates); err != nil || rr.Code != http.StatusOK {
		t.Fatalf("templates page = %d (%v): %s", rr.Code, err, rr.Body.String())
	}
	if templates.Total < 1 || int64(len(templates.Items)) != min(templates.Total, 2) {
		t.Errorf("templates page = %d of %d at 2 a page", len(templates.Items), templates.Total)
	}
}
