package bambuddy

// Same-package tests so baseURL can be pointed at an httptest server, matching
// internal/integrations/shopify/client_test.go. Every BamBuddy response shape
// here is copied from its live OpenAPI spec.

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const testAPIKey = "bb_test_key"

// testClient builds a client pointed at handler.
func testClient(t *testing.T, handler http.Handler) *Client {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	c := New(srv.URL, 5*time.Second)
	c.baseURL = srv.URL
	return c
}

func TestAddToQueueSendsTheKeyAndReturnsTheItem(t *testing.T) {
	var gotAuth, gotPath, gotCT string
	var gotBody QueueRequest

	c := testClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth, gotPath, gotCT = r.Header.Get("Authorization"), r.URL.Path, r.Header.Get("Content-Type")
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":42,"printer_id":7,"library_file_id":9,"archive_id":null,
			"position":1,"status":"pending","waiting_reason":null,"filament_short":false,
			"started_at":null,"completed_at":null,"error_message":null,"printer_name":"H2S-1"}`))
	}))

	item, err := c.AddToQueue(context.Background(), testAPIKey, QueueRequest{
		LibraryFileID: 9, PrinterID: 7, ManualStart: true, Quantity: 1,
	})
	if err != nil {
		t.Fatalf("AddToQueue: %v", err)
	}

	if gotAuth != "Bearer "+testAPIKey {
		t.Errorf("Authorization = %q, want the bearer form of the API key", gotAuth)
	}
	if gotPath != "/api/v1/queue/" {
		t.Errorf("path = %q, want the versioned queue path", gotPath)
	}
	if gotCT != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", gotCT)
	}
	if !gotBody.ManualStart {
		t.Error("manual_start was not sent: the print would start unattended")
	}
	if gotBody.LibraryFileID != 9 || gotBody.PrinterID != 7 {
		t.Errorf("body targeted file=%d printer=%d, want 9/7", gotBody.LibraryFileID, gotBody.PrinterID)
	}
	if item.ID != 42 || item.Status != StatusPending {
		t.Errorf("item = %d/%s, want 42/pending", item.ID, item.Status)
	}
	if item.PrinterName == nil || *item.PrinterName != "H2S-1" {
		t.Errorf("printer_name = %v, want H2S-1", item.PrinterName)
	}
}

// Queueing without a file would enqueue a print of nothing; catch it before it
// reaches the shop floor.
func TestAddToQueueRejectsMissingFile(t *testing.T) {
	c := testClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("request should never have been sent")
	}))
	if _, err := c.AddToQueue(context.Background(), testAPIKey, QueueRequest{PrinterID: 1}); err == nil {
		t.Fatal("queueing without a library file id succeeded")
	}
}

// A 409 is the filament-deficit path. It must be distinguishable and must NOT be
// retried: only a human or an explicit policy decides to print anyway.
func TestFilamentDeficitIsTypedAndNotRetried(t *testing.T) {
	var calls atomic.Int32
	c := testClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte(`{"detail":{"deficit_grams":42.5,"slot":1}}`))
	}))

	err := c.StartQueueItem(context.Background(), testAPIKey, 42, false)
	if err == nil {
		t.Fatal("a filament deficit was reported as success")
	}
	if !errors.Is(err, ErrFilamentDeficit) {
		t.Fatalf("err = %v, want ErrFilamentDeficit", err)
	}
	if n := calls.Load(); n != 1 {
		t.Errorf("attempts = %d, want 1 - a deficit must not be retried", n)
	}
	if !strings.Contains(err.Error(), "deficit_grams") {
		t.Errorf("err %q drops the server's reason, which is what makes it actionable", err)
	}
}

// The override path: skip_filament_check must actually reach the server.
func TestStartQueueItemCanSkipTheFilamentCheck(t *testing.T) {
	var gotQuery string
	c := testClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		w.WriteHeader(http.StatusOK)
	}))
	if err := c.StartQueueItem(context.Background(), testAPIKey, 7, true); err != nil {
		t.Fatalf("StartQueueItem: %v", err)
	}
	if !strings.Contains(gotQuery, "skip_filament_check=true") {
		t.Errorf("query = %q, want skip_filament_check=true", gotQuery)
	}
}

// 401/403 is terminal: the same key will never be accepted, so retrying only
// delays a clear error. The message must name the scope that is usually missing.
func TestUnauthorizedIsTerminal(t *testing.T) {
	var calls atomic.Int32
	c := testClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusForbidden)
	}))

	_, err := c.GetQueueItem(context.Background(), testAPIKey, 1)
	if !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("err = %v, want ErrUnauthorized", err)
	}
	if n := calls.Load(); n != 1 {
		t.Errorf("attempts = %d, want 1 - a rejected key must not be retried", n)
	}
	if !strings.Contains(err.Error(), "can_control_printer") {
		t.Errorf("err %q does not mention the scope that defaults to false", err)
	}
}

// 429 is retried, and the server's Retry-After is honoured rather than fought.
func TestRateLimitIsRetried(t *testing.T) {
	var calls atomic.Int32
	c := testClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":5,"name":"H2S-1","connected":true}`))
	}))

	st, err := c.GetPrinterStatus(context.Background(), testAPIKey, 5)
	if err != nil {
		t.Fatalf("GetPrinterStatus: %v", err)
	}
	if calls.Load() != 2 {
		t.Errorf("attempts = %d, want 2 (one 429 then success)", calls.Load())
	}
	if st.ID != 5 || !st.Connected {
		t.Errorf("status = %+v, want the decoded printer", st)
	}
}

// A 5xx may mean the request never ran, so it is safe and useful to retry.
func TestServerErrorIsRetried(t *testing.T) {
	var calls atomic.Int32
	c := testClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) < 3 {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":1,"status":"printing","position":0,"filament_short":false}`))
	}))

	item, err := c.GetQueueItem(context.Background(), testAPIKey, 1)
	if err != nil {
		t.Fatalf("GetQueueItem: %v", err)
	}
	if calls.Load() != 3 {
		t.Errorf("attempts = %d, want 3", calls.Load())
	}
	if item.Status != StatusPrinting {
		t.Errorf("status = %q, want printing", item.Status)
	}
}

func TestNotFoundIsTyped(t *testing.T) {
	c := testClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	if _, err := c.GetPrinterStatus(context.Background(), testAPIKey, 99); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

// Null-heavy printer status: everything except id/name/connected is null while a
// printer sits idle, and that must decode rather than error.
func TestPrinterStatusDecodesNulls(t *testing.T) {
	c := testClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":3,"name":"Idle","connected":false,"state":null,
			"current_print":null,"progress":null,"remaining_time":null,
			"layer_num":null,"total_layers":null,"gcode_file":null}`))
	}))
	st, err := c.GetPrinterStatus(context.Background(), testAPIKey, 3)
	if err != nil {
		t.Fatalf("GetPrinterStatus: %v", err)
	}
	if st.Progress != nil || st.LayerNum != nil || st.State != nil {
		t.Errorf("nulls did not decode as nil: %+v", st)
	}
	if st.Name != "Idle" || st.Connected {
		t.Errorf("status = %+v, want the idle printer", st)
	}
}

func TestUploadLibraryFileSendsMultipart(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "plate.gcode.3mf")
	if err := os.WriteFile(path, []byte("sliced-plate-bytes"), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	var gotField, gotFilename, gotContent, gotPath string
	c := testClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Errorf("not a multipart body: %v", err)
		}
		for field, files := range r.MultipartForm.File {
			gotField = field
			gotFilename = files[0].Filename
			f, _ := files[0].Open()
			b := make([]byte, files[0].Size)
			_, _ = f.Read(b)
			gotContent = string(b)
			_ = f.Close()
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":11,"filename":"BATCH-ABC.gcode.3mf","file_type":"gcode.3mf",
			"file_size":18,"thumbnail_path":null,"duplicate_of":null}`))
	}))

	got, err := c.UploadLibraryFile(context.Background(), testAPIKey, path, PlateFilename("BATCH-ABC"))
	if err != nil {
		t.Fatalf("UploadLibraryFile: %v", err)
	}
	if gotPath != "/api/v1/library/files" {
		t.Errorf("path = %q, want the library upload path", gotPath)
	}
	if gotField != "file" {
		t.Errorf("form field = %q, want %q - BamBuddy reads only that field", gotField, "file")
	}
	if gotFilename != "BATCH-ABC.gcode.3mf" {
		t.Errorf("filename = %q, want the batch-traceable name", gotFilename)
	}
	if gotContent != "sliced-plate-bytes" {
		t.Errorf("uploaded content = %q, want the file's bytes", gotContent)
	}
	if got.ID != 11 {
		t.Errorf("library file id = %d, want 11", got.ID)
	}
}

// An upload that 200s without an id would leave the caller queueing file 0.
func TestUploadRejectsMissingID(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "p.gcode.3mf")
	if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	c := testClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"filename":"p.gcode.3mf"}`))
	}))
	if _, err := c.UploadLibraryFile(context.Background(), testAPIKey, path, ""); err == nil {
		t.Fatal("an upload with no id was accepted")
	}
}

// A retried upload must replay the whole body, not a drained reader.
func TestUploadRetryReplaysTheBody(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "p.gcode.3mf")
	if err := os.WriteFile(path, []byte("payload"), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	var calls atomic.Int32
	var lastContent string
	c := testClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		_ = r.ParseMultipartForm(1 << 20)
		for _, files := range r.MultipartForm.File {
			f, _ := files[0].Open()
			b := make([]byte, files[0].Size)
			_, _ = f.Read(b)
			lastContent = string(b)
			_ = f.Close()
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":3,"filename":"p","file_type":"x","file_size":7,"thumbnail_path":null}`))
	}))

	if _, err := c.UploadLibraryFile(context.Background(), testAPIKey, path, ""); err != nil {
		t.Fatalf("UploadLibraryFile: %v", err)
	}
	if lastContent != "payload" {
		t.Errorf("retried body = %q, want the full payload replayed", lastContent)
	}
}

func TestTerminalCoversEveryEndState(t *testing.T) {
	for _, s := range []string{StatusCompleted, StatusFailed, StatusSkipped, StatusCancelled} {
		if !Terminal(s) {
			t.Errorf("Terminal(%q) = false, want true - a poller would watch it forever", s)
		}
	}
	for _, s := range []string{StatusPending, StatusPrinting} {
		if Terminal(s) {
			t.Errorf("Terminal(%q) = true, want false - the job is still live", s)
		}
	}
}
