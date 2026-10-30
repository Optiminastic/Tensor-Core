package httpapi

// One file, read two ways, disagreeing about the same product.
//
// A flagged template's PRINT scheme is built by selfColouredParts, which reads
// PartColourValues and resolves a colour NAME against the shelf. The picture
// the customer approves was built by withDeclaredColours, which read
// PartColours - well-formed "#RRGGBB" only. So the Royal holder, declaring
//
//	background_color = "purple";
//	text_color       = "#FFBF00";
//
// had its gold taken and its purple dropped: a holder drawn entirely in its
// text colour, under a countdown saying this is exactly how it will be made.
// The Lotus was worse - every colour it declares is a name, so the picture
// took none of them and the flag looked like it did nothing at all.

import (
	"context"
	"testing"

	"github.com/google/uuid"

	"github.com/Optiminastic/tensor-core/internal/personalise"
)

// previewServerWithSource is a server whose renderer serves one .scad.
func previewServerWithSource(t *testing.T, s *Server, source string) *Server {
	t.Helper()
	s.renderer = personalise.NewRenderer("", "", 0).WithTemplateLoader(
		func(context.Context, string) ([]byte, bool, error) {
			return []byte(source), true, nil
		})
	return s
}

const royalPreviewSource = `
background_color = "purple";
text_color       = "#FFBF00";
ornament_color   = "#000000";
`

func TestIntegrationThePictureTakesADeclaredColourNameNotOnlyAHex(t *testing.T) {
	store := setupStore(t)
	seedAll(t, store)
	ctx := context.Background()

	// The shop's purple, as the colour map records it. resolveColourHex reads
	// this before the shelf, so the picture shows the purple somebody chose
	// rather than a catalogue's idea of the word.
	if _, err := store.Pool.Exec(ctx,
		`INSERT INTO colour_map (id, colour_name, hex, is_primary)
		 VALUES ($1, 'purple', '#A235D8', true)
		 ON CONFLICT DO NOTHING`, uuid.New()); err != nil {
		t.Fatalf("colour map: %v", err)
	}

	s := previewServerWithSource(t, registryServer(t, store), royalPreviewSource)

	// What the registry configured. Every piece has a colour, so none of them
	// follows the customer and all three are candidates for repainting.
	parts := []colourPart{
		{Name: "background", Hex: "#111111"},
		{Name: "text", Hex: "#222222"},
		{Name: "ornament", Hex: "#333333"},
		{Name: "base", Hex: "#444444"},
	}
	got := s.withDeclaredColours(ctx, "pdhr-p-per", parts)

	byName := map[string]string{}
	for _, p := range got {
		byName[p.Name] = p.Hex
	}
	if byName["background"] != "#A235D8" {
		t.Errorf("background = %q, want #A235D8 - a declared NAME was dropped, which is "+
			"the whole product drawn in its text colour", byName["background"])
	}
	if byName["text"] != "#FFBF00" {
		t.Errorf("text = %q, want #FFBF00 - a declared hex must still be taken as written",
			byName["text"])
	}
	if byName["ornament"] != "#000000" {
		t.Errorf("ornament = %q, want #000000", byName["ornament"])
	}
	// PER PIECE, NOT ALL OR NOTHING. The file says nothing about "base", so
	// it keeps the colour the registry chose. selfColouredParts refuses the
	// whole scheme in that case and should: a PRINT missing a piece prints a
	// product missing it, where a picture missing one is still a picture.
	if byName["base"] != "#444444" {
		t.Errorf("base = %q, want the registry's #444444 kept", byName["base"])
	}
}

func TestIntegrationAPieceFollowingTheCustomerIsNeverRepainted(t *testing.T) {
	store := setupStore(t)
	seedAll(t, store)
	ctx := context.Background()
	s := previewServerWithSource(t, registryServer(t, store), royalPreviewSource)

	// An empty hex means "the colour they picked on the link". Painting the
	// file's default over it shows them a product in a colour they did not
	// choose, which is the one thing this picture exists to rule out.
	got := s.withDeclaredColours(ctx, "pdhr-p-per", []colourPart{{Name: "background"}})
	if len(got) != 1 || !got[0].FollowsCustomer() {
		t.Errorf("got %+v, want the customer's piece left alone", got)
	}
}

func TestIntegrationAnUnresolvableNameLeavesThatPieceAlone(t *testing.T) {
	store := setupStore(t)
	seedAll(t, store)
	ctx := context.Background()
	s := previewServerWithSource(t, registryServer(t, store),
		`background_color = "nosuchcolour";`)

	got := s.withDeclaredColours(ctx, "pdhr-p-per", []colourPart{{Name: "background", Hex: "#111111"}})
	if len(got) != 1 || got[0].Hex != "#111111" {
		t.Errorf("got %+v, want the registry's colour kept when the name means nothing", got)
	}
}
