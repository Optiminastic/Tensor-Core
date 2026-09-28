package personalise

import (
	"os"
	"path/filepath"
	"testing"
)

// A template imports its parts by relative name, and every render happens in a
// fresh temporary directory. Without staging, that import finds nothing and
// OpenSCAD exits 0 with the piece missing - measured on the heart keychain,
// where dropping decoration.stl took the "text" pass from 11,844 triangles to
// 4,339 and reported no error at all.
func TestStageAssetsPutsThePartsBesideTheTemplate(t *testing.T) {
	assets := t.TempDir()
	if err := os.WriteFile(filepath.Join(assets, "decoration.stl"), []byte("solid x"), 0o600); err != nil {
		t.Fatal(err)
	}
	// fonts/ comes too, because fontconfig SUBSTITUTES a missing family rather
	// than failing: the same keychain rendered 8,342 triangles instead of
	// 11,844 with Lobster absent, successfully, in the wrong face.
	if err := os.MkdirAll(filepath.Join(assets, "fonts"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(assets, "fonts", "Lobster-Regular.ttf"), []byte("ttf"), 0o600); err != nil {
		t.Fatal(err)
	}
	// A directory that is not fonts/ is left alone: one stray folder of STLs
	// beside the parts would otherwise be copied on every render.
	if err := os.MkdirAll(filepath.Join(assets, "archive"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(assets, "archive", "old.stl"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()
	r := NewRenderer("openscad", assets, 0)
	if err := r.stageAssets(dir); err != nil {
		t.Fatalf("stage: %v", err)
	}

	for _, want := range []string{"decoration.stl", filepath.Join("fonts", "Lobster-Regular.ttf")} {
		if _, err := os.Stat(filepath.Join(dir, want)); err != nil {
			t.Errorf("%s was not staged: %v", want, err)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "archive")); err == nil {
		t.Error("a directory that is not fonts/ was copied")
	}
}

// No asset directory is the normal case: the plank templates are pure geometry
// and import nothing. It must not become an error.
func TestStageAssetsIsQuietWithNothingConfigured(t *testing.T) {
	dir := t.TempDir()
	if err := NewRenderer("openscad", "", 0).stageAssets(dir); err != nil {
		t.Errorf("staging with no asset directory failed: %v", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("staged %d entries from nowhere", len(entries))
	}
}

// Configured and unreadable is reported rather than skipped: a template that
// needs its parts would otherwise render an incomplete model successfully,
// which is the whole failure this exists to stop.
func TestStageAssetsReportsAMissingAssetDirectory(t *testing.T) {
	dir := t.TempDir()
	r := NewRenderer("openscad", filepath.Join(dir, "nope"), 0)
	if err := r.stageAssets(dir); err == nil {
		t.Error("a missing asset directory was accepted silently")
	}
}
