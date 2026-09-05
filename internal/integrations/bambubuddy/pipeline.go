package bambubuddy

// Slicer pipelines: letting BambuBuddy slice a plate and queue it.
//
// The other write path (UploadFile + QueueForPrinting) needs a file that is
// ALREADY sliced, and a sliced file is bound to the machine it was sliced for.
// That binding is the problem this replaces: a plate sliced for an H2C cannot
// run on a free A2L, and cannot run on the P2S that happens to hold the right
// colours, so the bed waits for one specific machine while others sit idle.
//
// A pipeline is a saved {printer, process, filament-per-AMS-slot, bed} triplet
// configured in BambuBuddy. Running one against an UNSLICED model slices it for
// that pipeline's printer and puts the result in the queue, in a single call.
// Trying each pipeline in turn is therefore how a plate finds a machine: the
// first one that accepts it is one that can actually print it.
//
// The filament check is BambuBuddy's, not ours, and that is the point. It
// compares the plate's own requirements against real AMS trays, both in its own
// vocabulary - where Tensor's palette and the trays disagree on what "Blue" is.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
)

// Pipeline is one saved slicing configuration.
type Pipeline struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
	// TargetKind is "specific_printer" or "printer_class".
	TargetKind string `json:"target_kind"`
	// TargetModelClass is the printer model a class-targeted pipeline slices
	// for, e.g. "H2C". Null on one pinned to a specific printer.
	TargetModelClass *string `json:"target_model_class"`
	TargetPrinterID  *int    `json:"target_printer_id"`
}

// ListPipelines returns every configured pipeline.
//
// An install with none configured cannot slice anything, which is a setup
// problem rather than a runtime one - the caller reports it as such instead of
// silently doing nothing.
func (c *Client) ListPipelines(ctx context.Context) ([]Pipeline, error) {
	var out struct {
		Pipelines []Pipeline `json:"pipelines"`
	}
	if err := c.get(ctx, "/api/v1/slicer-pipelines/", &out); err != nil {
		return nil, err
	}
	return out.Pipelines, nil
}

// EligibilityIssue is one reason a pipeline cannot print a plate.
//
// A filament mismatch names the AMS slot and both colours, which is exactly what
// somebody standing at the printer needs: "slot 2 wants this, holds that".
type EligibilityIssue struct {
	Kind      string  `json:"kind"`
	SlotIndex *int    `json:"slot_index"`
	Expected  *string `json:"expected"`
	Actual    *string `json:"actual"`
}

// String renders one issue for an operator.
func (i EligibilityIssue) String() string {
	s := i.Kind
	if i.SlotIndex != nil {
		s = fmt.Sprintf("%s (slot %d)", s, *i.SlotIndex)
	}
	switch {
	case i.Expected != nil && i.Actual != nil:
		return fmt.Sprintf("%s: needs %s, has %s", s, *i.Expected, *i.Actual)
	case i.Expected != nil:
		return fmt.Sprintf("%s: needs %s", s, *i.Expected)
	default:
		return s
	}
}

// EligibilityReport is BambuBuddy's answer to "can this pipeline print this
// plate?", returned with a 409 when it cannot.
type EligibilityReport struct {
	OK                bool               `json:"ok"`
	TargetKind        string             `json:"target_kind"`
	TargetPrinterID   *int               `json:"target_printer_id"`
	TargetPrinterName *string            `json:"target_printer_name"`
	TargetModelClass  *string            `json:"target_model_class"`
	Issues            []EligibilityIssue `json:"issues"`
}

// PipelineJob is one copy within a run: which printer took it, and its queue
// entry.
type PipelineJob struct {
	AssignedPrinterID   *int    `json:"assigned_printer_id"`
	AssignedPrinterName *string `json:"assigned_printer_name"`
	// QueueEntryID is the print-queue item this copy became. Null until the run
	// has actually queued it.
	QueueEntryID *int    `json:"queue_entry_id"`
	Status       string  `json:"status"`
	ErrorMessage *string `json:"error_message"`
}

// PipelineRun is what running a pipeline produced: the slice, and the queue
// entries it created.
type PipelineRun struct {
	ID           int     `json:"id"`
	PipelineID   *int    `json:"pipeline_id"`
	PipelineName *string `json:"pipeline_name"`
	Status       string  `json:"status"`
	// SliceJobID and SlicedLibraryFileID identify the slice this run made. The
	// sliced file is a NEW library file - the uploaded model is left alone, so
	// the same plate can be re-sliced for a different printer later.
	SliceJobID          *int          `json:"slice_job_id"`
	SlicedLibraryFileID *int          `json:"sliced_library_file_id"`
	ErrorMessage        *string       `json:"error_message"`
	Jobs                []PipelineJob `json:"jobs"`
	TargetPrinterID     *int          `json:"target_printer_id"`
	TargetModelClass    *string       `json:"target_model_class"`
}

// QueueEntryID is the first queue item this run created, or nil if it made none
// yet. A run of one copy makes at most one.
func (r PipelineRun) QueueEntryID() *int {
	for _, j := range r.Jobs {
		if j.QueueEntryID != nil {
			return j.QueueEntryID
		}
	}
	return nil
}

// PrinterName is the machine this run went to, for the operator's log.
func (r PipelineRun) PrinterName() string {
	for _, j := range r.Jobs {
		if j.AssignedPrinterName != nil && *j.AssignedPrinterName != "" {
			return *j.AssignedPrinterName
		}
	}
	return ""
}

// NotEligibleError means the pipeline cannot print this plate, with BambuBuddy's
// reasons. Distinct from a transport or server failure: the caller should try
// the next pipeline rather than retry this one.
type NotEligibleError struct {
	Pipeline string
	Report   EligibilityReport
}

func (e NotEligibleError) Error() string {
	if len(e.Report.Issues) == 0 {
		return fmt.Sprintf("%s cannot print this plate", e.Pipeline)
	}
	reasons := make([]string, 0, len(e.Report.Issues))
	for _, i := range e.Report.Issues {
		reasons = append(reasons, i.String())
	}
	return fmt.Sprintf("%s: %s", e.Pipeline, joinReasons(reasons))
}

// RunPipeline slices an uploaded model for one pipeline's printer and queues it.
//
// force is deliberately not exposed. Left false, BambuBuddy answers 409 with the
// eligibility report when anything blocks - which is precisely the check this
// relies on. Forcing past it would slice a plate for a machine that cannot print
// it, which is the failure mode the whole design exists to avoid.
func (c *Client) RunPipeline(ctx context.Context, pipeline Pipeline, libraryFileID int) (PipelineRun, error) {
	body, err := json.Marshal(map[string]any{
		"source_library_file_id": libraryFileID,
		"copies":                 1,
		"force":                  false,
	})
	if err != nil {
		return PipelineRun{}, err
	}
	url := fmt.Sprintf("%s/api/v1/slicer-pipelines/%d/run", c.baseURL, pipeline.ID)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return PipelineRun{}, err
	}
	req.Header.Set("X-API-Key", c.apiKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return PipelineRun{}, fmt.Errorf("bambubuddy run pipeline: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	// 409 is the documented "not eligible" answer and carries the report, so it
	// is decoded rather than treated as a bare rejection.
	if resp.StatusCode == http.StatusConflict {
		var report EligibilityReport
		if err := json.NewDecoder(resp.Body).Decode(&report); err != nil {
			return PipelineRun{}, NotEligibleError{Pipeline: pipeline.Name}
		}
		return PipelineRun{}, NotEligibleError{Pipeline: pipeline.Name, Report: report}
	}
	// 202 Accepted is the NORMAL answer here, not an edge case: slicing is
	// asynchronous, so BambuBuddy acknowledges the run and works on it. Treating
	// anything but 200/201 as a rejection reported a started run as a failure -
	// and, worse, sent the caller on to try the next pipeline, so one plate
	// started slicing on two printers at once. A batch is one physical print.
	if resp.StatusCode != http.StatusOK &&
		resp.StatusCode != http.StatusCreated &&
		resp.StatusCode != http.StatusAccepted {
		return PipelineRun{}, ReasonError{Reason: rejectionReason(resp.Body, resp.StatusCode)}
	}

	var out PipelineRun
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return PipelineRun{}, fmt.Errorf("bambubuddy run pipeline: decode response: %w", err)
	}
	return out, nil
}

// joinReasons renders a list of issues as one sentence.
func joinReasons(reasons []string) string {
	switch len(reasons) {
	case 0:
		return ""
	case 1:
		return reasons[0]
	}
	out := reasons[0]
	for _, r := range reasons[1:] {
		out += "; " + r
	}
	return out
}
