package bambubuddy

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

// The request body is the whole fix, so it is asserted field by field.
//
// A two-colour bed printed in one colour because the pipeline passed a single
// filament preset. Repeating the preset per slot and naming the colours is what
// keeps the second slot alive, and a future refactor that "tidies" the
// repetition away would silently bring the bug back.
func TestSliceFileSendsAPresetAndAColourPerSlot(t *testing.T) {
	var got SliceRequest
	var gotPath string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Errorf("decode request: %v", err)
		}
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"job_id":52,"status":"pending"}`))
	}))
	defer srv.Close()

	preset := PresetVal{Source: "standard", ID: "Bambu PLA Basic @BBL A2L 0.4 nozzle"}
	job, err := New(srv.URL, "key").SliceFile(context.Background(), 64, SliceRequest{
		PrinterPreset:   &PresetVal{Source: "local", ID: "1"},
		ProcessPreset:   &PresetVal{Source: "local", ID: "4"},
		FilamentPresets: []PresetVal{preset, preset},
		FilamentColours: []string{"#FFFFFF", "#2850E0"},
		ExportThreeMF:   true,
	})
	if err != nil {
		t.Fatalf("SliceFile: %v", err)
	}

	if want := "/api/v1/library/files/64/slice"; gotPath != want {
		t.Errorf("path = %q, want %q", gotPath, want)
	}
	if job.JobID != 52 {
		t.Errorf("JobID = %d, want 52 (the field is job_id, not id)", job.JobID)
	}
	if len(got.FilamentPresets) != 2 {
		t.Errorf("sent %d filament presets, want 2 - one per plate slot; a single "+
			"preset is what collapsed a two-colour bed to one filament",
			len(got.FilamentPresets))
	}
	want := []string{"#FFFFFF", "#2850E0"}
	if len(got.FilamentColours) != len(want) {
		t.Fatalf("sent colours %v, want %v", got.FilamentColours, want)
	}
	for i := range want {
		if got.FilamentColours[i] != want[i] {
			t.Errorf("colour %d = %q, want %q - slot order must be the plate's",
				i, got.FilamentColours[i], want[i])
		}
	}
	// The plate is already packed by bedpack at exact offsets.
	if got.AutoArrange || got.AutoOrient {
		t.Error("auto_arrange/auto_orient must stay false, or the slicer re-lays out a packed plate")
	}
	if got.UseEmbeddedSettings {
		t.Error("use_embedded_settings must be false: Tensor is dictating the parameters")
	}
}

// The result file id lives at result.library_file_id - none of the top-level
// names a reasonable person would guess. Captured from the live instance.
func TestSliceJobReadsTheResultFileID(t *testing.T) {
	const body = `{
		"job_id": 52, "status": "completed",
		"result": {"library_file_id": 126, "name": "plate.gcode.3mf",
		           "print_time_seconds": 32527, "filament_used_g": 138.87}
	}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()

	job, err := New(srv.URL, "key").GetSliceJob(context.Background(), 52)
	if err != nil {
		t.Fatalf("GetSliceJob: %v", err)
	}
	if !job.Done() {
		t.Fatal("Done() = false for a completed slice that produced a file")
	}
	if job.Failed() {
		t.Error("Failed() = true for a completed slice")
	}
	if job.Result.LibraryFileID != 126 {
		t.Errorf("LibraryFileID = %d, want 126", job.Result.LibraryFileID)
	}
}

func TestSliceJobStatusHandling(t *testing.T) {
	cases := []struct {
		name         string
		job          SliceJob
		done, failed bool
	}{
		{"pending", SliceJob{Status: "pending"}, false, false},
		{"running", SliceJob{Status: "running"}, false, false},
		{"failed", SliceJob{Status: "failed"}, false, true},
		{"cancelled", SliceJob{Status: "cancelled"}, false, true},
		{"error message with no status", SliceJob{ErrorMessage: "slicer died"}, false, true},
		{
			// Giving up on a slice that was merely slow is the more expensive
			// mistake, so an unknown status means keep waiting.
			"an unrecognised status keeps waiting",
			SliceJob{Status: "post-processing"}, false, false,
		},
		{
			// A terminal status with no file is a failure however it is spelled.
			"completed with no result is not done",
			SliceJob{Status: "completed"}, false, false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.job.Done(); got != tc.done {
				t.Errorf("Done() = %v, want %v", got, tc.done)
			}
			if got := tc.job.Failed(); got != tc.failed {
				t.Errorf("Failed() = %v, want %v", got, tc.failed)
			}
		})
	}
}

func TestFilamentRequirementsReadsSlotsInOrder(t *testing.T) {
	const body = `{"file_id":126,"filename":"plate.gcode.3mf","filaments":[
		{"slot_id":1,"type":"PLA","color":"#FFFFFF","used_grams":138.9,"used_in_plate":true},
		{"slot_id":2,"type":"PLA","color":"#2850E0","used_grams":131.9,"used_in_plate":true}]}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()

	req, err := New(srv.URL, "key").FilamentRequirements(context.Background(), 126)
	if err != nil {
		t.Fatalf("FilamentRequirements: %v", err)
	}
	colours, types := req.Colours(), req.Types()
	if len(colours) != 2 || colours[0] != "#FFFFFF" || colours[1] != "#2850E0" {
		t.Errorf("Colours() = %v, want [#FFFFFF #2850E0]", colours)
	}
	if len(types) != 2 || types[0] != "PLA" {
		t.Errorf("Types() = %v, want two PLA entries", types)
	}
}

// BambuBuddy's own words reach the operator; a bare status does not say which
// preset was wrong.
func TestSliceFileSurfacesBambuBuddysReason(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = w.Write([]byte(`{"detail":"printer preset not found"}`))
	}))
	defer srv.Close()

	_, err := New(srv.URL, "key").SliceFile(context.Background(), 64, SliceRequest{})
	if err == nil {
		t.Fatal("SliceFile accepted a 422")
	}
	var reason ReasonError
	if !errors.As(err, &reason) {
		t.Fatalf("error %v is not a ReasonError, so the operator sees a status code", err)
	}
	if reason.Reason == "" {
		t.Error("ReasonError carried no reason")
	}
}

// A failed slice must arrive with BambuBuddy's own words, whichever field it
// chose to put them in. The field name is undocumented, and an operator told
// only "could not slice" has nothing to act on - BATCH-1000598 retried into
// the same wall every fifteen minutes.
func TestSliceJobReasonSurvivesARenamedField(t *testing.T) {
	for name, body := range map[string]string{
		// The one a live BambuBuddy sends. The others are kept because the
		// field is undocumented and this one was found by reading a failed job
		// off the running service, not from a spec.
		"error_detail":  `{"job_id":27,"status":"failed","error_detail":"G-code conflicts detected"}`,
		"error_message": `{"job_id":27,"status":"failed","error_message":"G-code conflicts detected"}`,
		"error":         `{"job_id":27,"status":"failed","error":"G-code conflicts detected"}`,
		"message":       `{"job_id":27,"status":"failed","message":"G-code conflicts detected"}`,
		"detail":        `{"job_id":27,"status":"failed","detail":"G-code conflicts detected"}`,
	} {
		var j SliceJob
		if err := json.Unmarshal([]byte(body), &j); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if got := j.Reason(); got != "G-code conflicts detected" {
			t.Errorf("%s: reason = %q, want BambuBuddy's words", name, got)
		}
		if !j.Failed() {
			t.Errorf("%s: a failed slice was not recognised as failed", name)
		}
	}
}

// Silence is not a reason. A slice that says nothing must not produce a
// confident-sounding sentence with no cause in it.
func TestSliceJobReasonIsEmptyWhenBambuBuddySaysNothing(t *testing.T) {
	var j SliceJob
	if err := json.Unmarshal([]byte(`{"job_id":27,"status":"failed"}`), &j); err != nil {
		t.Fatal(err)
	}
	if got := j.Reason(); got != "" {
		t.Errorf("reason = %q, want empty so the caller can say so plainly", got)
	}
	if !j.Failed() {
		t.Error("a status of failed was not treated as failed")
	}
}
