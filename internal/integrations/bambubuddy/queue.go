package bambubuddy

// BambuBuddy's print queue - the beds waiting to go onto a printer.
//
// Tensor has a queue of its own (batches), and the two are deliberately not the
// same thing. A Tensor batch is a plan; a BambuBuddy queue item is that plan
// handed over, sitting in front of a specific machine with a specific filament
// mapping. Once a plate is sent, BambuBuddy is the authority on what happens to
// it, so this is read and never mirrored into a table: a copy would be wrong
// the moment somebody reordered the queue in BambuBuddy's own UI.

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// Queue item statuses, as BambuBuddy reports them.
const (
	QueuePending   = "pending"
	QueuePrinting  = "printing"
	QueueCompleted = "completed"
	QueueCancelled = "cancelled"
	QueueFailed    = "failed"
)

// QueueItem is one plate in BambuBuddy's queue.
//
// A subset of what BambuBuddy stores: it also carries calibration flags, gcode
// injection, timelapse and preheat overrides, which are settings for the print
// rather than facts about it and have no place on a status board.
type QueueItem struct {
	ID       int `json:"id"`
	Position int `json:"position"`
	// PrinterID and PrinterName are the machine this item is bound to. An item
	// can be queued without one, waiting for any machine that fits.
	PrinterID   *int   `json:"printer_id"`
	PrinterName string `json:"printer_name"`
	Status      string `json:"status"`
	// WaitingReason is BambuBuddy's own explanation for an item that is not
	// moving - the filament it wants, the print it is waiting to follow.
	WaitingReason string `json:"waiting_reason"`
	ErrorMessage  string `json:"error_message"`

	// The plate itself. A queue item comes either from the library or from an
	// archived print, so exactly one of these pairs is populated.
	LibraryFileID        *int   `json:"library_file_id"`
	LibraryFileName      string `json:"library_file_name"`
	LibraryFileThumbnail string `json:"library_file_thumbnail"`
	ArchiveID            *int   `json:"archive_id"`
	ArchiveName          string `json:"archive_name"`
	ArchiveThumbnail     string `json:"archive_thumbnail"`

	PrintTimeSeconds  int     `json:"print_time_seconds"`
	FilamentUsedGrams float64 `json:"filament_used_grams"`
	FilamentType      string  `json:"filament_type"`
	// FilamentColour is comma-separated when a plate uses more than one, e.g.
	// "#FFFFFF,#D3B7A7".
	FilamentColour string   `json:"filament_color"`
	EstimatedCost  *float64 `json:"estimated_cost"`
	NozzleDiameter *float64 `json:"nozzle_diameter"`
	LayerHeight    *float64 `json:"layer_height"`
	BedType        string   `json:"bed_type"`
	// SlicedForModel is the printer model the plate was sliced for. A plate
	// cannot run on a different model, so this is what makes an item's machine
	// binding meaningful rather than arbitrary.
	SlicedForModel string `json:"sliced_for_model"`
	TargetModel    string `json:"target_model"`

	BatchID           *int   `json:"batch_id"`
	BatchName         string `json:"batch_name"`
	CreatedByUsername string `json:"created_by_username"`
	CreatedAt         string `json:"created_at"`
	StartedAt         string `json:"started_at"`
	CompletedAt       string `json:"completed_at"`
}

// Name is the plate's filename, whichever source it came from.
func (q QueueItem) Name() string {
	if n := strings.TrimSpace(q.LibraryFileName); n != "" {
		return n
	}
	if n := strings.TrimSpace(q.ArchiveName); n != "" {
		return n
	}
	return "Untitled plate"
}

// Colours splits the comma-separated colour field into individual swatches.
//
// Multi-material plates report every colour in one string; rendering that raw
// would show "#FFFFFF,#D3B7A7" where an operator expects two chips.
func (q QueueItem) Colours() []string {
	if strings.TrimSpace(q.FilamentColour) == "" {
		return nil
	}
	var out []string
	for _, c := range strings.Split(q.FilamentColour, ",") {
		if c = strings.TrimSpace(c); c != "" {
			out = append(out, c)
		}
	}
	return out
}

// Waiting reports whether the item has yet to start.
func (q QueueItem) Waiting() bool {
	return q.Status == QueuePending || q.Status == QueuePrinting
}

// ListQueue returns every item in BambuBuddy's print queue.
//
// Unpaginated because the endpoint answers with the whole queue, and a queue
// that grew past one response would be a scheduling problem long before it was
// a pagination one.
func (c *Client) ListQueue(ctx context.Context) ([]QueueItem, error) {
	var out []QueueItem
	if err := c.get(ctx, "/api/v1/queue/", &out); err != nil {
		return nil, err
	}
	return out, nil
}

// Thumbnail fetches a queue item's plate preview.
//
// Returned as bytes rather than a URL because BambuBuddy's own thumbnail routes
// need the API key and sit on a tailnet the browser is not on. Small images
// (a few KB), so buffering is simpler than plumbing a stream through and there
// is nothing to gain from the complexity.
func (c *Client) Thumbnail(ctx context.Context, kind string, id int) ([]byte, string, error) {
	var path string
	switch kind {
	case "library":
		path = fmt.Sprintf("/api/v1/library/files/%d/thumbnail", id)
	case "archive":
		path = fmt.Sprintf("/api/v1/archives/%d/thumbnail", id)
	default:
		return nil, "", ReasonError{Reason: "Unknown thumbnail source."}
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("X-API-Key", c.apiKey)

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, "", fmt.Errorf("bambubuddy thumbnail: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, "", ReasonError{Reason: rejectionReason(resp.Body, resp.StatusCode)}
	}
	// Capped so a wrong id cannot pull an arbitrarily large body into memory.
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxThumbnailBytes))
	if err != nil {
		return nil, "", err
	}
	return body, resp.Header.Get("Content-Type"), nil
}

// maxThumbnailBytes bounds a plate preview. BambuBuddy's are PNGs of a few KB;
// this is generous headroom, not a target.
const maxThumbnailBytes = 4 << 20

// GetQueueItem reads one item, so a caller can see what state it is in before
// acting on it.
//
// Read fresh rather than taken from a cached list: withdrawing a plate turns on
// what the printer is doing RIGHT NOW, and a status that was true a minute ago
// is exactly the kind of thing that cancels a print already halfway through.
func (c *Client) GetQueueItem(ctx context.Context, itemID int) (QueueItem, error) {
	var out QueueItem
	if err := c.get(ctx, fmt.Sprintf("/api/v1/queue/%d", itemID), &out); err != nil {
		return QueueItem{}, err
	}
	return out, nil
}

// RemoveQueueItem takes a plate back out of BambuBuddy's queue.
//
// What makes a locked bed editable: a bed already handed over cannot simply be
// re-planned in Tensor, because the plate it describes is sitting in front of a
// machine. Withdrawing it first is what turns "you may not edit this" into "the
// corrected plate goes back in the queue".
//
// A 404 is success. The item being gone is the state this asks for, and
// somebody removing it in BambuBuddy's own UI a moment earlier is not an error
// worth surfacing to whoever is editing the bed.
func (c *Client) RemoveQueueItem(ctx context.Context, itemID int) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete,
		fmt.Sprintf("%s/api/v1/queue/%d", c.baseURL, itemID), nil)
	if err != nil {
		return err
	}
	req.Header.Set("X-API-Key", c.apiKey)
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("bambubuddy remove queue item %d: %w", itemID, err)
	}
	defer func() { _ = resp.Body.Close() }()

	switch {
	case resp.StatusCode == http.StatusNotFound:
		return nil
	case resp.StatusCode >= 300:
		// The status alone, for the reason c.get gives: the body can echo the
		// request back, API key included.
		return fmt.Errorf("bambubuddy remove queue item %d: HTTP %d", itemID, resp.StatusCode)
	}
	return nil
}
