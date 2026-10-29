package httpapi

// Every stored part file in the registry, in one list.
//
// The registry holds three kinds of file and they live in two tables. Two are
// design templates - the .scad a product PRINTS from, and the .scad its
// customer is SHOWN - and they differ only by the role a variant links them
// under, which is why one file can be both. The third is this: a finished
// file kept against a component, uploaded and never opened again.
//
// Listed here so the Designs tab can show all three. The per-component route
// beside it is what the upload grid reads; this is the library view, and it
// deliberately carries no file contents and no download - a part file is
// reached through the part it belongs to, which is where deleting or replacing
// it makes sense.

import (
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

type partFileResponse struct {
	ID string `json:"id"`
	// ItemID is the component it belongs to - what a link to it is built from.
	ItemID   string `json:"item_id"`
	ItemName string `json:"item_name"`
	ItemCode string `json:"item_code"`
	// SKU is the colourway this file is for, or empty when one file serves
	// every SKU - see migration 0120's plate scope.
	SKU string `json:"sku"`
	// MachineFamily is the class it was sliced for. Meaningful for a .gcode,
	// which has the machine baked in; carried for the others because the grid
	// addresses every file by it.
	MachineFamily string `json:"machine_family"`
	Filename      string `json:"filename"`
	SizeBytes     int64  `json:"size_bytes"`
	// Kind is what the file IS, from its extension: "gcode", "3mf" or "stl".
	// Spelt out rather than left to the frontend so one answer serves every
	// caller.
	Kind       string `json:"kind"`
	Notes      string `json:"notes"`
	UploadedBy string `json:"uploaded_by"`
	CreatedAt  string `json:"created_at"`
}

func (s *Server) listPartFiles(c *gin.Context) {
	rows, err := s.store.Q.ListAllComponentPlates(c.Request.Context())
	if err != nil {
		detail(c, http.StatusInternalServerError, "Could not list the part files.")
		return
	}
	out := make([]partFileResponse, 0, len(rows))
	for _, r := range rows {
		item := partFileResponse{
			ID: r.ID.String(), ItemID: r.ItemID.String(), ItemName: r.ItemName,
			ItemCode: deref(r.ItemCode), SKU: r.Sku,
			MachineFamily: r.MachineFamily, Filename: r.Filename,
			SizeBytes: r.SizeBytes, Kind: partFileKind(r.Filename),
			Notes: deref(r.Notes), UploadedBy: r.UploadedBy,
		}
		if r.CreatedAt.Valid {
			item.CreatedAt = r.CreatedAt.Time.Format(time.RFC3339)
		}
		out = append(out, item)
	}
	c.JSON(http.StatusOK, gin.H{"items": out})
}

// partFileKind names the format from the filename, without the dot.
func partFileKind(filename string) string {
	return strings.TrimPrefix(plateExt(filename), ".")
}
