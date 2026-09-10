package httpapi

// The customer's own names, carried on the orders LIST so the page can be
// searched by them.
//
// The two plank names live in line_items, in a per-line properties array whose
// KEYS move between products - "STEP 4-First Name-" on one shape, "STEP 2 -
// First Name-" on another, "First Name on Plank" on a third. The list response
// deliberately does not ship line_items at all (see orderResponse: shipping
// every row's jsonb to render one integer multiplies the payload for nothing),
// so searching by name had nowhere to look.
//
// This ships the names ALONE - a handful of short strings per order rather than
// the whole document - which keeps that reasoning intact while letting the
// search box stay instant and client-side, the same as the Jobs and Batches
// pages. The server-side ?search= exists too, for pagination and API callers;
// this is what makes typing feel immediate.

import (
	"encoding/json"
	"strings"
)

// nameLikePropKeys are the normalised property keys that hold a person's name.
//
// Matched as whole-word phrases, not substrings, and deliberately broader than
// personalise's own list: this is a search index, so a false positive costs one
// extra searchable string and a false negative costs an order nobody can find.
var nameLikePropKeys = []string{"first name", "second name", "name on", "name"}

// personalisationNamesFor pulls every name the customer typed on an order.
//
// Deduplicated and capped: an order with fifty lines would otherwise carry
// fifty near-identical strings into a list response that exists to be small.
func personalisationNamesFor(raw []byte) []string {
	if len(raw) == 0 {
		return nil
	}
	var lines []struct {
		Properties []struct {
			Name  string `json:"name"`
			Value string `json:"value"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(raw, &lines); err != nil {
		return nil
	}

	seen := map[string]bool{}
	var out []string
	for _, line := range lines {
		for _, p := range line.Properties {
			// Shopify's own hidden attributes start with an underscore and are
			// never a customer's name.
			if strings.HasPrefix(strings.TrimSpace(p.Name), "_") {
				continue
			}
			if !isNameLikeKey(normalisePropKey(p.Name)) {
				continue
			}
			value := strings.TrimSpace(p.Value)
			if value == "" || seen[strings.ToLower(value)] {
				continue
			}
			seen[strings.ToLower(value)] = true
			out = append(out, value)
			if len(out) >= maxSearchableNames {
				return out
			}
		}
	}
	return out
}

// maxSearchableNames bounds what one order contributes to the list payload.
const maxSearchableNames = 12

func isNameLikeKey(normalised string) bool {
	for _, key := range nameLikePropKeys {
		if normalised == key || containsWholePhrase(normalised, key) {
			return true
		}
	}
	return false
}

// containsWholePhrase reports whether label contains key as a run of WHOLE
// words, so "heartfelt message" does not answer to "name" via "nameplate".
func containsWholePhrase(label, key string) bool {
	labelWords := strings.Fields(label)
	keyWords := strings.Fields(key)
	if len(keyWords) == 0 || len(labelWords) < len(keyWords) {
		return false
	}
	for i := 0; i+len(keyWords) <= len(labelWords); i++ {
		match := true
		for j, w := range keyWords {
			if labelWords[i+j] != w {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}
