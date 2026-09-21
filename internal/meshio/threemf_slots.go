package meshio

// Reading a plate's AMS slot declaration back out of the file.
//
// The inverse of plateProjectSettings, and it exists so the send path can ask
// the plate what it declares rather than working it out a second time.
//
// Recomputing would be the tempting shortcut and it is wrong twice over. The
// order is decided by planObjects, which puts multi-part models first so slot 1
// is the plank BODY (#FFFFFF) rather than a lettering colour - a rule nothing
// outside this package knows. And planColoursFromJobs, the obvious stand-in,
// returns only the lettering colours in job order and omits the body colour
// entirely, so a plate's two slots would come back as one. Two places computing
// one order is how 3dmodel.model and this declaration drifted apart before.

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

// Slot is one AMS slot a plate asks for, in the plate's own order.
type Slot struct {
	// Colour is "#RRGGBB" as the plate declares it. That is Tensor's idea of
	// the colour, which is not necessarily the hex the printer reports for the
	// spool - reconciling the two is the caller's job.
	Colour   string
	Material string
}

// ReadPlateSlots returns the slots a merged plate declares.
//
// A plate with no project settings is not an error: an uploaded 3MF from
// anywhere may carry none, and the caller has to decide what to do about a file
// that states no filament requirements. It comes back as no slots.
func ReadPlateSlots(data []byte) ([]Slot, error) {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, fmt.Errorf("read 3mf: %w", err)
	}

	for _, f := range zr.File {
		if !strings.EqualFold(f.Name, projectSettingsPath) {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return nil, fmt.Errorf("open %s: %w", projectSettingsPath, err)
		}
		raw, err := io.ReadAll(rc)
		_ = rc.Close()
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", projectSettingsPath, err)
		}
		return parsePlateSlots(raw)
	}
	return nil, nil
}

// parsePlateSlots turns the two arrays BambuBuddy reads into slots.
//
// The slot count comes from the COLOURS, because a colour with no matching type
// still needs a slot and would otherwise be dropped - the same rule
// plateProjectSettings applies when writing, where the longer array wins.
// DefaultFilamentType fills a missing type rather than leaving it empty, since
// an empty filament type is not something a slicer can act on.
func parsePlateSlots(raw []byte) ([]Slot, error) {
	var settings struct {
		FilamentType   []string `json:"filament_type"`
		FilamentColour []string `json:"filament_colour"`
	}
	if err := json.Unmarshal(raw, &settings); err != nil {
		return nil, fmt.Errorf("parse %s: %w", projectSettingsPath, err)
	}

	slots := make([]Slot, 0, len(settings.FilamentColour))
	for i, colour := range settings.FilamentColour {
		material := DefaultFilamentType
		if i < len(settings.FilamentType) && strings.TrimSpace(settings.FilamentType[i]) != "" {
			material = settings.FilamentType[i]
		}
		slots = append(slots, Slot{Colour: strings.ToUpper(strings.TrimSpace(colour)), Material: material})
	}
	return slots, nil
}
