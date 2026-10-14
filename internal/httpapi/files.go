package httpapi

import (
	"context"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/Optiminastic/tensor-core/internal/auth"
	"github.com/Optiminastic/tensor-core/internal/db"
	"github.com/Optiminastic/tensor-core/internal/db/gen"
	"github.com/Optiminastic/tensor-core/internal/meshio"
	"github.com/Optiminastic/tensor-core/internal/obs"
	"github.com/Optiminastic/tensor-core/internal/orientation"
	"github.com/Optiminastic/tensor-core/internal/storage"
)

// maxFileBytes caps a file-assets upload (models, QC/packaging photos). It is
// generous for a plate STL; larger uploads are refused with 413.
const maxFileBytes = 100 << 20 // 100 MB

// fileResponse is the API shape of a stored file. The storage key is internal and
// never exposed; the bounding box is present only for parsed model files.
type fileResponse struct {
	ID          string    `json:"id"`
	Filename    string    `json:"filename"`
	ContentType string    `json:"content_type"`
	SizeBytes   int64     `json:"size_bytes"`
	IsTemplate  bool      `json:"is_template"`
	UploadedBy  string    `json:"uploaded_by"`
	BboxXMm     *float64  `json:"bbox_x_mm"`
	BboxYMm     *float64  `json:"bbox_y_mm"`
	BboxZMm     *float64  `json:"bbox_z_mm"`
	CreatedAt   time.Time `json:"created_at"`
}

func fileDTO(f gen.FileAsset) fileResponse {
	return fileResponse{
		ID: f.ID.String(), Filename: f.Filename, ContentType: f.ContentType,
		SizeBytes: f.SizeBytes, IsTemplate: f.IsTemplate, UploadedBy: f.UploadedBy,
		BboxXMm: db.NumFloatPtr(f.BboxXMm), BboxYMm: db.NumFloatPtr(f.BboxYMm),
		BboxZMm: db.NumFloatPtr(f.BboxZMm), CreatedAt: db.Time(f.CreatedAt),
	}
}

// filesReady fails closed with 503 when object storage is not configured, so the
// file routes never half-work.
func (s *Server) filesReady(c *gin.Context) bool {
	if s.storage == nil {
		detail(c, http.StatusServiceUnavailable, "File storage is not configured.")
		return false
	}
	return true
}

func (s *Server) registerFiles(r *gin.Engine) {
	g := r.Group("/files")
	g.Use(s.guards.RequireUser())
	// production:read is the "participates in the production pipeline" gate: the
	// four pipeline roles have it, the design-only roles do not.
	g.POST("", s.guards.RequirePermission(auth.ProductionRead.Key()), s.uploadFile)
	g.GET("/:id", s.guards.RequirePermission(auth.ProductionRead.Key()), s.downloadFile)
}

func (s *Server) uploadFile(c *gin.Context) {
	if !s.filesReady(c) {
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
	tmpPath, ok := s.spoolUpload(c, fh, ext)
	if !ok {
		return
	}
	defer func() { _ = os.Remove(tmpPath) }()

	fileID := uuid.New()
	key := fmt.Sprintf("files/%s%s", fileID, ext)
	contentType := fh.Header.Get("Content-Type")
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	if err := s.storage.Upload(c.Request.Context(), key, tmpPath, contentType); err != nil {
		detail(c, http.StatusInternalServerError, "Could not store the uploaded file.")
		return
	}

	bx, by, bz := modelBBox(tmpPath, ext)
	row, err := s.store.Q.InsertFileAsset(c.Request.Context(), gen.InsertFileAssetParams{
		ID: fileID, Filename: fh.Filename, ContentType: contentType, SizeBytes: fh.Size,
		StorageKey: key, IsTemplate: false, UploadedBy: currentUserID(c),
		BboxXMm: bx, BboxYMm: by, BboxZMm: bz,
	})
	if err != nil {
		detail(c, http.StatusInternalServerError, "Could not record the uploaded file.")
		return
	}
	c.JSON(http.StatusCreated, fileDTO(row))
}

// spoolUpload streams the multipart upload to a temporary file, which is used
// both for the object-store upload and for bounding-box measurement.
func (s *Server) spoolUpload(c *gin.Context, fh *multipart.FileHeader, ext string) (string, bool) {
	src, err := fh.Open()
	if err != nil {
		detail(c, http.StatusBadRequest, "Could not read the uploaded file.")
		return "", false
	}
	defer func() { _ = src.Close() }()

	tmp, err := os.CreateTemp("", "tensor-upload-*"+ext)
	if err != nil {
		detail(c, http.StatusInternalServerError, "Could not buffer the upload.")
		return "", false
	}
	tmpPath := tmp.Name()
	if _, err := io.Copy(tmp, src); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
		detail(c, http.StatusInternalServerError, "Could not buffer the upload.")
		return "", false
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		detail(c, http.StatusInternalServerError, "Could not buffer the upload.")
		return "", false
	}
	return tmpPath, true
}

// modelBBox measures the axis-aligned bounding box (mm) of a model file, reusing
// the orientation package's STL/3MF loaders. A non-model file or a parse failure
// yields nil dimensions - it never fails the upload.
func modelBBox(path, ext string) (x, y, z *float64) {
	mesh, err := orientation.LoadModel(path, ext)
	if err != nil {
		return nil, nil, nil
	}
	dx := mesh.Max.X - mesh.Min.X
	dy := mesh.Max.Y - mesh.Min.Y
	dz := mesh.Max.Z - mesh.Min.Z
	return &dx, &dy, &dz
}

func (s *Server) downloadFile(c *gin.Context) {
	if !s.filesReady(c) {
		return
	}
	id, ok := parseUUIDParam(c, "id")
	if !ok {
		return
	}
	row, err := s.store.Q.GetFileAsset(c.Request.Context(), id)
	if err != nil {
		dbError(c, err, "That file does not exist.", "Could not load the file.")
		return
	}
	obj, err := s.storage.Get(c.Request.Context(), row.StorageKey)
	if err != nil {
		if storage.IsNotFound(err) {
			detail(c, http.StatusNotFound, "The file is no longer in storage.")
			return
		}
		detail(c, http.StatusInternalServerError, "Could not read the file.")
		return
	}
	defer func() { _ = obj.Body.Close() }()

	// THE DOWNLOAD IS THE FILE, byte for byte. An operator taking the print
	// file to a slicer must get what was uploaded, so the rewrite below is
	// asked for explicitly and never happens by default.
	if c.Query("preview") == "1" {
		// Read once, then decide. A file that cannot be rebuilt - an STL, one
		// that declares no colour, one too large to hold - is still a file the
		// viewer draws perfectly well, so the original bytes are served rather
		// than refused. Serving nothing here would break every preview that
		// works today to fix the ones that do not.
		raw, err := io.ReadAll(io.LimitReader(obj.Body, maxPreviewModelBytes))
		if err != nil {
			detail(c, http.StatusInternalServerError, "Could not read the file.")
			return
		}
		c.Header("Content-Disposition", "inline")
		if rebuilt, ok := previewModel(c.Request.Context(), raw, row.Filename); ok {
			c.Data(http.StatusOK, "model/3mf", rebuilt)
			return
		}
		c.Data(http.StatusOK, row.ContentType, raw)
		return
	}

	c.Header("Content-Disposition", fmt.Sprintf("attachment; filename=%q", row.Filename))
	c.DataFromReader(http.StatusOK, obj.Size, row.ContentType, obj.Body, nil)
}

// previewModel rewrites a model so a browser can see the colours it declares.
//
// A SLICER DOES NOT WRITE COLOUR WHERE A VIEWER LOOKS FOR IT. Bambu Studio
// records which extruder an object prints on and what each extruder is loaded
// with, in two of its own config files; there is no <basematerials> group
// anywhere, which is the only place three.js reads a colour from. So a
// designer's 3MF drew as a grey shape however carefully they had coloured it.
//
// Read3MF already understands both arrangements - it has to, because batching
// groups a bed by what a file declares - so this reads with it and writes the
// result back as a core-spec 3MF. The bytes change; the model does not. And it
// means the preview and the bed agree about colour by construction, rather
// than two readers being kept in step by hand.
//
// Returns false when there is nothing to gain: an STL, a file that will not
// parse, or one whose parts declare no colour at all. The caller then leaves
// the file alone.
func previewModel(ctx context.Context, data []byte, filename string) ([]byte, bool) {
	if !strings.EqualFold(filepath.Ext(filename), ".3mf") {
		return nil, false
	}
	parts, err := meshio.Read3MF(data)
	if err != nil || len(parts) == 0 {
		return nil, false
	}
	coloured := false
	for _, p := range parts {
		if strings.TrimSpace(p.Colour) != "" {
			coloured = true
			break
		}
	}
	if !coloured {
		// Nothing to carry across, and rewriting would only cost the viewer
		// whatever the original happened to say.
		return nil, false
	}
	rebuilt, err := meshio.Write3MF(parts)
	if err != nil {
		obs.FromContext(ctx).Warn("could not rebuild a model for preview", "error", err)
		return nil, false
	}
	return rebuilt, true
}

// maxPreviewModelBytes bounds what is pulled into memory to recolour, matching
// the limit the personalisation preview already uses.
const maxPreviewModelBytes = 64 << 20

// currentUserID returns the authenticated caller's id (empty string if somehow
// absent, which the guards make impossible on these routes).
func currentUserID(c *gin.Context) string {
	u, _ := auth.UserFrom(c)
	return u.ID
}
