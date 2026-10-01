package bambubuddy

// What a sliced plate DECLARES it prints with, read out of the file itself.
//
// FilamentRequirements is the obvious source and is the wrong one: BambuBuddy
// drops any filament whose usage rounds to zero. A two-colour plate whose
// second colour is a few centimetres of lettering comes back declaring ONE
// filament, and an ams_mapping sized from that is the wrong length.
//
// Length is the whole point. A printer given a four-entry mapping for a
// two-filament plate halts at the first tool change with
// "[0700-8012] Failed to get AMS mapping table", and pressing Resume simply
// retries the same lookup. Sending two entries for the same plate prints it.
// So the mapping has to be built against what the SLICER produced, not against
// what Tensor asked it to produce - the slicer drops filaments nothing uses.

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
)

// SlicedFilament is one filament slot a sliced plate declares.
type SlicedFilament struct {
	// ID is the plate's own filament id, 1-based, in declaration order. That
	// order IS the ams_mapping order.
	ID int
	// Colour is "#RRGGBB" as the slicer wrote it.
	Colour string
	Type   string
	// UsedForObject is false for a filament the plate declares but no object
	// prints in. Kept rather than filtered on, because filtering is exactly
	// what makes FilamentRequirements unusable here.
	UsedForObject bool
}

// sliceInfoPath is where Bambu Studio writes the plate's filament list.
const sliceInfoPath = "Metadata/slice_info.config"

// maxSlicedPlateBytes bounds a sliced 3MF pulled into memory. Real plates here
// run to about a megabyte; this is headroom, not a target.
const maxSlicedPlateBytes = 96 << 20

// SlicedFilaments reads the filament slots a sliced plate declares.
func (c *Client) SlicedFilaments(ctx context.Context, fileID int) ([]SlicedFilament, error) {
	path := fmt.Sprintf("/api/v1/library/files/%d/download", fileID)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-API-Key", c.apiKey)

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("bambubuddy sliced filaments: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, ReasonError{Reason: rejectionReason(resp.Body, resp.StatusCode)}
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxSlicedPlateBytes))
	if err != nil {
		return nil, fmt.Errorf("bambubuddy sliced filaments: %w", err)
	}
	return ParseSlicedFilaments(body)
}

// sliceInfo is the shape of slice_info.config that matters here.
type sliceInfo struct {
	Plates []struct {
		Filaments []struct {
			ID            string `xml:"id,attr"`
			Type          string `xml:"type,attr"`
			Colour        string `xml:"color,attr"`
			UsedForObject string `xml:"used_for_object,attr"`
		} `xml:"filament"`
	} `xml:"plate"`
}

// ParseSlicedFilaments pulls the filament list out of a sliced 3MF.
//
// Exported so the parsing can be tested against a real plate without a server.
func ParseSlicedFilaments(plate []byte) ([]SlicedFilament, error) {
	zr, err := zip.NewReader(bytes.NewReader(plate), int64(len(plate)))
	if err != nil {
		return nil, fmt.Errorf("sliced plate is not a readable 3MF: %w", err)
	}
	for _, f := range zr.File {
		if f.Name != sliceInfoPath {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return nil, fmt.Errorf("sliced plate: open %s: %w", sliceInfoPath, err)
		}
		raw, err := io.ReadAll(io.LimitReader(rc, maxSlicedPlateBytes))
		_ = rc.Close()
		if err != nil {
			return nil, fmt.Errorf("sliced plate: read %s: %w", sliceInfoPath, err)
		}
		return decodeSliceInfo(raw)
	}
	return nil, fmt.Errorf("sliced plate carries no %s", sliceInfoPath)
}

// decodeSliceInfo reads the FIRST plate's filaments.
//
// First rather than merged: Tensor sends one plate per bed and BambuBuddy
// reports these files as is_multi_plate false, so a second plate would mean the
// file is not the one this bed was sliced into - and silently concatenating two
// plates' filaments would produce a mapping that fits neither.
func decodeSliceInfo(raw []byte) ([]SlicedFilament, error) {
	var info sliceInfo
	if err := xml.Unmarshal(raw, &info); err != nil {
		return nil, fmt.Errorf("sliced plate: %s is not readable: %w", sliceInfoPath, err)
	}
	if len(info.Plates) == 0 {
		return nil, fmt.Errorf("sliced plate declares no plate at all")
	}
	declared := info.Plates[0].Filaments
	out := make([]SlicedFilament, 0, len(declared))
	for i, f := range declared {
		id, err := strconv.Atoi(strings.TrimSpace(f.ID))
		if err != nil {
			// Position is the fallback, because the id is only ever used to
			// keep the order, and losing one entry's id must not lose the file.
			id = i + 1
		}
		out = append(out, SlicedFilament{
			ID:            id,
			Colour:        strings.TrimSpace(f.Colour),
			Type:          strings.TrimSpace(f.Type),
			UsedForObject: strings.EqualFold(strings.TrimSpace(f.UsedForObject), "true"),
		})
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("sliced plate declares no filament")
	}
	return out, nil
}
