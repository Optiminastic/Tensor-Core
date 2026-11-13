// Package frontplate is the Premium Dual Name Plank's front text plate: a
// 192.8 x 14.6 mm plate with the customer's front text inlaid in Arial Bold.
//
// The shop's own front_plate.scad is embedded unchanged. It draws two parts -
// "plate" and "text" - which are rendered separately and written as one
// two-colour object, because OpenSCAD's STL carries no colour.
package frontplate

import (
	_ "embed"
	"fmt"
	"strings"
)

// SCAD is the shop's front_plate.scad, unchanged.
//
//go:embed assets/front_plate.scad
var SCAD []byte

// Parts are the pieces the file draws, in the order they are written, with
// the colours the file gives them (plate "white", text "red").
var Parts = []struct{ Name, Colour string }{
	{"plate", "#FFFFFF"},
	{"text", "#FF0000"},
}

// MaxRunes bounds the text. The file shrinks long text to fit the plate, and
// past this it is too small to read.
const MaxRunes = 40

// arialSymbols are the symbols above U+2000 the file draws in Arial; the same
// list as arial_symbols in the .scad.
const arialSymbols = "♥♠♣♦☺☻♪♫☼♀♂•…–—‘’“”€£₹™©®°"

// heartEmoji are drawn by the file as Arial's solid heart.
const heartEmoji = "❤♡💖💕💗💓💞💘💝💟🧡💛💚💙💜🖤🤍🤎"

// Unsupported lists the characters in text the worker cannot draw.
//
// The file draws anything else in an emoji font, and the worker has none:
// fontconfig would not fail, it would substitute another face and the plate
// would print in it. Refused here rather than printed wrong. Latin letters
// (accents included), digits, punctuation, the symbols above and heart emoji
// are all fine.
func Unsupported(text string) []string {
	var bad []string
	seen := map[rune]bool{}
	for _, r := range text {
		ok := r < 0x0250 || r == 0xFE0F || r == 0xFE0E || r == 0x200D ||
			strings.ContainsRune(arialSymbols, r) || strings.ContainsRune(heartEmoji, r)
		if !ok && !seen[r] {
			seen[r] = true
			bad = append(bad, string(r))
		}
	}
	return bad
}

// Filename is the 3MF's name for an order.
func Filename(order string) string {
	return fmt.Sprintf("%s_pdnp_front.3mf", order)
}
