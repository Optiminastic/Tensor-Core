package httpapi

import (
	"strings"
	"testing"

	"github.com/Optiminastic/tensor-core/internal/db/gen"
)

func mapped(key string, required bool) gen.ProductFieldMap {
	return gen.ProductFieldMap{PropertyKey: key, ScadVariable: "X", Required: required}
}

func carries(keys ...string) []observedPropertyResponse {
	out := make([]observedPropertyResponse, 0, len(keys))
	for _, k := range keys {
		out = append(out, observedPropertyResponse{Key: k, Label: k, Orders: 5})
	}
	return out
}

// A mapping that still matches its orders says nothing. A panel that spoke on
// every product would be a panel nobody reads.
func TestAMappingThatMatchesIsSilent(t *testing.T) {
	drift := driftOf(
		[]gen.ProductFieldMap{mapped("step 4 first name", true), mapped("step 5 second name", true)},
		carries("step 4 first name", "step 5 second name"),
	)
	if drift.Any() {
		t.Fatalf("spoke about a correct mapping: %+v", drift)
	}
}

// THE EXPENSIVE CASE. An optional field whose label was reworded renders a
// blank plank and exits 0, so the sentence has to say that rather than
// describing it the way it describes a held order.
//
// And a rename shows up as TWO facts - the old key stopped arriving, a new
// one started - which are one event. Reported as two, the reader has to join
// them up; reported as one, the sentence is the fix.
func TestAReworrdedOptionalFieldIsNamedAsPrintingBlank(t *testing.T) {
	var row skuReadiness
	noteDrift(&row, driftOf(
		[]gen.ProductFieldMap{mapped("first name", false)},
		[]observedPropertyResponse{
			{Key: "your first name", Label: "Your First Name", Orders: 12},
		},
	))
	if len(row.Problems) != 1 {
		t.Fatalf("problems %v", row.Problems)
	}
	said := row.Problems[0]
	for _, want := range []string{
		`"first name"`, `"Your First Name"`, "WITHOUT it", "never held", "Re-map",
	} {
		if !strings.Contains(said, want) {
			t.Fatalf("the sentence does not say %q: %s", want, said)
		}
	}
	// The new label is accounted for by that sentence, so it must not ALSO be
	// reported as an unmapped field somebody should go and look at.
	if len(row.Notes) != 0 {
		t.Fatalf("notes %v - the rename already explained that label", row.Notes)
	}
}

// A field that simply stopped being asked, with nothing that looks like its
// replacement. Then the honest sentence is the weaker one.
func TestWithNoCandidateTheWordingIsOnlySuspected(t *testing.T) {
	var row skuReadiness
	noteDrift(&row, driftOf(
		[]gen.ProductFieldMap{mapped("rose name", false)},
		carries("step 4 first name"),
	))
	if len(row.Problems) != 1 {
		t.Fatalf("problems %v", row.Problems)
	}
	if !strings.Contains(row.Problems[0], "may have changed") {
		t.Fatalf("claimed to know the new wording: %s", row.Problems[0])
	}
	if strings.Contains(row.Problems[0], "Re-map") {
		t.Fatalf("told somebody to re-map with nothing to re-map to: %s", row.Problems[0])
	}
}

// A REWORDED "second name" MUST NOT BE OFFERED AS THE NEW "first name". This
// is the swapped plank again, reached by following Tensor's own advice - so
// the ordinal rule has to hold here too, not just in the suggester.
func TestARenameIsNeverProposedAcrossAnOrdinal(t *testing.T) {
	drift := driftOf(
		[]gen.ProductFieldMap{mapped("step 4 first name", false)},
		[]observedPropertyResponse{
			{Key: "your second name", Label: "Your Second Name", Orders: 12},
		},
	)
	if got, ok := drift.Renamed["step 4 first name"]; ok {
		t.Fatalf("offered %q as the new wording of a FIRST name", got)
	}
}

// A required field that stopped matching holds every order instead. Still a
// problem, but a different one, and saying "prints blank" about it would be
// wrong.
func TestAReworkedRequiredFieldIsNamedAsHolding(t *testing.T) {
	var row skuReadiness
	noteDrift(&row, driftOf(
		[]gen.ProductFieldMap{mapped("first name", true)},
		carries("your first name"),
	))
	if len(row.Problems) != 1 {
		t.Fatalf("problems %v", row.Problems)
	}
	if strings.Contains(row.Problems[0], "WITHOUT it") {
		t.Fatalf("a held order was described as printing blank: %s", row.Problems[0])
	}
	if !strings.Contains(row.Problems[0], "held") {
		t.Fatalf("the sentence does not say the order is held: %s", row.Problems[0])
	}
}

// The two consequences are reported separately, because one sentence covering
// both would have to describe the milder one.
func TestTheTwoKindsOfStalenessAreSaidSeparately(t *testing.T) {
	var row skuReadiness
	noteDrift(&row, driftOf(
		[]gen.ProductFieldMap{mapped("first name", true), mapped("rose name", false)},
		carries("your first name"),
	))
	if len(row.Problems) != 2 {
		t.Fatalf("problems %v, want one for each kind", row.Problems)
	}
	// The one already printing blank planks is first, because a panel is read
	// from the top.
	if !strings.Contains(row.Problems[0], "WITHOUT it") {
		t.Fatalf("the expensive case is not first: %v", row.Problems)
	}
}

// A PRODUCT THAT HAS NOT SOLD YET HAS NO EVIDENCE EITHER WAY. Reporting its
// mapped fields as stale would put a red line against every product somebody
// is in the middle of setting up - which is the workflow this release is for.
func TestNoOrdersMeansNoVerdict(t *testing.T) {
	drift := driftOf([]gen.ProductFieldMap{mapped("name 1", true), mapped("name 2", false)}, nil)
	if drift.Any() {
		t.Fatalf("judged a product with no orders: %+v", drift)
	}
}

// A fixed row does not read the order at all, so it cannot have drifted from
// it. OUT_X=200 is the product's size, not the customer's answer.
func TestAFixedRowCannotDrift(t *testing.T) {
	two := "200"
	drift := driftOf(
		[]gen.ProductFieldMap{{PropertyKey: "", ScadVariable: "OUT_X", FixedValue: &two}},
		carries("step 4 first name"),
	)
	if len(drift.StaleRequired) != 0 || len(drift.StaleOptional) != 0 {
		t.Fatalf("a fixed row was called stale: %+v", drift)
	}
}

// An answer nothing reads is a NOTE. The commonest one is a field that was
// never for the model - and calling those faults would train people to skip
// the panel.
func TestAnUnmappedFieldIsANoteNotAFault(t *testing.T) {
	var row skuReadiness
	noteDrift(&row, driftOf(
		[]gen.ProductFieldMap{mapped("step 4 first name", true)},
		carries("step 4 first name", "gift message"),
	))
	if len(row.Problems) != 0 {
		t.Fatalf("problems %v - an unmapped field is not a fault", row.Problems)
	}
	if len(row.Notes) != 1 || !strings.Contains(row.Notes[0], `"gift message"`) {
		t.Fatalf("notes %v", row.Notes)
	}
}

// The WhatsApp number is on 211 of the shop's lines and is not for the model.
// It would otherwise be the one unmapped field on every single product.
func TestTheContactFieldsAreNotReportedAsUnmapped(t *testing.T) {
	drift := driftOf(
		[]gen.ProductFieldMap{mapped("step 4 first name", true)},
		[]observedPropertyResponse{
			{Key: "step 4 first name", Label: "STEP 4-First Name-", Orders: 1728},
			{Key: "step 6 whatsapp number", Label: "STEP 6 - WhatsApp Number", Orders: 211},
			{Key: "whatsapp number to send demo video", Label: "WhatsApp number to send demo video", Orders: 6},
		},
	)
	if len(drift.Unmapped) != 0 {
		t.Fatalf("reported contact fields as unmapped: %v", drift.Unmapped)
	}
}

// A sentence listing eleven labels is one nobody finishes. The first few
// carry the information: that there are some, and what they look like.
func TestALongListIsCutWithACount(t *testing.T) {
	var many []string
	for _, n := range []string{"a", "b", "c", "d", "e", "f"} {
		many = append(many, n)
	}
	said := unmappedWording(many)
	if !strings.Contains(said, "and 3 others") {
		t.Fatalf("the tail is not counted: %s", said)
	}
	if strings.Contains(said, `"and 3 others"`) {
		t.Fatalf("the count was quoted as if it were a field name: %s", said)
	}
	if strings.Contains(said, `"d"`) {
		t.Fatalf("listed past the cut: %s", said)
	}
}
