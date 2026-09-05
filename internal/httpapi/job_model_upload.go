package httpapi

// POST /production-jobs/:id/model - upload a model file and attach it to a job
// in one request.
//
// Two endpoints already exist that together do this: POST /files stores a file
// and POST /production-jobs/:id/print-file attaches one. Making the browser
// call both means a window where the file is stored and the job still has
// nothing - an orphan asset nobody can find and a job that still reads as
// waiting. One request removes that window.
//
// This is the manual path, for products Tensor cannot build itself. A Dual Name
// Plank is rendered from the customer's own names and never comes through here;
// everything else needs a person to supply the geometry, and this is where they
// do it.

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/Optiminastic/tensor-core/internal/auth"
	"github.com/Optiminastic/tensor-core/internal/db/gen"
	"github.com/Optiminastic/tensor-core/internal/obs"
	"github.com/Optiminastic/tensor-core/internal/production"
)

// modelUploadExtensions are the formats the pipeline can actually read.
//
// Checked here rather than left to the slicer: a .zip or a .f3d uploaded by
// mistake would be stored, attached, and only fail much later at slicing, by
// which time the job looks ready and nobody remembers what was attached.
var modelUploadExtensions = map[string]bool{
	".stl": true,
	".3mf": true,
}

func (s *Server) registerJobModelUpload(g *gin.RouterGroup) {
	// production:update, not production:read: attaching a model changes what
	// will be printed, which is not something a read-only role may do.
	g.POST("/:id/model", s.guards.RequirePermission(auth.ProductionUpdate.Key()), s.uploadJobModel)
}

func (s *Server) uploadJobModel(c *gin.Context) {
	if !s.filesReady(c) {
		return
	}
	id, ok := parseUUIDParam(c, "id")
	if !ok {
		return
	}
	ctx := c.Request.Context()

	job, err := s.store.Q.GetProductionJobByID(ctx, id)
	if err != nil {
		dbError(c, err, "That production job does not exist.", "Could not load the production job.")
		return
	}

	fh, err := c.FormFile("file")
	if err != nil {
		detail(c, http.StatusUnprocessableEntity, "A 'file' is required.")
		return
	}
	if fh.Size <= 0 {
		detail(c, http.StatusUnprocessableEntity, "The uploaded file is empty.")
		return
	}
	if fh.Size > maxFileBytes {
		detail(c, http.StatusRequestEntityTooLarge, "The file is larger than the 100 MB limit.")
		return
	}

	ext := strings.ToLower(filepath.Ext(fh.Filename))
	if !modelUploadExtensions[ext] {
		detail(c, http.StatusUnprocessableEntity,
			"A print file must be an .stl or .3mf. "+ext+" cannot be sliced.")
		return
	}

	tmpPath, ok := s.spoolUpload(c, fh, ext)
	if !ok {
		return
	}
	defer func() { _ = os.Remove(tmpPath) }()

	// Measured before anything is recorded. A file the loader cannot read is
	// not a print file, and finding that out now beats finding it out when the
	// batch planner tries to pack a bed with no dimensions.
	bx, by, bz := modelBBox(tmpPath, ext)
	if bx == nil || by == nil || bz == nil {
		detail(c, http.StatusUnprocessableEntity,
			"That file could not be read as a 3D model. Check it opens in a slicer first.")
		return
	}

	fileID := uuid.New()
	key := fmt.Sprintf("files/%s%s", fileID, ext)
	contentType := fh.Header.Get("Content-Type")
	if contentType == "" {
		contentType = "model/stl"
	}
	if err := s.storage.Upload(ctx, key, tmpPath, contentType); err != nil {
		detail(c, http.StatusInternalServerError, "Could not store the uploaded file.")
		return
	}

	if _, err := s.store.Q.InsertFileAsset(ctx, gen.InsertFileAssetParams{
		ID: fileID, Filename: fh.Filename, ContentType: contentType, SizeBytes: fh.Size,
		StorageKey: key, IsTemplate: false, UploadedBy: currentUserID(c),
		BboxXMm: bx, BboxYMm: by, BboxZMm: bz,
	}); err != nil {
		detail(c, http.StatusInternalServerError, "Could not record the uploaded file.")
		return
	}

	// Clears issue_reason when it is exactly 'stl_missing', which is what lets
	// the job into batching.
	updated, err := s.store.Q.SetProductionJobPrintFile(ctx, gen.SetProductionJobPrintFileParams{
		PrintFileID: &fileID, ID: id,
	})
	if err != nil {
		detail(c, http.StatusInternalServerError, "Could not attach the file to the job.")
		return
	}

	// An uploaded model resolves a failed render just as a successful retry
	// would: the job has geometry now, whatever produced it.
	if err := s.store.Q.ClearJobModelError(ctx, id); err != nil {
		obs.FromContext(ctx).Warn("could not clear the model error after an upload",
			"job", id, "error", err)
	}

	// A manually supplied model has no design behind it either, so it hits the
	// same profile_missing guard a generated one does.
	if err := s.setGeneratedMachineFamily(ctx, id); err != nil {
		obs.FromContext(ctx).Warn("could not set the machine family after an upload",
			"job", id, "error", err)
	}

	if err := recordJobEvent(ctx, s.store.Q, jobEvent{
		JobID:     id,
		EventType: production.EventModelUploaded,
		ActorID:   currentUserID(c),
		Comment:   strPtr(fh.Filename),
	}); err != nil {
		obs.FromContext(ctx).Warn("could not record the model upload", "job", id, "error", err)
	}

	obs.FromContext(ctx).Info("model uploaded for job",
		"job", job.JobNumber, "file", fh.Filename, "bytes", fh.Size)

	// The job may have just become batchable.
	s.triggerBatchPlanIfThresholdMet(ctx)
	c.JSON(http.StatusOK, s.singleJobDTO(ctx, updated))
}
