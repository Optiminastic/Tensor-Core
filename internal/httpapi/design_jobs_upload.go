package httpapi

// Supplying the design files for one ordered item, which may be several.
//
// A plank is one model. A product a person draws often is not: a DUOSNAP has a
// body and a clip, a lamp has a shade and a base, and the designer knows how
// many pieces it takes only once they have drawn them. So the upload takes as
// many 3MFs as they want to give it.
//
// ONE FILE PER JOB, because a job is the unit everything downstream counts in.
// A bed is packed from jobs, a plate is merged from their models, and dispatch
// sends what a bed holds - so two models on one job would be invisible to all
// three. The first file lands on the job itself and every file after it gets a
// job of its own beside it, carrying the same order, the same customer and the
// same answers, differing only in which piece it is.
//
// That is not a new shape: a Soulmate Combo has printed as three jobs with a
// part_role each since the registry learned about it. This is the same
// arrangement, worked out from the files a designer uploaded rather than from
// a product configured in advance.

import (
	"context"
	"encoding/json"
	"fmt"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/Optiminastic/tensor-core/internal/db/gen"
	"github.com/Optiminastic/tensor-core/internal/obs"
	"github.com/Optiminastic/tensor-core/internal/production"
)

// maxDesignFilesPerJob bounds one upload.
//
// Generous against any real product - the most pieces anything here prints in
// is three - and small enough that a mis-selected folder cannot turn one
// ordered item into fifty jobs on the floor.
const maxDesignFilesPerJob = 12

// supplyDesignJobModel attaches a designer's 3MFs to a job Tensor cannot build.
//
// A WRAPPER RATHER THAN THE OPERATOR'S UPLOAD, because a route is not secured
// by what its list shows: :id comes from the caller, and registering
// uploadJobModel verbatim here would let a designer attach a hand-made file to
// a Dual Name Plank - silently replacing a rendered model with one nothing
// re-renders, and making design:job_supply mean something other than its own
// description. job_rerender.go refuses the mirror image, so between them every
// job is covered exactly once.
func (s *Server) supplyDesignJobModel(c *gin.Context) {
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
	if s.rendersProduct(ctx, deref(job.Sku), deref(job.ProductName)) {
		detail(c, http.StatusUnprocessableEntity,
			"Tensor builds this product's model itself, so it is not a design file to supply. "+
				"Use Rebuild model on the job instead.")
		return
	}

	files, ok := designFilesFrom(c)
	if !ok {
		return
	}

	stored := make([]storedDesignFile, 0, len(files))
	for _, fh := range files {
		one, ok := s.storeDesignFile(c, fh)
		if !ok {
			return
		}
		stored = append(stored, one)
	}

	// The first file is this job's. Everything after it becomes a job beside
	// it - see the note at the top of this file for why one model per job.
	head := stored[0]
	updated, err := s.store.Q.SetProductionJobPrintFile(ctx, printFileParams(id, head))
	if err != nil {
		detail(c, http.StatusInternalServerError, "Could not attach the file to the job.")
		return
	}
	if err := s.store.Q.ClearJobModelError(ctx, id); err != nil {
		obs.FromContext(ctx).Warn("could not clear the model error after a design upload",
			"job", id, "error", err)
	}
	s.recordDesignUpload(ctx, c, id, head)
	s.nameJobForPiece(ctx, updated, head)

	made := []gen.ProductionJob{updated}
	for _, extra := range stored[1:] {
		sibling, err := s.addPieceJob(ctx, job, extra)
		if err != nil {
			// The files are stored and the first job is attached, so this is
			// reported rather than rolled back: a designer who uploaded four
			// pieces and got three jobs can upload the fourth again, and the
			// alternative - discarding all of it - costs them the whole set.
			obs.FromContext(ctx).Error("could not create the job for a design piece",
				"job", job.JobNumber, "file", extra.Filename, "error", err)
			detail(c, http.StatusInternalServerError, fmt.Sprintf(
				"%s was stored but its job could not be made. The other pieces are on the floor; "+
					"upload this one again.", extra.Filename))
			return
		}
		made = append(made, sibling)
	}

	if updated.BatchID != nil {
		if batch, bErr := s.store.Q.GetBatchByID(ctx, *updated.BatchID); bErr == nil &&
			(batch.Status == production.BatchPendingApproval || batch.Status == production.BatchOpen) {
			s.replateWhenRendersSettle(ctx, updated)
		}
	}
	s.triggerBatchPlanIfThresholdMet(ctx)

	out := s.productionJobsDTO(ctx, made)
	for i := range out {
		out[i].UnmappedColours = stored[i].Unmapped
	}
	c.JSON(http.StatusOK, out)
}

// designFilesFrom reads the upload, accepting one file or many.
//
// "files" is the designer's form and "file" is what every existing caller
// sends, so both are taken - the operator's upload button and this page post
// to different routes but the same handler shape, and breaking one to add the
// other would be a poor trade.
func designFilesFrom(c *gin.Context) ([]*multipart.FileHeader, bool) {
	form, err := c.MultipartForm()
	if err != nil {
		detail(c, http.StatusUnprocessableEntity, "Choose at least one design file.")
		return nil, false
	}
	files := append([]*multipart.FileHeader{}, form.File["files"]...)
	files = append(files, form.File["file"]...)

	if len(files) == 0 {
		detail(c, http.StatusUnprocessableEntity, "Choose at least one design file.")
		return nil, false
	}
	if len(files) > maxDesignFilesPerJob {
		detail(c, http.StatusUnprocessableEntity, fmt.Sprintf(
			"That is %d files. One ordered item takes at most %d pieces - check the selection.",
			len(files), maxDesignFilesPerJob))
		return nil, false
	}
	return files, true
}

// storedDesignFile is one uploaded model, measured, stored and recorded.
type storedDesignFile struct {
	FileID   uuid.UUID
	Filename string
	// Piece is the file's own name without its extension, which is what the
	// designer called this part of the product.
	Piece    string
	Colours  []string
	Unmapped []string
	BboxX    *float64
	BboxY    *float64
	BboxZ    *float64
}

// storeDesignFile validates, measures and stores one file. Everything it
// refuses, it refuses before anything is written.
func (s *Server) storeDesignFile(
	c *gin.Context, fh *multipart.FileHeader,
) (storedDesignFile, bool) {
	ctx := c.Request.Context()

	if fh.Size <= 0 {
		detail(c, http.StatusUnprocessableEntity, fh.Filename+" is empty.")
		return storedDesignFile{}, false
	}
	if fh.Size > maxFileBytes {
		detail(c, http.StatusRequestEntityTooLarge, fh.Filename+" is larger than the 100 MB limit.")
		return storedDesignFile{}, false
	}
	ext := strings.ToLower(filepath.Ext(fh.Filename))
	if !modelUploadExtensions[ext] {
		detail(c, http.StatusUnprocessableEntity,
			"A print file must be an .stl or .3mf. "+fh.Filename+" cannot be sliced.")
		return storedDesignFile{}, false
	}

	tmpPath, ok := s.spoolUpload(c, fh, ext)
	if !ok {
		return storedDesignFile{}, false
	}
	defer func() { _ = os.Remove(tmpPath) }()

	bx, by, bz := modelBBox(tmpPath, ext)
	if bx == nil || by == nil || bz == nil {
		detail(c, http.StatusUnprocessableEntity,
			fh.Filename+" could not be read as a 3D model. Check it opens in a slicer first.")
		return storedDesignFile{}, false
	}

	colours, unmapped, err := s.coloursForUpload(ctx, c, tmpPath, ext)
	if err != nil {
		detail(c, http.StatusUnprocessableEntity, err.Error())
		return storedDesignFile{}, false
	}

	fileID := uuid.New()
	key := fmt.Sprintf("files/%s%s", fileID, ext)
	contentType := fh.Header.Get("Content-Type")
	if contentType == "" {
		contentType = "model/stl"
	}
	if err := s.storage.Upload(ctx, key, tmpPath, contentType); err != nil {
		detail(c, http.StatusInternalServerError, "Could not store "+fh.Filename+".")
		return storedDesignFile{}, false
	}
	if _, err := s.store.Q.InsertFileAsset(ctx, gen.InsertFileAssetParams{
		ID: fileID, Filename: fh.Filename, ContentType: contentType, SizeBytes: fh.Size,
		StorageKey: key, IsTemplate: false, UploadedBy: currentUserID(c),
		BboxXMm: bx, BboxYMm: by, BboxZMm: bz,
	}); err != nil {
		detail(c, http.StatusInternalServerError, "Could not record "+fh.Filename+".")
		return storedDesignFile{}, false
	}

	return storedDesignFile{
		FileID: fileID, Filename: fh.Filename, Piece: pieceNameFrom(fh.Filename),
		Colours: colours, Unmapped: unmapped, BboxX: bx, BboxY: by, BboxZ: bz,
	}, true
}

// pieceNameFrom is what the designer called this part, taken from the file.
//
// The file name is the only place the designer says what a piece IS - "clip",
// "shade", "base" - so it becomes the job's part_role and is how an operator
// tells one of an item's jobs from another on the floor. Trimmed to the
// column's width rather than rejected: a long file name is a naming habit, not
// a mistake to refuse an upload over.
func pieceNameFrom(filename string) string {
	stem := strings.TrimSuffix(filepath.Base(filename), filepath.Ext(filename))
	stem = strings.TrimSpace(strings.Join(strings.Fields(stem), " "))
	if stem == "" {
		return ""
	}
	if len(stem) > 24 {
		// Trailing separators go with the cut. "an-extremely-long-piece-" is
		// a word broken in half wearing a hyphen, and it is the word an
		// operator reads off the job.
		stem = strings.TrimRight(strings.TrimSpace(stem[:24]), "-_. ")
	}
	return stem
}

func printFileParams(id uuid.UUID, f storedDesignFile) gen.SetProductionJobPrintFileParams {
	params := gen.SetProductionJobPrintFileParams{ID: id, PrintFileID: &f.FileID}
	if len(f.Colours) > 0 {
		if encoded, err := json.Marshal(f.Colours); err == nil {
			count := int32(len(f.Colours))
			params.SetColours, params.Colours, params.ColourCount = true, encoded, &count
		}
	}
	return params
}

// nameJobForPiece records which piece of the product this job is, once the
// designer's file has said so.
//
// Only when the upload named one, and only when the job does not already carry
// a role from the registry - a configured product already knows its pieces,
// and a file name must not rename them.
func (s *Server) nameJobForPiece(ctx context.Context, job gen.ProductionJob, f storedDesignFile) {
	role := strings.TrimSpace(job.PartRole)
	if f.Piece == "" || (role != "" && !strings.EqualFold(role, "body")) {
		return
	}
	if err := s.store.Q.SetProductionJobPartRole(ctx, gen.SetProductionJobPartRoleParams{
		ID: job.ID, PartRole: &f.Piece,
	}); err != nil {
		obs.FromContext(ctx).Warn("could not name the job's piece",
			"job", job.JobNumber, "piece", f.Piece, "error", err)
	}
}

// addPieceJob creates the job for one more piece of the same ordered item.
//
// A sibling, not a fragment: splitProductionJob is reused for the copying -
// same order, same customer, same answers, same due date - but this is not
// fewer units of the same thing, it is a different part of it. So the quantity
// is the parent's in full and the piece carries its own model.
func (s *Server) addPieceJob(
	ctx context.Context, parent gen.ProductionJob, f storedDesignFile,
) (gen.ProductionJob, error) {
	number, err := s.nextPieceJobNumber(ctx, parent)
	if err != nil {
		return gen.ProductionJob{}, err
	}

	params := splitProductionJob(parent, parent.Quantity, number)
	params.PrintFileID = &f.FileID
	params.BboxXMm, params.BboxYMm, params.BboxZMm = f.BboxX, f.BboxY, f.BboxZ
	// The piece's own model says what it prints in; the parent's colours
	// describe the parent's file and would be wrong here.
	if len(f.Colours) > 0 {
		if encoded, mErr := json.Marshal(f.Colours); mErr == nil {
			count := int32(len(f.Colours))
			params.Colours, params.ColourCount = encoded, &count
		}
	} else {
		params.Colours, params.ColourCount = []byte("[]"), nil
	}
	if f.Piece != "" {
		params.PartRole = &f.Piece
	}
	// Carried so the piece's own job can still show what the customer asked
	// for - splitProductionJob does not copy these, having never needed to.
	params.PersonalisationProperties = parent.PersonalisationProperties
	params.VariantTitle = parent.VariantTitle
	// It has a model, so none of the "there is no model here" reasons apply.
	params.IssueReason = nil

	created, err := s.store.Q.InsertProductionJob(ctx, params)
	if err != nil {
		return gen.ProductionJob{}, err
	}
	if err := recordJobEvent(ctx, s.store.Q, jobEvent{
		JobID:     created.ID,
		EventType: production.EventModelUploaded,
		ActorID:   "system",
		Comment:   strPtr(f.Filename),
	}); err != nil {
		obs.FromContext(ctx).Warn("could not record the piece's upload",
			"job", created.JobNumber, "error", err)
	}
	obs.FromContext(ctx).Info("a design piece became its own job",
		"parent", parent.JobNumber, "job", created.JobNumber,
		"piece", f.Piece, "file", f.Filename)
	return created, nil
}

// nextPieceJobNumber finds the next free suffix for this order.
//
// The same numbering a combo's three jobs get - JOB-115768, -2, -3 - so an
// item that turned out to take three pieces reads on the floor exactly like a
// product that was always going to.
func (s *Server) nextPieceJobNumber(
	ctx context.Context, parent gen.ProductionJob,
) (string, error) {
	orderNumber := ""
	if parent.OrderID != nil {
		if order, err := s.store.Q.GetOrderByID(ctx, *parent.OrderID); err == nil {
			orderNumber = order.OrderNumber
		}
	}
	if orderNumber == "" {
		return s.store.Q.NextJobNumber(ctx)
	}
	// Walks rather than counting the order's jobs: a piece added to an order
	// that already has three jobs must not be offered a number one of them
	// holds, and jobNumberFor checks each candidate.
	for index := 1; index < maxDesignFilesPerJob*4; index++ {
		candidate := jobNumberForOrder(orderNumber, index)
		if candidate == "" {
			break
		}
		taken, err := s.store.Q.JobNumberExists(ctx, candidate)
		if err != nil {
			return "", err
		}
		if !taken {
			return candidate, nil
		}
	}
	return s.store.Q.NextJobNumber(ctx)
}

// recordDesignUpload notes the file against the job, for the history.
func (s *Server) recordDesignUpload(
	ctx context.Context, c *gin.Context, jobID uuid.UUID, f storedDesignFile,
) {
	if err := recordJobEvent(ctx, s.store.Q, jobEvent{
		JobID: jobID, EventType: production.EventModelUploaded,
		ActorID: currentUserID(c), Comment: strPtr(f.Filename),
	}); err != nil {
		obs.FromContext(ctx).Warn("could not record the design upload",
			"job", jobID, "error", err)
	}
}
