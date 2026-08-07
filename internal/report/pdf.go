// Package report renders a design's cost report as a PDF, so a Project Lead can
// download or email a self-contained cost sheet. It is pure: the handler gathers
// the data and this package lays it out - no DB, no I/O beyond returning bytes.
package report

import (
	"bytes"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/go-pdf/fpdf"
)

// Metric is one labelled slice-metric row.
type Metric struct {
	Label string
	Value string
}

// BreakdownRow is one Design CP cost component and its rupee amount.
type BreakdownRow struct {
	Label  string
	Amount float64
}

// ReportData is everything the cost report shows. The handler assembles it from
// the design row, its latest metrics, and its pricing.
type ReportData struct {
	DesignName    string
	Brand         string
	Material      string
	Finish        string
	SKU           string
	GeneratedAt   time.Time
	Verdict       string // green | yellow | red | ""
	DesignCP      float64
	CPPct         float64 // fraction, e.g. 0.28
	RecommendedSP *int
	Metrics       []Metric
	Breakdown     []BreakdownRow
	Suggestions   []string
	// Advisory print-reliability signal (empty band = not assessed).
	FailureBand    string
	FailureScore   int
	FailureFactors []string
}

var verdictColour = map[string][3]int{
	"green":  {22, 122, 63},
	"yellow": {161, 118, 12},
	"red":    {176, 42, 42},
}

// BuildDesignReport lays out the report and returns the PDF bytes.
func BuildDesignReport(d ReportData) ([]byte, error) {
	pdf := fpdf.New("P", "mm", "A4", "")
	pdf.SetTitle("Design Cost Report", false)
	pdf.SetMargins(15, 15, 15)
	pdf.SetAutoPageBreak(true, 15)
	pdf.AddPage()
	tr := pdf.UnicodeTranslatorFromDescriptor("") // UTF-8 -> the core font's Latin-1

	header(pdf, tr, d)
	headline(pdf, tr, d)
	if d.FailureBand != "" {
		section(pdf, tr, "Failure risk")
		pdf.SetFont("Helvetica", "", 10)
		pdf.CellFormat(0, 6.5, fmt.Sprintf("%s  (score %d / 100)", titleCase(d.FailureBand), d.FailureScore),
			"", 1, "L", false, 0, "")
		for _, f := range d.FailureFactors {
			bullet(pdf, tr, f)
		}
	}
	section(pdf, tr, "Slice metrics")
	kvTable(pdf, tr, d.Metrics)
	section(pdf, tr, "Design CP breakdown")
	breakdownTable(pdf, tr, d.Breakdown, d.DesignCP)
	if len(d.Suggestions) > 0 {
		section(pdf, tr, "Suggestions")
		for _, s := range d.Suggestions {
			bullet(pdf, tr, s)
		}
	}

	var buf bytes.Buffer
	if err := pdf.Output(&buf); err != nil {
		return nil, fmt.Errorf("report: render pdf: %w", err)
	}
	return buf.Bytes(), nil
}

func header(pdf *fpdf.Fpdf, tr func(string) string, d ReportData) {
	pdf.SetFont("Helvetica", "B", 18)
	pdf.CellFormat(0, 10, "Design Cost Report", "", 1, "L", false, 0, "")
	pdf.SetFont("Helvetica", "B", 12)
	pdf.CellFormat(0, 7, tr(d.DesignName), "", 1, "L", false, 0, "")

	pdf.SetFont("Helvetica", "", 10)
	pdf.SetTextColor(110, 110, 110)
	meta := fmt.Sprintf("Brand: %s    Material: %s    Finish: %s", titleCase(d.Brand), d.Material, d.Finish)
	if d.SKU != "" {
		meta += "    SKU: " + d.SKU
	}
	pdf.CellFormat(0, 5.5, tr(meta), "", 1, "L", false, 0, "")
	pdf.CellFormat(0, 5.5, "Generated: "+d.GeneratedAt.Format("02 Jan 2006 15:04"), "", 1, "L", false, 0, "")
	pdf.SetTextColor(0, 0, 0)
	pdf.Ln(3)
}

// headline draws the verdict chip and the three key numbers.
func headline(pdf *fpdf.Fpdf, tr func(string) string, d ReportData) {
	if c, ok := verdictColour[d.Verdict]; ok {
		pdf.SetFillColor(c[0], c[1], c[2])
		pdf.SetTextColor(255, 255, 255)
		pdf.SetFont("Helvetica", "B", 10)
		pdf.CellFormat(34, 8, titleCase(d.Verdict), "", 0, "C", true, 0, "")
		pdf.SetTextColor(0, 0, 0)
	}
	pdf.SetFont("Helvetica", "", 10)
	sp := "-"
	if d.RecommendedSP != nil {
		sp = fmt.Sprintf("INR %s", commaInt(*d.RecommendedSP))
	}
	line := fmt.Sprintf("    Design CP: INR %s      CP %%: %.1f%%      Recommended SP: %s",
		money(d.DesignCP), d.CPPct*100, sp)
	pdf.CellFormat(0, 8, tr(line), "", 1, "L", false, 0, "")
	pdf.Ln(3)
}

func section(pdf *fpdf.Fpdf, tr func(string) string, title string) {
	pdf.Ln(2)
	pdf.SetFont("Helvetica", "B", 12)
	pdf.SetTextColor(0, 0, 0)
	pdf.CellFormat(0, 7, tr(title), "", 1, "L", false, 0, "")
	pdf.SetDrawColor(210, 210, 210)
	y := pdf.GetY()
	pdf.Line(15, y, 195, y)
	pdf.Ln(1.5)
}

func kvTable(pdf *fpdf.Fpdf, tr func(string) string, rows []Metric) {
	pdf.SetFont("Helvetica", "", 10)
	fill := false
	for _, r := range rows {
		shade(pdf, fill)
		pdf.SetTextColor(90, 90, 90)
		pdf.CellFormat(70, 6.5, tr(r.Label), "", 0, "L", fill, 0, "")
		pdf.SetTextColor(0, 0, 0)
		pdf.CellFormat(110, 6.5, tr(r.Value), "", 1, "L", fill, 0, "")
		fill = !fill
	}
}

func breakdownTable(pdf *fpdf.Fpdf, tr func(string) string, rows []BreakdownRow, total float64) {
	pdf.SetFont("Helvetica", "", 10)
	fill := false
	for _, r := range rows {
		shade(pdf, fill)
		pdf.SetTextColor(90, 90, 90)
		pdf.CellFormat(120, 6.5, tr(r.Label), "", 0, "L", fill, 0, "")
		pdf.SetTextColor(0, 0, 0)
		pdf.CellFormat(60, 6.5, "INR "+money(r.Amount), "", 1, "R", fill, 0, "")
		fill = !fill
	}
	pdf.SetFont("Helvetica", "B", 10)
	pdf.CellFormat(120, 7, "Design CP", "T", 0, "L", false, 0, "")
	pdf.CellFormat(60, 7, "INR "+money(total), "T", 1, "R", false, 0, "")
}

func bullet(pdf *fpdf.Fpdf, tr func(string) string, text string) {
	pdf.SetFont("Helvetica", "", 10)
	pdf.SetTextColor(0, 0, 0)
	x := pdf.GetX()
	pdf.CellFormat(5, 6, "-", "", 0, "L", false, 0, "")
	pdf.MultiCell(175, 6, tr(text), "", "L", false)
	pdf.SetX(x)
}

func shade(pdf *fpdf.Fpdf, fill bool) {
	if fill {
		pdf.SetFillColor(245, 245, 243)
	}
}

func titleCase(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// money formats a rupee amount with thousands separators, rounded to whole rupees.
func money(v float64) string {
	return commaInt(int(v + 0.5))
}

func commaInt(n int) string {
	neg := n < 0
	if neg {
		n = -n
	}
	s := strconv.Itoa(n)
	var b strings.Builder
	for i := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteByte(s[i])
	}
	out := b.String()
	if neg {
		out = "-" + out
	}
	return out
}
