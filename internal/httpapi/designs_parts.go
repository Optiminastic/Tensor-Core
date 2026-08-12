package httpapi

import (
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/Optiminastic/tensor-core/internal/db"
	"github.com/Optiminastic/tensor-core/internal/db/gen"
)

// A design's parts are its "recipe": the named components a multi-part product is
// built from (e.g. body, lid, two legs). A design with no parts is a single-part
// product and is unaffected. Roles are unique per design; quantity is how many of an
// identical part one product needs. print_file_id / material / colour / nozzle are
// optional per-part overrides that fall back to the design when unset.

type designPartResponse struct {
	ID            string    `json:"id"`
	DesignID      string    `json:"design_id"`
	Role          string    `json:"role"`
	PartIndex     int32     `json:"part_index"`
	Quantity      int32     `json:"quantity"`
	PrintFileID   *string   `json:"print_file_id"`
	Material      *string   `json:"material"`
	Colour        *string   `json:"colour"`
	NozzleProfile *string   `json:"nozzle_profile"`
	CreatedAt     time.Time `json:"created_at"`
}

type designPartRequest struct {
	Role          string  `json:"role" binding:"required,min=1,max=64"`
	PartIndex     int32   `json:"part_index"`
	Quantity      int32   `json:"quantity"`
	PrintFileID   *string `json:"print_file_id"`
	Material      *string `json:"material"`
	Colour        *string `json:"colour"`
	NozzleProfile *string `json:"nozzle_profile"`
}

type designPartUpdateRequest struct {
	Role          *string `json:"role"`
	PartIndex     *int32  `json:"part_index"`
	Quantity      *int32  `json:"quantity"`
	PrintFileID   *string `json:"print_file_id"`
	Material      *string `json:"material"`
	Colour        *string `json:"colour"`
	NozzleProfile *string `json:"nozzle_profile"`
}

func (s *Server) listDesignParts(c *gin.Context) {
	designID, ok := parseUUIDParam(c, "id")
	if !ok {
		return
	}
	parts, err := s.store.Q.ListDesignPartsByDesign(c.Request.Context(), designID)
	if err != nil {
		detail(c, http.StatusInternalServerError, "Could not load the design's parts.")
		return
	}
	out := make([]designPartResponse, 0, len(parts))
	for _, p := range parts {
		out = append(out, designPartDTO(p))
	}
	c.JSON(http.StatusOK, out)
}

func (s *Server) createDesignPart(c *gin.Context) {
	designID, ok := parseUUIDParam(c, "id")
	if !ok {
		return
	}
	var req designPartRequest
	if !bindJSON(c, &req) {
		return
	}
	if req.Quantity < 1 {
		req.Quantity = 1
	}
	fileID, ok := s.resolveOptionalFile(c, req.PrintFileID)
	if !ok {
		return
	}
	part, err := s.store.Q.InsertDesignPart(c.Request.Context(), gen.InsertDesignPartParams{
		ID: uuid.New(), DesignID: designID, Role: req.Role, PartIndex: req.PartIndex,
		Quantity: req.Quantity, PrintFileID: fileID, Material: req.Material,
		Colour: req.Colour, NozzleProfile: req.NozzleProfile,
	})
	if err != nil {
		if isUniqueViolation(err) {
			detail(c, http.StatusConflict, "This design already has a part with that role.")
			return
		}
		detail(c, http.StatusInternalServerError, "Could not add the part.")
		return
	}
	c.JSON(http.StatusCreated, designPartDTO(part))
}

func (s *Server) updateDesignPart(c *gin.Context) {
	designID, ok := parseUUIDParam(c, "id")
	if !ok {
		return
	}
	partID, ok := parseUUIDParam(c, "partId")
	if !ok {
		return
	}
	var req designPartUpdateRequest
	if !bindJSON(c, &req) {
		return
	}
	if req.Quantity != nil && *req.Quantity < 1 {
		detail(c, http.StatusUnprocessableEntity, "Quantity must be at least 1.")
		return
	}
	ctx := c.Request.Context()

	// A part is only editable through its own design, so its brand-access gate applies.
	existing, err := s.store.Q.GetDesignPart(ctx, partID)
	if err != nil {
		dbError(c, err, "That part does not exist.", "Could not load the part.")
		return
	}
	if existing.DesignID != designID {
		detail(c, http.StatusNotFound, "That part does not exist.")
		return
	}

	// print_file_id is only touched when the field is present in the request, so a
	// partial update never clears a part's file by omission.
	setFile := req.PrintFileID != nil
	var fileID *uuid.UUID
	if setFile {
		fileID, ok = s.resolveOptionalFile(c, req.PrintFileID)
		if !ok {
			return
		}
	}

	updated, err := s.store.Q.UpdateDesignPart(ctx, gen.UpdateDesignPartParams{
		ID: partID, Role: req.Role, PartIndex: req.PartIndex, Quantity: req.Quantity,
		SetPrintFile: setFile, PrintFileID: fileID,
		Material: req.Material, Colour: req.Colour, NozzleProfile: req.NozzleProfile,
	})
	if err != nil {
		if isUniqueViolation(err) {
			detail(c, http.StatusConflict, "This design already has a part with that role.")
			return
		}
		detail(c, http.StatusInternalServerError, "Could not update the part.")
		return
	}
	c.JSON(http.StatusOK, designPartDTO(updated))
}

func (s *Server) deleteDesignPart(c *gin.Context) {
	designID, ok := parseUUIDParam(c, "id")
	if !ok {
		return
	}
	partID, ok := parseUUIDParam(c, "partId")
	if !ok {
		return
	}
	ctx := c.Request.Context()

	existing, err := s.store.Q.GetDesignPart(ctx, partID)
	if err != nil {
		dbError(c, err, "That part does not exist.", "Could not load the part.")
		return
	}
	if existing.DesignID != designID {
		detail(c, http.StatusNotFound, "That part does not exist.")
		return
	}
	if err := s.store.Q.DeleteDesignPart(ctx, partID); err != nil {
		detail(c, http.StatusInternalServerError, "Could not delete the part.")
		return
	}
	c.Status(http.StatusNoContent)
}

func designPartDTO(p gen.DesignPart) designPartResponse {
	return designPartResponse{
		ID: p.ID.String(), DesignID: p.DesignID.String(), Role: p.Role,
		PartIndex: p.PartIndex, Quantity: p.Quantity,
		PrintFileID: uuidPtrStr(p.PrintFileID), Material: p.Material, Colour: p.Colour,
		NozzleProfile: p.NozzleProfile, CreatedAt: db.Time(p.CreatedAt),
	}
}
