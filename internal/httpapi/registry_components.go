package httpapi

// Components: the parts of a product that nobody personalises.
//
// A product is not one printed thing. The diya holder that attaches to the
// Royal, the red heart on a plank, the box a lit plank sits in - each is
// printed separately and none of them carries a customer's name. The
// personalised halves already have a home: a design role with a field mapping
// becomes a production job per order, batched and queued. These had none.
//
// THEY ARE NOT DESIGN ROLES, and that distinction is the whole design. A red
// heart is shared by every colour of plank and by several products; an
// inventory item is shared, a role belongs to one product. Keeping them off
// the role list also keeps them off the job-creation path by construction -
// partRolesForRender walks roles, and a component is not one - rather than by
// the accident that an unmapped role happens to make no job.

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/Optiminastic/tensor-core/internal/db/gen"
)

// Component sources. 'bought' arrives from a supplier; 'printed' the shop
// makes, and must say what from.
const (
	sourceBought  = "bought"
	sourcePrinted = "printed"
)

func (s *Server) registerRegistryComponents(g *gin.RouterGroup, read, manage gin.HandlerFunc) {
	// Authoring lives HERE rather than on /inventory-items, which is guarded by
	// filament:manage - the permission for loading spools. Deciding that a part
	// is made here and binding a design file to it is a configuration act, so
	// it sits with the rest of the registry behind config:manage. The shelf
	// itself - name, count, unit, price - keeps its own routes untouched.
	g.PATCH("/components/:id", manage, s.setComponentSource)
	// The component's OWN design file. A part nobody personalises has nothing
	// to vary, so it is a fixed mesh rather than a .scad - and it cannot go
	// through /registry/templates, whose upload refuses anything but .scad.
	g.POST("/components/:id/model", manage, s.uploadComponentModel)
	g.GET("/components/:id/plates", read, s.listComponentPlates)
	// The SKU travels in the body, not the path, because a SHARED plate has
	// none - a part that does not change with the colourway keeps one set for
	// every SKU, and an empty segment is not a path a router can express.
	g.POST("/components/:id/plates/:family", manage, s.uploadComponentPlate)
	g.DELETE("/components/:id/plates/:family", manage, s.deleteComponentPlate)
}

type componentSourceRequest struct {
	Source string `json:"source" binding:"required,oneof=bought printed"`
	// TemplateKey names a .scad already in the registry, for a component with
	// variables. Exactly one of these two when printed.
	TemplateKey string `json:"template_key"`
	// ModelFileID is a mesh uploaded for this component - the usual case, since
	// a part nobody personalises has nothing to vary.
	ModelFileID string `json:"model_file_id"`
	// ShopifyProductGID is the product this component is a part of. The
	// component is named by choosing it, and its SKUs are what the plates hang
	// off.
	ShopifyProductGID string `json:"shopify_product_gid"`
	// PlateScope is 'shared' when one set of plates serves every SKU, or
	// 'per_sku' when each colourway needs its own.
	PlateScope string `json:"plate_scope" binding:"omitempty,oneof=shared per_sku"`
	// ShopifyProductTitle is that product's name, stored so the registry's
	// picker can say what a component belongs to without a live Shopify call.
	ShopifyProductTitle string `json:"shopify_product_title"`
}

type componentResponse struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Source      string `json:"source"`
	TemplateKey string `json:"template_key"`
	ModelFileID string `json:"model_file_id"`
	// ModelFilename is what the mesh was called, so the dialog can name the
	// file it is about to replace rather than saying "a file is attached".
	ModelFilename       string `json:"model_filename"`
	ShopifyProductGID   string `json:"shopify_product_gid"`
	ShopifyProductTitle string `json:"shopify_product_title"`
	PlateScope          string `json:"plate_scope"`
}

// setComponentSource declares how a component is made.
func (s *Server) setComponentSource(c *gin.Context) {
	id, ok := parseUUIDParam(c, "id")
	if !ok {
		return
	}
	var req componentSourceRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		detail(c, http.StatusUnprocessableEntity,
			"Say whether this part is bought in or printed here.")
		return
	}
	ctx := c.Request.Context()

	key := strings.TrimSpace(req.TemplateKey)
	var fileID *uuid.UUID
	if raw := strings.TrimSpace(req.ModelFileID); raw != "" {
		parsed, err := uuid.Parse(raw)
		if err != nil {
			detail(c, http.StatusUnprocessableEntity, "That model file id is not valid.")
			return
		}
		fileID = &parsed
	}

	// Refused by name here as well as by the CHECK, so the person configuring
	// it reads a sentence rather than a constraint violation.
	if req.Source == sourceBought && (key != "" || fileID != nil) {
		detail(c, http.StatusUnprocessableEntity,
			"A bought-in part has no design file. Mark it printed here, or clear the file.")
		return
	}
	if req.Source == sourcePrinted {
		if (key == "") == (fileID == nil) {
			detail(c, http.StatusUnprocessableEntity,
				"A printed part needs exactly one of a template or an uploaded model, "+
					"not both and not neither.")
			return
		}
		if key != "" && !s.templateExists(ctx, key) {
			detail(c, http.StatusUnprocessableEntity,
				"No design file is registered under "+key+".")
			return
		}
	}

	var keyArg *string
	if key != "" {
		keyArg = &key
	}
	row, err := s.store.Q.SetInventoryItemSource(ctx, gen.SetInventoryItemSourceParams{
		ID: id, Source: req.Source, TemplateKey: keyArg, ModelFileID: fileID,
		ShopifyProductGid:   optional(req.ShopifyProductGID),
		PlateScope:          optional(req.PlateScope),
		ShopifyProductTitle: optional(req.ShopifyProductTitle),
	})
	if err != nil {
		if isNoRows(err) {
			detail(c, http.StatusNotFound, "That part is not on the shelf.")
			return
		}
		detail(c, http.StatusInternalServerError, "Could not save how this part is made.")
		return
	}
	c.JSON(http.StatusOK, componentResponse{
		ID: row.ID.String(), Name: row.Name, Source: row.Source,
		TemplateKey: deref(row.TemplateKey), ModelFileID: uuidString(row.ModelFileID),
		ShopifyProductGID: deref(row.ShopifyProductGid), PlateScope: row.PlateScope,
		ShopifyProductTitle: deref(row.ShopifyProductTitle),
	})
}

// maxComponentModelBytes bounds the mesh. A sub-part is one small object; a
// file far past this is a whole plate or the wrong export.
const maxComponentModelBytes = 64 << 20

// uploadComponentModel stores the mesh a printed component is made from.
//
// It also MARKS THE PART PRINTED and clears any template key, because those
// three facts are one decision: a file arriving here is the answer to "what
// makes this", and leaving the part marked bought-in with a model attached
// would be a state the CHECK refuses anyway.
func (s *Server) uploadComponentModel(c *gin.Context) {
	ctx := c.Request.Context()
	id, ok := parseUUIDParam(c, "id")
	if !ok {
		return
	}
	if _, err := s.store.Q.GetInventoryItem(ctx, id); err != nil {
		detail(c, http.StatusNotFound, "That part is not on the shelf.")
		return
	}

	header, err := c.FormFile("file")
	if err != nil {
		detail(c, http.StatusUnprocessableEntity, "Attach the model to upload.")
		return
	}
	ext := modelExt(header.Filename)
	if ext == "" {
		detail(c, http.StatusUnprocessableEntity,
			"A component's model is an .stl or a .3mf. A parameterised part points at a "+
				".scad in the Designs tab instead.")
		return
	}
	if header.Size > maxComponentModelBytes {
		detail(c, http.StatusUnprocessableEntity, "That file is larger than 64 MB.")
		return
	}
	// Storage is asked for only once the file is known to be one worth storing.
	// Checked first, "you sent the .scad" came back as "storage is not
	// configured" - which names the wrong problem to the only person who can
	// fix the real one.
	if !s.filesReady(c) {
		return
	}

	src, err := header.Open()
	if err != nil {
		detail(c, http.StatusInternalServerError, "Could not read the uploaded file.")
		return
	}
	defer func() { _ = src.Close() }()
	data, err := io.ReadAll(io.LimitReader(src, maxComponentModelBytes+1))
	if err != nil {
		detail(c, http.StatusInternalServerError, "Could not read the uploaded file.")
		return
	}

	fileID := uuid.New()
	key := fmt.Sprintf("components/%s/model-%s%s", id, fileID, ext)
	if err := s.storage.Put(ctx, key, bytes.NewReader(data),
		int64(len(data)), contentTypeFor(ext)); err != nil {
		detail(c, http.StatusBadGateway, "Could not store the model file.")
		return
	}

	who := currentUserID(c)
	var row gen.InventoryItem
	err = s.store.InTx(ctx, func(q *gen.Queries) error {
		if _, err := q.InsertFileAsset(ctx, gen.InsertFileAssetParams{
			ID: fileID, Filename: header.Filename, ContentType: contentTypeFor(ext),
			SizeBytes: int64(len(data)), StorageKey: key, UploadedBy: who,
			IsTemplate: false,
		}); err != nil {
			return err
		}
		saved, err := q.SetInventoryItemSource(ctx, gen.SetInventoryItemSourceParams{
			ID: id, Source: sourcePrinted, TemplateKey: nil, ModelFileID: &fileID,
			ShopifyProductGid: nil, PlateScope: nil, ShopifyProductTitle: nil,
		})
		if err != nil {
			return err
		}
		row = saved
		return nil
	})
	if err != nil {
		detail(c, http.StatusInternalServerError, "Could not save the model.")
		return
	}

	c.JSON(http.StatusOK, componentResponse{
		ID: row.ID.String(), Name: row.Name, Source: row.Source,
		TemplateKey: deref(row.TemplateKey), ModelFileID: uuidString(row.ModelFileID),
		ModelFilename: header.Filename, ShopifyProductGID: deref(row.ShopifyProductGid),
		PlateScope: row.PlateScope, ShopifyProductTitle: deref(row.ShopifyProductTitle),
	})
}

// modelExt is the extension when the file is a mesh Tensor can print, or "".
func modelExt(filename string) string {
	switch {
	case strings.HasSuffix(strings.ToLower(filename), ".stl"):
		return ".stl"
	case strings.HasSuffix(strings.ToLower(filename), ".3mf"):
		return ".3mf"
	default:
		return ""
	}
}

func contentTypeFor(ext string) string {
	if ext == ".3mf" {
		return "model/3mf"
	}
	return "model/stl"
}

// templateExists reports whether a design file is registered under this key.
//
// UPLOADED OR EMBEDDED. The Designs tab lists both - an embedded key is one
// this binary ships with, shown as "Built in" - and checking only the upload
// table refused every key nobody had replaced yet, which is most of them.
func (s *Server) templateExists(ctx context.Context, key string) bool {
	if _, err := s.store.Q.GetActiveTemplate(ctx, key); err == nil {
		return true
	}
	for _, embedded := range embeddedTemplateKeys {
		if strings.EqualFold(embedded, key) {
			return true
		}
	}
	return false
}

func uuidString(id *uuid.UUID) string {
	if id == nil {
		return ""
	}
	return id.String()
}
