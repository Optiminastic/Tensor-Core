package bambubuddy

import (
	"archive/zip"
	"bytes"
	"testing"
)

// plateZip builds a 3MF carrying one slice_info.config.
func plateZip(t *testing.T, sliceInfo string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, err := zw.Create(sliceInfoPath)
	if err != nil {
		t.Fatalf("create %s: %v", sliceInfoPath, err)
	}
	if _, err := w.Write([]byte(sliceInfo)); err != nil {
		t.Fatalf("write %s: %v", sliceInfoPath, err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("close zip: %v", err)
	}
	return buf.Bytes()
}

// Verbatim from the plate that halted H2 and H3, trimmed to the filaments.
// BambuBuddy's own /filament-requirements returned ONE filament for this file,
// because the yellow's usage rounds to zero - which is why the file is read
// instead.
const h2cWhiteYellow = `<?xml version="1.0" encoding="UTF-8"?>
<config>
  <plate>
    <metadata key="index" value="1"/>
    <metadata key="filament_maps" value="2 1 1 1"/>
    <filament id="1" tray_info_idx="OFoiVqVM" type="PLA" color="#FFFFFF" used_m="0.01" used_g="0.03" used_for_object="true"/>
    <filament id="2" tray_info_idx="OFoiVqVM" type="PLA" color="#FFF144" used_m="0.00" used_g="0.00" used_for_object="true"/>
  </plate>
</config>`

func TestParseSlicedFilamentsKeepsAFilamentWithZeroUsage(t *testing.T) {
	got, err := ParseSlicedFilaments(plateZip(t, h2cWhiteYellow))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("declared filaments = %d, want 2 (the zero-usage yellow must survive)", len(got))
	}
	if got[0].Colour != "#FFFFFF" || got[1].Colour != "#FFF144" {
		t.Fatalf("colours = %q, %q", got[0].Colour, got[1].Colour)
	}
	if got[0].ID != 1 || got[1].ID != 2 {
		t.Fatalf("ids = %d, %d; declaration order is the ams_mapping order", got[0].ID, got[1].ID)
	}
	if !got[1].UsedForObject {
		t.Fatal("the yellow prints an object; used_for_object must be read, not inferred from usage")
	}
}

func TestParseSlicedFilamentsReadsTypeAndOrder(t *testing.T) {
	got, err := ParseSlicedFilaments(plateZip(t, h2cWhiteYellow))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for i, f := range got {
		if f.Type != "PLA" {
			t.Fatalf("filament %d type = %q, want PLA", i+1, f.Type)
		}
	}
}

func TestParseSlicedFilamentsRefusesANonThreeMF(t *testing.T) {
	if _, err := ParseSlicedFilaments([]byte("not a zip")); err == nil {
		t.Fatal("expected a refusal for something that is not a 3MF")
	}
}

func TestParseSlicedFilamentsRefusesAPlateWithoutSliceInfo(t *testing.T) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, _ := zw.Create("Metadata/model_settings.config")
	_, _ = w.Write([]byte("<config/>"))
	_ = zw.Close()

	if _, err := ParseSlicedFilaments(buf.Bytes()); err == nil {
		t.Fatal("expected a refusal when the plate carries no slice_info.config")
	}
}

func TestParseSlicedFilamentsRefusesAPlateDeclaringNoFilament(t *testing.T) {
	_, err := ParseSlicedFilaments(plateZip(t, `<?xml version="1.0"?><config><plate/></config>`))
	if err == nil {
		t.Fatal("expected a refusal for a plate declaring no filament")
	}
}
