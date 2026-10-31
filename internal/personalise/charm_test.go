package personalise

// Which planks go down the charm path.
//
// The asymmetry is the point: a charm sends the order to a DIFFERENT FILE, so
// a false positive prints the wrong product for a customer who asked for the
// plain plank - and plain planks are nearly all of them.

import "testing"

// props is dnp_test.go's helper - the same package, and one builder for a
// line's options is the point of it.

func TestAChosenCharmNamesItself(t *testing.T) {
	// The three the storefront sells today, read off live orders.
	for _, charm := range []string{"Warm Embrace", "Ghost Lovers", "Heart Huggers"} {
		if got := CharmFromProperties(props("Charm", charm)); got != charm {
			t.Errorf("CharmFromProperties(%q) = %q, want %q", charm, got, charm)
		}
	}
}

func TestNoCharmLeavesThePlankAlone(t *testing.T) {
	// THE IMPORTANT ONE. Every plank the shop has ever printed is this case,
	// and it must reach the untouched path byte for byte.
	if got := CharmFromProperties(props("Charm", "No charm")); got != "" {
		t.Errorf("CharmFromProperties = %q, want no charm", got)
	}
}

func TestAPlankWithNoCharmOptionAtAllIsNotACharm(t *testing.T) {
	// Every order placed before the option existed, and the 2-STEP plank,
	// which carries two names and nothing else.
	line := props("STEP 4-First Name-", "LOKESH", "STEP 5-Second Name", "SMITHA")
	if got := CharmFromProperties(line); got != "" {
		t.Errorf("CharmFromProperties = %q, want no charm", got)
	}
}

func TestABlankCharmIsNoCharm(t *testing.T) {
	// The option imported with an empty value. Rendering a charm plank off a
	// blank field would print a product nobody ordered.
	if got := CharmFromProperties(props("Charm", "   ")); got != "" {
		t.Errorf("CharmFromProperties = %q, want no charm", got)
	}
}

func TestTheStorefrontMayRecapitaliseItsOwnOption(t *testing.T) {
	// "NO CHARM" and "no charm" are the same answer. Matching bytes would send
	// plain planks to the charm file the day the storefront changed its case.
	for _, value := range []string{"NO CHARM", "no charm", " No Charm "} {
		if got := NormaliseCharm(value); got != "" {
			t.Errorf("NormaliseCharm(%q) = %q, want no charm", value, got)
		}
	}
}

func TestACharmSendsThePlankToItsOwnFile(t *testing.T) {
	p, err := ParamsFromProperties(props(
		"STEP 4-First Name-", "LOKESH", "STEP 5-Second Name", "SMITHA",
		"STEP 3-", "1 RED HEART", "Charm", "Warm Embrace"))
	if err != nil {
		t.Fatalf("ParamsFromProperties: %v", err)
	}
	if p.Template != templateCharm {
		t.Errorf("template = %q, want %q", p.Template, templateCharm)
	}
	if p.Charm != "Warm Embrace" {
		t.Errorf("charm = %q, want Warm Embrace", p.Charm)
	}
	// The file cannot draw side hearts, so the count must not survive into a
	// parts list that would then expect a second printed piece.
	if p.Hearts != 0 {
		t.Errorf("hearts = %d on a charm plank, want 0", p.Hearts)
	}
}

func TestAPlainPlankStillRendersExactlyAsItDid(t *testing.T) {
	// THE REGRESSION THIS WHOLE FEATURE RISKS. Same line, charm set to the
	// storefront's "No charm": it must come out as the joinery plank with its
	// heart count intact.
	p, err := ParamsFromProperties(props(
		"STEP 4-First Name-", "LOKESH", "STEP 5-Second Name", "SMITHA",
		"STEP 3-", "2 RED HEART", "Charm", "No charm"))
	if err != nil {
		t.Fatalf("ParamsFromProperties: %v", err)
	}
	if p.Template != templateJoinery {
		t.Errorf("template = %q, want %q", p.Template, templateJoinery)
	}
	if p.Hearts != 2 {
		t.Errorf("hearts = %d, want 2 - the charm path ate a plain plank's hearts", p.Hearts)
	}
}

func TestTheCharmWinsOverThePhotoFrame(t *testing.T) {
	// A frame ordered WITH a charm comes off the charm file. ForProduct would
	// otherwise send it back to the frame's own template, because the callers
	// apply it after the charm has already been decided.
	p, err := ParamsFromProperties(props(
		"left name", "ARJUN", "right name", "DIYA", "Charm", "Ghost Lovers"))
	if err != nil {
		t.Fatalf("ParamsFromProperties: %v", err)
	}
	p = p.ForProduct("DNPF-RED", "DUAL NAME & PHOTO FRAME")
	if p.Template != templateCharm {
		t.Errorf("template = %q, want %q - the frame overrode the charm", p.Template, templateCharm)
	}
}

func TestAFrameWithoutACharmIsUntouched(t *testing.T) {
	p, err := ParamsFromProperties(props("left name", "ARJUN", "right name", "DIYA"))
	if err != nil {
		t.Fatalf("ParamsFromProperties: %v", err)
	}
	p = p.ForProduct("DNPF-RED", "DUAL NAME & PHOTO FRAME")
	if p.Template != templateFrame {
		t.Errorf("template = %q, want %q", p.Template, templateFrame)
	}
	if p.Shape != FrameShape {
		t.Errorf("shape = %v, want the frame's", p.Shape)
	}
}

func TestTheCharmFileIsAskedForItsOwnPieceNames(t *testing.T) {
	// PART/base/text mean nothing to this file. If these come out as Tensor's
	// own names, export_part keeps its default and both colour passes return
	// the whole plank - two stacked solids in one 3MF.
	p := Params{Template: templateCharm, NameLeft: "A", NameRight: "B", Charm: "Warm Embrace"}

	base := p.ArgsForPart(PartBase)
	if got := base[charmPartVariable]; got != Quote(charmPartPlank) {
		t.Errorf("%s = %q, want %q", charmPartVariable, got, Quote(charmPartPlank))
	}
	if _, wrong := base["PART"]; wrong {
		t.Error("PART was passed to the charm file, which never declares it")
	}

	text := p.ArgsForPart(PartText)
	if got := text[charmPartVariable]; got != Quote(charmPartText) {
		t.Errorf("%s = %q, want %q", charmPartVariable, got, Quote(charmPartText))
	}
}

func TestTheCharmFileIsNotSentTheHeartFilesArguments(t *testing.T) {
	// side_hearts is not declared here. Passing it would be silently ignored,
	// which is precisely why it must not be passed: it would read as though
	// the hearts were handled.
	args := Params{Template: templateCharm, NameLeft: "A", NameRight: "B"}.Args()
	if _, found := args["side_hearts"]; found {
		t.Error("side_hearts was passed to a file that cannot draw them")
	}
	if args["add_charm"] != "true" {
		t.Errorf("add_charm = %q, want true", args["add_charm"])
	}
}

func TestTheShopsColoursReachTheFilesOwnWording(t *testing.T) {
	// Every colour the shop sells a plank in, read off live orders.
	for shop, want := range map[string]string{
		"RED": "Red", "SKY BLUE": "SkyBlue", "BABY PINK": "Pink", "GOLD": "Gold",
		"WHITE": "White", "BLACK": "Black", "BLUE": "Blue", "GREEN": "Green",
		"PURPLE": "Purple", "YELLOW": "Yellow",
	} {
		if got := CharmColourName(shop); got != want {
			t.Errorf("CharmColourName(%q) = %q, want %q", shop, got, want)
		}
	}
}

func TestAnUnknownColourLeavesTheFilesDefaultAlone(t *testing.T) {
	// Not an error: the PRINTED colour comes from the hex Tensor attaches to
	// each mesh, so an unrecognised name here cannot print the wrong product
	// and must not fail the render.
	if got := CharmColourName("CHARTREUSE"); got != "" {
		t.Errorf("CharmColourName = %q, want empty", got)
	}
	args := Params{Template: templateCharm, TextColour: "CHARTREUSE"}.Args()
	if _, set := args["text_color"]; set {
		t.Error("an unrecognised colour was passed to the file anyway")
	}
}

func TestACharmPlankRefusesANameItCannotLayOut(t *testing.T) {
	// Eleven letters fits the plain plank and does not fit the charm's, which
	// gives up 40mm to the charm. The message has to say which product it is
	// talking about, or it reads as a contradiction of the limit the same shop
	// sees on every other plank.
	line := props(
		"STEP 4-First Name-", "ALEXANDRIAA", "STEP 5-Second Name", "SMITHA",
		"Charm", "Warm Embrace")
	if _, err := ParamsFromProperties(line); err == nil {
		t.Fatal("an 11-letter name was accepted on a charm plank")
	}

	// ...and the SAME name is still perfectly good without a charm.
	plain := props("STEP 4-First Name-", "ALEXANDRIAA", "STEP 5-Second Name", "SMITHA")
	if _, err := ParamsFromProperties(plain); err != nil {
		t.Errorf("the charm's shorter limit leaked onto the plain plank: %v", err)
	}
}
