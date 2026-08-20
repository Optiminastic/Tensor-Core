package bambubuddy

// The one write path into BambuBuddy: upload a model file to its library and
// add it to the print queue.
//
// The rest of this package is deliberately read-only (see the package comment).
// This is the exception, and it stops short of the thing that actually matters:
// it queues work, it does not START a print. BambuBuddy's own queue is what an
// operator releases to the machine, so nothing here can put plastic on a bed
// without a human acting.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"path/filepath"
	"strings"
	"time"
)

// uploadTimeout is generous because this one is genuinely slow: a 3MF can run
// to tens of megabytes and BambuBuddy parses it (thumbnails, plates, filament
// requirements) before answering.
const uploadTimeout = 5 * time.Minute

// UploadedFile is BambuBuddy's record of a file added to its library.
type UploadedFile struct {
	ID       int    `json:"id"`
	Filename string `json:"filename"`
	FileType string `json:"file_type"`
	FileSize int64  `json:"file_size"`
	// DuplicateOf is set when BambuBuddy recognised identical content already in
	// the library. It is not an error - the existing file is reused - but it is
	// worth surfacing so an operator is not confused by an unchanged file count.
	DuplicateOf *int `json:"duplicate_of"`
}

// UploadFile sends one model file to BambuBuddy's library.
//
// The reader is streamed into a multipart body rather than buffered whole: a
// print file is large, and holding one in memory per concurrent upload is how a
// service falls over on a busy afternoon.
func (c *Client) UploadFile(ctx context.Context, filename string, r io.Reader) (UploadedFile, error) {
	pr, pw := io.Pipe()
	mw := multipart.NewWriter(pw)

	go func() {
		part, err := mw.CreateFormFile("file", filepath.Base(filename))
		if err != nil {
			_ = pw.CloseWithError(err)
			return
		}
		if _, err := io.Copy(part, r); err != nil {
			_ = pw.CloseWithError(err)
			return
		}
		_ = pw.CloseWithError(mw.Close())
	}()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/api/v1/library/files/", pr)
	if err != nil {
		return UploadedFile{}, err
	}
	req.Header.Set("X-API-Key", c.apiKey)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("Accept", "application/json")

	client := &http.Client{Timeout: uploadTimeout}
	resp, err := client.Do(req)
	if err != nil {
		return UploadedFile{}, fmt.Errorf("bambubuddy upload: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		// BambuBuddy's rejections are genuinely useful and worth passing on -
		// a raw .gcode comes back with a full explanation of why it needs to be
		// a .gcode.3mf container instead. Swallowing that and reporting only a
		// status code leaves the operator with no idea what to do differently.
		return UploadedFile{}, ReasonError{Reason: rejectionReason(resp.Body, resp.StatusCode)}
	}
	var out UploadedFile
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return UploadedFile{}, fmt.Errorf("bambubuddy upload: decode response: %w", err)
	}
	return out, nil
}

// maxReasonBytes bounds how much of an error body is read. A rejection is a
// sentence; anything longer is not an explanation and should not be pasted into
// a UI or a log.
const maxReasonBytes = 2 << 10

// rejectionReason extracts BambuBuddy's own explanation from an error response.
//
// FastAPI puts it in {"detail": "..."}. Falling back to the status code alone
// keeps this honest when the body is empty or some other shape - better a bare
// code than a confidently wrong message.
func rejectionReason(body io.Reader, status int) string {
	raw, err := io.ReadAll(io.LimitReader(body, maxReasonBytes))
	if err != nil || len(raw) == 0 {
		return fmt.Sprintf("BambuBuddy rejected the file (HTTP %d)", status)
	}
	var detail struct {
		Detail string `json:"detail"`
	}
	if err := json.Unmarshal(raw, &detail); err == nil && strings.TrimSpace(detail.Detail) != "" {
		return detail.Detail
	}
	return fmt.Sprintf("BambuBuddy rejected the file (HTTP %d)", status)
}

// ReasonError is a rejection BambuBuddy explained in its own words.
//
// The distinction matters at the UI boundary. BambuBuddy's reasons are written
// for an operator ("...they need a .gcode.3mf zip container...") and are worth
// showing verbatim. Everything else - a decode failure, a dropped connection -
// is an internal fault whose text means nothing to the person holding the file,
// and it leaked into the machine page as a raw Go type error before this
// existed. Callers show ReasonError and log the rest.
type ReasonError struct{ Reason string }

func (e ReasonError) Error() string { return e.Reason }

// QueuedItem is the print-queue entry created for a file.
type QueuedItem struct {
	ID     int    `json:"id"`
	Status string `json:"status"`
	// WaitingReason is BambuBuddy's own explanation of why an item has not
	// started - a busy printer, a filament check, the previous-success gate.
	WaitingReason string `json:"waiting_reason"`
}

// QueueOptions are the print settings applied to a queued item.
//
// These mirror BambuBuddy's own "Edit Queue Item" dialog, so a file sent from
// Tensor lands configured exactly as an operator would configure it by hand
// rather than on BambuBuddy's bare defaults.
type QueueOptions struct {
	// TargetModel routes to ANY idle printer of this model ("H2C"), which is
	// BambuBuddy's "Any H2C" mode. Left empty to target one specific printer.
	TargetModel string
	// PrinterID pins the item to one machine. Ignored when TargetModel is set -
	// the dialog treats these as either/or, and sending both would be asking
	// for two different things at once.
	PrinterID int
	// InsertAtTop is the dialog's ASAP: top of the queue, starting as soon as an
	// eligible printer is idle.
	InsertAtTop bool
	// RequirePreviousSuccess holds the item back if the previous print failed,
	// so a jammed nozzle or a failed first layer does not quietly consume the
	// rest of the queue.
	RequirePreviousSuccess bool
	// AutoOffAfter powers the printer down when the print finishes.
	AutoOffAfter bool
}

// QueueForPrinting adds a library file to the print queue with the given
// settings, and returns the queue item.
//
// This is what makes "print now if idle, queue otherwise" work, and it works by
// NOT implementing that rule here. BambuBuddy already runs a dispatcher - it
// owns the printer connection, the filament checks, the previous-success gate
// and settings like queue_shortest_first and queue_keep_bed_warm. Given a
// targeted item it starts the print when a machine is free and holds it when
// one is not.
//
// Tensor deciding "the machine looked idle a second ago, so start" would race
// that dispatcher and duplicate its rules, badly: two systems both believing
// they may start a print on the same bed. So the target is set and the decision
// is left where the knowledge is.
//
// manual_start is deliberately false: an item staged for manual start would sit
// there until somebody pressed a button in BambuBuddy, which is the opposite of
// what this is for.
func (c *Client) QueueForPrinting(ctx context.Context, libraryFileID int, opts QueueOptions) (QueuedItem, error) {
	payload := map[string]any{
		"library_file_id":          libraryFileID,
		"manual_start":             false,
		"insert_at_top":            opts.InsertAtTop,
		"require_previous_success": opts.RequirePreviousSuccess,
		"auto_off_after":           opts.AutoOffAfter,
	}
	if opts.TargetModel != "" {
		payload["target_model"] = opts.TargetModel
	} else if opts.PrinterID > 0 {
		payload["printer_id"] = opts.PrinterID
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return QueuedItem{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/api/v1/queue/", bytes.NewReader(body))
	if err != nil {
		return QueuedItem{}, err
	}
	req.Header.Set("X-API-Key", c.apiKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return QueuedItem{}, fmt.Errorf("bambubuddy queue: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		return QueuedItem{}, ReasonError{Reason: rejectionReason(resp.Body, resp.StatusCode)}
	}
	var out QueuedItem
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return QueuedItem{}, fmt.Errorf("bambubuddy queue: decode response: %w", err)
	}
	return out, nil
}
