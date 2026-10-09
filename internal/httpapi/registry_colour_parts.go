package httpapi

// Configuring the coloured pieces of one design file.
//
// Four routes and one idea: a reference 3MF names the pieces and states their
// colours, Tensor reads them, and an editor says which piece follows the
// customer's choice. The designer builds that file in the slicer already, so
// the alternative is re-typing hexes into a form and getting one wrong.

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/Optiminastic/tensor-core/internal/db/gen"
	"github.com/Optiminastic/tensor-core/internal/meshio"
	"github.com/Optiminastic/tensor-core/internal/personalise"
)

// maxReferenceModelBytes bounds the upload. A reference model is one product,
// not a bed, and anything larger is a mistake worth naming rather than a file
// worth parsing.
const maxReferenceModelBytes = 32 << 20

func (s *Server) registerRegistryColourParts(g *gin.RouterGroup, read, manage gin.HandlerFunc) {
	g.GET("/products/:code/colour-parts", read, s.listColourParts)
	// POST, not PUT: the body is a FILE, and the response is what Tensor read
	// out of it rather than what the caller sent.
	g.POST("/products/:code/colour-parts/:role", manage, s.uploadColourReference)
	g.DELETE("/products/:code/colour-parts/:role", manage, s.clearColourParts)
	g.PATCH("/colour-parts/:id", manage, s.updateColourPart)
}

type colourPartResponse struct {
	ID       string `json:"id"`
	Role     string `json:"role"`
	PartName string `json:"part_name"`
	// ColourHex is "#RRGGBB", or empty when the piece follows the customer's
	// choice. Empty rather than null so the frontend has one shape for a value
	// that is either always a colour or absent.
	ColourHex string `json:"colour_hex"`
	Position  int32  `json:"position"`
}

func toColourPartResponse(r gen.DesignColourPart) colourPartResponse {
	return colourPartResponse{
		ID: r.ID.String(), Role: r.Role, PartName: r.PartName,
		ColourHex: deref(r.ColourHex), Position: r.Position,
	}
}

// listColourParts returns every configured piece of a product, across roles.
func (s *Server) listColourParts(c *gin.Context) {
	product, ok := s.productByCode(c)
	if !ok {
		return
	}
	rows, err := s.store.Q.ListAllDesignColourParts(c.Request.Context(), product.ID)
	if err != nil {
		detail(c, http.StatusInternalServerError, "Could not load the colour parts.")
		return
	}
	out := make([]colourPartResponse, 0, len(rows))
	for _, r := range rows {
		out = append(out, toColourPartResponse(r))
	}
	c.JSON(http.StatusOK, gin.H{"items": out})
}

// uploadColourReference reads a 3MF and replaces one role's pieces with it.
//
// REPLACES rather than merges. A piece that has gone from the file has gone
// from the product, and keeping it would ask the template for a piece the
// design no longer has - which renders nothing and holds the job.
func (s *Server) uploadColourReference(c *gin.Context) {
	ctx := c.Request.Context()
	product, ok := s.productByCode(c)
	if !ok {
		return
	}
	role := strings.TrimSpace(c.Param("role"))
	if role == "" {
		detail(c, http.StatusBadRequest, "Name the design file these pieces belong to.")
		return
	}

	data, ok := readUploadedModel(c)
	if !ok {
		return
	}

	raw, err := meshio.Read3MF(data)
	if err != nil {
		detail(c, http.StatusUnprocessableEntity,
			fmt.Sprintf("Could not read that 3MF: %v", err))
		return
	}
	named := make([]threeMFPart, 0, len(raw))
	for _, p := range raw {
		named = append(named, threeMFPart{Name: p.Name, Colour: p.Colour})
	}
	parts, err := partsFrom3MF(named)
	if err != nil {
		detail(c, http.StatusUnprocessableEntity, err.Error())
		return
	}

	// Every piece is rendered before any is stored.
	//
	// This is the whole risk of naming pieces in two places. A template that
	// declares PART and never branches on it answers every request with the
	// same solid; one that spells a piece differently answers that request
	// with nothing. Both slice and both print. Catching it here costs one
	// render per piece, once; catching it later costs a bed.
	if refusal := s.verifyPartsRender(ctx, product.ID, role, parts); refusal != "" {
		detail(c, http.StatusUnprocessableEntity, refusal)
		return
	}

	if err := s.store.Q.ClearDesignColourParts(ctx, gen.ClearDesignColourPartsParams{
		ProductID: product.ID, Role: role,
	}); err != nil {
		detail(c, http.StatusInternalServerError, "Could not replace the colour parts.")
		return
	}

	out := make([]colourPartResponse, 0, len(parts))
	for i, p := range parts {
		var hex *string
		if !p.FollowsCustomer() {
			v := strings.ToUpper(p.Hex)
			hex = &v
		}
		row, err := s.store.Q.InsertDesignColourPart(ctx, gen.InsertDesignColourPartParams{
			ID: uuid.New(), ProductID: product.ID, Role: role,
			PartName: p.Name, ColourHex: hex, Position: int32(i),
		})
		if err != nil {
			detail(c, http.StatusInternalServerError,
				fmt.Sprintf("Could not store the part %q.", p.Name))
			return
		}
		out = append(out, toColourPartResponse(row))
	}
	c.JSON(http.StatusOK, gin.H{"items": out})
}

// readUploadedModel pulls the .3mf off the request, or answers and returns false.
func readUploadedModel(c *gin.Context) ([]byte, bool) {
	header, err := c.FormFile("file")
	if err != nil {
		detail(c, http.StatusUnprocessableEntity, "Attach the reference .3mf to upload.")
		return nil, false
	}
	if !strings.HasSuffix(strings.ToLower(header.Filename), ".3mf") {
		detail(c, http.StatusUnprocessableEntity,
			"A colour reference is a .3mf exported from the slicer, with each piece "+
				"a named object carrying its own colour.")
		return nil, false
	}
	if header.Size > maxReferenceModelBytes {
		detail(c, http.StatusUnprocessableEntity, "That file is larger than 32 MB.")
		return nil, false
	}
	src, err := header.Open()
	if err != nil {
		detail(c, http.StatusInternalServerError, "Could not read the uploaded file.")
		return nil, false
	}
	defer func() { _ = src.Close() }()
	data, err := io.ReadAll(io.LimitReader(src, maxReferenceModelBytes+1))
	if err != nil {
		detail(c, http.StatusInternalServerError, "Could not read the uploaded file.")
		return nil, false
	}
	return data, true
}

// verifyPartsRender asks the template for each piece and reports the first one
// that comes back empty.
//
// Returns a sentence for the operator, or "" when every piece rendered. Skipped
// when OpenSCAD is unavailable: a service that cannot render has no way to
// check, and refusing a configuration on that basis would block the one machine
// where this is configured from the one where it prints.
func (s *Server) verifyPartsRender(
	ctx context.Context, productID uuid.UUID, role string, parts []colourPart,
) string {
	if s.renderer == nil || !s.renderer.Available() {
		return ""
	}
	template, ok := s.templateKeyForRole(ctx, productID, role)
	if !ok {
		// No .scad yet. Configuring colours before uploading the file is a
		// reasonable order to work in, and the pieces are still worth storing.
		return ""
	}
	for _, p := range parts {
		stl, err := s.renderer.RenderSTL(ctx, template, map[string]string{
			"PART": personalise.Quote(p.Name),
		})
		if err != nil {
			return fmt.Sprintf("%s could not be rendered for the part %q: %v",
				template, p.Name, err)
		}
		mesh, err := meshFromSTL(stl)
		if err != nil || len(mesh.Triangles) == 0 {
			return fmt.Sprintf(
				"%s rendered nothing for the part %q. Check that it branches on PART "+
					"and spells this piece exactly as the 3MF names it.", template, p.Name)
		}
	}
	return ""
}

type updateColourPartRequest struct {
	// ColourHex is "#RRGGBB", or empty to make the piece follow the customer's
	// chosen colour.
	ColourHex string `json:"colour_hex" binding:"omitempty,len=7,startswith=#"`
}

// updateColourPart moves one piece between a fixed colour and the order's.
func (s *Server) updateColourPart(c *gin.Context) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		detail(c, http.StatusBadRequest, "That is not a colour part id.")
		return
	}
	var req updateColourPartRequest
	if !bindJSON(c, &req) {
		return
	}
	var hex *string
	if v := strings.ToUpper(strings.TrimSpace(req.ColourHex)); v != "" {
		hex = &v
	}
	row, err := s.store.Q.SetDesignColourPartColour(c.Request.Context(),
		gen.SetDesignColourPartColourParams{ID: id, ColourHex: hex})
	if isNoRows(err) {
		detail(c, http.StatusNotFound, "That colour part no longer exists.")
		return
	}
	if err != nil {
		detail(c, http.StatusInternalServerError, "Could not update the colour part.")
		return
	}
	c.JSON(http.StatusOK, toColourPartResponse(row))
}

// clearColourParts returns one role to the two-pass behaviour.
func (s *Server) clearColourParts(c *gin.Context) {
	product, ok := s.productByCode(c)
	if !ok {
		return
	}
	if err := s.store.Q.ClearDesignColourParts(c.Request.Context(),
		gen.ClearDesignColourPartsParams{
			ProductID: product.ID, Role: strings.TrimSpace(c.Param("role")),
		}); err != nil {
		detail(c, http.StatusInternalServerError, "Could not clear the colour parts.")
		return
	}
	c.Status(http.StatusNoContent)
}
