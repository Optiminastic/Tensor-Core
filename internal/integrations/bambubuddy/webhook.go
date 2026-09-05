package bambubuddy

// BambuBuddy's webhook endpoints: the cheap way to ask what the fleet is doing
// right now.
//
// They answer the same questions as /api/v1/printers/{id}/status and
// /api/v1/queue/, in a fraction of the bytes:
//
//	GET /api/v1/webhook/printer/{id}/status  -> id, name, connected, state,
//	                                            current_print, progress,
//	                                            remaining_time
//	GET /api/v1/webhook/queue                -> EVERY printer's pending and
//	                                            printing counts, in ONE call
//
// The full status carries nozzles, AMS trays, temperatures, HMS errors, fan
// speeds and firmware - the right payload for the 60-second sync that writes
// machines.filaments, and far too much for a poll that only wants to know how
// long is left on the current print. The queue endpoint is the bigger win: the
// scheduler needs each machine's queue DEPTH, and getting it used to mean
// pulling every queue item in the fleet and grouping them by printer.
//
// They are called "webhook" endpoints because they are what BambuBuddy exposes
// to outside callers holding an API key. Nothing is pushed - these are pulls,
// and Tensor polls them.

import (
	"context"
	"fmt"
)

// WebhookStatus is one printer's live state, as the light endpoint reports it.
type WebhookStatus struct {
	ID        int    `json:"id"`
	Name      string `json:"name"`
	Connected bool   `json:"connected"`
	// State is BambuBuddy's own word, upper case: "RUNNING", "IDLE", "FAILED".
	// Deliberately not mapped to Tensor's fleet states here - that mapping
	// belongs with the caller that stores them.
	State string `json:"state"`
	// CurrentPrint is the plate's filename, which for a Tensor-built bed is its
	// orders and colour: "114647-114649-...-BLUE_plate_1".
	CurrentPrint string `json:"current_print"`
	// Progress is 0-100.
	Progress float64 `json:"progress"`
	// RemainingTime is minutes left on the current print, 0 when idle. This is
	// the number every projection of "when does this machine free up" rests on.
	RemainingTime int `json:"remaining_time"`
}

// Printing reports whether the machine is laying plastic right now.
func (w WebhookStatus) Printing() bool { return w.State == "RUNNING" }

// WebhookQueueStatus is one printer's queue depth.
type WebhookQueueStatus struct {
	PrinterID   int    `json:"printer_id"`
	PrinterName string `json:"printer_name"`
	// Pending is waiting; Printing is running now. Their sum is what the
	// scheduler charges a machine for.
	Pending  int                  `json:"pending"`
	Printing int                  `json:"printing"`
	Items    []WebhookQueueItemID `json:"items"`
}

// Depth is how much work is on this machine, queued or running.
func (q WebhookQueueStatus) Depth() int { return q.Pending + q.Printing }

// WebhookQueueItemID identifies one item on a printer's queue. Deliberately
// thin - the full item (print time, filament, model) comes from ListQueue when
// something needs it.
type WebhookQueueItemID struct {
	ID        int    `json:"id"`
	ArchiveID *int   `json:"archive_id"`
	LibraryID *int   `json:"library_file_id"`
	Position  int    `json:"position"`
	Status    string `json:"status"`
}

// WebhookPrinterStatus reads one printer's live state.
//
// Preferred over GetStatus for anything polled: the same connected/state/
// remaining-time answer, without the AMS trays, temperatures and HMS log that
// only the fleet sync has a use for.
func (c *Client) WebhookPrinterStatus(ctx context.Context, printerID int) (WebhookStatus, error) {
	var out WebhookStatus
	if err := c.get(ctx, fmt.Sprintf("/api/v1/webhook/printer/%d/status", printerID), &out); err != nil {
		return WebhookStatus{}, err
	}
	return out, nil
}

// WebhookQueueDepths reads every printer's queue depth in one call.
//
// One request for the whole fleet, which is the point: the scheduler asks this
// of every machine on every planning run, and doing it by pulling the entire
// queue and grouping by printer meant moving every queued item's print time,
// filament and thumbnail path across the tailnet to count rows.
func (c *Client) WebhookQueueDepths(ctx context.Context) ([]WebhookQueueStatus, error) {
	var out []WebhookQueueStatus
	if err := c.get(ctx, "/api/v1/webhook/queue", &out); err != nil {
		return nil, err
	}
	return out, nil
}
