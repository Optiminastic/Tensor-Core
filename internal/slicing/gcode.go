package slicing

// Turns Bambu Studio's slice output into physical metrics. Ported from the Python
// worker's gcode.py. Three sources:
//   - result.json (next to the export): print time, layer height, infill, walls,
//     per-feature times, filament-change count.
//   - Metadata/slice_info.config inside the .gcode.3mf: filament length (used_m)
//     and whether support was used.
//   - Metadata/plate_1.gcode inside the archive: the toolpath, read to split
//     filament between model and support (Bambu tags each region "; FEATURE: <name>"
//     and emits relative E).
//
// The CLI's reported grams are unreliable - they came back 0 on the H2S profile
// this pipeline originally used, whose bundled density is 0, and nothing
// guarantees the current H2C presets are any better. Weight is therefore always
// derived from length x cross-section x the density in profiles.go's
// materialFilament, and the support share from the gcode's per-feature extrusion.

import (
	"archive/zip"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"math"
	"os"
	"regexp"
	"strconv"
	"strings"
)

const filamentDiameterMM = 1.75

const plateGcodeName = "Metadata/plate_1.gcode"

var eRE = regexp.MustCompile(`(?:^|\s)E(-?\d*\.?\d+)`)

// SliceMetrics is the whole-plate physical output of one slice.
type SliceMetrics struct {
	PrintTimeS       float64
	FilamentLengthMM float64
	FilamentWeightG  float64
	SupportWeightG   float64
	PurgeWeightG     float64
	ColourChanges    int
	LayerHeightMM    float64
	InfillDensityPct float64
	WallLoops        int
	SupportUsed      bool
}

// ResultJSON is the slicer's result.json (only the fields we read).
type ResultJSON struct {
	ReturnCode          *int          `json:"return_code"`
	ErrorString         string        `json:"error_string"`
	LayerHeight         float64       `json:"layer_height"`
	SparseInfillDensity float64       `json:"sparse_infill_density"`
	WallLoops           int           `json:"wall_loops"`
	SlicedPlates        []resultPlate `json:"sliced_plates"`
}

type resultPlate struct {
	TotalPredication    *float64           `json:"total_predication"`
	MainPredication     *float64           `json:"main_predication"`
	LayerHeight         float64            `json:"layer_height"`
	FeatureTypeTimes    map[string]float64 `json:"feature_type_times"`
	FilamentChangeTimes int                `json:"filament_change_times"`
}

func round2(v float64) float64 { return math.Round(v*100) / 100 }

// WeightFromLength derives grams from filament length: volume(cm^3) x density.
func WeightFromLength(lengthMM, densityGCm3 float64) float64 {
	areaMM2 := math.Pi * math.Pow(filamentDiameterMM/2.0, 2)
	volumeCm3 := lengthMM * areaMM2 / 1000.0
	return round2(volumeCm3 * densityGCm3)
}

// LoadResultJSON reads and decodes the slicer's result.json.
func LoadResultJSON(path string) (ResultJSON, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return ResultJSON{}, err
	}
	var result ResultJSON
	if err := json.Unmarshal(raw, &result); err != nil {
		return ResultJSON{}, err
	}
	return result, nil
}

// LoadSliceInfo reads Metadata/slice_info.config from the exported .gcode.3mf.
func LoadSliceInfo(gcode3mfPath string) (string, error) {
	return readArchiveMember(gcode3mfPath, "Metadata/slice_info.config", false)
}

// LoadPlateGcode reads the plate's G-code from the archive; "" if absent.
func LoadPlateGcode(gcode3mfPath string) (string, error) {
	body, err := readArchiveMember(gcode3mfPath, plateGcodeName, true)
	if err == nil {
		return body, nil
	}
	// Fall back to the first Metadata/plate_*.gcode present.
	archive, err := zip.OpenReader(gcode3mfPath)
	if err != nil {
		return "", err
	}
	defer archive.Close()
	name := ""
	for _, f := range archive.File {
		if strings.HasPrefix(f.Name, "Metadata/plate_") && strings.HasSuffix(f.Name, ".gcode") {
			if name == "" || f.Name < name {
				name = f.Name
			}
		}
	}
	if name == "" {
		return "", nil
	}
	return readArchiveMember(gcode3mfPath, name, true)
}

func readArchiveMember(path, member string, missingOK bool) (string, error) {
	archive, err := zip.OpenReader(path)
	if err != nil {
		return "", err
	}
	defer archive.Close()
	for _, f := range archive.File {
		if f.Name == member {
			rc, err := f.Open()
			if err != nil {
				return "", err
			}
			defer rc.Close()
			raw, err := io.ReadAll(rc)
			if err != nil {
				return "", err
			}
			return string(raw), nil
		}
	}
	if missingOK {
		return "", fmt.Errorf("archive member %q not found", member)
	}
	return "", fmt.Errorf("archive member %q not found", member)
}

// SupportFraction is the share (0..1) of extruded filament spent on support,
// from the gcode's per-feature relative-E. Support regions are Support, Support
// interface and Support transition. It is a ratio, so it stays correct even
// though absolute E and the reported total length differ slightly.
func SupportFraction(gcodeText string) float64 {
	feature := ""
	var total, support float64
	for _, line := range strings.Split(gcodeText, "\n") {
		if strings.HasPrefix(line, ";") {
			if strings.HasPrefix(line, "; FEATURE:") {
				feature = strings.ToLower(strings.TrimSpace(line[len("; FEATURE:"):]))
			}
			continue
		}
		if (strings.HasPrefix(line, "G1") || strings.HasPrefix(line, "G0")) && strings.Contains(line, "E") {
			m := eRE.FindStringSubmatch(line)
			if m == nil {
				continue
			}
			e, err := strconv.ParseFloat(m[1], 64)
			if err != nil {
				continue
			}
			total += e
			if feature != "" && strings.HasPrefix(feature, "support") {
				support += e
			}
		}
	}
	if total <= 0 {
		return 0
	}
	frac := support / total
	if frac < 0 {
		return 0
	}
	if frac > 1 {
		return 1
	}
	return frac
}

type sliceInfoConfig struct {
	Plates []sliceInfoPlate `xml:"plate"`
}

type sliceInfoPlate struct {
	Metadata  []sliceInfoAttr `xml:"metadata"`
	Filaments []struct {
		UsedM string `xml:"used_m,attr"`
	} `xml:"filament"`
}

type sliceInfoAttr struct {
	Key   string `xml:"key,attr"`
	Value string `xml:"value,attr"`
}

func lengthAndSupport(sliceInfoXML string) (lengthMM float64, supportUsed bool, err error) {
	var cfg sliceInfoConfig
	if err := xml.Unmarshal([]byte(sliceInfoXML), &cfg); err != nil {
		return 0, false, err
	}
	for _, plate := range cfg.Plates {
		for _, f := range plate.Filaments {
			if f.UsedM == "" {
				continue
			}
			if v, perr := strconv.ParseFloat(f.UsedM, 64); perr == nil {
				lengthMM += v * 1000.0
			}
		}
		for _, m := range plate.Metadata {
			if m.Key == "support_used" {
				supportUsed = strings.EqualFold(m.Value, "true")
			}
		}
	}
	return lengthMM, supportUsed, nil
}

// ExtractMetrics turns the three slice outputs into whole-plate physical metrics.
func ExtractMetrics(result ResultJSON, sliceInfoXML string, densityGCm3 float64, gcodeText string) (SliceMetrics, error) {
	if result.ReturnCode == nil || *result.ReturnCode != 0 {
		msg := result.ErrorString
		if msg == "" {
			msg = "slice failed"
		}
		return SliceMetrics{}, fmt.Errorf("%s", msg)
	}
	if len(result.SlicedPlates) == 0 {
		return SliceMetrics{}, fmt.Errorf("result.json has no sliced_plates")
	}
	plate := result.SlicedPlates[0]

	timeS := firstNonZero(plate.TotalPredication, plate.MainPredication)
	lengthMM, supportUsed, err := lengthAndSupport(sliceInfoXML)
	if err != nil {
		return SliceMetrics{}, err
	}

	supportInFeatures := false
	for key := range plate.FeatureTypeTimes {
		if strings.Contains(strings.ToLower(key), "support") {
			supportInFeatures = true
			break
		}
	}

	totalWeightG := WeightFromLength(lengthMM, densityGCm3)
	// Support weight is the support share of the toolpath applied to the reliable
	// total weight, so it stays consistent with filament_weight_g.
	supportWeightG := round2(totalWeightG * SupportFraction(gcodeText))

	layerHeight := result.LayerHeight
	if layerHeight == 0 {
		layerHeight = plate.LayerHeight
	}

	return SliceMetrics{
		PrintTimeS:       timeS,
		FilamentLengthMM: round2(lengthMM),
		FilamentWeightG:  totalWeightG,
		SupportWeightG:   supportWeightG,
		// Flush/purge only occurs at colour changes; a single-colour print has none.
		PurgeWeightG:     0,
		ColourChanges:    plate.FilamentChangeTimes,
		LayerHeightMM:    layerHeight,
		InfillDensityPct: result.SparseInfillDensity,
		WallLoops:        result.WallLoops,
		SupportUsed:      supportUsed || supportInFeatures,
	}, nil
}

func firstNonZero(vals ...*float64) float64 {
	for _, v := range vals {
		if v != nil && *v != 0 {
			return *v
		}
	}
	return 0
}
