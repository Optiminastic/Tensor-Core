package httpapi

// How long a customer's text may be, decided without anybody typing it twice.
//
// The limit reaches the page as max_length and the input as maxLength, and it
// used to come from exactly one place: product_field_maps.max_runes on the row
// the renderer resolved. That is correct and it has a hole big enough to have
// swallowed two products.
//
// fieldMapsFor replaces a product's mapping with a SKU's ENTIRELY - all or
// nothing, so a half-written mapping is visibly half written rather than
// silently filled in from the product and pointed at a variable the SKU's own
// file does not declare. That rule is right for the MAPPING. It is wrong for
// the LIMIT, which is not a mapping at all: it names no variable and can point
// at nothing. Applied to it, a child SKU that mapped its one field and left
// the limit blank did not inherit the product's number - it fell to the
// built-in default of twelve.
//
// Measured on this shop: PDHR was configured for 20 and PWDH for 16, both at
// product level, and both were really 12 because every child SKU carried its
// own mapping row. Orders already in the database carry names of 13 and 19
// characters, so customers opening the link could not retype their own name.
// Nothing said so on any screen.
//
// So the limit resolves on its own terms, most specific first:
//
//  1. the SKU's own max_runes - somebody set this for this colourway;
//  2. the product's max_runes - set once, inherited by every child SKU;
//  3. the design file's own MAX_CHARS, if it states one;
//  4. maxCustomerFieldRunes.
//
// Step 3 is what makes a NEW product need no typing at all. The person adding
// one uploads a .scad and knows what its text box holds; stating it in the
// file puts the number where the geometry it describes is, the same bargain
// PartColours already makes for colours. No file has to: a product that
// declares nothing behaves exactly as it does today.

import (
	"context"
	"strconv"
	"strings"

	"github.com/google/uuid"

	"github.com/Optiminastic/tensor-core/internal/db/gen"
	"github.com/Optiminastic/tensor-core/internal/personalise"
)

// declaredLimitVars are the names a design file may state its text limit under.
//
// More than one spelling because these files are written by hand and the cost
// of a near miss is silence - a file saying MAX_LETTERS would simply not be
// read, and look like the feature not working. All three mean the same thing.
var declaredLimitVars = []string{"MAX_CHARS", "MAX_LETTERS", "TEXT_MAX_CHARS"}

// resolveFieldLimits is the precedence above, as a function of its inputs.
//
// Pure, and that is deliberate: the rule is four lines of priority and every
// way of getting it wrong is silent, so it is tested directly rather than
// through a render. own and product are keyed by normalised property key.
//
// Only fields that are EDITABLE get a limit, because a limit on a field the
// customer cannot change is a number nothing reads.
func resolveFieldLimits(
	editable map[string]bool, own, product map[string]int, declared int,
) map[string]int {
	out := make(map[string]int, len(editable))
	for key := range editable {
		switch {
		case own[key] > 0:
			out[key] = own[key]
		case product[key] > 0:
			out[key] = product[key]
		case declared > 0:
			out[key] = declared
		}
	}
	return out
}

// productFieldLimits is the limit the PRODUCT states for each of its fields.
//
// Read separately from fieldMapsFor's answer rather than merged into it: that
// function's all-or-nothing rule is load-bearing for the mapping and must not
// be softened for every caller just because the limit wants inheriting.
func (s *Server) productFieldLimits(
	ctx context.Context, productID uuid.UUID, role string,
) map[string]int {
	rows, err := s.store.Q.ListProductFieldMaps(ctx, gen.ListProductFieldMapsParams{
		ProductID: productID, Role: role,
	})
	if err != nil {
		return nil
	}
	out := make(map[string]int, len(rows))
	for _, r := range rows {
		if r.MaxRunes != nil && *r.MaxRunes > 0 {
			out[personalise.NormaliseKey(r.PropertyKey)] = int(*r.MaxRunes)
		}
	}
	return out
}

// templateDeclaredLimit is the limit a design file states for itself, or zero.
//
// Zero for every file that says nothing, which is all of them today - so this
// adds a way to configure a product and takes none away.
func (s *Server) templateDeclaredLimit(ctx context.Context, key string) int {
	if s.renderer == nil || strings.TrimSpace(key) == "" {
		return 0
	}
	source, err := s.renderer.Source(ctx, key)
	if err != nil || len(source) == 0 {
		return 0
	}
	return declaredLimit(source)
}

// declaredLimit reads MAX_CHARS out of a file's own parameter block.
//
// A value that is not a plain positive whole number is ignored rather than
// guessed at: MAX_CHARS = BOX_W / 6 is an expression this cannot evaluate, and
// a wrong limit silently truncates somebody's name.
func declaredLimit(source []byte) int {
	for _, p := range personalise.DeclaredParams(source) {
		name := strings.ToUpper(strings.TrimSpace(p.Name))
		if !isDeclaredLimitVar(name) {
			continue
		}
		n, err := strconv.Atoi(strings.TrimSpace(strings.Trim(strings.TrimSpace(p.Default), `"'`)))
		if err != nil || n <= 0 || n > maxDeclaredLimit {
			continue
		}
		return n
	}
	return 0
}

// maxDeclaredLimit bounds what a file may claim, matching the 1..120 the
// registry column already enforces. A file asking for 10000 is a typo, and
// honouring it would put a public form behind no limit at all.
const maxDeclaredLimit = 120

func isDeclaredLimitVar(upperName string) bool {
	for _, v := range declaredLimitVars {
		if upperName == v {
			return true
		}
	}
	return false
}
