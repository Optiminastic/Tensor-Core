package httpapi

// What a customer may change about their own order.
//
// The dangerous direction is permissiveness: a field that should not have been
// editable becomes a different product made at the wrong price, and nobody
// finds out until it is on a plate.

import (
	"strings"
	"testing"

	"github.com/Optiminastic/tensor-core/internal/config"
	"github.com/Optiminastic/tensor-core/internal/db/gen"
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

// The storefront writes the same field under longer labels, and the renderer
// has always found them - lookupRaw matches a label that CONTAINS the key,
// which is what makes "First Name on Plank" render as a first name. This side
// compared labels for equality instead, so a Dual Name Plank with Light showed
// the customer both of their names and gave them an input box for neither.
//
// Both rules are now personalise.MatchesKey. If this test fails, the page has
// gone read-only for a whole product family again.
func TestALongerLabelIsStillTheSameField(t *testing.T) {
	for _, label := range []string{
		"First Name on Plank",  // DNPLB - the bug this test exists for
		"Second Name on Plank", // DNPLB
		"Name on Plank",        // DNP-Doc, the desk plank
		"STEP 4-First Name-",   // DNP, the ordinary plank
		"STEP 5-Second Name",   // DNP
	} {
		if !editableProperty(label) {
			t.Errorf("%q is a name the customer typed and must be editable", label)
		}
	}

	// Containment must not drag anything else in with it. A heart count picks
	// a different model, and a contact detail is not printed at all.
	for _, label := range []string{
		"STEP 3-", "Hearts", "1 RED HEART", "3 RED HEART",
		"STEP 6 - WhatsApp Number", "Base Colour", "Text Colour",
	} {
		if editableProperty(label) {
			t.Errorf("%q must not have become editable", label)
		}
	}
}

func TestAnUneditableFieldIsIgnoredEvenIfSubmitted(t *testing.T) {
	// The page does not offer it, but the request is a POST a stranger can
	// shape however they like. The server decides, not the form.
	current := props("STEP 4 - First Name", "AARAV", "Hearts", "2")
	updated, changed, err := applyCustomerEdit(current, map[string]string{
		"STEP 4 - First Name": "ARJUN",
		"Hearts":              "99",
	}, plankRules())
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
	updated, _, err := applyCustomerEdit(current, map[string]string{"Right Name": "C"}, plankRules())
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
	_, _, err := applyCustomerEdit(props("Left Name", "A"), map[string]string{"Left Name": "   "}, plankRules())
	if err == nil {
		t.Fatal("an empty name was accepted")
	}
}

func TestATooLongNameIsRefusedInTheFormNotInTheRender(t *testing.T) {
	// The plank caps at twelve. Finding out in a render that fails thirty
	// seconds later gives an error naming neither the field nor the reason.
	_, _, err := applyCustomerEdit(props("Left Name", "A"),
		map[string]string{"Left Name": "ABCDEFGHIJKLMNOP"}, plankRules())
	if err == nil {
		t.Fatal("a sixteen-character name was accepted")
	}
}

func TestLengthIsCountedInCharactersNotBytes(t *testing.T) {
	// "आरव" is three characters and nine bytes. Counting bytes would refuse
	// values that fit perfectly well.
	//
	// Asserted on a FREE-TEXT field rather than a name, because a name is now
	// English letters only - the font on the machine has no glyph for another
	// script, so a Devanagari name would come out a row of hollow boxes. The
	// rune-counting this proves still matters: every other field a customer
	// can edit takes whatever they type.
	open := fieldRules{open: true, maxRunes: maxFreeTextRunes}
	updated, changed, err := applyCustomerEdit(props("Sub Text", "A"),
		map[string]string{"Sub Text": "आरवकुमार"}, open)
	if err != nil {
		t.Fatalf("a short Devanagari value was refused: %v", err)
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
		map[string]string{"Left Name": "AARAV"}, plankRules())
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
		map[string]string{"Left Name": " AARAV \n KUMAR "}, plankRules())
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
	updated, _, err := applyCustomerEdit(current, map[string]string{"_gift_wrap_id": "hacked"}, plankRules())
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

// --- one job per unit ---------------------------------------------------------

func TestThreePlanksBecomeThreeJobsOnlyWhenTheCustomerWillBeAsked(t *testing.T) {
	// The whole point of splitting: three planks ordered together are three
	// things to name, and they cannot have different names while quantity is
	// an integer on one row carrying one set of values.
	on := &Server{cfg: config.Settings{PersonalisationCheckEnabled: true}}
	off := &Server{cfg: config.Settings{PersonalisationCheckEnabled: false}}
	plank := production.LineItem{PersonalisationRequired: true}

	if !on.splitsPerUnit(plank, 3) {
		t.Error("three personalised planks should become three jobs")
	}
	// With the check off, job creation must behave exactly as it always did.
	// This is the one change that alters the queue's shape for every order.
	if off.splitsPerUnit(plank, 3) {
		t.Error("the queue's shape changed with the feature switched off")
	}
}

func TestAPlainLineIsNeverSplit(t *testing.T) {
	// Nothing on it is made to instructions, so three of them are three
	// identical things and one job of quantity three is the honest shape.
	on := &Server{cfg: config.Settings{PersonalisationCheckEnabled: true}}
	if on.splitsPerUnit(production.LineItem{}, 5) {
		t.Error("a plain line was split per unit")
	}
}

func TestASingleUnitIsNeverSplit(t *testing.T) {
	on := &Server{cfg: config.Settings{PersonalisationCheckEnabled: true}}
	if on.splitsPerUnit(production.LineItem{PersonalisationRequired: true}, 1) {
		t.Error("a single unit was split")
	}
}

func TestABulkLineIsLeftWhole(t *testing.T) {
	// Fifty units would be half an hour of OpenSCAD before anybody is
	// messaged, and nobody edits fifty names on a phone inside thirty
	// minutes.
	on := &Server{cfg: config.Settings{PersonalisationCheckEnabled: true}}
	big := production.LineItem{PersonalisationRequired: true}
	if on.splitsPerUnit(big, maxPerUnitSplit+1) {
		t.Errorf("a line of %d was split per unit", maxPerUnitSplit+1)
	}
	if !on.splitsPerUnit(big, maxPerUnitSplit) {
		t.Errorf("a line of exactly %d should still split", maxPerUnitSplit)
	}
}

func TestRepeatsOfOneProductAreNumberedForTheCustomer(t *testing.T) {
	// Three identical headings with different names under them is a page
	// nobody can check. Counted from the page rather than from the line's
	// quantity, which reads 1 on each of the three jobs.
	items := []publicItemResponse{
		{ProductName: "Dual Name Plank"},
		{ProductName: "Soulmate Combo"},
		{ProductName: "Dual Name Plank"},
		{ProductName: "Dual Name Plank"},
	}
	numberSiblings(items)

	if items[0].UnitIndex != 1 || items[0].UnitCount != 3 {
		t.Errorf("first plank = %d of %d, want 1 of 3", items[0].UnitIndex, items[0].UnitCount)
	}
	if items[3].UnitIndex != 3 {
		t.Errorf("last plank = %d, want 3", items[3].UnitIndex)
	}
	// The only Soulmate says nothing rather than "1 of 1".
	if items[1].UnitIndex != 0 || items[1].UnitCount != 0 {
		t.Errorf("lone item = %d of %d, want unnumbered", items[1].UnitIndex, items[1].UnitCount)
	}
}

func TestTheThreePiecesOfOneComboAreNotThreeCombos(t *testing.T) {
	// A live order proved this one. A Soulmate Combo prints from three design
	// files - a plank, a rose and a keychain - so one combo arrives as three
	// jobs sharing a product name. Counting those as repeats told the
	// customer they had bought three of something they bought one of.
	items := []publicItemResponse{
		{ProductName: "Soulmate Combo", Part: ""},
		{ProductName: "Soulmate Combo", Part: "Rose"},
		{ProductName: "Soulmate Combo", Part: "Keychain"},
	}
	numberSiblings(items)
	for i, item := range items {
		if item.UnitCount != 0 {
			t.Errorf("piece %d was numbered %d of %d - one combo is not three",
				i, item.UnitIndex, item.UnitCount)
		}
	}
}

func TestThreeCombosAreNumberedWithinEachPiece(t *testing.T) {
	// Three combos are three planks, three roses and three keychains. Each
	// set is numbered 1-3 so the customer can tell which rose goes with which
	// plank.
	items := []publicItemResponse{
		{ProductName: "Soulmate Combo", Part: ""},
		{ProductName: "Soulmate Combo", Part: "Rose"},
		{ProductName: "Soulmate Combo", Part: ""},
		{ProductName: "Soulmate Combo", Part: "Rose"},
	}
	numberSiblings(items)
	if items[0].UnitIndex != 1 || items[0].UnitCount != 2 {
		t.Errorf("first plank = %d of %d, want 1 of 2", items[0].UnitIndex, items[0].UnitCount)
	}
	if items[1].UnitIndex != 1 || items[1].UnitCount != 2 {
		t.Errorf("first rose = %d of %d, want 1 of 2", items[1].UnitIndex, items[1].UnitCount)
	}
	if items[2].UnitIndex != 2 {
		t.Errorf("second plank = %d, want 2", items[2].UnitIndex)
	}
}

func TestThePartIsNamedInWordsNotIdentifiers(t *testing.T) {
	// The registry stores these as identifiers - the floor's words and the
	// template's. A customer reading "Heart_keychain" is being shown a
	// database value.
	cases := map[string]string{
		"heart_keychain": "Heart keychain",
		"rose":           "Rose",
		"dnp_plank":      "Dnp plank",
		"body":           "",
		"":               "",
	}
	for role, want := range cases {
		if got := partLabel(gen.ProductionJob{PartRole: role}); got != want {
			t.Errorf("partLabel(%q) = %q, want %q", role, got, want)
		}
	}
}

// plankRules is the rule set every test above was written against: no registry
// entry, so the hardcoded plank allow-list decides, with the plank's own
// twelve-rune cap.
func plankRules() fieldRules {
	return fieldRules{maxRunes: maxCustomerFieldRunes}
}

// The products Tensor does NOT render, which is where the allow-list's whole
// reason stops applying: there is no template to re-select and no render to
// feed, a person reads these values and makes the thing, and the customer
// correcting their own spelling is the entire point of the page.
func TestAProductNobodyRendersEditsItsOwnFields(t *testing.T) {
	open := fieldRules{open: true, maxRunes: maxFreeTextRunes}

	// Real labels, off real orders: a Couple's Date Plank, a Bone Tag, a
	// DUOSNAP. Not one of them is in editableKeys, and every one of them was
	// read-only on the customer's page.
	for _, label := range []string{"name-left", "name-right", "date", "pet-name", "TOP NAME"} {
		if !open.canEdit(label, "AMEER") {
			t.Errorf("%q should be editable on a product nobody renders", label)
		}
		if plankRules().canEdit(label, "AMEER") {
			t.Errorf("%q must stay locked where the value feeds a render", label)
		}
	}
}

func TestAContactDetailIsNeverEditableAndNeverShown(t *testing.T) {
	open := fieldRules{open: true, maxRunes: maxFreeTextRunes}

	// The DUOSNAP asks for one so the shop can reach the buyer. This page is a
	// token in a link, and what a guessed one may expose was decided when it
	// was built: no contact details.
	for _, label := range []string{"Whatsapp Number", "Phone", "Mobile Number", "Email"} {
		if !contactProperty(label) {
			t.Errorf("%q should be recognised as a contact detail", label)
		}
		if open.canEdit(label, "9989259369") {
			t.Errorf("%q must not be editable", label)
		}
	}

	// MATCHED AS WHOLE WORDS. A Bone Tag asks for "number-1" and engraves it
	// on the tag, because a pet tag carries its owner's phone number - that
	// one IS the personalisation, and a substring test on "number" would have
	// hidden it.
	if contactProperty("number-1") {
		t.Error(`"number-1" is engraved on a bone tag, not a way of reaching anybody`)
	}
	if !open.canEdit("number-1", "8197005533") {
		t.Error(`"number-1" should be editable`)
	}
}

func TestAnUploadedPhotoIsShownButNotRetyped(t *testing.T) {
	open := fieldRules{open: true, maxRunes: maxFreeTextRunes}
	const photo = "https://option.nyc3.digitaloceanspaces.com/uploads/96492/file-1791813733.jpg"
	if open.canEdit("PHOTO", photo) {
		t.Error("a link to the customer's upload must not become a text box")
	}
	if !open.canEdit("PHOTO", "a caption") {
		t.Error("the rule is about the value being a link, not about the label")
	}
}

func TestTheLengthLimitFollowsTheProduct(t *testing.T) {
	// Twelve is the PLANK's: a longer name does not fit the model. A date is
	// ten characters and a phone number ten digits, both close enough to be
	// refused by accident on a product with no model to overflow.
	current := props("date", "10-07-2016")
	if _, _, err := applyCustomerEdit(current,
		map[string]string{"date": "10-07-2016"},
		fieldRules{open: true, maxRunes: maxFreeTextRunes}); err != nil {
		t.Errorf("a ten-character date was refused: %v", err)
	}

	long := strings.Repeat("A", maxFreeTextRunes+1)
	_, _, err := applyCustomerEdit(props("name-left", "A"),
		map[string]string{"name-left": long},
		fieldRules{open: true, maxRunes: maxFreeTextRunes})
	if err == nil {
		t.Error("a value past the free-text limit should still be refused")
	}
	if err != nil && !strings.Contains(err.Error(), "60") {
		t.Errorf("the refusal should name the limit it applied, got %v", err)
	}
}

// Which items get a picture on the customer's page, decided by the same
// predicate job creation uses. Pure half only - the registry half needs a
// database - which is enough, because every product below is one the registry
// does not describe.
func TestOnlyTheProductsTensorRendersGetAPreview(t *testing.T) {
	for _, tc := range []struct {
		sku, product string
		renders      bool
	}{
		// Rendered: a picture, and the fields feed it.
		{"DNP-BLU", "Dual Name Plank - BLUE / NO LIGHT", true},
		{"SC-WHT", "Soulmate COMBO - WHITE", true},
		{"DNPF-1", "DUAL NAME & PHOTO FRAME - GOLD", true},
		// A plank whose variant lost its SKU in the catalogue - matched on the
		// product name, which is why that fallback exists.
		{"", "Dual Name Plank - GREEN / I NEED LIGHT", true},

		// NOT rendered. Every one of these is a real line off a real order,
		// and each showed "Building your preview..." forever.
		{"", "3D Name Plank - Large - 4 cm height / BLUE / GOLD", false},
		{"", "Couple's Date Plank - BLUE / BLUE / YELLOW", false},
		{"DT-BoneTag-8", "Bone Tag - Black / Gold", false},
		{"T3DPS-DUO-4", "DUOSNAP - Black", false},
		{"SNP-PNK", "Single Name Plank - Pink / NO LIGHT", false},
		{"3DMS01-6", "Night Lamp", false},
		// KNOWN WRONG, and left wrong on purpose so it is visible. "DNP-Doc"
		// splits to the segment "DNP", so Tensor files a Desk Name Plank as a
		// Dual Name Plank. It is a different product - it asks for "NAME on
		// PLANK", "BASE COLOUR" and "TEXT COLOUR", not two names - so its
		// render never happened: JOB-115098 sat at stl_missing with no model
		// and was made by hand. Its card will say "Building your preview..."
		// as it always has. Fixing the classification is a separate change
		// from showing fields-only cards, and guessing at it here would be
		// changing which path a live product takes.
		{"DNP-Doc", "Doctor - Desk Name Plank", true},
	} {
		t.Run(tc.product, func(t *testing.T) {
			if got := IsGeneratedProduct(tc.sku, tc.product); got != tc.renders {
				t.Errorf("IsGeneratedProduct(%q, %q) = %v, want %v",
					tc.sku, tc.product, got, tc.renders)
			}
		})
	}
}
