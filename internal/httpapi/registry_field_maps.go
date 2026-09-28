package httpapi

// Editing which order field feeds which OpenSCAD variable.
//
// The configuration half of per-SKU rendering. A product says "the customer's
// First Name is my NAME_L", and the render path builds -D flags from it
// instead of from a branch in Go.
//
// Edited as a LIST and saved whole, the way a bill of materials is: a
// half-applied edit that left a product mapping a first name and not a second
// would hold every order it touched, and the failure would arrive hours later
// on the issues board rather than in front of the person who caused it.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/Optiminastic/tensor-core/internal/db/gen"
	"github.com/Optiminastic/tensor-core/internal/personalise"
	"github.com/Optiminastic/tensor-core/internal/production"
)

type fieldMapResponse struct {
	// Role is which of the product's design files this row feeds.
	Role string `json:"role"`
	// Required says whether an order that does not answer it is held. False
	// means the variable is simply not passed and the template's default
	// stands - right for a rose the customer chose not to name.
	Required bool `json:"required"`
	// PropertyKey is the normalised form, which is what matching uses and so
	// what the editor must show. Displaying the customer's raw label would be
	// friendlier and would hide the thing that actually has to line up.
	PropertyKey  string `json:"property_key"`
	ScadVariable string `json:"scad_variable"`
	ValueType    string `json:"value_type"`
	Position     int32  `json:"position"`
}

type fieldMapWriteRequest struct {
	Maps []struct {
		PropertyKey  string `json:"property_key" binding:"required,max=120"`
		ScadVariable string `json:"scad_variable" binding:"required,max=64"`
		ValueType    string `json:"value_type" binding:"required,oneof=string number"`
		// Absent means required, so a caller written before optional fields
		// existed keeps the behaviour it was written against.
		Optional bool `json:"optional"`
	} `json:"maps"`
}

// observedPropertyResponse is a field the customers have actually sent.
type observedPropertyResponse struct {
	// Key is normalised - what a mapping must be written against.
	Key string `json:"key"`
	// Label is the storefront's own wording, for recognising it. The same
	// field is labelled three ways across three products, so the raw label is
	// what a person recognises and the normalised key is what matches.
	Label string `json:"label"`
	// Sample is one real answer, which settles "is this the name or the
	// number?" faster than any documentation.
	Sample string `json:"sample"`
	// Orders is how many recent orders carried it. A field on one order in
	// two hundred is probably a retired option rather than something to map.
	Orders int `json:"orders"`
}

func (s *Server) registerRegistryFieldMaps(g *gin.RouterGroup, read, manage gin.HandlerFunc) {
	g.GET("/products/:code/field-maps", read, s.listProductFieldMaps)
	g.PUT("/products/:code/field-maps", manage, s.putProductFieldMaps)
	g.GET("/products/:code/observed-properties", read, s.observedProperties)
	// What this product prints, file by file. One entry for a product that
	// prints one thing, three for a combo.
	g.GET("/products/:code/parts", read, s.listProductParts)
}

func (s *Server) listProductFieldMaps(c *gin.Context) {
	product, ok := s.productByCode(c)
	if !ok {
		return
	}
	rows, err := s.store.Q.ListProductFieldMaps(c.Request.Context(),
		gen.ListProductFieldMapsParams{ProductID: product.ID, Role: roleParam(c)})
	if err != nil {
		detail(c, http.StatusInternalServerError, "Could not read the field mapping.")
		return
	}
	out := make([]fieldMapResponse, 0, len(rows))
	for _, r := range rows {
		out = append(out, fieldMapResponse{
			Role: r.Role, Required: r.Required,
			PropertyKey: r.PropertyKey, ScadVariable: r.ScadVariable,
			ValueType: r.ValueType, Position: r.Position,
		})
	}
	c.JSON(http.StatusOK, out)
}

// fieldMapEntry is one validated row on its way to the database.
type fieldMapEntry struct {
	key, variable, valueType string
	required                 bool
}

// putProductFieldMaps replaces a product's whole mapping.
func (s *Server) putProductFieldMaps(c *gin.Context) {
	product, ok := s.productByCode(c)
	if !ok {
		return
	}
	var req fieldMapWriteRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		detail(c, http.StatusUnprocessableEntity,
			"Each mapped field needs an order field, a variable, and a type of string or number.")
		return
	}
	role := roleParam(c)

	entries := make([]fieldMapEntry, 0, len(req.Maps))
	seen := map[string]bool{}
	for _, m := range req.Maps {
		// Normalised on the way in, never on the way out: the mapping is
		// matched against what the importer stored, and storing the raw label
		// would mean normalising it on every render instead - and getting it
		// wrong once silently.
		key := personalise.NormaliseKey(m.PropertyKey)
		variable := strings.TrimSpace(m.ScadVariable)
		if key == "" || variable == "" {
			detail(c, http.StatusUnprocessableEntity,
				"An order field and a variable are both needed on every row.")
			return
		}
		// Caught here rather than by the unique index, so the message says
		// which variable rather than being a constraint violation.
		if seen[strings.ToLower(variable)] {
			detail(c, http.StatusUnprocessableEntity, fmt.Sprintf(
				"%s is mapped twice. OpenSCAD takes the last -D on the command line, "+
					"so two rows for one variable would make the model depend on the order they were saved in.",
				variable))
			return
		}
		seen[strings.ToLower(variable)] = true
		entries = append(entries, fieldMapEntry{
			key: key, variable: variable, valueType: m.ValueType, required: !m.Optional,
		})
	}

	// Refused before it is saved, while the person who can fix it is here. A
	// mapping naming a variable the template does not declare renders a model
	// missing the thing the customer asked for and exits 0 - OpenSCAD accepts
	// an unknown -D and simply never reads it.
	if why, bad := s.mappingNamesUnknownVariables(c, product, role, entries); bad {
		detail(c, http.StatusUnprocessableEntity, why)
		return
	}

	ctx := c.Request.Context()
	err := s.store.InTx(ctx, func(q *gen.Queries) error {
		// Scoped to this role, or saving the rose's mapping would delete the
		// keychain's on the way past.
		if err := q.ClearProductFieldMaps(ctx, gen.ClearProductFieldMapsParams{
			ProductID: product.ID, Role: role,
		}); err != nil {
			return err
		}
		for i, e := range entries {
			if err := q.InsertProductFieldMap(ctx, gen.InsertProductFieldMapParams{
				ID: uuid.New(), ProductID: product.ID, PropertyKey: e.key,
				ScadVariable: e.variable, ValueType: e.valueType,
				Role: role, Required: e.required, Position: int32(i),
			}); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		detail(c, http.StatusInternalServerError, "Could not save the field mapping.")
		return
	}
	c.Status(http.StatusNoContent)
}

// mappingNamesUnknownVariables checks the mapping against the product's own
// template, when it has one.
//
// Best-effort by design. A product may be mapped before its .scad is uploaded,
// and refusing that would force an order of operations on the person
// configuring it for no reason - the render path checks again anyway, where it
// can fail the one job rather than the whole configuration.
func (s *Server) mappingNamesUnknownVariables(
	c *gin.Context, product gen.Product, role string, entries []fieldMapEntry,
) (string, bool) {
	if s.renderer == nil {
		return "", false
	}
	key, ok := s.templateKeyForRole(c.Request.Context(), product.ID, role)
	if !ok {
		return "", false
	}
	source, err := s.renderer.Source(c.Request.Context(), key)
	if err != nil {
		return "", false
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.variable)
	}
	missing := personalise.DeclaresAll(source, names)
	if len(missing) == 0 {
		return "", false
	}
	return fmt.Sprintf(
		"%s does not declare %s. OpenSCAD accepts a variable it does not read, so a "+
			"model mapped to it would come out missing what the customer asked for.",
		key, strings.Join(missing, ", ")), true
}

// observedProperties lists the fields customers have actually sent for this
// product's SKUs.
//
// The left-hand side of the mapping editor. Without it somebody has to type
// "step 4 first name" exactly, from memory, having first worked out that the
// storefront's "STEP 4-First Name-:" normalises to that - and a key typed one
// character wrong matches nothing, silently, in a way that looks exactly like
// an order with no properties.
func (s *Server) observedProperties(c *gin.Context) {
	product, ok := s.productByCode(c)
	if !ok {
		return
	}
	ctx := c.Request.Context()

	variants, err := s.store.Q.ListVariantsForProduct(ctx, product.ID)
	if err != nil {
		detail(c, http.StatusInternalServerError, "Could not read the product's variants.")
		return
	}
	skus := make(map[string]bool, len(variants))
	for _, v := range variants {
		if v.Sku != nil && strings.TrimSpace(*v.Sku) != "" {
			skus[strings.ToLower(strings.TrimSpace(*v.Sku))] = true
		}
	}
	if len(skus) == 0 {
		// No SKU means no order can be matched to this product at all. An
		// empty list is the honest answer and the page says so.
		c.JSON(http.StatusOK, []observedPropertyResponse{})
		return
	}

	docs, err := s.store.Q.ListRecentOrderLineItems(ctx, observedPropertyOrderLimit)
	if err != nil {
		detail(c, http.StatusInternalServerError, "Could not read recent orders.")
		return
	}

	found := map[string]*observedPropertyResponse{}
	for _, doc := range docs {
		var lines []production.LineItem
		// An order whose line items will not parse loses its contribution to
		// this list, never the page: this is a suggestion box, and one
		// unreadable row is not worth refusing the whole screen over.
		if err := json.Unmarshal(doc, &lines); err != nil {
			continue
		}
		for _, line := range lines {
			if line.SKU == nil || !skus[strings.ToLower(strings.TrimSpace(*line.SKU))] {
				continue
			}
			for _, p := range line.Properties {
				if p.Hidden() || strings.TrimSpace(p.Value) == "" {
					continue
				}
				key := personalise.NormaliseKey(p.Name)
				if key == "" {
					continue
				}
				if seen, ok := found[key]; ok {
					seen.Orders++
					continue
				}
				found[key] = &observedPropertyResponse{
					Key: key, Label: strings.TrimSpace(p.Name),
					Sample: strings.TrimSpace(p.Value), Orders: 1,
				}
			}
		}
	}

	out := make([]observedPropertyResponse, 0, len(found))
	for _, v := range found {
		out = append(out, *v)
	}
	// Commonest first: a field on one order in two hundred is a retired option
	// rather than something to map, and it should not sit above the name.
	sort.Slice(out, func(i, j int) bool {
		if out[i].Orders != out[j].Orders {
			return out[i].Orders > out[j].Orders
		}
		return out[i].Key < out[j].Key
	})
	c.JSON(http.StatusOK, out)
}

// observedPropertyOrderLimit bounds how far back the editor looks.
//
// Enough to cover every option a live product offers, few enough that the
// query stays a page load rather than a report. A field that has not appeared
// in two hundred orders is not one somebody is configuring for today.
const observedPropertyOrderLimit = 200

// productByCode resolves the :code path parameter, answering the request
// itself when it cannot.
//
// By CODE rather than id, matching getRegistryProduct: the code is what a
// person says out loud ("the DNP") and so what a URL should carry.
func (s *Server) productByCode(c *gin.Context) (gen.Product, bool) {
	product, err := s.store.Q.GetProductByCode(
		c.Request.Context(), strings.TrimSpace(c.Param("code")))
	if isNoRows(err) {
		detail(c, http.StatusNotFound, "That product is not in the registry.")
		return gen.Product{}, false
	}
	if err != nil {
		detail(c, http.StatusInternalServerError, "Could not load the product.")
		return gen.Product{}, false
	}
	return product, true
}

// templateKeyForRole is the .scad one of a product's design files resolves to.
//
// One key per role per product: every variant of a plank prints from the same
// file and differs by colour, which is not geometry. Where the variants
// disagree about one role - which the schema allows and nothing yet produces -
// nothing is returned rather than guessing which variant the mapping was
// written against.
func (s *Server) templateKeyForRole(
	ctx context.Context, productID uuid.UUID, role string,
) (string, bool) {
	rows, err := s.store.Q.ListDesignsForProduct(ctx, productID)
	if err != nil {
		return "", false
	}
	var key string
	for _, r := range rows {
		if !strings.EqualFold(strings.TrimSpace(r.Role), strings.TrimSpace(role)) {
			continue
		}
		if r.TemplateKey == nil || strings.TrimSpace(*r.TemplateKey) == "" {
			continue
		}
		k := strings.TrimSpace(*r.TemplateKey)
		if key != "" && !strings.EqualFold(key, k) {
			return "", false
		}
		key = k
	}
	return key, key != ""
}

// designRolesForProduct is the design files this product prints, in order.
//
// Derived from variant_designs rather than stored anywhere of its own: a part
// EXISTS because a file was assigned to it, and a second list would be a
// second thing to keep in step with the first.
//
// Ordered, and stable: job creation walks this to decide how many jobs a line
// becomes, and a set that came back in a different order each time would
// number the same combo's parts differently on two orders.
func (s *Server) designRolesForProduct(ctx context.Context, productID uuid.UUID) []string {
	rows, err := s.store.Q.ListDesignsForProduct(ctx, productID)
	if err != nil {
		return nil
	}
	seen := map[string]bool{}
	roles := make([]string, 0, 3)
	for _, r := range rows {
		if r.TemplateKey == nil || strings.TrimSpace(*r.TemplateKey) == "" {
			// A variant pointing at an uploaded 3MF rather than a template.
			// Real, and not something this can render from a mapping.
			continue
		}
		role := strings.TrimSpace(r.Role)
		if role == "" {
			role = designRoleBody
		}
		if seen[strings.ToLower(role)] {
			continue
		}
		seen[strings.ToLower(role)] = true
		roles = append(roles, role)
	}
	sort.Strings(roles)
	return roles
}

// roleParam is the design file a field-map request is about.
//
// Defaulted rather than required: a product with one file is the common case
// and asking every caller to name "body" would be ceremony. The editor sends
// it explicitly once a product has two.
func roleParam(c *gin.Context) string {
	role := strings.TrimSpace(c.Query("role"))
	if role == "" {
		return designRoleBody
	}
	return role
}
