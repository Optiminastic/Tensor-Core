package personalise

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// sourceLoader serves one .scad from memory, so these tests exercise the real
// subprocess without needing a template on disk.
func sourceLoader(src string) TemplateLoader {
	return func(_ context.Context, _ string) ([]byte, bool, error) {
		return []byte(src), true, nil
	}
}

// A piece the script chose not to draw.
//
// Measured on the mirror before this existed: PART="tick" with
// verified_tick=false exits 0, logs "Current top level object is empty." and
// leaves the output path UNTOUCHED - no file, not an empty one. That read
// failure used to surface as "read rendered model: ...The system cannot find
// the file specified", which is indistinguishable from a disk problem, so a
// conditional piece could not be told from a broken render.
func TestADrawnNothingIsReportedAsAnEmptyModel(t *testing.T) {
	r := rendererOrSkip(t).WithTemplateLoader(sourceLoader(`
PART = "";
if (PART == "heart") cube(10);
`))

	_, err := r.RenderSTL(context.Background(), "conditional", map[string]string{
		"PART": Quote("tick"),
	})
	if err == nil {
		t.Fatal("a piece that drew nothing must not come back as a model")
	}
	if !errors.Is(err, ErrEmptyModel) {
		t.Fatalf("want ErrEmptyModel so an optional piece can be told apart, got %v", err)
	}
}

// The same file, asked for the piece it does draw. Without this the test above
// would pass against a renderer that had simply stopped working.
func TestTheSamePieceRendersWhenTheScriptDrawsIt(t *testing.T) {
	r := rendererOrSkip(t).WithTemplateLoader(sourceLoader(`
PART = "";
if (PART == "heart") cube(10);
`))

	out, err := r.RenderSTL(context.Background(), "conditional", map[string]string{
		"PART": Quote("heart"),
	})
	if err != nil {
		t.Fatalf("the piece the script draws must render: %v", err)
	}
	if len(out) == 0 {
		t.Fatal("rendered nothing for a piece the script draws")
	}
}

// A BROKEN render must not be mistaken for an absent piece, or the mirror
// prints without its badge and the job reports success.
func TestABrokenTemplateIsNotAnEmptyModel(t *testing.T) {
	r := rendererOrSkip(t).WithTemplateLoader(sourceLoader(`this is not openscad(((`))

	_, err := r.RenderSTL(context.Background(), "broken", nil)
	if err == nil {
		t.Fatal("a syntax error must fail the render")
	}
	if errors.Is(err, ErrEmptyModel) {
		t.Fatalf("a syntax error must not read as an absent piece: %v", err)
	}
	if !strings.Contains(err.Error(), "openscad") {
		t.Fatalf("the refusal should name the renderer: %v", err)
	}
}
