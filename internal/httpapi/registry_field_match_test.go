package httpapi

// Every label in this file is one the shop's own orders carry, read out of
// the orders table on 2026-11-03:
//
//	step 5-second name            1729 lines
//	step 4-first name-            1728
//	step 3-                       1713
//	step 6 - whatsapp number       211
//	charm                           87
//	name                            43
//	first name on plank             35
//	second name on plank            35
//	red heart                       34
//	step 3 -second name             30
//	step 2 - first name-            30
//	step 4 - name on 3d rose        27
//	step 5 - name on heart keychain 27
//	left name / right name          10 each
//	text colour / base colour        3 each
//
// A test written against invented labels would pass and tell us nothing: the
// whole difficulty here is that this shop names one field five ways.

import (
	"testing"

	"github.com/Optiminastic/tensor-core/internal/personalise"
)

// The pairing the shop needs most, in both of the wordings it sells under.
func TestTheTwoNamesPairWithTheirOwnSide(t *testing.T) {
	for _, tc := range []struct {
		variable, key string
		want          matchStrength
	}{
		{"NAME_L", "step 4 first name", strengthLikely},
		{"NAME_R", "step 5 second name", strengthLikely},
		{"NAME_L", "first name on plank", strengthLikely},
		{"NAME_R", "second name on plank", strengthLikely},
		{"name1", "name 1", strengthLikely},
		{"name2", "name 2", strengthLikely},
		{"NAME_L", "left name", strengthLikely},
		{"NAME_R", "right name", strengthLikely},
		{"TOP_NAME", "step 4 first name", strengthLikely},
		{"ROSE_NAME", "step 4 name on 3d rose", strengthLikely},
		{"TEXT_COLOR", "text colour", strengthLikely},
	} {
		_, got, ok := scoreMatch(wordsOfVariable(tc.variable), wordsOfPropertyKey(tc.key))
		if !ok {
			t.Fatalf("%s did not pair with %q at all", tc.variable, tc.key)
		}
		if got != tc.want {
			t.Fatalf("%s vs %q: %s, want %s", tc.variable, tc.key, got, tc.want)
		}
	}
}

// THE ONE THAT MATTERS. A plank rendered with its names swapped renders,
// slices, prints and exits 0 - there is no downstream check that can catch
// it, so this has to be a refusal here rather than a lower score.
func TestANameNeverPairsWithTheOtherOne(t *testing.T) {
	for _, tc := range [][2]string{
		{"NAME_L", "step 5 second name"},
		{"NAME_R", "step 4 first name"},
		{"NAME_L", "second name on plank"},
		{"NAME_R", "first name on plank"},
		{"name1", "name 2"},
		{"name2", "name 1"},
		{"NAME_L", "right name"},
		{"NAME_R", "left name"},
		{"TOP_NAME", "bottom name"},
		{"BOTTOM_NAME", "top name"},
	} {
		if _, _, ok := scoreMatch(wordsOfVariable(tc[0]), wordsOfPropertyKey(tc[1])); ok {
			t.Fatalf("%s was offered %q - a swapped plank prints and exits 0", tc[0], tc[1])
		}
	}
}

// The step number is the storefront's form layout, not the field. Reading it
// as an ordinal would make "STEP 4-First Name-" disagree with every variable
// in every file - and it is the shop's highest-volume label, on 1,728 lines.
func TestTheStepNumberIsNotAnOrdinal(t *testing.T) {
	got := wordsOfPropertyKey("step 4 first name")
	if got.Ordinal != 1 {
		t.Fatalf("ordinal %d, want 1 (from \"first\", not 4)", got.Ordinal)
	}
	// "text" rather than "name": the two are one word here, because the Royal
	// diya holder declares TEXT and its orders carry "Name" - see
	// wordSynonyms.
	if len(got.Words) != 1 || got.Words[0] != "text" {
		t.Fatalf("words %v, want [text]", got.Words)
	}
}

// Four naming conventions by three authors, all of which exist in the shop's
// files today.
func TestEveryVariableConventionSplits(t *testing.T) {
	for _, tc := range []struct {
		name string
		want []string
	}{
		// NAME_L and name1 reduce to "text", not "name" - see wordSynonyms.
		// The L and the 1 are the ordinal and are held separately.
		{"NAME_L", []string{"text"}},
		{"name1", []string{"text"}},
		{"sideHearts", []string{"side", "heart"}},
		{"TEXT_COLOR", []string{"text", "colour"}},
		{"plank_length", []string{"length"}},
	} {
		got := wordsOfVariable(tc.name)
		if len(got.Words) != len(tc.want) {
			t.Fatalf("%s: words %v, want %v", tc.name, got.Words, tc.want)
		}
		for i := range tc.want {
			if got.Words[i] != tc.want[i] {
				t.Fatalf("%s: words %v, want %v", tc.name, got.Words, tc.want)
			}
		}
	}
}

// Two fields of the same shape on one product. The rose and the keychain are
// both "a name on a thing", and the thing is the only word that separates
// them.
func TestTheRoseAndTheKeychainDoNotTakeEachOthersName(t *testing.T) {
	params := []personalise.Param{
		{Name: "ROSE_NAME", Type: personalise.ParamString},
		{Name: "KEYCHAIN_NAME", Type: personalise.ParamString},
	}
	observed := []observedPropertyResponse{
		{Key: "step 4 name on 3d rose", Label: "STEP 4 - Name on 3D Rose", Orders: 27},
		{Key: "step 5 name on heart keychain", Label: "STEP 5 - Name on Heart Keychain", Orders: 27},
	}
	got := byVariable(suggestFieldMaps(params, observed, 0))

	if got["ROSE_NAME"].PropertyKey != "step 4 name on 3d rose" {
		t.Fatalf("ROSE_NAME took %q", got["ROSE_NAME"].PropertyKey)
	}
	if got["KEYCHAIN_NAME"].PropertyKey != "step 5 name on heart keychain" {
		t.Fatalf("KEYCHAIN_NAME took %q", got["KEYCHAIN_NAME"].PropertyKey)
	}
}

// One key is never proposed for two variables. Without the assignment pass
// both names would take whichever key scored best for each in isolation.
func TestNoKeyIsProposedTwice(t *testing.T) {
	params := []personalise.Param{
		{Name: "NAME_L", Type: personalise.ParamString},
		{Name: "NAME_R", Type: personalise.ParamString},
		{Name: "NAME_3", Type: personalise.ParamString},
	}
	observed := []observedPropertyResponse{
		{Key: "step 4 first name", Label: "STEP 4-First Name-", Orders: 1728},
		{Key: "step 5 second name", Label: "STEP 5-Second Name", Orders: 1729},
	}
	seen := map[string]string{}
	for _, row := range suggestFieldMaps(params, observed, 0) {
		if row.PropertyKey == "" {
			continue
		}
		if had, ok := seen[row.PropertyKey]; ok {
			t.Fatalf("%q proposed for both %s and %s", row.PropertyKey, had, row.ScadVariable)
		}
		seen[row.PropertyKey] = row.ScadVariable
	}
}

// The heart option is why ValueCount exists: the storefront sells "1 RED
// HEART", never "1", and renames the option freely.
//
// A one-word variable, because side_hearts would be filtered out - it matches
// only one of its two words against this label, and see
// TestHalfAMatchIsNotProposed for why half a match is no match. The kind rule
// under test here is the same either way.
func TestANumericVariableWhoseAnswersAreWordsIsACount(t *testing.T) {
	params := []personalise.Param{
		{Name: "hearts", Type: personalise.ParamNumber, Default: "1"},
	}
	observed := []observedPropertyResponse{
		{Key: "red heart", Label: "RED HEART", Sample: "1 RED HEART", Orders: 34},
	}
	got := byVariable(suggestFieldMaps(params, observed, 0))
	row, ok := got["hearts"]
	if !ok {
		t.Fatal("hearts was not paired with the heart option")
	}
	if row.ValueType != string(personalise.ValueCount) {
		t.Fatalf("value type %q, want count - a string reaches OpenSCAD as "+
			`hearts="1 RED HEART", which is not a number`, row.ValueType)
	}
}

// A numeric variable whose answers really are numbers stays a number.
func TestANumberStaysANumber(t *testing.T) {
	params := []personalise.Param{{Name: "HEART_NUM", Type: personalise.ParamNumber}}
	observed := []observedPropertyResponse{
		{Key: "heart num", Label: "Heart number", Sample: "2", Orders: 4},
	}
	got := byVariable(suggestFieldMaps(params, observed, 0))
	if got["HEART_NUM"].ValueType != string(personalise.ValueNumber) {
		t.Fatalf("value type %q, want number", got["HEART_NUM"].ValueType)
	}
}

// The renderer sets PART itself, once per coloured piece. A mapping that
// overwrote it would draw the same piece every pass, and the 3MF would hold
// overlapping copies of the model in different colours - which slices and
// prints.
func TestThePiecesVariableIsNeverOffered(t *testing.T) {
	params := []personalise.Param{
		{Name: "PART", Type: personalise.ParamString, Default: `"all"`},
		{Name: "export_part", Type: personalise.ParamString, Default: `"all"`},
		{Name: "MAX_CHARS", Type: personalise.ParamNumber, Default: "20"},
		{Name: "NAME_L", Type: personalise.ParamString},
	}
	observed := []observedPropertyResponse{
		{Key: "part", Label: "Part", Orders: 3},
		{Key: "step 4 first name", Label: "STEP 4-First Name-", Orders: 1728},
	}
	for _, row := range suggestFieldMaps(params, observed, 0) {
		switch row.ScadVariable {
		case "PART", "export_part":
			t.Fatalf("%s was offered for mapping", row.ScadVariable)
		case "MAX_CHARS":
			t.Fatal("MAX_CHARS was offered; it is the file describing its own text box")
		}
	}
}

// The file's own word beats the guess, and claims its key before any guess is
// scored against it.
func TestWhatTheFileSaysWins(t *testing.T) {
	params := []personalise.Param{
		{
			Name: "NAME_L", Type: personalise.ParamString,
			Note: "@from STEP 5-Second Name @max 18",
		},
		{Name: "NAME_R", Type: personalise.ParamString},
	}
	observed := []observedPropertyResponse{
		{Key: "step 4 first name", Label: "STEP 4-First Name-", Orders: 1728},
		{Key: "step 5 second name", Label: "STEP 5-Second Name", Orders: 1729},
	}
	got := byVariable(suggestFieldMaps(params, observed, 0))

	// Deliberately the "wrong" pairing by the word rules - an author may have
	// built the storefront fields in the other order, and they are the one
	// who knows.
	if got["NAME_L"].PropertyKey != "step 5 second name" {
		t.Fatalf("NAME_L took %q, not what the file said", got["NAME_L"].PropertyKey)
	}
	if got["NAME_L"].Source != sourceFile || got["NAME_L"].Strength != strengthStated {
		t.Fatalf("source %q strength %q", got["NAME_L"].Source, got["NAME_L"].Strength)
	}
	if got["NAME_L"].MaxRunes == nil || *got["NAME_L"].MaxRunes != 18 {
		t.Fatalf("limit %v, want 18", got["NAME_L"].MaxRunes)
	}
	// And the key it took is gone, so the guess for NAME_R cannot also take
	// it - which would be the swapped plank again, by another route.
	if got["NAME_R"].PropertyKey == "step 5 second name" {
		t.Fatal("NAME_R was offered a key the file had already claimed")
	}
}

// @fixed with no value means the value already in the file, which is how a
// plank states OUT_X = 200 once. Without it a plank renders at its natural
// size - 377mm on one real order.
func TestAFixedVariableCarriesTheFilesOwnValue(t *testing.T) {
	params := []personalise.Param{
		{Name: "OUT_X", Type: personalise.ParamNumber, Default: "200", Note: "@fixed"},
	}
	got := byVariable(suggestFieldMaps(params, nil, 0))
	row, ok := got["OUT_X"]
	if !ok {
		t.Fatal("OUT_X was not offered")
	}
	if row.FixedValue != "200" {
		t.Fatalf("fixed value %q, want 200", row.FixedValue)
	}
	if row.PropertyKey != "" {
		t.Fatalf("a fixed row reads the order: %q", row.PropertyKey)
	}
}

// A file-wide MAX_CHARS reaches every text field, which is what shipped for
// the personalisation link's own limit - the two must agree or the form
// refuses a name the model would have rendered.
func TestTheFilesDeclaredLimitReachesItsTextFields(t *testing.T) {
	params := []personalise.Param{{Name: "NAME_L", Type: personalise.ParamString}}
	observed := []observedPropertyResponse{
		{Key: "step 4 first name", Label: "STEP 4-First Name-", Orders: 1728},
	}
	got := byVariable(suggestFieldMaps(params, observed, 20))
	if got["NAME_L"].MaxRunes == nil || *got["NAME_L"].MaxRunes != 20 {
		t.Fatalf("limit %v, want 20", got["NAME_L"].MaxRunes)
	}
	// Not on a number: there is no text box to overflow and the API refuses
	// the pair.
	nums := byVariable(suggestFieldMaps(
		[]personalise.Param{{Name: "HEART_NUM", Type: personalise.ParamNumber}},
		[]observedPropertyResponse{{Key: "heart num", Label: "Heart number", Sample: "2"}}, 20))
	if nums["HEART_NUM"].MaxRunes != nil {
		t.Fatalf("a numeric field got a character limit: %v", nums["HEART_NUM"].MaxRunes)
	}
}

// A product that has never sold. The honest answer is no suggestions, not a
// wrong one - and the editor's typed-key path is what gets past it.
func TestAProductWithNoOrdersGetsNoGuesses(t *testing.T) {
	params := []personalise.Param{
		{Name: "NAME_L", Type: personalise.ParamString},
		{Name: "TEXT", Type: personalise.ParamString},
	}
	if got := suggestFieldMaps(params, nil, 0); len(got) != 0 {
		t.Fatalf("invented %d suggestions from no evidence: %+v", len(got), got)
	}
}

// ...unless the file configured itself, which is the whole point: a design
// annotated by its author needs no orders to be mappable.
func TestAnAnnotatedFileNeedsNoOrdersAtAll(t *testing.T) {
	params := []personalise.Param{
		{Name: "NAME_L", Type: personalise.ParamString, Note: "@from Name 1 @max 20"},
		{Name: "NAME_R", Type: personalise.ParamString, Note: "@from Name 2 @optional"},
		{Name: "OUT_X", Type: personalise.ParamNumber, Default: "200", Note: "@fixed"},
	}
	got := suggestFieldMaps(params, nil, 0)
	if len(got) != 3 {
		t.Fatalf("offered %d of 3: %+v", len(got), got)
	}
	by := byVariable(got)
	if by["NAME_R"].Optional != true {
		t.Fatal("@optional did not reach the suggestion")
	}
	if by["NAME_L"].PropertyKey != "name 1" {
		t.Fatalf("NAME_L key %q", by["NAME_L"].PropertyKey)
	}
	// Said plainly, because the other reading of "no order carried it" is a
	// label that has since been reworded.
	if by["NAME_L"].Why == "" {
		t.Fatal("no reason given for a field no order has carried")
	}
}

func byVariable(rows []fieldSuggestionResponse) map[string]fieldSuggestionResponse {
	out := make(map[string]fieldSuggestionResponse, len(rows))
	for _, r := range rows {
		out[r.ScadVariable] = r
	}
	return out
}

// A file may say how a variable is filled without saying WHERE from - the
// limit and the requiredness are useful on their own, and the author who
// wrote "@max 20" into the file expects the form to enforce 20.
func TestWhatTheFileSaidSurvivesAGuessedAddress(t *testing.T) {
	params := []personalise.Param{
		{
			Name: "NAME_L", Type: personalise.ParamString,
			Note: "The left name. @max 14 @optional @default A",
		},
	}
	observed := []observedPropertyResponse{
		{Key: "step 4 first name", Label: "STEP 4-First Name-", Sample: "Asha", Orders: 1728},
	}
	got := byVariable(suggestFieldMaps(params, observed, 0))
	row, ok := got["NAME_L"]
	if !ok {
		t.Fatal("NAME_L was not proposed")
	}
	// The address is a guess...
	if row.Source != sourceMatch || row.PropertyKey != "step 4 first name" {
		t.Fatalf("source %q key %q", row.Source, row.PropertyKey)
	}
	// ...and everything the file actually stated came through anyway.
	if row.MaxRunes == nil || *row.MaxRunes != 14 {
		t.Fatalf("limit %v, want 14 - the author wrote it in the file", row.MaxRunes)
	}
	if !row.Optional {
		t.Error("@optional was dropped")
	}
	if row.DefaultValue != "A" {
		t.Errorf("@default was dropped: %q", row.DefaultValue)
	}
}

// FOUND ON LIVE DATA, 2026-11-03. The Royal diya holder's file declares TEXT
// and its 21 recent lines carry "Name" - so the one pairing that product
// needs was the one pairing that was missed, and the endpoint returned an
// empty list for a product that is perfectly mappable.
func TestTheSingleTextFieldIsFoundWhateverItIsCalled(t *testing.T) {
	params := []personalise.Param{
		{Name: "TEXT", Section: "Text", Type: personalise.ParamString, Default: `"DIWALI"`},
		{Name: "TEXT_SIZE", Section: "Text", Type: personalise.ParamNumber, Default: "7.789"},
		{Name: "TEXT_COLOR", Section: "Colours", Type: personalise.ParamString, Default: `"gold"`},
		{Name: "PLATE_COLOR", Section: "Colours", Type: personalise.ParamString, Default: `"red"`},
	}
	observed := []observedPropertyResponse{
		{Key: "name", Label: "Name", Sample: "Gangavarapu nilayam", Orders: 21},
	}
	got := byVariable(suggestFieldMaps(params, observed, 0))

	if got["TEXT"].PropertyKey != "name" {
		t.Fatalf("TEXT was not paired with the Name field: %q", got["TEXT"].PropertyKey)
	}
	// And the near misses stay out. TEXT_SIZE and TEXT_COLOR each share the
	// word "text" with the label; proposing the customer's name into a font
	// size would render, slice and print.
	for _, wrong := range []string{"TEXT_SIZE", "TEXT_COLOR", "PLATE_COLOR"} {
		if row, offered := got[wrong]; offered {
			t.Errorf("%s was offered %q", wrong, row.PropertyKey)
		}
	}
}

// Position still outranks wording, which is what makes the name/text synonym
// safe: a plank declaring both must not have TEXT steal a name.
func TestTextDoesNotStealANamedPosition(t *testing.T) {
	params := []personalise.Param{
		{Name: "NAME_L", Type: personalise.ParamString},
		{Name: "NAME_R", Type: personalise.ParamString},
		{Name: "TEXT", Type: personalise.ParamString},
	}
	observed := []observedPropertyResponse{
		{Key: "step 4 first name", Label: "STEP 4-First Name-", Orders: 1728},
		{Key: "step 5 second name", Label: "STEP 5-Second Name", Orders: 1729},
	}
	got := byVariable(suggestFieldMaps(params, observed, 0))
	if got["NAME_L"].PropertyKey != "step 4 first name" {
		t.Fatalf("NAME_L took %q", got["NAME_L"].PropertyKey)
	}
	if got["NAME_R"].PropertyKey != "step 5 second name" {
		t.Fatalf("NAME_R took %q", got["NAME_R"].PropertyKey)
	}
}

// FOUND ON LIVE DATA, 2026-11-03. dnp_heart_joinery declares side_hearts AND
// heart_drop ("mm the filler heart is grown + pushed down"). Against the
// label "3 - Red Heart" both share exactly the word "heart" and scored
// IDENTICALLY, so the winner was whichever sorted first by name - heart_drop.
// A heart COUNT proposed into a millimetre geometry parameter renders,
// slices, prints, and is quietly the wrong shape.
//
// No scoring tweak fixes that: on the evidence the two really are equally
// good. So half a match is no match, and the cost is one dropdown pick.
func TestHalfAMatchIsNotProposed(t *testing.T) {
	params := []personalise.Param{
		{Name: "side_hearts", Type: personalise.ParamNumber, Default: "1"},
		{
			Name: "heart_drop", Type: personalise.ParamNumber, Default: "0.6",
			Note: "mm the filler heart is grown + pushed down (stronger base)",
		},
	}
	observed := []observedPropertyResponse{
		{Key: "3 red heart", Label: "3 - Red Heart", Sample: "1 RED HEART", Orders: 4},
	}
	for _, row := range suggestFieldMaps(params, observed, 0) {
		t.Fatalf("%s was proposed %q on one shared word out of two",
			row.ScadVariable, row.PropertyKey)
	}
}

// ...and a full match is still proposed, so the filter has not quietly
// turned the feature off. A file calling it HEARTS says one word, and the
// label accounts for it.
func TestAFullMatchStillSurvivesTheFilter(t *testing.T) {
	params := []personalise.Param{
		{Name: "HEARTS", Type: personalise.ParamNumber, Default: "1"},
	}
	observed := []observedPropertyResponse{
		{Key: "red heart", Label: "RED HEART", Sample: "1 RED HEART", Orders: 34},
	}
	got := byVariable(suggestFieldMaps(params, observed, 0))
	row, ok := got["HEARTS"]
	if !ok {
		t.Fatal("the filter dropped a match that accounted for every word")
	}
	if row.PropertyKey != "red heart" {
		t.Fatalf("HEARTS took %q", row.PropertyKey)
	}
}
