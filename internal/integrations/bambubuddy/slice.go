package bambubuddy

// Slicing a library file on BambuBuddy, with Tensor dictating the parameters.
//
// This is the call that fixes two faults at once, and the reason both existed
// is the same: running a slicer PIPELINE passes only a file id, so everything
// about how the plate is sliced comes from whatever that pipeline was
// configured with.
//
//  1. A pipeline carries ONE filament preset. Tensor's plate declares two
//     slots - white for the plank bodies, the lettering colour for the rest -
//     and the slicer collapsed them to a single filament. Verified against this
//     shop's own files: the unsliced plate declared #FFFFFF and #1560BD, and the
//     sliced result declared #FFFFFF alone, 265g of it. The bed printed
//     monochrome.
//
//  2. The colours came from the plate, which carries Tensor's idea of blue
//     (#1560BD) rather than the spool actually loaded (#2850E0), so BambuBuddy's
//     filament check refused it.
//
// Passing filament_presets REPEATED PER SLOT and filament_colours taken from
// the chosen printer's own trays answers both. The same plate re-sliced that way
// came back declaring #FFFFFF 138.9g and #2850E0 131.9g - two real colours,
// matching two real spools.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

// PresetVal is one Bambu Studio preset, as a pipeline reports it.
//
// Both fields are strings even though ids look numeric ("1", "4"): a preset can
// equally be named ("Bambu PLA Basic @BBL A2L 0.4 nozzle"), and source says
// which namespace to look it up in - "local", "standard" or "cloud".
type PresetVal struct {
	Source string `json:"source"`
	ID     string `json:"id"`
}

// SliceRequest is what Tensor tells BambuBuddy to do with a plate.
type SliceRequest struct {
	PrinterPreset *PresetVal `json:"printer_preset,omitempty"`
	ProcessPreset *PresetVal `json:"process_preset,omitempty"`
	// FilamentPresets must hold ONE ENTRY PER PLATE SLOT. A single entry is what
	// made two-colour beds print in one colour; repeating the pipeline's preset
	// is what keeps the second slot alive.
	FilamentPresets []PresetVal `json:"filament_presets,omitempty"`
	// FilamentColours is "#RRGGBB" per slot, in the plate's own slot order,
	// taken from the chosen printer's real AMS trays. This is the whole point:
	// the sliced file then declares the colours that are physically loaded, so
	// the filament check compares a tray against itself.
	FilamentColours []string `json:"filament_colours,omitempty"`
	// ExportThreeMF asks for a .gcode.3mf rather than bare gcode - the queue
	// takes the former and it carries the filament declaration with it.
	ExportThreeMF bool `json:"export_3mf"`
	// UseEmbeddedSettings false: the settings above are the ones that apply.
	UseEmbeddedSettings bool `json:"use_embedded_settings"`
	// AutoOrient and AutoArrange are false because bedpack already placed every
	// part at an exact offset and meshio merged them there. Letting the slicer
	// rearrange the plate would discard that packing.
	AutoOrient  bool   `json:"auto_orient"`
	AutoArrange bool   `json:"auto_arrange"`
	BedType     string `json:"bed_type,omitempty"`
}

// SliceResult is the file a finished slice produced.
type SliceResult struct {
	// LibraryFileID is the NEW library file holding the sliced plate. The
	// source file is left alone, so the same plate can be sliced again for a
	// different printer.
	LibraryFileID    int     `json:"library_file_id"`
	Name             string  `json:"name"`
	PrintTimeSeconds int     `json:"print_time_seconds"`
	FilamentUsedG    float64 `json:"filament_used_g"`
}

// SliceJob is a slice in progress or finished.
//
// The id field is job_id, not id, on both the POST response and the status read.
type SliceJob struct {
	JobID  int    `json:"job_id"`
	Status string `json:"status"`
	// ErrorMessage is BambuBuddy's own words when a slice fails; the field name
	// is not documented, so an empty value is not proof of success - Failed
	// consults Status too.
	ErrorMessage string       `json:"error_message"`
	Result       *SliceResult `json:"result"`
}

// Done reports whether the slice produced a file.
//
// Keyed on the RESULT, not the status string: a terminal status with no file is
// a failure however it is spelled, and the caller needs a file id to queue.
func (j SliceJob) Done() bool {
	return j.Result != nil && j.Result.LibraryFileID > 0
}

// Failed reports whether waiting any longer is pointless.
//
// An unrecognised status counts as STILL WORKING, matching PipelineRun.Finished:
// giving up on a slice that was merely slow is the more expensive mistake, and
// River's attempt limit bounds the wait anyway.
func (j SliceJob) Failed() bool {
	switch strings.ToLower(strings.TrimSpace(j.Status)) {
	case "failed", "error", "cancelled", "canceled":
		return true
	}
	return strings.TrimSpace(j.ErrorMessage) != ""
}

// FilamentRequirement is one slot the sliced plate needs loaded.
type FilamentRequirement struct {
	SlotID      int     `json:"slot_id"`
	Type        string  `json:"type"`
	Colour      string  `json:"color"`
	UsedGrams   float64 `json:"used_grams"`
	TrayInfoIdx string  `json:"tray_info_idx"`
	UsedInPlate bool    `json:"used_in_plate"`
}

// FilamentRequirements is what a file asks for, read back from the file itself.
//
// Worth reading after slicing rather than trusting the request: it is the only
// way to see what the slicer actually produced, and it is what caught a
// two-colour plate coming back with one filament.
type FilamentRequirements struct {
	FileID    int                   `json:"file_id"`
	Filename  string                `json:"filename"`
	Filaments []FilamentRequirement `json:"filaments"`
}

// Colours lists the required colours in slot order.
func (r FilamentRequirements) Colours() []string {
	out := make([]string, 0, len(r.Filaments))
	for _, f := range r.Filaments {
		out = append(out, f.Colour)
	}
	return out
}

// Types lists the required filament types in slot order, for the queue's own
// eligibility check.
func (r FilamentRequirements) Types() []string {
	out := make([]string, 0, len(r.Filaments))
	for _, f := range r.Filaments {
		out = append(out, f.Type)
	}
	return out
}

// SliceFile asks BambuBuddy to slice one library file.
//
// Answers 202 with the job barely started - slicing is asynchronous - so the
// caller polls GetSliceJob rather than expecting a file back.
func (c *Client) SliceFile(ctx context.Context, fileID int, req SliceRequest) (SliceJob, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return SliceJob{}, err
	}
	url := fmt.Sprintf("%s/api/v1/library/files/%d/slice", c.baseURL, fileID)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return SliceJob{}, err
	}
	httpReq.Header.Set("X-API-Key", c.apiKey)
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "application/json")

	resp, err := c.http.Do(httpReq)
	if err != nil {
		return SliceJob{}, fmt.Errorf("bambubuddy slice file %d: %w", fileID, err)
	}
	defer func() { _ = resp.Body.Close() }()

	// 202 is the NORMAL answer, not an edge case.
	if resp.StatusCode != http.StatusOK &&
		resp.StatusCode != http.StatusCreated &&
		resp.StatusCode != http.StatusAccepted {
		return SliceJob{}, ReasonError{Reason: rejectionReason(resp.Body, resp.StatusCode)}
	}

	var out SliceJob
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return SliceJob{}, fmt.Errorf("bambubuddy slice file %d: decode response: %w", fileID, err)
	}
	return out, nil
}

// GetSliceJob reads a slice's progress.
func (c *Client) GetSliceJob(ctx context.Context, jobID int) (SliceJob, error) {
	var out SliceJob
	if err := c.get(ctx, fmt.Sprintf("/api/v1/slice-jobs/%d", jobID), &out); err != nil {
		return SliceJob{}, err
	}
	return out, nil
}

// FilamentRequirements reads what a file needs loaded, slot by slot.
func (c *Client) FilamentRequirements(ctx context.Context, fileID int) (FilamentRequirements, error) {
	var out FilamentRequirements
	path := fmt.Sprintf("/api/v1/library/files/%d/filament-requirements", fileID)
	if err := c.get(ctx, path, &out); err != nil {
		return FilamentRequirements{}, err
	}
	return out, nil
}
