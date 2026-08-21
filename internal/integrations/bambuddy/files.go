package bambuddy

// Uploading a sliced plate into BamBuddy's library. The library id this returns
// is what a queue item points at.

import (
	"bytes"
	"context"
	"fmt"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"

	"github.com/Optiminastic/tensor-core/internal/retry"
)

// LibraryFile is BamBuddy's record of an uploaded file.
type LibraryFile struct {
	ID       int    `json:"id"`
	Filename string `json:"filename"`
	FileType string `json:"file_type"`
	FileSize int64  `json:"file_size"`
	// DuplicateOf is set when BamBuddy recognised identical content it already
	// holds. The upload still succeeds and ID is usable, so this is information,
	// not an error - but it means re-dispatching a batch will not pile up copies.
	DuplicateOf *int `json:"duplicate_of"`
}

// UploadLibraryFile uploads a sliced plate and returns its library record.
//
// filename is what an operator sees in BamBuddy, so it should identify the batch
// rather than repeat a temp path. BamBuddy only queues already-sliced files
// (.gcode or .gcode.3mf), which is exactly what the plate slice produces.
//
// The whole file is buffered in memory to build the multipart body. A sliced
// plate is a few hundred KB, and buffering is what lets a retry replay the body -
// streaming from the file handle could not be re-read after a failed attempt.
func (c *Client) UploadLibraryFile(ctx context.Context, apiKey, path, filename string) (LibraryFile, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return LibraryFile{}, apiErr("could not read the plate file for upload")
	}
	if filename == "" {
		filename = filepath.Base(path)
	}

	var body bytes.Buffer
	form := multipart.NewWriter(&body)
	part, err := form.CreateFormFile("file", filename)
	if err != nil {
		return LibraryFile{}, apiErr("could not build the upload request")
	}
	if _, err := part.Write(content); err != nil {
		return LibraryFile{}, apiErr("could not build the upload request")
	}
	if err := form.Close(); err != nil {
		return LibraryFile{}, apiErr("could not build the upload request")
	}
	encoded := body.Bytes()
	contentType := form.FormDataContentType()

	var out LibraryFile
	err = retry.Do(ctx, retryPolicy, func(ctx context.Context) error {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost,
			c.url("/library/files"), bytes.NewReader(encoded))
		if err != nil {
			return apiErr("could not build the upload request")
		}
		req.Header.Set("Content-Type", contentType)
		req.Header.Set("Accept", "application/json")
		authorize(req, apiKey)
		return c.send(req, "/library/files", &out)
	})
	if err != nil {
		return LibraryFile{}, err
	}
	if out.ID == 0 {
		return LibraryFile{}, apiErr("BamBuddy accepted the upload but returned no file id")
	}
	return out, nil
}

// PlateFilename is the operator-facing name for a batch's plate in BamBuddy.
// Built from the batch number so a queue entry is traceable back to Tensor at a
// glance, rather than showing an opaque uuid or "plate.stl".
func PlateFilename(batchNumber string) string {
	return fmt.Sprintf("%s.gcode.3mf", batchNumber)
}
