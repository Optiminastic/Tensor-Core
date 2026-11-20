package slicing

// Fills a User preset with every key its bundled system parents define.
//
// A User process inherits a system one ("0.20mm Standard @BBL H2C"), which
// inherits a chain of its own (fdm_process_dual_0.20_nozzle_0.4 <-
// fdm_process_dual_common <- fdm_process_common). BambuBuddy resolves that
// whole chain. The CLI, given a User preset file, does not reach every level
// and falls back to built-in defaults for what it misses. In the 2026-11-20
// spike that left three settings different from BambuBuddy's slice of the
// same bed (support ironing spacing and speed, monotonic travel into wall).
// With the chain filled in from the bundled profiles, all 559 settings matched.
//
// Keys the preset already sets are never touched: the preset wins, the chain
// only fills gaps. The preset keeps its inherits, so the CLI's printer and
// process compatibility checks still find the parent.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// presetMetaKeys describe a preset rather than configure the slicer, so they
// are never copied from a parent.
var presetMetaKeys = map[string]bool{
	"name": true, "from": true, "inherits": true, "instantiation": true,
	"setting_id": true, "type": true, "compatible_printers": true,
	"include": true, "version": true, "description": true,
}

// SystemPresets indexes one kind of bundled Bambu preset ("process",
// "machine", "filament") by name.
type SystemPresets map[string]map[string]any

// LoadSystemPresets reads every bundled preset of one kind under bambuRoot.
func LoadSystemPresets(bambuRoot, kind string) (SystemPresets, error) {
	dir := filepath.Join(bambuRoot, "resources", "profiles", "BBL", kind)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("read bundled %s presets: %w", kind, err)
	}
	out := SystemPresets{}
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".json" {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			return nil, err
		}
		var p map[string]any
		// Bambu ships a few non-preset JSON files beside the presets; skip any
		// that do not parse or carry no name rather than failing the slice.
		if json.Unmarshal(b, &p) != nil {
			continue
		}
		if name, _ := p["name"].(string); name != "" {
			out[name] = p
		}
	}
	return out, nil
}

// FillFromChain adds every non-meta key defined along preset's inherits chain
// that preset does not set itself. It returns how many keys it added, and an
// error when the named parent is not bundled, which means the profile was
// made for a different Bambu Studio version than the one installed.
func (s SystemPresets) FillFromChain(preset map[string]any) (int, error) {
	parent, _ := preset["inherits"].(string)
	if parent == "" {
		return 0, nil
	}
	chain, err := s.chain(parent)
	if err != nil {
		return 0, err
	}
	merged := map[string]any{}
	for _, p := range chain {
		for k, v := range p {
			merged[k] = v
		}
	}
	added := 0
	for k, v := range merged {
		if presetMetaKeys[k] {
			continue
		}
		if _, set := preset[k]; set {
			continue
		}
		preset[k] = v
		added++
	}
	return added, nil
}

// chain is name's ancestry, root first.
func (s SystemPresets) chain(name string) ([]map[string]any, error) {
	var out []map[string]any
	seen := map[string]bool{}
	for name != "" {
		if seen[name] {
			return nil, fmt.Errorf("bundled preset %q inherits itself", name)
		}
		seen[name] = true
		p, ok := s[name]
		if !ok {
			return nil, fmt.Errorf("bundled preset %q is not in this Bambu Studio install", name)
		}
		out = append([]map[string]any{p}, out...)
		name, _ = p["inherits"].(string)
	}
	return out, nil
}
