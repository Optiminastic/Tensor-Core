package slicing

// Runs Bambu Studio headless to slice one bed for printing, with preset files
// written from a slicing profile (print_profile.go).
//
// Unlike RunSlice, which costs a bare STL, this takes a merged plate whose
// parts bedpack has already placed: no --arrange and no --orient, or the slicer
// would discard the layout the bed was planned around.
//
// Presets are passed as files, never left embedded in the 3MF. The embedded
// path crashes the CLI in glfwInit (BambuBuddy's use_embedded_settings hits
// the same crash); the preset path only logs a harmless GLFW warning and
// writes no thumbnail.

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// PrintSliceInput is one bed's plate and the preset files to slice it with.
type PrintSliceInput struct {
	BambuRoot string
	PlatePath string
	Files     PresetFiles
	// Dir is a scratch directory the caller owns and removes.
	Dir     string
	Timeout time.Duration
}

// RunPrintSlice writes the preset files into in.Dir, slices the plate and
// returns the path of the exported .gcode.3mf.
func RunPrintSlice(ctx context.Context, in PrintSliceInput) (SliceOutput, error) {
	machine, process, filaments, err := writePresetFiles(in.Dir, in.Files)
	if err != nil {
		return SliceOutput{}, err
	}
	outdir := filepath.Join(in.Dir, "out")
	if err := os.MkdirAll(outdir, 0o755); err != nil {
		return SliceOutput{}, err
	}
	args := []string{
		"-a", filepath.Join(in.BambuRoot, "AppRun"),
		"--load-settings", machine + ";" + process,
		"--load-filaments", strings.Join(filaments, ";"),
		"--slice", "0",
		"--outputdir", outdir,
		"--export-3mf", exportName,
		in.PlatePath,
	}
	runCtx, cancel := context.WithTimeout(ctx, in.Timeout)
	defer cancel()
	cmd := exec.CommandContext(runCtx, "xvfb-run", args...)
	// The CLI drops config and logs in the working directory's HOME; keep its
	// litter inside the scratch dir.
	cmd.Dir = in.Dir
	out, runErr := cmd.CombinedOutput()
	if runCtx.Err() == context.DeadlineExceeded {
		return SliceOutput{}, fmt.Errorf("slice timed out after %s", in.Timeout)
	}
	if err := runCtx.Err(); err != nil {
		return SliceOutput{}, fmt.Errorf("slice aborted before completing: %w", err)
	}
	return collectOutput(outdir, runErr, string(out))
}

// collectOutput checks what the slicer left behind and names the reason when it
// left nothing usable.
func collectOutput(outdir string, runErr error, log string) (SliceOutput, error) {
	resultPath := filepath.Join(outdir, resultName)
	gcodePath := filepath.Join(outdir, exportName)
	if _, err := os.Stat(gcodePath); err == nil {
		return SliceOutput{ResultJSONPath: resultPath, Gcode3mfPath: gcodePath}, nil
	}
	if _, err := os.Stat(resultPath); err == nil {
		if reason := sliceErrorReason(resultPath); reason != "" {
			return SliceOutput{}, fmt.Errorf("the slicer refused this bed: %s", reason)
		}
	}
	// No result at all is a crash: a segfault leaves nothing but its exit code.
	return SliceOutput{}, fmt.Errorf("the slicer exported no file (%v): %s", runErr, tail(log))
}

// writePresetFiles writes each preset to its own file and returns the paths in
// the order the CLI wants them.
func writePresetFiles(dir string, f PresetFiles) (string, string, []string, error) {
	write := func(name string, b []byte) (string, error) {
		p := filepath.Join(dir, name)
		return p, os.WriteFile(p, b, 0o644)
	}
	machine, err := write("machine.json", f.Machine)
	if err != nil {
		return "", "", nil, err
	}
	process, err := write("process.json", f.Process)
	if err != nil {
		return "", "", nil, err
	}
	filaments := make([]string, 0, len(f.Filaments))
	for i, b := range f.Filaments {
		p, err := write(fmt.Sprintf("filament_%d.json", i+1), b)
		if err != nil {
			return "", "", nil, err
		}
		filaments = append(filaments, p)
	}
	return machine, process, filaments, nil
}
