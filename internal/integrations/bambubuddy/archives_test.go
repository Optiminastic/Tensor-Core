package bambubuddy

// What a history row means, when BambuBuddy's own numbers are incomplete.
//
// Every one of these is a real shape from the live archive: seven of twelve
// rows there carry status "archived", several carry an actual time of zero, and
// one carries two colours in a single comma-separated string.

import (
	"reflect"
	"testing"
)

// The estimate stands in when the printer reported no actual time.
//
// Falling back rather than showing zero: an archive whose actual never landed
// is far more usefully shown with the slicer's estimate than with a blank, and
// most of the live archive is in exactly that state.
func TestArchiveDurationAndFilamentFallBackToTheEstimate(t *testing.T) {
	for _, c := range []struct {
		name         string
		a            Archive
		wantSeconds  int
		wantFilament float64
	}{
		{
			"the printer reported both",
			Archive{
				PrintTimeSeconds: 1046, ActualTimeSeconds: 1020,
				FilamentUsedGrams: 4.0, TotalFilamentActualGrams: 3.7,
			},
			1020, 3.7,
		},
		{
			// The common case in the live archive.
			"no actual time or weight ever landed",
			Archive{PrintTimeSeconds: 30876, FilamentUsedGrams: 209.7},
			30876, 209.7,
		},
		{
			"nothing at all",
			Archive{},
			0, 0,
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := c.a.DurationSeconds(); got != c.wantSeconds {
				t.Errorf("DurationSeconds = %d, want %d", got, c.wantSeconds)
			}
			if got := c.a.FilamentGrams(); got != c.wantFilament {
				t.Errorf("FilamentGrams = %v, want %v", got, c.wantFilament)
			}
		})
	}
}

// "archived" is a finished print.
//
// It is the status most of the live archive carries, and leaving it out made
// the majority of the history look like it was still running.
func TestArchiveFinished(t *testing.T) {
	for status, want := range map[string]bool{
		ArchiveCompleted: true,
		ArchiveFailed:    true,
		ArchiveCancelled: true,
		ArchiveArchived:  true,
		ArchivePrinting:  false,
		"":               false,
		// A status this build has not heard of is not assumed to be finished -
		// the safe reading of an unknown word is that the print is still live.
		"paused": false,
	} {
		if got := (Archive{Status: status}).Finished(); got != want {
			t.Errorf("Finished(%q) = %v, want %v", status, got, want)
		}
	}
}

// The name falls back to the file, then to something printable.
//
// A board rendering an empty cell for a plate that has a filename is losing
// information it was handed.
func TestArchiveName(t *testing.T) {
	for _, c := range []struct{ name, print, file, want string }{
		{"both", "BLUE-114840-114873", "plate.3mf", "BLUE-114840-114873"},
		{"file only", "", "BLACK-114776.stl", "BLACK-114776.stl"},
		{"neither", "", "", "Untitled print"},
		{"whitespace is not a name", "   ", "plate.3mf", "plate.3mf"},
	} {
		t.Run(c.name, func(t *testing.T) {
			a := Archive{PrintName: c.print, Filename: c.file}
			if got := a.Name(); got != c.want {
				t.Errorf("Name = %q, want %q", got, c.want)
			}
		})
	}
}

// One colour field, one chip per material.
//
// Shared with QueueItem.Colours, which is the point: the queue and the history
// report this field identically, and two renderings of one idea is how a board
// starts contradicting itself.
func TestSplitColours(t *testing.T) {
	for _, c := range []struct {
		name string
		in   string
		want []string
	}{
		{"single", "#00AE42", []string{"#00AE42"}},
		{"multi-material, as the live archive sends it", "#D3B7A7,#FFFFFF",
			[]string{"#D3B7A7", "#FFFFFF"}},
		{"spacing is not a colour", " #D3B7A7 , #FFFFFF ", []string{"#D3B7A7", "#FFFFFF"}},
		{"empty", "", nil},
		{"only separators", " , , ", nil},
	} {
		t.Run(c.name, func(t *testing.T) {
			got := splitColours(c.in)
			if !reflect.DeepEqual(got, c.want) {
				t.Errorf("splitColours(%q) = %v, want %v", c.in, got, c.want)
			}
			// The queue and the archive must agree, since they share this.
			q := QueueItem{FilamentColour: c.in}.Colours()
			if !reflect.DeepEqual(q, got) {
				t.Errorf("QueueItem.Colours = %v but Archive.Colours = %v", q, got)
			}
		})
	}
}
