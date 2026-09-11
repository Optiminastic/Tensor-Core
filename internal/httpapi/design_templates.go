package httpapi

// Replacing a product's OpenSCAD template without a deploy.
//
// The three plank templates are compiled into the binary, which makes them fast
// and impossible to lose - and impossible to change without a developer.
// templates/README.md records what that cost: the embedded copies drifted five
// days behind the shop's masters and nobody noticed until file sizes were
// compared.
//
// An upload here becomes the source the renderer uses. With no uploads the
// binary's own templates are used, exactly as before, so this adds a capability
// rather than changing a behaviour.

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/Optiminastic/tensor-core/internal/auth"
	"github.com/Optiminastic/tensor-core/internal/db"
	"github.com/Optiminastic/tensor-core/internal/db/gen"
	"github.com/Optiminastic/tensor-core/internal/obs"
	"github.com/Optiminastic/tensor-core/internal/personalise"
)

// maxTemplateBytes bounds an upload. The shop's own masters are 45 KB; a
// megabyte is room for one that grows tenfold and still refuses a 3MF sent to
// the wrong endpoint by mistake.
const maxTemplateBytes = 1 << 20

type templateResponse struct {
	Key        string    `json:"key"`
	Version    int32     `json:"version"`
	Filename   string    `json:"filename"`
	SizeBytes  int64     `json:"size_bytes"`
	UploadedBy string    `json:"uploaded_by"`
	Notes      *string   `json:"notes"`
	CreatedAt  time.Time `json:"created_at"`
	// Source says where the renderer will read this template from. "uploaded"
	// once somebody has replaced it; "embedded" while it still comes from the
	// binary. The distinction is the whole point of the page.
	Source string `json:"source"`
}

// embeddedTemplateKeys are the templates this binary ships with.
//
// Listed so the Designs tab can show a key that nobody has uploaded yet - a
// page that only listed uploads would be empty on a fresh install and would
// look like the shop had no designs at all.
var embeddedTemplateKeys = []string{"dnp_with_no_heart", "dual_one_heart", "dnp_two_heart"}

func (s *Server) registerDesignTemplates(r *gin.Engine) {
	g := r.Group("/registry/templates")
	g.Use(s.guards.RequireUser())
	g.GET("", s.guards.RequirePermission(auth.ConfigRead.Key()), s.listDesignTemplates)
	g.GET("/:key/history", s.guards.RequirePermission(auth.ConfigRead.Key()), s.templateHistory)
	g.POST("/:key", s.guards.RequirePermission(auth.ConfigManage.Key()), s.uploadDesignTemplate)
}

// uploadedTemplates is every template somebody has replaced, by lower-cased key.
//
// Shared by the templates list and the product panel so both answer "where will
// the renderer read this from" the same way. A page that disagreed with the
// renderer about which file prints would be worse than no page.
func (s *Server) uploadedTemplates(ctx context.Context) (map[string]templateResponse, error) {
	rows, err := s.store.Q.ListActiveTemplates(ctx)
	if err != nil {
		return nil, err
	}
	uploaded := make(map[string]templateResponse, len(rows))
	for _, t := range rows {
		uploaded[strings.ToLower(t.TemplateKey)] = templateResponse{
			Key: t.TemplateKey, Version: t.Version, Filename: t.Filename,
			SizeBytes: t.SizeBytes, UploadedBy: t.UploadedBy, Notes: t.Notes,
			CreatedAt: db.Time(t.CreatedAt), Source: "uploaded",
		}
	}
	return uploaded, nil
}

// templatesForKeys resolves named keys to what actually prints them: the
// uploaded file where one exists, the embedded one otherwise. Order is the
// caller's, so a product lists its designs in variant order rather than
// whichever order the upload table happens to return.
func (s *Server) templatesForKeys(ctx context.Context, keys []string) ([]templateResponse, error) {
	uploaded, err := s.uploadedTemplates(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]templateResponse, 0, len(keys))
	for _, key := range keys {
		if t, ok := uploaded[strings.ToLower(key)]; ok {
			out = append(out, t)
			continue
		}
		out = append(out, templateResponse{Key: key, Source: "embedded"})
	}
	return out, nil
}

func (s *Server) listDesignTemplates(c *gin.Context) {
	uploaded, err := s.uploadedTemplates(c.Request.Context())
	if err != nil {
		detail(c, http.StatusInternalServerError, "Could not list design templates.")
		return
	}

	out := make([]templateResponse, 0, len(embeddedTemplateKeys)+len(uploaded))
	for _, key := range embeddedTemplateKeys {
		if t, ok := uploaded[key]; ok {
			out = append(out, t)
			delete(uploaded, key)
			continue
		}
		out = append(out, templateResponse{Key: key, Source: "embedded"})
	}
	// An upload for a key this binary does not ship is still real and still
	// used; hiding it would make a template render from a file the page denies
	// exists.
	for _, t := range uploaded {
		out = append(out, t)
	}
	c.JSON(http.StatusOK, out)
}

func (s *Server) templateHistory(c *gin.Context) {
	rows, err := s.store.Q.ListTemplateHistory(c.Request.Context(), c.Param("key"))
	if err != nil {
		detail(c, http.StatusInternalServerError, "Could not read the template's history.")
		return
	}
	out := make([]templateResponse, 0, len(rows))
	for _, t := range rows {
		source := "superseded"
		if t.Status == "active" {
			source = "uploaded"
		}
		out = append(out, templateResponse{
			Key: t.TemplateKey, Version: t.Version, Filename: t.Filename,
			SizeBytes: t.SizeBytes, UploadedBy: t.UploadedBy, Notes: t.Notes,
			CreatedAt: db.Time(t.CreatedAt), Source: source,
		})
	}
	c.JSON(http.StatusOK, out)
}

// uploadDesignTemplate stores a new .scad and makes it the one that renders.
func (s *Server) uploadDesignTemplate(c *gin.Context) {
	ctx := c.Request.Context()
	if !s.filesReady(c) {
		return
	}
	key := strings.ToLower(strings.TrimSpace(c.Param("key")))
	if key == "" || strings.ContainsAny(key, "/\\.") {
		detail(c, http.StatusBadRequest, "That is not a valid template key.")
		return
	}

	header, err := c.FormFile("file")
	if err != nil {
		detail(c, http.StatusUnprocessableEntity, "Attach the .scad file to upload.")
		return
	}
	if ext := strings.ToLower(filepath.Ext(header.Filename)); ext != ".scad" {
		detail(c, http.StatusUnprocessableEntity,
			"A template is an OpenSCAD .scad file. Upload the .scad, not the rendered model.")
		return
	}
	if header.Size > maxTemplateBytes {
		detail(c, http.StatusUnprocessableEntity, "That template is larger than 1 MB.")
		return
	}

	src, err := header.Open()
	if err != nil {
		detail(c, http.StatusInternalServerError, "Could not read the uploaded file.")
		return
	}
	defer func() { _ = src.Close() }()
	source, err := io.ReadAll(io.LimitReader(src, maxTemplateBytes+1))
	if err != nil {
		detail(c, http.StatusInternalServerError, "Could not read the uploaded file.")
		return
	}

	// Refused before it is stored, not after it has printed something. A
	// template that does not declare the parameters the renderer passes will
	// silently ignore them - producing a plank with no names on it, which looks
	// like a rendering bug rather than a bad upload.
	if missing := personalise.MissingTemplateParams(source); len(missing) > 0 {
		detail(c, http.StatusUnprocessableEntity, fmt.Sprintf(
			"That template does not declare %s. Tensor sets those with -D, and a "+
				"template without them renders the wrong thing silently.",
			strings.Join(missing, ", ")))
		return
	}

	fileID := uuid.New()
	storageKey := fmt.Sprintf("templates/%s/%s.scad", key, fileID)
	if err := s.storage.Put(ctx, storageKey, bytes.NewReader(source),
		int64(len(source)), "application/x-openscad"); err != nil {
		obs.FromContext(ctx).Error("could not store a design template", "key", key, "error", err)
		detail(c, http.StatusBadGateway, "Could not store the template file.")
		return
	}

	actor := currentUserID(c)
	notes := strings.TrimSpace(c.PostForm("notes"))
	var notesPtr *string
	if notes != "" {
		notesPtr = &notes
	}

	var created gen.DesignTemplate
	err = s.store.InTx(ctx, func(q *gen.Queries) error {
		asset, err := q.InsertFileAsset(ctx, gen.InsertFileAssetParams{
			ID: fileID, Filename: header.Filename, ContentType: "application/x-openscad",
			SizeBytes: int64(len(source)), StorageKey: storageKey, UploadedBy: actor,
		})
		if err != nil {
			return err
		}
		next, err := q.NextTemplateVersion(ctx, key)
		if err != nil {
			return err
		}
		// Retire the old one inside the same transaction as inserting the new,
		// or a failure between them leaves a key with no active template and
		// every plank falling back to the embedded shape.
		if err := q.SupersedeTemplate(ctx, key); err != nil {
			return err
		}
		created, err = q.InsertTemplate(ctx, gen.InsertTemplateParams{
			ID: uuid.New(), TemplateKey: key, FileID: asset.ID,
			Version: next, UploadedBy: actor, Notes: notesPtr,
		})
		return err
	})
	if err != nil {
		obs.FromContext(ctx).Error("could not record a design template", "key", key, "error", err)
		detail(c, http.StatusInternalServerError, "Could not record the template.")
		return
	}

	obs.FromContext(ctx).Info("design template replaced",
		"key", key, "version", created.Version, "by", actor, "bytes", len(source))
	c.JSON(http.StatusCreated, templateResponse{
		Key: key, Version: created.Version, Filename: header.Filename,
		SizeBytes: int64(len(source)), UploadedBy: actor, Notes: notesPtr,
		CreatedAt: db.Time(created.CreatedAt), Source: "uploaded",
	})
}

// templateLoader reads an uploaded template for the renderer.
//
// Returns ok=false when nobody has replaced this key, which is the normal case
// and means "use the embedded copy". An error is only returned when a row says
// there IS an override and the object could not be fetched - see the note on
// personalise.TemplateLoader for why that must not fall back.
func (s *Server) templateLoader() func(context.Context, string) ([]byte, bool, error) {
	return func(ctx context.Context, key string) ([]byte, bool, error) {
		row, err := s.store.Q.GetActiveTemplate(ctx, key)
		if isNoRows(err) {
			return nil, false, nil
		}
		if err != nil {
			return nil, false, err
		}
		if s.storage == nil {
			return nil, false, fmt.Errorf("object storage is not configured")
		}
		obj, err := s.storage.Get(ctx, row.StorageKey)
		if err != nil {
			return nil, false, err
		}
		defer func() { _ = obj.Body.Close() }()
		source, err := io.ReadAll(io.LimitReader(obj.Body, maxTemplateBytes+1))
		if err != nil {
			return nil, false, err
		}
		return source, true, nil
	}
}
