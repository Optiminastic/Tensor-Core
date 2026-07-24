// Package slicing is the Go side of the slice queue: it hands a job to the Python
// dispatcher over authenticated HTTP. Go never speaks the Celery protocol itself;
// the dispatcher owns enqueue and concurrency.
package slicing

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// Specs are the slice inputs the worker needs (a subset of the design's answers).
type Specs struct {
	Material    string  `json:"material"`
	Quality     string  `json:"quality"`
	UnitsPerBed int     `json:"units_per_bed"`
	InfillPct   float64 `json:"infill_pct"`
}

// Dispatcher posts jobs to the slicer worker's FastAPI dispatcher.
type Dispatcher struct {
	baseURL string
	secret  string
	http    *http.Client
}

// NewDispatcher builds a dispatcher client. A short timeout is enough: /jobs only
// enqueues and returns 202, the slice itself runs asynchronously.
func NewDispatcher(baseURL, secret string) *Dispatcher {
	return &Dispatcher{
		baseURL: baseURL,
		secret:  secret,
		http:    &http.Client{Timeout: 10 * time.Second},
	}
}

type jobRequest struct {
	JobID  string `json:"job_id"`
	StlKey string `json:"stl_key"`
	Specs  Specs  `json:"specs"`
}

// Enqueue asks the worker to slice stlKey for the given job. It returns an error
// unless the dispatcher accepted the job (202).
func (d *Dispatcher) Enqueue(ctx context.Context, jobID, stlKey string, specs Specs) error {
	payload, err := json.Marshal(jobRequest{JobID: jobID, StlKey: stlKey, Specs: specs})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, d.baseURL+"/jobs", bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Internal-Secret", d.secret)

	resp, err := d.http.Do(req)
	if err != nil {
		return fmt.Errorf("dispatch: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusAccepted {
		return fmt.Errorf("dispatch: unexpected status %d", resp.StatusCode)
	}
	return nil
}
