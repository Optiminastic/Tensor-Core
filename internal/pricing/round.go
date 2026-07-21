// Package pricing is the pure costing and pricing engine. It has no database,
// no I/O and no globals beyond the built-in assumption constants. Every
// function takes plain inputs and returns plain results, mirroring the original
// Python engine in app/pricing/ one-for-one.
package pricing

import "strconv"

// roundHalfEven rounds x to n decimal places using round-half-to-even (banker's
// rounding), matching Python's built-in round(x, n).
//
// It routes through strconv.FormatFloat, which rounds the exact value of the
// float to n digits half-to-even -- the same rule CPython's round() applies --
// then parses the result back. Reproducing Python's rounding exactly is the
// whole game here: selling prices and CP percentages must match the Python
// backend to the last paisa, and a naive multiply/divide would drift on the
// half-way cases.
func roundHalfEven(x float64, n int) float64 {
	f, _ := strconv.ParseFloat(strconv.FormatFloat(x, 'f', n, 64), 64)
	return f
}

// formatFixed reproduces Python's f"{x:.Nf}" (fixed-point, half-to-even).
func formatFixed(x float64, n int) string {
	return strconv.FormatFloat(x, 'f', n, 64)
}

// formatPercent0 reproduces Python's f"{x:.0%}": multiply by 100, format with no
// decimals half-to-even, then append a percent sign. e.g. 0.2803 -> "28%".
func formatPercent0(x float64) string {
	return strconv.FormatFloat(x*100, 'f', 0, 64) + "%"
}
