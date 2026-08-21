package slicing

// Runs Bambu Studio headless (its AppRun under xvfb) to slice one model. Ported
// from the Python worker's slicer.py. To batch, the model is loaded units_per_bed
// times and auto-arranged; the caller divides the plate totals per unit.

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const (
	exportName = "out.gcode.3mf"
	resultName = "result.json"
	// plateProjectName is the project assembled from a machine's settings
	// template for a template-driven plate slice.
	plateProjectName = "plate-project.3mf"
)

// SliceOutput is the pair of files a successful slice leaves in outdir.
type SliceOutput struct {
	ResultJSONPath string
	Gcode3mfPath   string
}

// RunSlice slices stlPath with the resolved H2S profiles into outdir. It enables
// auto-support and applies the infill override, mirroring the Python invocation.
// It fails with a clear reason when the slicer crashes or exports no G-code,
// rather than letting the caller trip over a missing file.
func RunSlice(
	ctx context.Context, bambuRoot string, profiles ResolvedProfiles,
	stlPath string, infillPct float64, unitsPerBed int, settings SliceSettings,
	outdir string, timeout time.Duration,
) (SliceOutput, error) {
	copies := unitsPerBed
	if copies < 1 {
		copies = 1
	}
	enableSupport := 0
	if settings.SupportEnabled() {
		enableSupport = 1
	}
	appRun := filepath.Join(bambuRoot, "AppRun")
	args := []string{
		"-a", appRun,
		"--load-settings", profiles.MachinePath + ";" + profiles.ProcessPath,
		"--load-filaments", profiles.FilamentPath,
		"--arrange", "1",
		"--orient", "1",
		"--slice", "0",
		// Auto-support: Bambu adds support only where overhangs need it, so support
		// material is a real, costed metric. On by default; the caller can turn it off.
		fmt.Sprintf("--enable-support=%d", enableSupport),
		fmt.Sprintf("--sparse-infill-density=%g%%", infillPct),
		"--outputdir", outdir,
		"--export-3mf", exportName,
	}
	// Advanced overrides (layer height, walls, infill pattern, support angle). Each
	// is allowlisted and clamped by SliceSettings, so nothing arbitrary reaches here.
	args = append(args, settings.Flags()...)
	for i := 0; i < copies; i++ {
		args = append(args, stlPath)
	}

	return runBambu(ctx, args, outdir, timeout)
}

// RunPlateSlice slices an already-packed batch plate: one merged STL whose parts
// bedpack has positioned. It differs from RunSlice in exactly three ways, all
// load-bearing (verified against Bambu Studio on a real merged plate):
//
//   - --orient 0. Auto-orient is free to rotate the model to minimise supports.
//     On a plate that would rotate the whole packed layout as a rigid body, which
//     can lay a bed of parts on its side. The layout is already decided.
//   - one copy. The plate already contains every unit, so the units_per_bed
//     repetition RunSlice does would print the whole bed N times.
//   - a project template for machines that have one, in place of presets. This
//     is what makes a multi-extruder machine sliceable at all; see below.
//
// --arrange stays ON, and that is not optional: bedpack packs from the bed origin,
// and slicing those raw coordinates fails with "Found G-code outside of the
// printable area" (return_code -104) because the skirt falls off the edge.
// Arrange only translates the merged mesh rigidly, so the packing survives.
func RunPlateSlice(
	ctx context.Context, bambuRoot string, profiles ResolvedProfiles,
	stlPath string, infillPct float64, settings SliceSettings,
	outdir string, timeout time.Duration,
) (SliceOutput, error) {
	enableSupport := 0
	if settings.SupportEnabled() {
		enableSupport = 1
	}
	appRun := filepath.Join(bambuRoot, "AppRun")
	args := []string{
		"-a", appRun,
		"--arrange", "1",
		"--orient", "0",
		"--slice", "0",
		fmt.Sprintf("--enable-support=%d", enableSupport),
		fmt.Sprintf("--sparse-infill-density=%g%%", infillPct),
		"--outputdir", outdir,
		"--export-3mf", exportName,
	}
	args = append(args, settings.Flags()...)

	// A multi-extruder machine cannot be driven by presets alone: its
	// filament-to-nozzle map exists only inside a project file, and without one
	// the slicer stops at return_code -66. Inject the plate into the machine's
	// settings template and slice that. See project3mf.go.
	input := stlPath
	if HasPlateTemplate(profiles.Family, profiles.NozzleMM) {
		input = filepath.Join(outdir, plateProjectName)
		if err := BuildPlateProject(profiles.Family, profiles.NozzleMM, stlPath, input); err != nil {
			return SliceOutput{}, fmt.Errorf("build plate project: %w", err)
		}
	} else {
		args = append(args,
			"--load-settings", profiles.MachinePath+";"+profiles.ProcessPath,
			"--load-filaments", profiles.FilamentPath,
		)
	}
	args = append(args, input)

	return runBambu(ctx, args, outdir, timeout)
}

// runBambu executes the slicer and turns its output into a SliceOutput. Shared by
// the design and plate paths so both report failures the same way.
func runBambu(ctx context.Context, args []string, outdir string, timeout time.Duration) (SliceOutput, error) {
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(runCtx, "xvfb-run", args...)
	out, runErr := cmd.CombinedOutput()
	if runCtx.Err() == context.DeadlineExceeded {
		return SliceOutput{}, fmt.Errorf("slice timed out after %s", timeout)
	}

	resultPath := filepath.Join(outdir, resultName)
	if _, err := os.Stat(resultPath); err != nil {
		return SliceOutput{}, fmt.Errorf("slicer produced no result.json (%v): %s", runErr, tail(string(out)))
	}
	// A run can write result.json but still fail to export the G-code (a bad slice,
	// an unsupported model). Surface the real reason.
	gcodePath := filepath.Join(outdir, exportName)
	if _, err := os.Stat(gcodePath); err != nil {
		reason := sliceErrorReason(resultPath)
		if reason == "" {
			reason = tail(string(out))
		}
		return SliceOutput{}, fmt.Errorf("slice produced no G-code: %s", reason)
	}
	return SliceOutput{ResultJSONPath: resultPath, Gcode3mfPath: gcodePath}, nil
}

// sliceErrorReason reads the slicer's own failure reason from result.json.
func sliceErrorReason(resultPath string) string {
	result, err := LoadResultJSON(resultPath)
	if err != nil {
		return ""
	}
	if result.ReturnCode == nil || *result.ReturnCode == 0 {
		return ""
	}
	msg := result.ErrorString
	if msg == "" {
		msg = "no message"
	}
	return fmt.Sprintf("return_code %d: %s", *result.ReturnCode, msg)
}

func tail(s string) string {
	s = strings.TrimSpace(s)
	const limit = 300
	if len(s) > limit {
		return s[len(s)-limit:]
	}
	if s == "" {
		return "no output"
	}
	return s
}
