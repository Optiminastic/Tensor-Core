package httpapi

import "testing"

// The file name is the only place a designer says what a piece IS, so it
// becomes the job's part_role and the word an operator reads on the floor.
func TestPieceNameFromFilename(t *testing.T) {
	for _, tc := range []struct{ file, want string }{
		{"clip.3mf", "clip"},
		{"DUOSNAP body.3mf", "DUOSNAP body"},
		{"  shade  .STL", "shade"},
		{"base_v2.3mf", "base_v2"},
		{"a/b/c/lid.3mf", "lid"},
		// Inner whitespace collapses, so two files that differ only in spacing
		// do not become two different pieces.
		{"top   plate.3mf", "top plate"},
		// Trimmed rather than refused: a long name is a habit, not a mistake
		// to reject an upload over.
		{"an-extremely-long-piece-name-that-will-not-fit.3mf", "an-extremely-long-piece"},
		{".3mf", ""},
	} {
		if got := pieceNameFrom(tc.file); got != tc.want {
			t.Errorf("pieceNameFrom(%q) = %q, want %q", tc.file, got, tc.want)
		}
	}
}
