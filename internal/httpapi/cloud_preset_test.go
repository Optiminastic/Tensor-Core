package httpapi

// A slicer complaint has to say which pipeline it is about.
//
// Tensor does not choose presets. It reads the pipeline's own out of BambuBuddy
// and passes them straight back, so "Cloud preset selected for filament, but no
// Bambu Cloud session is stored" describes the PIPELINE's configuration - and
// read on a batch row, with several pipelines on the floor and no indication
// which one the bed used, it reads instead as Tensor demanding a cloud account.

import (
	"strings"
	"testing"

	"github.com/Optiminastic/tensor-core/internal/integrations/bambubuddy"
)

func cloudPreset(id string) *bambubuddy.PresetVal {
	return &bambubuddy.PresetVal{Source: "cloud", ID: id}
}

func localPreset(id string) *bambubuddy.PresetVal {
	return &bambubuddy.PresetVal{Source: "local", ID: id}
}

const cloudComplaint = "Cloud preset selected for filament, but no Bambu Cloud " +
	"session is stored. Sign in to Bambu Cloud and retry."

func TestACloudPresetComplaintNamesThePipelineAndThePreset(t *testing.T) {
	p := bambubuddy.Pipeline{
		Name:          "H2C two-nozzle",
		PrinterPreset: localPreset("Bambu Lab H2C 0.4 nozzle"),
		ProcessPreset: localPreset("0.28mm heart"),
		// One preset, repeated once per plate slot, as sendBatchToMachine does.
		FilamentPresets: []bambubuddy.PresetVal{
			{Source: "cloud", ID: "Bambu PLA Basic @BBL H2C - Copy"},
			{Source: "cloud", ID: "Bambu PLA Basic @BBL H2C - Copy"},
		},
	}

	got := withPipelineContext(cloudComplaint, p)

	// The slicer's own words survive: they are the authoritative part.
	if !strings.HasPrefix(got, cloudComplaint) {
		t.Errorf("message = %q, want it to begin with BambuBuddy's own wording", got)
	}
	for _, want := range []string{"H2C two-nozzle", "Bambu PLA Basic @BBL H2C - Copy"} {
		if !strings.Contains(got, want) {
			t.Errorf("message = %q, want it to name %q", got, want)
		}
	}
	// One preset repeated per slot is one problem, not two.
	if strings.Count(got, "Bambu PLA Basic @BBL H2C - Copy") != 1 {
		t.Errorf("message = %q names the same preset twice; it is repeated per plate "+
			"slot and that is not two faults", got)
	}
	// Both ways out, because one of them needs no Bambu account at all.
	for _, want := range []string{"built-in preset", "Sign in to Bambu Cloud"} {
		if !strings.Contains(got, want) {
			t.Errorf("message = %q, want it to offer %q", got, want)
		}
	}
}

// Every other slicer complaint passes through untouched. Appending a pipeline
// note to "the plate does not fit the bed" would be noise.
func TestAnUnrelatedSlicerComplaintIsNotDecorated(t *testing.T) {
	p := bambubuddy.Pipeline{Name: "H2C two-nozzle",
		FilamentPresets: []bambubuddy.PresetVal{{Source: "cloud", ID: "x"}}}

	const other = "G-code conflicts detected."
	if got := withPipelineContext(other, p); got != other {
		t.Errorf("message = %q, want it unchanged", got)
	}
}

// A cloud complaint from a pipeline with no cloud preset is left alone rather
// than decorated with an empty list. It means the fault is somewhere this
// function cannot see, and inventing a culprit would be worse than silence.
func TestACloudComplaintWithoutACloudPresetIsLeftAlone(t *testing.T) {
	p := bambubuddy.Pipeline{
		Name:            "All local",
		PrinterPreset:   localPreset("Bambu Lab H2C 0.4 nozzle"),
		FilamentPresets: []bambubuddy.PresetVal{{Source: "local", ID: "Bambu PLA Basic"}},
	}
	if got := withPipelineContext(cloudComplaint, p); got != cloudComplaint {
		t.Errorf("message = %q, want it unchanged when nothing in the pipeline is cloud", got)
	}
}

// The printer and process presets can be the cloud ones too, and are named with
// the right word for which they are.
func TestEveryKindOfCloudPresetIsNamedByItsKind(t *testing.T) {
	p := bambubuddy.Pipeline{
		Name:          "Cloudy",
		PrinterPreset: cloudPreset("Some H2C"),
		ProcessPreset: cloudPreset("0.28mm custom"),
	}
	got := withPipelineContext(cloudComplaint, p)

	for _, want := range []string{`cloud printer preset "Some H2C"`, `cloud process preset "0.28mm custom"`} {
		if !strings.Contains(got, want) {
			t.Errorf("message = %q, want it to contain %q", got, want)
		}
	}
}
