package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/google/uuid"

	"github.com/Optiminastic/tensor-core/internal/auth"
	"github.com/Optiminastic/tensor-core/internal/config"
	"github.com/Optiminastic/tensor-core/internal/db/gen"
	"github.com/Optiminastic/tensor-core/internal/integrations/bambubuddy"
	"github.com/Optiminastic/tensor-core/internal/production"
	"github.com/Optiminastic/tensor-core/internal/slicing"
)

func TestSlicesWithTensorFollowsTheClassList(t *testing.T) {
	s := &Server{cfg: config.Settings{TensorSliceFamilies: []string{"a2l", " P2S "}}}
	model := func(m string) gen.Machine { return gen.Machine{Model: &m} }
	for m, want := range map[string]bool{"A2L": true, "p2s": true, "H2C": false, "": false} {
		if got := s.slicesWithTensor(model(m)); got != want {
			t.Errorf("%q: got %v, want %v", m, got, want)
		}
	}
	if (&Server{}).slicesWithTensor(model("A2L")) {
		t.Error("an empty list keeps every class on BambuBuddy")
	}
}

func TestIntegrationTensorSliceMarksTheBedAndEnqueuesTogether(t *testing.T) {
	store := setupStore(t)
	seedAll(t, store)
	ctx := context.Background()
	if _, err := store.Pool.Exec(ctx, `TRUNCATE slicing_profiles; DELETE FROM river_job WHERE kind = 'slice_print'`); err != nil {
		t.Fatalf("reset: %v", err)
	}
	client, err := slicing.NewInsertOnlyClient(store.Pool)
	if err != nil {
		t.Fatalf("river client: %v", err)
	}
	minter := newTokenMinter(t)
	s := NewServer(config.Settings{Environment: "development", AuthAudience: "tensor-core"},
		store, auth.NewGuards(minter.verifier, ""), nil)
	s.EnableSliceEnqueuer(slicing.NewEnqueuer(client))

	batch, err := store.Q.InsertBatch(ctx, gen.InsertBatchParams{
		ID: uuid.New(), BatchNumber: "BATCH-TS-1", Status: production.BatchOpen,
	})
	if err != nil {
		t.Fatalf("insert batch: %v", err)
	}
	machine, err := store.Q.InsertFleetMachine(ctx, gen.InsertFleetMachineParams{
		ID: uuid.New(), MachineID: "SER-TS", Name: "A1", Status: "idle", Filaments: []byte("[]"),
	})
	if err != nil {
		t.Fatalf("insert machine: %v", err)
	}
	send := tensorSend{
		Batch: batch, Machine: machine, Pipeline: bambubuddy.Pipeline{ID: 7, Name: "Heart A2L"},
		Plate: gen.FileAsset{StorageKey: "batches/x/plate.3mf", Filename: "BATCH-TS-1.3mf"},
		Assignments: []slotAssignment{}, PrinterID: 9,
	}

	// No profile for the pipeline: refused, the reason recorded, and the bed
	// left unmarked so it can be sent once the profiles are imported.
	if _, err := s.sendForTensorSlice(ctx, send, queueBatchResponse{}); !isStatus(err, http.StatusConflict) {
		t.Fatalf("no profile = %v, want 409", err)
	}
	after, _ := store.Q.GetBatchByID(ctx, batch.ID)
	if after.BambuSliceJobID != nil || after.PrintError == nil {
		t.Fatalf("refused bed: marker=%v error=%v", after.BambuSliceJobID, after.PrintError)
	}

	pipelineID := int32(7)
	profile, err := store.Q.UpsertSlicingProfileFromPipeline(ctx, gen.UpsertSlicingProfileFromPipelineParams{
		ID: uuid.New(), BambuPipelineID: &pipelineID, Name: "Heart A2L", MachineFamily: "A2L",
		PrinterPresetName: "m", PrinterPreset: []byte(`{}`), ProcessPresetName: "p", ProcessPreset: []byte(`{}`),
		FilamentPresetName: "f", FilamentPreset: []byte(`{}`),
	})
	if err != nil {
		t.Fatalf("profile: %v", err)
	}
	out, err := s.sendForTensorSlice(ctx, send, queueBatchResponse{})
	if err != nil || !out.Queued {
		t.Fatalf("send = %+v, %v", out, err)
	}
	after, _ = store.Q.GetBatchByID(ctx, batch.ID)
	if after.BambuSliceJobID == nil || *after.BambuSliceJobID != 0 || after.FleetMachineID == nil ||
		*after.FleetMachineID != machine.ID || after.PrintError != nil {
		t.Fatalf("marked bed: marker=%v machine=%v error=%v", after.BambuSliceJobID, after.FleetMachineID, after.PrintError)
	}

	var raw []byte
	if err := store.Pool.QueryRow(ctx,
		`SELECT args FROM river_job WHERE kind = 'slice_print' AND queue = 'slice'`).Scan(&raw); err != nil {
		t.Fatalf("slice_print job: %v", err)
	}
	var args slicing.SlicePrintArgs
	if err := json.Unmarshal(raw, &args); err != nil {
		t.Fatal(err)
	}
	if args.BatchID != batch.ID || args.ProfileID != profile.ID || args.PlateKey != "batches/x/plate.3mf" ||
		args.PrinterID != 9 || args.MachineID != machine.ID {
		t.Errorf("job args: %+v", args)
	}
}

func isStatus(err error, code int) bool {
	se, ok := err.(*statusError)
	return ok && se.status == code
}
