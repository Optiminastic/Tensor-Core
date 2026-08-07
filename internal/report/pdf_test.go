package report

import (
	"bytes"
	"testing"
	"time"
)

func TestBuildDesignReportProducesPDF(t *testing.T) {
	sp := 1299
	data := ReportData{
		DesignName:    "Nameplate Alpha",
		Brand:         "gifting",
		Material:      "PLA",
		Finish:        "none",
		SKU:           "GIF-0001",
		GeneratedAt:   time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC),
		Verdict:       "yellow",
		DesignCP:      240.5,
		CPPct:         0.28,
		RecommendedSP: &sp,
		Metrics: []Metric{
			{Label: "Print time", Value: "2.10 h"},
			{Label: "Filament", Value: "42 g"},
			{Label: "Support", Value: "6 g (used)"},
			{Label: "Units per bed", Value: "4"},
		},
		Breakdown: []BreakdownRow{
			{Label: "Filament", Amount: 120},
			{Label: "Machine hours", Amount: 80},
			{Label: "Finishing labour", Amount: 40.5},
		},
		Suggestions: []string{"Reduce infill to 10%", "Batch 6 per bed to cut machine time"},
	}

	out, err := BuildDesignReport(data)
	if err != nil {
		t.Fatalf("BuildDesignReport: %v", err)
	}
	if !bytes.HasPrefix(out, []byte("%PDF")) {
		t.Errorf("output is not a PDF (prefix = %q)", out[:min(8, len(out))])
	}
	if len(out) < 800 {
		t.Errorf("PDF suspiciously small: %d bytes", len(out))
	}
}

func TestCommaInt(t *testing.T) {
	cases := map[int]string{0: "0", 999: "999", 1299: "1,299", 1234567: "1,234,567", -2500: "-2,500"}
	for in, want := range cases {
		if got := commaInt(in); got != want {
			t.Errorf("commaInt(%d) = %q, want %q", in, got, want)
		}
	}
}
