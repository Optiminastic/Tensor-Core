package httpapi

// What a customer may change about their own order.
//
// The dangerous direction is permissiveness: a field that should not have been
// editable becomes a different product made at the wrong price, and nobody
// finds out until it is on a plate.

import (
	"testing"

	"github.com/Optiminastic/tensor-core/internal/production"
)

func props(pairs ...string) []production.LineProp {
	out := make([]production.LineProp, 0, len(pairs)/2)
	for i := 0; i+1 < len(pairs); i += 2 {
		out = append(out, production.LineProp{Name: pairs[i], Value: pairs[i+1]})
	}
	return out
}

func TestANameIsEditableAndAHeartCountIsNot(t *testing.T) {
	// The heart count is not a parameter - it selects a different .scad with
	// different margins and plate thickness. Editing it would quietly change
	// which product is made.
	if !editableProperty("STEP 4 - First Name") {
		t.Error("the first name should be editable")
	}
	if !editableProperty("Rose Name") {
		t.Error("the rose name should be editable")
	}
	if editableProperty("Hearts") {
		t.Error("the heart count must never be editable")
	}
	if editableProperty("STEP 3") {
		t.Error("the positional hearts question must never be editable")
	}
	// Anything unrecognised stays read-only. An allow-list, not a deny-list.
	if editableProperty("Gift wrap") {
		t.Error("an unrecognised field must not be editable")
	}
}

func TestAnUneditableFieldIsIgnoredEvenIfSubmitted(t *testing.T) {
	// The page does not offer it, but the request is a POST a stranger can
	// shape however they like. The server decides, not the form.
	current := props("STEP 4 - First Name", "AARAV", "Hearts", "2")
	updated, changed, err := applyCustomerEdit(current, map[string]string{
		"STEP 4 - First Name": "ARJUN",
		"Hearts":              "99",
	})
	if err != nil {
		t.Fatalf("edit: %v", err)
	}
	if !changed {
		t.Error("the name change was not noticed")
	}
	if updated[0].Value != "ARJUN" {
		t.Errorf("name = %q, want the edit applied", updated[0].Value)
	}
	if updated[1].Value != "2" {
		t.Errorf("hearts = %q, want the original - it is not editable", updated[1].Value)
	}
}

func TestTheAnswerOrderIsKept(t *testing.T) {
	// It is the order the customer answered the questions in, and the page
	// renders it that way. Rebuilding from the submitted map would reorder it
	// by whatever the browser happened to send.
	current := props("Left Name", "A", "Right Name", "B", "Hearts", "1")
	updated, _, err := applyCustomerEdit(current, map[string]string{"Right Name": "C"})
	if err != nil {
		t.Fatalf("edit: %v", err)
	}
	want := []string{"Left Name", "Right Name", "Hearts"}
	for i, label := range want {
		if updated[i].Name != label {
			t.Errorf("position %d = %q, want %q", i, updated[i].Name, label)
		}
	}
}

func TestAnEmptyValueIsRefused(t *testing.T) {
	// Meta's template would refuse it, the model would engrave nothing, and
	// the customer would not know until it arrived blank.
	_, _, err := applyCustomerEdit(props("Left Name", "A"), map[string]string{"Left Name": "   "})
	if err == nil {
		t.Fatal("an empty name was accepted")
	}
}

func TestATooLongNameIsRefusedInTheFormNotInTheRender(t *testing.T) {
	// The plank caps at twelve. Finding out in a render that fails thirty
	// seconds later gives an error naming neither the field nor the reason.
	_, _, err := applyCustomerEdit(props("Left Name", "A"),
		map[string]string{"Left Name": "ABCDEFGHIJKLMNOP"})
	if err == nil {
		t.Fatal("a sixteen-character name was accepted")
	}
}

func TestLengthIsCountedInCharactersNotBytes(t *testing.T) {
	// "आरव" is three characters and nine bytes. Counting bytes would refuse
	// names that fit perfectly well.
	updated, changed, err := applyCustomerEdit(props("Left Name", "A"),
		map[string]string{"Left Name": "आरवकुमार"})
	if err != nil {
		t.Fatalf("a short Devanagari name was refused: %v", err)
	}
	if !changed || updated[0].Value != "आरवकुमार" {
		t.Errorf("value = %q", updated[0].Value)
	}
}

func TestSubmittingTheSameValueChangesNothing(t *testing.T) {
	// An edit that changes nothing must not count as a change: it would stamp
	// the original snapshot and every job a customer merely opened would
	// read as "Changed" on the floor's queue.
	_, changed, err := applyCustomerEdit(props("Left Name", "AARAV"),
		map[string]string{"Left Name": "AARAV"})
	if err != nil {
		t.Fatalf("edit: %v", err)
	}
	if changed {
		t.Error("resubmitting the same value counted as a change")
	}
}

func TestWhitespaceIsFlattenedBeforeItIsCompared(t *testing.T) {
	// A pasted name can carry a newline. Four or more spaces or a tab would
	// also be refused downstream, and more to the point it is not what the
	// customer meant to type.
	updated, _, err := applyCustomerEdit(props("Left Name", "A"),
		map[string]string{"Left Name": " AARAV \n KUMAR "})
	if err != nil {
		t.Fatalf("edit: %v", err)
	}
	if updated[0].Value != "AARAV KUMAR" {
		t.Errorf("value = %q, want the whitespace flattened", updated[0].Value)
	}
}

func TestHiddenShopifyPlumbingIsNeverTouched(t *testing.T) {
	// `_`-prefixed keys are the storefront's own state, not something a
	// customer typed.
	current := props("_gift_wrap_id", "abc", "Left Name", "A")
	updated, _, err := applyCustomerEdit(current, map[string]string{"_gift_wrap_id": "hacked"})
	if err != nil {
		t.Fatalf("edit: %v", err)
	}
	if updated[0].Value != "abc" {
		t.Errorf("hidden field = %q, want untouched", updated[0].Value)
	}
}

func TestTheColourSetFollowsTheChosenColour(t *testing.T) {
	// A job's colours decide which bed it can share, so they have to move with
	// the personalisation colour or the plate would be grouped on the old one.
	if got := string(coloursJSON("gold")); got != `["GOLD"]` {
		t.Errorf("colours = %s", got)
	}
	if got := string(coloursJSON("  ")); got != "[]" {
		t.Errorf("no colour = %s, want an empty list", got)
	}
}
