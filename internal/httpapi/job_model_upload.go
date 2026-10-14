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
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/Optiminastic/tensor-core/internal/auth"
	"github.com/Optiminastic/tensor-core/internal/db/gen"
	"github.com/Optiminastic/tensor-core/internal/meshio"
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

	// WHAT FILAMENTS THE FILE ASKS FOR, read from the file itself.
	//
	// The planner groups a bed by production_jobs.colours and nothing else, so
	// an upload that recorded only geometry produced a job that was
	// dimensionally packable and invisible to batching. Resolved by exact hex
	// only - see job_model_colours.go for why a near match must never be
	// guessed at here.
	colours, unmapped, err := s.coloursForUpload(ctx, c, tmpPath, ext)
	if err != nil {
		detail(c, http.StatusUnprocessableEntity, err.Error())
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

	// Clears issue_reason when it is one of the four this upload is the remedy
	// for, which is what lets the job into batching - and writes the colours
	// the file declares in the same statement, so the job is never batchable
	// by a colour the model does not ask for.
	params := gen.SetProductionJobPrintFileParams{PrintFileID: &fileID, ID: id}
	if len(colours) > 0 {
		encoded, mErr := json.Marshal(colours)
		if mErr != nil {
			detail(c, http.StatusInternalServerError, "Could not record the file's colours.")
			return
		}
		count := int32(len(colours))
		params.SetColours, params.Colours, params.ColourCount = true, encoded, &count
	}
	updated, err := s.store.Q.SetProductionJobPrintFile(ctx, params)
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

	// No family is stamped on an uploaded model either: it has geometry and a
	// colour, which is everything BambuBuddy needs to place it on a printer.
	// See GenerateModelForJob for why the family pin went away.

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

	if len(unmapped) > 0 {
		// Said out loud rather than guessed at. The job keeps whatever colour
		// it had, so the queue goes on reporting that it has none - which is
		// the honest state and names an action somebody can take.
		obs.FromContext(ctx).Warn("uploaded model declares colours the shop has not recorded",
			"job", job.JobNumber, "hexes", strings.Join(unmapped, " "))
	}

	// IF IT WAS ALREADY ON A BED, THAT BED NOW HOLDS A PLATE BUILT FROM THE
	// OLD MODEL. Nothing re-plated after a manual upload, so merged_file_id
	// went on pointing at geometry the job no longer has - the replacement a
	// designer uploads for a renamed item would have changed nothing at all.
	//
	// Only a bed still open to change: a completed one is the record of what
	// was physically printed and must not be rewritten.
	if updated.BatchID != nil {
		if batch, bErr := s.store.Q.GetBatchByID(ctx, *updated.BatchID); bErr == nil &&
			(batch.Status == production.BatchPendingApproval || batch.Status == production.BatchOpen) {
			s.replateWhenRendersSettle(ctx, updated)
		}
	}

	// The job may have just become batchable.
	s.triggerBatchPlanIfThresholdMet(ctx)

	dto := s.singleJobDTO(ctx, updated)
	dto.UnmappedColours = unmapped
	c.JSON(http.StatusOK, dto)
}

// coloursForUpload decides what filaments the uploaded model asks for.
//
// The designer may say so explicitly with a `colours` field - a JSON array of
// the shop's own filament names - and that wins outright, because a person
// looking at the model knows things the file does not record. Every name they
// give must resolve, or the upload is refused: a name nothing can print is a
// job that would hold at errUnknownColour with somebody believing they had
// chosen.
//
// Otherwise the file is asked, and only a 3MF has an answer. An STL carries no
// colour at all, and a 3MF that declares none is not an error either - both
// leave the job's existing colours untouched.
func (s *Server) coloursForUpload(
	ctx context.Context, c *gin.Context, tmpPath, ext string,
) (colours, unmapped []string, err error) {
	if declared := strings.TrimSpace(c.PostForm("colours")); declared != "" {
		var names []string
		if jsonErr := json.Unmarshal([]byte(declared), &names); jsonErr != nil {
			return nil, nil, fmt.Errorf(
				"'colours' must be a JSON array of filament names, like [\"RED\",\"WHITE\"]")
		}
		out := make([]string, 0, len(names))
		for _, name := range names {
			name = strings.TrimSpace(name)
			if name == "" {
				continue
			}
			if _, resolveErr := s.resolveColourHex(ctx, name); resolveErr != nil {
				return nil, nil, fmt.Errorf(
					"%q is not a colour this shop can print, so the job would hold "+
						"with nobody having chosen", name)
			}
			out = append(out, production.CanonicalColourName(name))
		}
		return out, nil, nil
	}

	if ext != ".3mf" {
		return nil, nil, nil
	}
	data, readErr := os.ReadFile(tmpPath)
	if readErr != nil {
		// The geometry already measured, so this is not worth refusing the
		// upload for.
		obs.FromContext(ctx).Warn("could not re-read the upload to look for colours",
			"error", readErr)
		return nil, nil, nil
	}
	parts, parseErr := meshio.Read3MF(data)
	if parseErr != nil || len(parts) == 0 {
		return nil, nil, nil
	}
	colours, unmapped = coloursFromParts(parts, s.uploadColourNames(ctx))
	return colours, unmapped, nil
}

// uploadColourNames reads the shop's confirmed spools over the built-in table,
// falling back to the built-in table alone when they cannot be read - which is
// a narrower answer, never a wrong one.
//
// Deliberately NOT the colourNamesByHex in printing_queue.go: that one answers
// for a plate already built and returns nothing at all when the map is
// unreadable, which here would silently drop every colour from an upload.
func (s *Server) uploadColourNames(ctx context.Context) map[string]string {
	rows, err := s.store.Q.ListColourMap(ctx)
	if err != nil {
		obs.FromContext(ctx).Warn("could not read the colour map", "error", err)
		return colourNamesFromMap(nil)
	}
	out := make([]colourMapRow, 0, len(rows))
	for _, r := range rows {
		out = append(out, colourMapRow{
			ColourName: r.ColourName, Hex: r.Hex, IsPrimary: r.IsPrimary,
		})
	}
	return colourNamesFromMap(out)
}
