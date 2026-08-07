package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/Optiminastic/tensor-core/internal/mailer"
	"github.com/Optiminastic/tensor-core/internal/report"
)

// downloadDesignReport builds and streams the design's cost report as a PDF: the
// slice metrics, failure risk, the Design CP breakdown, the verdict, the
// recommended selling price, and the suggestions - a self-contained sheet a
// Project Lead can save or email.
func (s *Server) downloadDesignReport(c *gin.Context) {
	id, ok := parseUUIDParam(c, "id")
	if !ok {
		return
	}
	data, base, err := s.gatherDesignReport(c.Request.Context(), id)
	if err != nil {
		dbError(c, err, "That design does not exist.", "Could not load the design.")
		return
	}
	pdf, err := report.BuildDesignReport(data)
	if err != nil {
		s.logger.Error("report build failed", "design", id, "error", err)
		detail(c, http.StatusInternalServerError, "Could not build the cost report.")
		return
	}
	filename := base + "-cost-report.pdf"
	c.Header("Content-Disposition", "attachment; filename=\""+filename+"\"")
	c.Data(http.StatusOK, "application/pdf", pdf)
}

// emailDesignReport builds the cost-report PDF and emails it to a recipient. Fails
// closed (503) when SMTP is not configured.
func (s *Server) emailDesignReport(c *gin.Context) {
	if !s.cfg.EmailConfigured() {
		detail(c, http.StatusServiceUnavailable, "Email is not configured.")
		return
	}
	id, ok := parseUUIDParam(c, "id")
	if !ok {
		return
	}
	var req struct {
		To string `json:"to" binding:"required,email"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		detail(c, http.StatusUnprocessableEntity, "Enter a valid recipient email address.")
		return
	}

	data, base, err := s.gatherDesignReport(c.Request.Context(), id)
	if err != nil {
		dbError(c, err, "That design does not exist.", "Could not load the design.")
		return
	}
	pdf, err := report.BuildDesignReport(data)
	if err != nil {
		s.logger.Error("report build failed", "design", id, "error", err)
		detail(c, http.StatusInternalServerError, "Could not build the cost report.")
		return
	}

	err = mailer.Send(
		mailer.Config{Host: s.cfg.SMTPHost, Port: s.cfg.SMTPPort, User: s.cfg.SMTPUser, Pass: s.cfg.SMTPPass, From: s.cfg.SMTPFrom},
		req.To,
		"Cost report: "+data.DesignName,
		fmt.Sprintf("Attached is the cost report for %q.", data.DesignName),
		mailer.Attachment{Filename: base + "-cost-report.pdf", ContentType: "application/pdf", Data: pdf},
	)
	if err != nil {
		s.logger.Error("email report failed", "design", id, "error", err)
		detail(c, http.StatusBadGateway, "Could not send the email. Check the SMTP settings.")
		return
	}
	c.JSON(http.StatusOK, gin.H{"sent": true, "to": req.To})
}

// gatherDesignReport loads a design and assembles its report data (shared by the
// PDF download and the email). Returns the data and a safe filename base.
func (s *Server) gatherDesignReport(ctx context.Context, id uuid.UUID) (report.ReportData, string, error) {
	d, err := s.store.Q.GetDesignByID(ctx, id)
	if err != nil {
		return report.ReportData{}, "", err
	}
	metrics, err := s.latestMetrics(ctx, id)
	if err != nil {
		return report.ReportData{}, "", err
	}
	pricing, err := s.designPricing(ctx, id)
	if err != nil {
		return report.ReportData{}, "", err
	}

	data := report.ReportData{
		DesignName:  d.Name,
		Brand:       d.BrandSlug,
		Material:    d.Material,
		Finish:      d.Finish,
		SKU:         strVal(d.Sku),
		GeneratedAt: time.Now(),
	}
	if metrics != nil {
		data.Metrics = reportMetrics(metrics)
		if risk := failureRiskFor(metrics); risk != nil {
			data.FailureBand = risk.Band
			data.FailureScore = risk.Score
			data.FailureFactors = risk.Factors
		}
	}
	if pricing != nil {
		data.Verdict = pricing.Verdict
		data.DesignCP = pricing.DesignCP
		data.CPPct = pricing.CPPct
		data.RecommendedSP = pricing.RecommendedSP
		data.Breakdown = reportBreakdown(pricing.Breakdown)
		data.Suggestions = pricing.Suggestions
	}
	return data, safeName(d.Name), nil
}

func reportMetrics(m *metricsResponse) []report.Metric {
	return []report.Metric{
		{Label: "Print time", Value: fmt.Sprintf("%.2f h", m.PrintTimeHr)},
		{Label: "Effective machine time / unit", Value: fmt.Sprintf("%.2f h", m.EffectiveMachineTimeHr)},
		{Label: "Filament", Value: fmt.Sprintf("%.0f g", m.FilamentG)},
		{Label: "Purge / waste", Value: fmt.Sprintf("%.0f g", m.PurgeG)},
		{Label: "Support", Value: supportLabel(m)},
		{Label: "Units per bed", Value: fmt.Sprintf("%d", m.UnitsPerBed)},
		{Label: "Layer height", Value: fmt.Sprintf("%.2f mm", m.LayerHeightMm)},
		{Label: "Infill", Value: fmt.Sprintf("%.0f%%", m.InfillDensityPct)},
		{Label: "Wall loops", Value: fmt.Sprintf("%d", m.WallLoops)},
		{Label: "Colour changes", Value: fmt.Sprintf("%d", m.ColourChanges)},
	}
}

func supportLabel(m *metricsResponse) string {
	if m.SupportUsed {
		return fmt.Sprintf("%.0f g (used)", m.SupportG)
	}
	return "none"
}

// reportBreakdown parses the stored Design CP breakdown JSON into ordered rows.
func reportBreakdown(raw json.RawMessage) []report.BreakdownRow {
	var b struct {
		Filament         float64 `json:"filament"`
		Purge            float64 `json:"purge"`
		Electricity      float64 `json:"electricity"`
		Machine          float64 `json:"machine"`
		Finishing        float64 `json:"finishing"`
		Consumables      float64 `json:"consumables"`
		FailureProvision float64 `json:"failure_provision"`
	}
	if len(raw) == 0 || json.Unmarshal(raw, &b) != nil {
		return nil
	}
	return []report.BreakdownRow{
		{Label: "Filament", Amount: b.Filament},
		{Label: "Purge / waste", Amount: b.Purge},
		{Label: "Electricity", Amount: b.Electricity},
		{Label: "Machine hours", Amount: b.Machine},
		{Label: "Finishing labour", Amount: b.Finishing},
		{Label: "Consumables", Amount: b.Consumables},
		{Label: "Failure provision", Amount: b.FailureProvision},
	}
}
