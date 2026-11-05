package httpapi

import "testing"

// The Family Crossword Frame, as its orders actually arrived.
//
// It sells two to six names, so the later boxes are on a minority of lines.
// Every field defaulted to holding the job, and the four-name order failed
// with `this order carries no "name 5"` - a product that could only ever make
// the six-name version of itself.
func fcfObserved() []observedPropertyResponse {
	return []observedPropertyResponse{
		{Key: "name 1", Orders: 3},
		{Key: "name 2", Orders: 3},
		{Key: "name 3", Orders: 3},
		{Key: "name 4", Orders: 3},
		{Key: "name 5", Orders: 1},
		{Key: "name 6", Orders: 1},
		{Key: "names count", Orders: 2},
	}
}

func TestAFieldTheOrdersDisagreeAboutIsProposedOptional(t *testing.T) {
	observed := fcfObserved()
	most := mostSeenOrders(observed)
	if most != 3 {
		t.Fatalf("most seen = %d, want 3 - the denominator the rest are judged against", most)
	}

	for _, p := range observed {
		optional := absentFromSomeOrders(p, most)
		// On every line: its absence is a blank, not a smaller product.
		if p.Orders == most && optional {
			t.Errorf("%q is on every line and must still hold the job", p.Key)
		}
		// On some lines only: holding the job for it refuses the orders that
		// legitimately do not carry it.
		if p.Orders < most && !optional {
			t.Errorf("%q is on %d of %d lines and should be proposed optional",
				p.Key, p.Orders, most)
		}
	}
}

// A product whose every field is on every line - the plank - must be
// untouched by this, or a blank name stops holding the job and prints scrap.
func TestAProductWhoseFieldsAlwaysArriveIsUnchanged(t *testing.T) {
	observed := []observedPropertyResponse{
		{Key: "first name", Orders: 1768},
		{Key: "second name", Orders: 1768},
	}
	most := mostSeenOrders(observed)
	for _, p := range observed {
		if absentFromSomeOrders(p, most) {
			t.Fatalf("%q is on all %d lines and must stay required", p.Key, most)
		}
	}
}

// No orders yet is no evidence, and guessing optional from nothing would ship
// a product whose required fields never hold anything.
func TestNoOrdersProposesNothingOptional(t *testing.T) {
	if got := mostSeenOrders(nil); got != 0 {
		t.Fatalf("most seen of nothing = %d, want 0", got)
	}
	if absentFromSomeOrders(observedPropertyResponse{Key: "name 1", Orders: 0}, 0) {
		t.Fatal("with no orders observed, nothing should be called optional")
	}
}
