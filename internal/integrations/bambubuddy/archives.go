package bambubuddy

// BambuBuddy's print history - what actually came off the beds.
//
// The counterpart to queue.go, and read the same way: through, never mirrored.
// A finished print is BambuBuddy's record. It knows how long the plate really
// took, how much filament it really used, and why it failed - none of which
// Tensor can derive from its own batch rows, because a batch says what was
// SENT and an archive says what HAPPENED.
//
// That gap is the reason this file exists. Machine Management's History tab
// used to list Tensor batches with status 'completed', which meant a plate that
// failed on the printer never appeared, and a plate somebody ran directly from
// BambuBuddy never appeared either. An operator looking at the history of the
// floor was shown the history of Tensor's intentions.

import (
	"context"
	"fmt"
	"net/url"
	"strings"
)

// Archive statuses, as BambuBuddy reports them. The same vocabulary as the
// queue, because an archive is what a queue item becomes.
const (
	ArchiveCompleted = "completed"
	ArchiveFailed    = "failed"
	ArchivePrinting  = "printing"
	ArchiveCancelled = "cancelled"
	// ArchiveArchived is a plate BambuBuddy has filed away rather than one it
	// watched run - it is the status most of a real archive carries, and
	// omitting it from Finished made the majority of the history look live.
	ArchiveArchived = "archived"
)

// Archive is one print BambuBuddy has a record of.
//
// A subset of what it stores: the full row also carries tags, photos,
// timelapses, project links, duplicate detection and F3D paths, which belong to
// BambuBuddy's own library UI rather than to a history board in Tensor.
type Archive struct {
	ID        int    `json:"id"`
	PrintName string `json:"print_name"`
	Filename  string `json:"filename"`
	Status    string `json:"status"`
	// PrinterID is the machine that ran it. Null for a plate archived without
	// ever being printed - uploaded, then never sent.
	PrinterID *int `json:"printer_id"`

	// Estimated versus actual. PrintTimeSeconds is what the slicer predicted;
	// ActualTimeSeconds is what the printer took. Showing the second where one
	// exists is the whole value of a history board - it is how anyone learns
	// that a bed consistently runs long.
	PrintTimeSeconds  int `json:"print_time_seconds"`
	ActualTimeSeconds int `json:"actual_time_seconds"`

	FilamentUsedGrams float64 `json:"filament_used_grams"`
	// TotalFilamentActualGrams is what the printer reported consuming, against
	// the slicer's estimate above.
	TotalFilamentActualGrams float64 `json:"total_filament_actual_grams"`
	FilamentType             string  `json:"filament_type"`
	// FilamentColour is comma-separated for a multi-material plate, the same
	// shape QueueItem uses - see Colours.
	FilamentColour string `json:"filament_color"`

	Cost       *float64 `json:"cost"`
	EnergyCost *float64 `json:"energy_cost"`
	EnergyKwh  *float64 `json:"energy_kwh"`

	Quantity       int      `json:"quantity"`
	LayerHeight    *float64 `json:"layer_height"`
	NozzleDiameter *float64 `json:"nozzle_diameter"`
	BedType        string   `json:"bed_type"`
	SlicedForModel string   `json:"sliced_for_model"`

	// RunCount and its two breakdowns: one archive can be reprinted, and the
	// counts are how a repeatedly-failing plate is spotted.
	RunCount           int `json:"run_count"`
	SuccessfulRunCount int `json:"successful_run_count"`
	FailedRunCount     int `json:"failed_run_count"`
	// FailureReason is BambuBuddy's own wording. Tensor has nothing better to
	// offer about a print it did not watch.
	FailureReason string `json:"failure_reason"`

	CreatedByUsername string `json:"created_by_username"`
	CreatedAt         string `json:"created_at"`
	StartedAt         string `json:"started_at"`
	CompletedAt       string `json:"completed_at"`
	LastRunAt         string `json:"last_run_at"`
}

// Name is the print's own name, falling back to the file it came from.
func (a Archive) Name() string {
	if n := strings.TrimSpace(a.PrintName); n != "" {
		return n
	}
	if n := strings.TrimSpace(a.Filename); n != "" {
		return n
	}
	return "Untitled print"
}

// Colours splits the comma-separated colour field into individual swatches,
// exactly as QueueItem.Colours does - the two fields carry the same shape and
// an operator should not meet two different renderings of one idea.
func (a Archive) Colours() []string {
	return splitColours(a.FilamentColour)
}

// Finished reports whether this print has stopped running, either way.
//
// A history board that omits failures is not a history; both belong, and the
// status is what distinguishes them.
func (a Archive) Finished() bool {
	switch a.Status {
	case ArchiveCompleted, ArchiveFailed, ArchiveCancelled, ArchiveArchived:
		return true
	}
	return false
}

// DurationSeconds is how long the print actually took, falling back to the
// slicer's estimate when the printer reported nothing.
//
// The fallback is deliberately not zero: an archive whose actual time never
// landed is far more usefully shown with its estimate than with a blank, and
// the caller can tell the two apart by comparing against ActualTimeSeconds.
func (a Archive) DurationSeconds() int {
	if a.ActualTimeSeconds > 0 {
		return a.ActualTimeSeconds
	}
	return a.PrintTimeSeconds
}

// FilamentGrams is what the print really consumed, falling back to the estimate
// for the same reason DurationSeconds does.
func (a Archive) FilamentGrams() float64 {
	if a.TotalFilamentActualGrams > 0 {
		return a.TotalFilamentActualGrams
	}
	return a.FilamentUsedGrams
}

// ListArchives returns BambuBuddy's print history, newest first.
//
// Bounded by limit because this list grows without end - every plate ever run
// stays in it - and a history board shows a page, not an archive. BambuBuddy
// orders newest-first itself, so the cap takes the recent end rather than an
// arbitrary slice.
func (c *Client) ListArchives(ctx context.Context, limit int) ([]Archive, error) {
	if limit <= 0 {
		limit = DefaultArchiveLimit
	}
	if limit > MaxArchiveLimit {
		limit = MaxArchiveLimit
	}
	q := url.Values{}
	q.Set("limit", fmt.Sprintf("%d", limit))

	var out []Archive
	if err := c.get(ctx, "/api/v1/archives/?"+q.Encode(), &out); err != nil {
		return nil, err
	}
	return out, nil
}

// How much history one read returns.
//
// The default is a screen's worth of scrolling rather than a number with any
// meaning of its own; the maximum exists so a caller cannot ask BambuBuddy to
// serialise its entire archive into one response on a tailnet link.
const (
	DefaultArchiveLimit = 100
	MaxArchiveLimit     = 500
)

// splitColours turns BambuBuddy's comma-separated colour field into swatches.
//
// Shared by Archive and QueueItem: a multi-material plate reports every colour
// in one string, and rendering that raw shows "#FFFFFF,#D3B7A7" where an
// operator expects two chips.
func splitColours(field string) []string {
	if strings.TrimSpace(field) == "" {
		return nil
	}
	var out []string
	for _, c := range strings.Split(field, ",") {
		if c = strings.TrimSpace(c); c != "" {
			out = append(out, c)
		}
	}
	return out
}
