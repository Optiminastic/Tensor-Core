package httpapi

// The ready-made plate a printed component is replenished from.
//
// A component nobody personalises is the same every time, so slicing one again
// for every run answers a question already answered. Printing more red hearts
// becomes sending a saved file rather than arranging and slicing a bed.
//
// The file is SLICED G-CODE, chosen over a 3MF for speed on the floor, and
// that choice has one real cost: a sliced plate is only valid for the filament
// and the profile it assumed. Change either and the file is quietly wrong, and
// nothing in it says so. Hence pipeline_name, material and colour beside it -
// not to prevent the problem, which nothing here can, but so the screen can
// say what it assumed instead of an operator finding out at the printer.

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/Optiminastic/tensor-core/internal/db/gen"
	"github.com/Optiminastic/tensor-core/internal/obs"
)

// maxComponentPlateBytes bounds the upload. A sliced plate of one small part
// runs to a few megabytes; far beyond that is a whole bed of something else.
const maxComponentPlateBytes = 64 << 20

type componentPlateResponse struct {
	// SKU and machine class together address one plate: a sliced file carries
	// the filament it was made with, so a colourway needs its own.
	SKU           string `json:"sku"`
	MachineFamily string `json:"machine_family"`
	Filename      string `json:"filename"`
	SizeBytes     int64  `json:"size_bytes"`
	UploadedBy    string `json:"uploaded_by"`
	FileID        string `json:"file_id"`
}

func (s *Server) listComponentPlates(c *gin.Context) {
	id, ok := parseUUIDParam(c, "id")
	if !ok {
		return
	}
	rows, err := s.store.Q.ListComponentPlates(c.Request.Context(), id)
	if err != nil {
		detail(c, http.StatusInternalServerError, "Could not load this part's plates.")
		return
	}
	out := make([]componentPlateResponse, 0, len(rows))
	for _, r := range rows {
		out = append(out, componentPlateResponse{
			SKU: r.Sku, MachineFamily: r.MachineFamily, Filename: r.Filename,
			SizeBytes: r.SizeBytes, UploadedBy: r.UploadedBy,
			FileID: r.FileID.String(),
		})
	}
	c.JSON(http.StatusOK, gin.H{"items": out})
}

// uploadComponentPlate stores one SKU's finished plate for one machine class.
//
// The file is the whole request. Nothing else is asked for: the colour is the
// SKU's own, and a plate somebody has already sliced does not need describing
// again in four fields that can disagree with it.
func (s *Server) uploadComponentPlate(c *gin.Context) {
	ctx := c.Request.Context()
	id, ok := parseUUIDParam(c, "id")
	if !ok {
		return
	}
	family, ok := plateFamilyParam(c)
	if !ok {
		return
	}
	// EMPTY IS A REAL ANSWER: a component whose plate does not change with the
	// colourway keeps one set for every SKU, stored under no SKU at all.
	sku := strings.TrimSpace(c.PostForm("sku"))

	item, err := s.componentForPlate(c, id)
	if err != nil {
		return
	}

	data, filename, ok := readPlateUpload(c)
	if !ok {
		return
	}
	// Asked for only once the file is known to be one worth storing, so a
	// wrong file type is not reported as storage being down.
	if !s.filesReady(c) {
		return
	}

	fileID := uuid.New()
	slot := strings.ToLower(sku)
	if slot == "" {
		slot = "shared"
	}
	key := fmt.Sprintf("components/%s/%s/%s%s", id, slot, fileID, plateExt(filename))
	if err := s.storage.Put(ctx, key, bytes.NewReader(data),
		int64(len(data)), "text/plain"); err != nil {
		detail(c, http.StatusBadGateway, "Could not store the plate file.")
		return
	}

	who := currentUserID(c)
	var row gen.ComponentPlate
	err = s.store.InTx(ctx, func(q *gen.Queries) error {
		if _, err := q.InsertFileAsset(ctx, gen.InsertFileAssetParams{
			ID: fileID, Filename: filename, ContentType: "text/plain",
			SizeBytes: int64(len(data)), StorageKey: key, UploadedBy: who,
			IsTemplate: false,
		}); err != nil {
			return err
		}
		saved, err := q.UpsertComponentPlate(ctx, gen.UpsertComponentPlateParams{
			ID: uuid.New(), InventoryItemID: id, Sku: sku, MachineFamily: family,
			FileID: fileID, Notes: optional(c.PostForm("notes")), UploadedBy: who,
		})
		if err != nil {
			return err
		}
		row = saved
		return nil
	})
	if err != nil {
		detail(c, http.StatusInternalServerError, "Could not save the plate.")
		return
	}
	obs.FromContext(ctx).Info("stored a component plate",
		"component", item.Name, "sku", sku, "family", family)

	c.JSON(http.StatusOK, componentPlateResponse{
		SKU: row.Sku, MachineFamily: row.MachineFamily, Filename: filename,
		SizeBytes: int64(len(data)), UploadedBy: row.UploadedBy,
		FileID: row.FileID.String(),
	})
}

func (s *Server) deleteComponentPlate(c *gin.Context) {
	id, ok := parseUUIDParam(c, "id")
	if !ok {
		return
	}
	family, ok := plateFamilyParam(c)
	if !ok {
		return
	}
	sku := strings.TrimSpace(c.Query("sku"))
	n, err := s.store.Q.DeleteComponentPlate(c.Request.Context(),
		gen.DeleteComponentPlateParams{
			InventoryItemID: id, Sku: sku, MachineFamily: family,
		})
	if err != nil {
		detail(c, http.StatusInternalServerError, "Could not remove the plate.")
		return
	}
	if n == 0 {
		detail(c, http.StatusNotFound, "There is no plate for that SKU on that machine.")
		return
	}
	c.Status(http.StatusNoContent)
}

// componentForPlate refuses a plate on a part the shop does not print.
//
// A bought-in part has nothing to slice, so a plate against one is a mistake
// worth naming rather than a row nobody will ever read.
func (s *Server) componentForPlate(c *gin.Context, id uuid.UUID) (gen.InventoryItem, error) {
	item, err := s.store.Q.GetInventoryItem(c.Request.Context(), id)
	if err != nil {
		detail(c, http.StatusNotFound, "That part is not on the shelf.")
		return item, err
	}
	if item.Source != sourcePrinted {
		detail(c, http.StatusUnprocessableEntity,
			"Only a part printed here has a plate. Mark it printed first.")
		return item, errNotPrinted
	}
	return item, nil
}

var errNotPrinted = fmt.Errorf("component is not printed here")

func plateFamilyParam(c *gin.Context) (string, bool) {
	family := strings.ToUpper(strings.TrimSpace(c.Param("family")))
	for _, known := range machineFamilies() {
		if family == known {
			return family, true
		}
	}
	detail(c, http.StatusUnprocessableEntity,
		"Name a machine class: "+strings.Join(machineFamilies(), ", ")+".")
	return "", false
}

func readPlateUpload(c *gin.Context) ([]byte, string, bool) {
	header, err := c.FormFile("file")
	if err != nil {
		detail(c, http.StatusUnprocessableEntity, "Attach the sliced plate to upload.")
		return nil, "", false
	}
	name := strings.ToLower(header.Filename)
	if !strings.HasSuffix(name, ".gcode") && !strings.HasSuffix(name, ".3mf") {
		detail(c, http.StatusUnprocessableEntity,
			"A plate is the sliced .gcode the printer takes, or the .3mf it came from.")
		return nil, "", false
	}
	if header.Size > maxComponentPlateBytes {
		detail(c, http.StatusUnprocessableEntity, "That file is larger than 64 MB.")
		return nil, "", false
	}
	src, err := header.Open()
	if err != nil {
		detail(c, http.StatusInternalServerError, "Could not read the uploaded file.")
		return nil, "", false
	}
	defer func() { _ = src.Close() }()
	data, err := io.ReadAll(io.LimitReader(src, maxComponentPlateBytes+1))
	if err != nil {
		detail(c, http.StatusInternalServerError, "Could not read the uploaded file.")
		return nil, "", false
	}
	return data, header.Filename, true
}

func plateExt(filename string) string {
	if strings.HasSuffix(strings.ToLower(filename), ".3mf") {
		return ".3mf"
	}
	return ".gcode"
}

// optional is a form field that may be absent, as a nullable column wants it.
func optional(raw string) *string {
	v := strings.TrimSpace(raw)
	if v == "" {
		return nil
	}
	return &v
}
