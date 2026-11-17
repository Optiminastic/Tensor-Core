package httpapi

// Any product the registry knows how to make, made from an order.
//
// The registry already holds, per product, the .scad each part prints from
// and how the order's fields fill its variables - the Couple Date Plank maps
// name-left, name-right, date and its three colours to name_1, name_2, date,
// left_name_color, right_name_color and couple_date_color, and nothing else.
// So Product Creation reads it rather than restating it: each registry item
// in the order is rendered exactly as the production worker renders that
// part for a job (renderComboPart), and a new product added to the registry
// can be built here with no change to this file.
//
// One item comes back as its 3MF; several as a ZIP of all of them.

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/Optiminastic/tensor-core/internal/db/gen"
	"github.com/Optiminastic/tensor-core/internal/production"
)

const (
	registryProduct   = "registry"
	coupleDateProduct = "couple-date-plank"
	// coupleDateCode is the Couple Date Plank's registry code.
	coupleDateCode = "COUPLES-DATE-PLANK"
)

// notSlug is what partSlug strips from a registry code to make a file name.
var notSlug = regexp.MustCompile(`[^a-z0-9]+`)

func partSlug(s string) string {
	return strings.Trim(notSlug.ReplaceAllString(strings.ToLower(s), "-"), "-")
}

// registryParts finds the order's lines the registry renders and the parts
// each prints. onlyCode, when set, keeps one product's lines.
func (s *Server) registryParts(ctx context.Context, order gen.Order, onlyCode string) ([]comboPart, string) {
	var lines []production.LineItem
	if err := jsonUnmarshalLines(order.LineItems, &lines); err != nil {
		return nil, "This order's line items could not be read."
	}
	var parts []comboPart
	used := map[string]int{}
	for _, li := range lines {
		li = s.resolveLineSKU(ctx, li)
		sku := strings.TrimSpace(deref(li.SKU))
		if sku == "" {
			continue
		}
		product, err := s.store.Q.FindProductBySKU(ctx, sku)
		if err != nil || (onlyCode != "" && !strings.EqualFold(product.Code, onlyCode)) {
			continue
		}
		for _, role := range s.partRolesForSKU(ctx, sku) {
			// Named for the part when it has a name of its own (plank, rose,
			// couple-date-plank), for the product when it is just "body".
			name := partSlug(product.Code)
			if r := roleOrBody(role); r != designRoleBody {
				name = partSlug(r)
			}
			// The same product twice in one order is two files, not one.
			if used[name]++; used[name] > 1 {
				name = fmt.Sprintf("%s-%d", name, used[name])
			}
			parts = append(parts, comboPart{name: name, sku: sku, role: roleOrBody(role), line: li})
		}
	}
	if len(parts) == 0 {
		if onlyCode == coupleDateCode {
			return nil, fmt.Sprintf("Order %s has no Couple Date Plank.", order.OrderNumber)
		}
		return nil, fmt.Sprintf("Order %s has nothing the registry knows how to make.", order.OrderNumber)
	}
	return parts, ""
}

// startRegistryBuild answers POST .../registry/builds and
// .../couple-date-plank/builds.
func (s *Server) startRegistryBuild(product, onlyCode string) gin.HandlerFunc {
	return func(c *gin.Context) {
		order, _, ok := s.productCreationOrder(c, []string{"3mf"})
		if !ok {
			return
		}
		parts, problem := s.registryParts(c.Request.Context(), order, onlyCode)
		if problem != "" {
			detail(c, http.StatusUnprocessableEntity, problem)
			return
		}
		s.startProductBuild(c, order, product, registryVariant(parts))
	}
}

// registryVariant identifies what the files are made from, so a corrected
// order builds afresh instead of handing back the old files.
func registryVariant(parts []comboPart) string {
	h := sha256.New()
	for _, p := range parts {
		fmt.Fprintf(h, "%s|%s|%s|%s|", p.name, p.sku, p.role, deref(p.line.VariantTitle))
		for _, prop := range p.line.Properties {
			fmt.Fprintf(h, "%s=%s;", prop.Name, prop.Value)
		}
	}
	return hex.EncodeToString(h.Sum(nil)[:8])
}

// registryBuilder builds every registry item in an order, or one product's.
func registryBuilder(onlyCode string) productBuilder {
	return func(ctx context.Context, s *Server, order gen.Order) (builtFile, error) {
		parts, problem := s.registryParts(ctx, order, onlyCode)
		if problem != "" {
			return builtFile{}, errors.New(problem)
		}
		var out builtFile
		var previews [][]byte
		for _, p := range parts {
			model, err := s.renderComboPart(ctx, order, p)
			if err != nil {
				return builtFile{}, fmt.Errorf("%s: %w", p.name, err)
			}
			out.parts = append(out.parts, builtPart{name: p.name, data: model})
			previews = append(previews, previewsOf(p.name, model)...)
		}
		if len(previews) > 0 {
			out.preview = sideBySide(previews)
		}
		if len(out.parts) == 1 {
			out.name = fmt.Sprintf("%s_%s.3mf", order.OrderNumber, out.parts[0].name)
			out.data = out.parts[0].data
			return out, nil
		}
		archive, err := zipParts(order.OrderNumber, out.parts)
		if err != nil {
			return builtFile{}, err
		}
		out.name = order.OrderNumber + "_print_files.zip"
		out.data = archive
		return out, nil
	}
}

// zipParts puts a build's files in one archive, named for the order.
func zipParts(orderNumber string, parts []builtPart) ([]byte, error) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, p := range parts {
		w, err := zw.CreateHeader(&zip.FileHeader{
			Name: fmt.Sprintf("%s_%s.3mf", orderNumber, p.name), Method: zip.Deflate, Modified: time.Now(),
		})
		if err != nil {
			return nil, err
		}
		if _, err := w.Write(p.data); err != nil {
			return nil, err
		}
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
