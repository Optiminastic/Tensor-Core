package slicing

// Some BambuBuddy pipelines slice with one of Bambu's own filaments rather
// than a saved local copy: {"source":"cloud","id":"GFSA00_25"} names a system
// filament by its setting_id, {"source":"standard","id":"Bambu PLA Basic @BBL
// P2S"} by its name. BambuBuddy's API carries no settings for those, so the
// import stores a reference (SystemFilamentRef) and the slice worker, which
// has the same bundled profiles BambuBuddy's slicer resolves them from, turns
// it back into a flat filament here.
//
// A system filament file cannot be handed to the CLI as it is: loaded by path
// its inherits chain does not resolve and the slice comes out with a blank
// filament of density 0. Flattening the chain first gives the same file
// BambuBuddy's saved copies are.

import (
	"fmt"
	"strings"
)

// systemRefKey marks a stored filament preset as a reference to a bundled one.
const systemRefKey = "tensor_system_filament"

// SystemFilamentRef is the preset the import stores for a filament BambuBuddy
// names but cannot hand over: the pipeline's source ("cloud" or "standard")
// and its id (a setting_id or a preset name).
func SystemFilamentRef(source, id string) map[string]any {
	return map[string]any{
		"type": "filament", "from": "System",
		systemRefKey: map[string]any{"source": source, "id": id},
	}
}

// IsSystemFilamentRef reports whether a stored filament preset is a reference.
func IsSystemFilamentRef(preset map[string]any) bool {
	_, ok := preset[systemRefKey]
	return ok
}

// SystemFilamentLabel names a reference for people: the preset name when the
// pipeline gave one, otherwise the setting_id.
func SystemFilamentLabel(source, id string) string {
	if strings.EqualFold(source, "standard") {
		return id
	}
	return "Bambu system filament " + id
}

// ResolveFilament returns preset unchanged unless it is a reference, in which
// case it returns the bundled filament it names, flattened.
func (s SystemPresets) ResolveFilament(preset map[string]any) (map[string]any, error) {
	ref, ok := preset[systemRefKey].(map[string]any)
	if !ok {
		return preset, nil
	}
	id, _ := ref["id"].(string)
	name := s.filamentName(id)
	if name == "" {
		return nil, fmt.Errorf("the Bambu filament %q is not in this Bambu Studio install", id)
	}
	flat := map[string]any{"type": "filament", "inherits": name}
	if _, err := s.FillFromChain(flat); err != nil {
		return nil, err
	}
	flat["filament_settings_id"] = []any{name}
	return flat, nil
}

// filamentName finds a bundled filament by name, or else by setting_id.
func (s SystemPresets) filamentName(id string) string {
	if _, ok := s[id]; ok {
		return id
	}
	for name, p := range s {
		if sid, _ := p["setting_id"].(string); sid != "" && sid == id {
			return name
		}
	}
	return ""
}
