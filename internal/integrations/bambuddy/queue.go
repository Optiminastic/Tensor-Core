package bambuddy

// The print queue: BamBuddy's canonical way to start a print. Its own docs
// describe the direct library/archive print endpoints as legacy and point here,
// and only this route accepts the plate/AMS/calibration options.

import (
	"context"
	"fmt"
	"net/http"
	"time"
)

// Queue item statuses, as returned in QueueItem.Status.
const (
	StatusPending   = "pending"
	StatusPrinting  = "printing"
	StatusCompleted = "completed"
	StatusFailed    = "failed"
	StatusSkipped   = "skipped"
	StatusCancelled = "cancelled"
)

// Terminal reports whether a queue status will not change again, so a poller
// knows when to stop watching an item.
func Terminal(status string) bool {
	switch status {
	case StatusCompleted, StatusFailed, StatusSkipped, StatusCancelled:
		return true
	default:
		return false
	}
}

// QueueRequest is the subset of POST /queue/ Tensor sets. BamBuddy marks no
// field required and defaults the rest, so anything omitted keeps its default.
type QueueRequest struct {
	// LibraryFileID points at an uploaded file. BamBuddy also accepts archive_id
	// instead; Tensor always uploads, so only this is modelled.
	LibraryFileID int `json:"library_file_id"`
	// PrinterID targets one printer. Omitted (zero) lets BamBuddy's own scheduler
	// choose, which Tensor does not want: the batch already names its machine.
	PrinterID int `json:"printer_id,omitempty"`
	// PlateID selects a plate inside a multi-plate 3MF. Tensor's plates are
	// single-plate, so this stays unset.
	PlateID *int `json:"plate_id,omitempty"`
	// AMSMapping maps model slots to AMS trays. Unset means BamBuddy decides.
	AMSMapping []int `json:"ams_mapping,omitempty"`
	Quantity   int   `json:"quantity,omitempty"`
	// ManualStart stages the job without starting it: the file lands on the
	// printer's queue and waits for a person. This is Tensor's safety valve
	// against an unattended print from a bad SKU-to-design mapping.
	ManualStart bool `json:"manual_start"`
	// SkipFilamentCheck bypasses the deficit check that otherwise 409s a start.
	SkipFilamentCheck bool `json:"skip_filament_check,omitempty"`
}

// QueueItem is BamBuddy's queue record. Optional fields are pointers because the
// API returns explicit nulls, and "absent" is meaningfully different from zero
// (a nil FilamentUsedGrams is "not reported yet", not "used none").
type QueueItem struct {
	ID                int        `json:"id"`
	PrinterID         *int       `json:"printer_id"`
	LibraryFileID     *int       `json:"library_file_id"`
	ArchiveID         *int       `json:"archive_id"`
	Position          int        `json:"position"`
	Status            string     `json:"status"`
	WaitingReason     *string    `json:"waiting_reason"`
	FilamentShort     bool       `json:"filament_short"`
	StartedAt         *time.Time `json:"started_at"`
	CompletedAt       *time.Time `json:"completed_at"`
	ErrorMessage      *string    `json:"error_message"`
	PrintTimeSeconds  *int       `json:"print_time_seconds"`
	FilamentUsedGrams *float64   `json:"filament_used_grams"`
	PrinterName       *string    `json:"printer_name"`
}

// AMSTray is one filament slot in an AMS unit. Remain is a PERCENTAGE, not
// grams - the printer reports how full the spool is, not how much is left by
// weight, and treating it as grams would silently corrupt inventory.
type AMSTray struct {
	ID        int     `json:"id"`
	TrayColor *string `json:"tray_color"`
	TrayType  *string `json:"tray_type"`
	Remain    int     `json:"remain"`
	Exists    *bool   `json:"exists"`
}

// AMSUnit is one AMS enclosure and its trays.
type AMSUnit struct {
	ID   int       `json:"id"`
	Tray []AMSTray `json:"tray"`
}

// PrinterStatus is the live view of one printer. Only id, name and connected are
// guaranteed; everything else is null while the printer is offline or idle.
type PrinterStatus struct {
	ID            int       `json:"id"`
	Name          string    `json:"name"`
	Connected     bool      `json:"connected"`
	State         *string   `json:"state"`
	CurrentPrint  *string   `json:"current_print"`
	SubtaskName   *string   `json:"subtask_name"`
	GcodeFile     *string   `json:"gcode_file"`
	Progress      *float64  `json:"progress"`
	RemainingTime *int      `json:"remaining_time"`
	LayerNum      *int      `json:"layer_num"`
	TotalLayers   *int      `json:"total_layers"`
	AMS           []AMSUnit `json:"ams"`
}

// AddToQueue enqueues an uploaded file on a printer and returns the created
// queue item.
//
// Not retried on a 4xx by construction: a duplicate enqueue would put the same
// plate on the printer twice, so only transport failures and 5xx (where the
// request may never have been processed) are replayed by the retry policy.
func (c *Client) AddToQueue(ctx context.Context, apiKey string, req QueueRequest) (QueueItem, error) {
	if req.LibraryFileID == 0 {
		return QueueItem{}, apiErr("cannot queue a print without a library file id")
	}
	var out QueueItem
	if err := c.doJSON(ctx, http.MethodPost, "/queue/", apiKey, req, &out); err != nil {
		return QueueItem{}, err
	}
	if out.ID == 0 {
		return QueueItem{}, apiErr("BamBuddy accepted the print but returned no queue item id")
	}
	return out, nil
}

// GetQueueItem reads one queue item - the primary signal for how a dispatched
// plate is progressing.
func (c *Client) GetQueueItem(ctx context.Context, apiKey string, itemID int) (QueueItem, error) {
	var out QueueItem
	path := fmt.Sprintf("/queue/%d", itemID)
	if err := c.doJSON(ctx, http.MethodGet, path, apiKey, nil, &out); err != nil {
		return QueueItem{}, err
	}
	return out, nil
}

// GetPrinterStatus reads a printer's live state: layer progress, remaining time
// and connectivity, which is what Tensor's fleet view shows.
func (c *Client) GetPrinterStatus(ctx context.Context, apiKey string, printerID int) (PrinterStatus, error) {
	var out PrinterStatus
	path := fmt.Sprintf("/printers/%d/status", printerID)
	if err := c.doJSON(ctx, http.MethodGet, path, apiKey, nil, &out); err != nil {
		return PrinterStatus{}, err
	}
	return out, nil
}

// StartQueueItem releases a staged (manual_start) item so the scheduler picks it
// up. Tensor does not call this on the automatic path - staging is the whole
// point of the safety valve - but it is what a "start it anyway" action wires to.
//
// A filament deficit answers 409, surfaced as ErrFilamentDeficit; retrying with
// skipFilamentCheck true is the deliberate override.
func (c *Client) StartQueueItem(ctx context.Context, apiKey string, itemID int, skipFilamentCheck bool) error {
	path := fmt.Sprintf("/queue/%d/start", itemID)
	if skipFilamentCheck {
		path += "?skip_filament_check=true"
	}
	return c.doJSON(ctx, http.MethodPost, path, apiKey, nil, nil)
}
