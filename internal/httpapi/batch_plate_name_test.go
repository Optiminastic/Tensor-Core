package httpapi

import (
	"encoding/json"
	"testing"

	"github.com/Optiminastic/tensor-core/internal/db/gen"
)

// plateJob is a batch member as the plate namer sees one.
func plateJob(jobNumber string, colours ...string) gen.ProductionJob {
	raw, _ := json.Marshal(colours)
	return gen.ProductionJob{JobNumber: jobNumber, Colours: raw}
}

// The name is what an operator reads standing at a printer with a downloaded
// file: whose orders are on this bed, and which filament to load.
func TestPlateFileStem(t *testing.T) {
	for _, c := range []struct {
		name string
		jobs []gen.ProductionJob
		want string
	}{
		{
			// The shape the shop asked for.
			name: "orders then colour",
			jobs: []gen.ProductionJob{
				plateJob("JOB-114556", "BLUE"),
				plateJob("JOB-114557", "BLUE"),
				plateJob("JOB-114558", "BLUE"),
			},
			want: "114556-114557-114558-BLUE",
		},
		{
			// One order with two planks on the same bed appears once. Repeating
			// it would make the name say four orders are on a bed of three.
			name: "an order contributing twice is named once",
			jobs: []gen.ProductionJob{
				plateJob("JOB-114666", "GOLD"),
				plateJob("JOB-114666-2", "GOLD"),
				plateJob("JOB-114670", "GOLD"),
			},
			want: "114666-114670-GOLD",
		},
		{
			// Ascending regardless of packing order, so the same bed always
			// produces the same name.
			name: "orders are sorted",
			jobs: []gen.ProductionJob{
				plateJob("JOB-114900", "RED"),
				plateJob("JOB-114100", "RED"),
			},
			want: "114100-114900-RED",
		},
		{
			// A space would read as a separator; the colour has to stay one
			// token or "BABY PINK" looks like two more orders.
			name: "a two-word colour stays one token",
			jobs: []gen.ProductionJob{plateJob("JOB-114556", "BABY PINK")},
			want: "114556-BABY_PINK",
		},
		{
			name: "colour is upper-cased",
			jobs: []gen.ProductionJob{plateJob("JOB-114556", "sky blue")},
			want: "114556-SKY_BLUE",
		},
		{
			// Not reachable under colour batching, but a hand-assembled batch
			// should still say what is on it rather than silently naming one.
			name: "a mixed bed names every colour",
			jobs: []gen.ProductionJob{
				plateJob("JOB-114556", "BLUE"),
				plateJob("JOB-114557", "GOLD"),
			},
			want: "114556-114557-BLUE-GOLD",
		},
		{
			name: "no colour recorded",
			jobs: []gen.ProductionJob{plateJob("JOB-114556")},
			want: "114556",
		},
		{
			// A job with no order behind it - a reprint, or one added by hand -
			// carries NextJobNumber's sequence value, which is the same "JOB-n"
			// shape as an order-derived number and cannot be told apart from it.
			// So the name gets the sequence value. That is still the job's own
			// traceable identifier, which is the point of the name.
			name: "a sequence-minted job number contributes its own digits",
			jobs: []gen.ProductionJob{plateJob("JOB-7", "BLUE")},
			want: "7-BLUE",
		},
		{
			// Nothing readable at all: the batch number stands in, because a
			// plate must still have a name.
			name: "an unreadable job number falls back to the batch",
			jobs: []gen.ProductionJob{plateJob("PJ-7", "BLUE")},
			want: "BATCH-0042-BLUE",
		},
		{
			name: "nothing to go on",
			jobs: []gen.ProductionJob{plateJob("PJ-7")},
			want: "BATCH-0042",
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := plateFileStem(c.jobs, "BATCH-0042"); got != c.want {
				t.Errorf("plateFileStem = %q, want %q", got, c.want)
			}
		})
	}
}

// The order number is the FIRST digit segment of a job number, which is the
// opposite of what orderNumberDigits does to an ORDER number. Reading the last
// one would name a plate after the per-order product index.
func TestOrderNumberFromJobNumber(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"JOB-114556", "114556"},
		{"JOB-114666-2", "114666"},
		{"JOB-114666-4", "114666"},
		{"  JOB-114556  ", "114556"},
		// NextJobNumber's sequential fallback has the same shape as an
		// order-derived number, so it reads as one. Nothing in the string can
		// separate them, and the digits identify the job either way.
		{"JOB-7", "7"},
		// Not a job number at all.
		{"T3DPS-114556", ""},
		{"", ""},
		{"JOB-", ""},
		{"JOB-ABC", ""},
	} {
		if got := orderNumberFromJobNumber(c.in); got != c.want {
			t.Errorf("orderNumberFromJobNumber(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
