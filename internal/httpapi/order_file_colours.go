package httpapi

// A design file whose colour variables the ORDER fills.
//
// "Declares its own colours" covered two kinds of file. One states a fixed
// colour per piece (Family Heart: TOP_COLOR = "#C12E1E") and is rendered a
// piece at a time. The other works its colours out of the order (Desk Name
// Plank: colors = "Gold & Black") and is rendered once, coloured by the file.
//
// Couple's Date Plank is the second kind written like the first: it declares
// left_name_color = "red" and friends as plain literals, and the registry maps
// the order's FIRST NAME Colour onto them. Read as fixed per-piece colours,
// Tensor derived pieces from the variable names (left_name, couple_date) that
// the file's own `part` switch does not know, and every render came back
// empty. When the mapping fills a colour variable the file declares, the
// order decides the colours, and the file is rendered once like the desk
// plank.

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"

	"github.com/Optiminastic/tensor-core/internal/db/gen"
	"github.com/Optiminastic/tensor-core/internal/meshio"
	"github.com/Optiminastic/tensor-core/internal/obs"
	"github.com/Optiminastic/tensor-core/internal/personalise"
)

// orderSetsFileColours reports whether a flagged file's colour variables are
// filled from the order by this render's mapping.
func (s *Server) orderSetsFileColours(ctx context.Context, plan renderPlan) bool {
	if s.renderer == nil || strings.TrimSpace(plan.Template) == "" {
		return false
	}
	self, err := s.store.Q.IsSelfColouredTemplate(ctx, plan.Template)
	if err != nil || !self {
		return false
	}
	return len(orderColourArgs(s.declaredPartColours(ctx, plan.Template), plan.Args)) > 0
}

// orderColourArgs is the render arguments that set one of the file's declared
// colour variables - <piece>_color or <piece>_colour, in any case.
func orderColourArgs(declared map[string]string, args map[string]string) []string {
	if len(declared) == 0 {
		return nil
	}
	pieces := make(map[string]bool, len(declared))
	for piece := range declared {
		pieces[strings.ToLower(piece)] = true
	}
	var out []string
	for name := range args {
		lower := strings.ToLower(name)
		for _, suffix := range []string{"_color", "_colour"} {
			if piece, ok := strings.CutSuffix(lower, suffix); ok && pieces[piece] {
				out = append(out, name)
			}
		}
	}
	return out
}

// withShopColourHexes replaces each order-filled colour NAME with the shop's
// own swatch for it - the colour map, then the built-in table, the same
// resolution every render uses - so "YELLOW" prints as the yellow spool on the
// shelf rather than OpenSCAD's idea of yellow. A value already a hex, or a
// name nothing knows, is passed through for OpenSCAD to read as it is.
func (s *Server) withShopColourHexes(ctx context.Context, plan renderPlan) renderPlan {
	names := orderColourArgs(s.declaredPartColours(ctx, plan.Template), plan.Args)
	if len(names) == 0 {
		return plan
	}
	args := make(map[string]string, len(plan.Args))
	for k, v := range plan.Args {
		args[k] = v
	}
	for _, name := range names {
		value := unquoteSCAD(args[name])
		if value == "" || strings.HasPrefix(value, "#") {
			continue
		}
		if hex, err := s.resolveColourHex(ctx, value); err == nil {
			args[name] = personalise.Quote(hex)
		}
	}
	plan.Args = args
	return plan
}

// unquoteSCAD reads back a value personalise.Quote wrote.
func unquoteSCAD(v string) string {
	if u, err := strconv.Unquote(strings.TrimSpace(v)); err == nil {
		return strings.TrimSpace(u)
	}
	return strings.Trim(strings.TrimSpace(v), `"`)
}

// setJobColoursFromModel records every colour the rendered model prints in.
//
// The job was created with the variant title's FIRST colour only - "BLUE /
// YELLOW / RED" gives BLUE - and colour decides which printer can take the
// bed. A plank printing blue, yellow and red with only BLUE recorded would be
// sent to a printer that has no yellow loaded. Best effort: a failure costs
// the planner accuracy, not the model.
func (s *Server) setJobColoursFromModel(ctx context.Context, job gen.ProductionJob, model []byte) {
	parts, err := meshio.Read3MF(model)
	if err != nil {
		return
	}
	colours := modelColours(parts)
	if len(colours) == 0 {
		return
	}
	raw, err := json.Marshal(colours)
	if err != nil {
		return
	}
	if err := s.store.Q.SetJobColoursFromModel(ctx, gen.SetJobColoursFromModelParams{
		ID: job.ID, Colours: raw,
	}); err != nil {
		obs.FromContext(ctx).Warn("could not record the model's colours on the job",
			"job", job.JobNumber, "error", err)
	}
}
