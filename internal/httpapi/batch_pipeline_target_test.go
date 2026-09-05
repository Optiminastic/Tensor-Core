package httpapi

// A plate may only be sliced by a pipeline that targets the printer class it
// was planned for.
//
// This is the constraint that lets a bed be sized for its machine. Without it,
// Tensor runs every pipeline BambuBuddy returns and takes the first that does
// not refuse - so a 7-up H2C plate can be sliced for a P2S, whose bed it does
// not fit. BambuBuddy's own eligibility check looks at printer class and
// filament, never at whether the parts fit the bed, so nothing downstream
// catches it.

import (
	"strings"
	"testing"

	"github.com/Optiminastic/tensor-core/internal/integrations/bambubuddy"
)

func classPipeline(id int, name, class string) bambubuddy.Pipeline {
	return bambubuddy.Pipeline{
		ID: id, Name: name, TargetKind: "printer_class", TargetModelClass: &class,
	}
}

func printerPipeline(id int, name string, printerID int) bambubuddy.Pipeline {
	return bambubuddy.Pipeline{
		ID: id, Name: name, TargetKind: "specific_printer", TargetPrinterID: &printerID,
	}
}

func names(pipelines []bambubuddy.Pipeline) []string {
	out := make([]string, 0, len(pipelines))
	for _, p := range pipelines {
		out = append(out, p.Name)
	}
	return out
}

func TestPipelinesForKeepsOnlyTheBedsOwnClass(t *testing.T) {
	all := []bambubuddy.Pipeline{
		classPipeline(1, "p2s", "P2S"),
		classPipeline(2, "h2c", "H2C"),
		classPipeline(3, "a2l", "A2L"),
	}

	got := pipelinesFor(all, "H2C")
	if len(got) != 1 || got[0].Name != "h2c" {
		t.Errorf("pipelines for H2C = %v, want only the H2C one - slicing an H2C plate "+
			"through the P2S pipeline produces a plate that does not fit its bed", names(got))
	}

	// Case is BambuBuddy's, not ours: it reports "H2C" but a profile family
	// could be stored lower-case.
	if got := pipelinesFor(all, "h2c"); len(got) != 1 || got[0].Name != "h2c" {
		t.Errorf("pipelines for lower-case h2c = %v, want the H2C one", names(got))
	}
}

// An unknown class widens rather than refuses, matching how the colour gate
// treats a batch whose jobs do not agree on a family: hold nothing back on the
// strength of a fact nobody recorded.
func TestPipelinesForWidensWhenTheClassIsUnknown(t *testing.T) {
	all := []bambubuddy.Pipeline{classPipeline(1, "p2s", "P2S"), classPipeline(2, "h2c", "H2C")}
	if got := pipelinesFor(all, ""); len(got) != 2 {
		t.Errorf("pipelines for an unknown class = %v, want all of them", names(got))
	}
	if got := pipelinesFor(all, "   "); len(got) != 2 {
		t.Errorf("pipelines for a blank class = %v, want all of them", names(got))
	}
}

// A pipeline that names no target is a valid one-pipeline install, and is kept
// last - after the ones that do match. Dropping it would mean nothing prints,
// which is a worse failure than slicing through a pipeline that never said what
// it was for.
func TestPipelinesForKeepsAnUntargetedPipelineLast(t *testing.T) {
	all := []bambubuddy.Pipeline{
		{ID: 1, Name: "anything"},
		classPipeline(2, "h2c", "H2C"),
	}
	got := pipelinesFor(all, "H2C")
	if len(got) != 2 {
		t.Fatalf("pipelines = %v, want the H2C one and the untargeted one", names(got))
	}
	if got[0].Name != "h2c" {
		t.Errorf("pipeline order = %v, want the class match tried first", names(got))
	}
}

// A pipeline pinned to one printer is skipped when the class is known: the
// pipeline does not say what model that printer is, and guessing is how a plate
// reaches the wrong bed.
func TestPipelinesForSkipsAPipelinePinnedToAnUnknownPrinter(t *testing.T) {
	all := []bambubuddy.Pipeline{
		printerPipeline(1, "pinned-to-7", 7),
		classPipeline(2, "h2c", "H2C"),
	}
	got := pipelinesFor(all, "H2C")
	if len(got) != 1 || got[0].Name != "h2c" {
		t.Errorf("pipelines = %v, want only the H2C class pipeline", names(got))
	}
}

// The hold reason has to name the class, because the fix is a setup change:
// "add a pipeline for H2C" is actionable, "no printer can print this" is not.
func TestPipelineTargetNoteNamesTheClass(t *testing.T) {
	note := pipelineTargetNote("H2C")
	if note == "" || !strings.Contains(note, "H2C") {
		t.Errorf("note = %q, want it to name H2C", note)
	}
	if note := pipelineTargetNote(""); note == "" {
		t.Error("an unknown class still needs a reason")
	}
}
