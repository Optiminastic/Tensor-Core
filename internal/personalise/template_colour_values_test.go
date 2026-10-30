package personalise

// What a file says its pieces are painted, as written.
//
// PartColourValues feeds the PRINT model of a file that states its own
// colours, where a bare name is the shop's own word for a spool and gets
// resolved against the shelf. PartColours feeds the customer's PICTURE, which
// has no filament behind it, so it keeps only well-formed hexes. The two
// differ on exactly that, and these pin the difference.

import "testing"

func TestPartColourValuesKeepsNamesThatPartColoursDrops(t *testing.T) {
	// The shop's own lotus holder, which writes colour names rather than
	// hexes. Dropping these would silently paint nothing and read, on screen,
	// as the file's colours being ignored.
	source := []byte(`
BACK_COLOR    = "purple";
OUTLINE_COLOR = "#FF69B4";
TEXT_COLOR    = "gold";
DIYA_COLOR    = "pink";
TEXT          = "Srivastava's";
`)

	values := PartColourValues(source)
	for piece, want := range map[string]string{
		"back": "purple", "outline": "#FF69B4", "text": "gold", "diya": "pink",
	} {
		if got := values[piece]; got != want {
			t.Errorf("PartColourValues[%q] = %q, want %q", piece, got, want)
		}
	}
	// TEXT is not a colour: only a *_COLOR declaration names a piece.
	if _, ok := values["tex"]; ok {
		t.Error("TEXT was read as a piece colour")
	}
	if len(values) != 4 {
		t.Errorf("read %d pieces, want 4: %v", len(values), values)
	}

	// The picture's parser keeps only the hex, which is why both exist.
	hexes := PartColours(source)
	if len(hexes) != 1 || hexes["outline"] != "#FF69B4" {
		t.Errorf("PartColours = %v, want only the well-formed hex", hexes)
	}
}

func TestPartColourValuesIgnoresWhatIsNotADeclaration(t *testing.T) {
	source := []byte(`
// A comment mentioning BACK_COLOR = "red";
module plate() {
    INNER_COLOR = "green";
}
BACK_COLOR = "blue";
`)
	values := PartColourValues(source)
	// Indented assignments are module-local and deliberately excluded by the
	// parser; everything after the first module is the script computing with
	// its params rather than declaring them.
	if _, ok := values["inner"]; ok {
		t.Error("a module-local assignment was read as a piece colour")
	}
	// And a declaration after the first module is past where DeclaredParams
	// stops reading, which is the existing contract - pinned so a change to
	// it is a decision rather than a surprise.
	if _, ok := values["back"]; ok {
		t.Error("a declaration after the first module was read; " +
			"DeclaredParams stops at the first module by design")
	}
}

func TestPartColourValuesSkipsAnEmptyValue(t *testing.T) {
	// A piece with nothing to paint it is not a piece. Returned, it would
	// become a colourPart with an empty hex - which means "follows the
	// customer" to every reader of that struct, and paints a fixed piece in
	// whatever the customer bought.
	if values := PartColourValues([]byte(`BACK_COLOR = "";` + "\n")); len(values) != 0 {
		t.Errorf("PartColourValues = %v, want none", values)
	}
}
