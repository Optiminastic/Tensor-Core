package httpapi

// Approving a bulk order: upload the personalisation, and the work becomes real.
//
// The upload is one workbook with one sheet per ordered product, named by the
// product's code. What each sheet must contain is derived from the registry's
// field maps - see bulk_order_sheet.go - so the sample this file generates and
// the validator that reads the upload are built from the same definition and
// cannot disagree about what a correct file looks like.
//
// Approval is all-or-nothing. Every sheet is validated before anything is
// written, and the jobs are created in one transaction: a file that is right
// about SC and wrong about SCWL produces no jobs at all, rather than half an
// order on the floor and a spreadsheet somebody has to reconcile by hand.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/xuri/excelize/v2"

	"github.com/Optiminastic/tensor-core/internal/db/gen"
	"github.com/Optiminastic/tensor-core/internal/obs"
)

// bulkJobInput is one job's worth of inputs. A struct because five positional
// arguments of which two are uuids invites a caller to transpose them.
type bulkJobInput struct {
	order gen.BulkOrder
	line  gen.BulkOrderLine
	row   sheetRow
	index int
}

// maxSheetUploadBytes caps the upload.
//
// 10 MB is far more than a spreadsheet of names needs - a thousand rows is tens
// of kilobytes - and the cap is here so a mis-picked file fails immediately
// rather than after a minute of streaming.
const maxSheetUploadBytes = 10 << 20

// sheetsForOrder works out which sheets this order's file must contain.
//
// Products whose SKUs are not in the registry, or which are in it without field
// maps, are skipped: there is nothing to ask for. Returns the sheets and the
// SKUs that were skipped, so the response can say so rather than silently
// expecting less than the operator does.
func (s *Server) sheetsForOrder(
	c *gin.Context, lines []gen.BulkOrderLine,
) ([]productSheet, []string, bool) {
	ctx := c.Request.Context()

	rowsByProduct := map[uuid.UUID]int{}
	productIDs := make([]uuid.UUID, 0, 4)
	var skipped []string
	seen := map[uuid.UUID]bool{}

	for _, line := range lines {
		if line.VariantID == nil {
			skipped = append(skipped, line.Sku)
			continue
		}
		row, err := s.store.Q.GetVariantProduct(ctx, *line.VariantID)
		if err != nil {
			skipped = append(skipped, line.Sku)
			continue
		}
		rowsByProduct[row.ProductID] += int(line.Quantity)
		if !seen[row.ProductID] {
			seen[row.ProductID] = true
			productIDs = append(productIDs, row.ProductID)
		}
	}

	if len(productIDs) == 0 {
		return nil, skipped, true
	}
	maps, err := s.store.Q.ListFieldMapsForProducts(ctx, productIDs)
	if err != nil {
		detail(c, http.StatusInternalServerError, "Could not read the product registry.")
		return nil, nil, false
	}
	return buildProductSheets(maps, rowsByProduct), skipped, true
}

// downloadSheetTemplate serves the workbook this order expects, with its
// headers and the right number of blank rows.
//
// Generated from the same sheet definitions the validator uses, so a template
// filled in and uploaded unchanged in shape always passes. A separate
// hand-maintained sample would drift the first time a field map changed.
func (s *Server) downloadSheetTemplate(c *gin.Context) {
	order, lines, ok := s.loadBulkOrder(c)
	if !ok {
		return
	}
	sheets, _, ok := s.sheetsForOrder(c, lines)
	if !ok {
		return
	}
	if len(sheets) == 0 {
		detail(c, http.StatusConflict,
			"None of this order's products have personalisation fields in the registry yet, "+
				"so there is nothing to fill in.")
		return
	}

	f := buildTemplateWorkbook(sheets, false)
	defer func() { _ = f.Close() }()

	c.Header("Content-Disposition", fmt.Sprintf(
		`attachment; filename="%s-personalisation.xlsx"`, order.QuotationNumber))
	c.Header("Content-Type",
		"application/vnd.openxmlformats-officedocument.spreadsheetml.sheet")
	if err := f.Write(c.Writer); err != nil {
		obs.FromContext(c.Request.Context()).Warn("could not write the template", "error", err)
	}
}

// buildTemplateWorkbook writes the headers, and optionally example rows.
//
// withExamples fills a few rows in so somebody can see the shape expected -
// what a name looks like, that a heart count is a bare number and not "3
// hearts". An empty template is right for a real order, where the row count
// must match the quantity exactly.
func buildTemplateWorkbook(sheets []productSheet, withExamples bool) *excelize.File {
	f := excelize.NewFile()
	// A new file comes with "Sheet1", which is not one of ours.
	defaultSheet := f.GetSheetName(0)

	for _, sheet := range sheets {
		if _, err := f.NewSheet(sheet.Code); err != nil {
			continue
		}
		for i, col := range sheet.Columns {
			cell, err := excelize.CoordinatesToCellName(i+1, 1)
			if err != nil {
				continue
			}
			_ = f.SetCellStr(sheet.Code, cell, col.Header)
		}

		rows := sheet.Rows
		if withExamples && rows > 4 {
			rows = 4
		}
		for r := 0; r < rows; r++ {
			for i, col := range sheet.Columns {
				cell, err := excelize.CoordinatesToCellName(i+1, r+2)
				if err != nil {
					continue
				}
				switch {
				case col.Header == serialHeader:
					// Always written, template or not: the serial is the one
					// column nobody should have to type.
					_ = f.SetCellInt(sheet.Code, cell, int64(r+1))
				case !withExamples:
					// Left blank for a real order.
				case col.Numeric:
					_ = f.SetCellInt(sheet.Code, cell, 3)
				case col.Header == colourHeader:
					_ = f.SetCellStr(sheet.Code, cell, exampleColours[r%len(exampleColours)])
				default:
					_ = f.SetCellStr(sheet.Code, cell, exampleNames[r%len(exampleNames)])
				}
			}
		}
	}
	_ = f.DeleteSheet(defaultSheet)
	return f
}

var (
	exampleNames   = []string{"AARAV", "DIYA", "KABIR", "MEERA"}
	exampleColours = []string{"WHITE", "RED", "BABY PINK", "GOLD"}
)

// downloadSheetSample serves a filled-in example rather than a blank template.
//
// The shop asked for a sample covering several products, which an order-shaped
// template cannot be: a real template has exactly the rows that order promises,
// and an order for one product has one sheet. This one is built from EVERY
// mapped product in the registry with a few example rows each, so somebody can
// see what a name, a colour and a numeric count look like before they have an
// order in front of them.
func (s *Server) downloadSheetSample(c *gin.Context) {
	ctx := c.Request.Context()
	variants, err := s.store.Q.ListSellableVariants(ctx)
	if err != nil {
		detail(c, http.StatusInternalServerError, "Could not read the product registry.")
		return
	}
	// Four rows of each product, which is enough to show the shape without
	// making a file somebody scrolls.
	rows := map[uuid.UUID]int{}
	ids := make([]uuid.UUID, 0, 8)
	seen := map[uuid.UUID]bool{}
	for _, v := range variants {
		product, err := s.store.Q.GetVariantProduct(ctx, v.ID)
		if err != nil || seen[product.ProductID] {
			continue
		}
		seen[product.ProductID] = true
		ids = append(ids, product.ProductID)
		rows[product.ProductID] = 4
	}
	maps, err := s.store.Q.ListFieldMapsForProducts(ctx, ids)
	if err != nil {
		detail(c, http.StatusInternalServerError, "Could not read the product registry.")
		return
	}
	sheets := buildProductSheets(maps, rows)
	if len(sheets) == 0 {
		detail(c, http.StatusConflict,
			"No product in the registry has personalisation fields yet, so there is "+
				"nothing to show in a sample.")
		return
	}

	f := buildTemplateWorkbook(sheets, true)
	defer func() { _ = f.Close() }()
	c.Header("Content-Disposition", `attachment; filename="bulk-order-sample.xlsx"`)
	c.Header("Content-Type",
		"application/vnd.openxmlformats-officedocument.spreadsheetml.sheet")
	if err := f.Write(c.Writer); err != nil {
		obs.FromContext(ctx).Warn("could not write the sample", "error", err)
	}
}

// approveBulkOrder validates the uploaded workbook and turns it into jobs.
func (s *Server) approveBulkOrder(c *gin.Context) {
	order, lines, ok := s.loadBulkOrder(c)
	if !ok {
		return
	}
	sheets, skipped, ok := s.sheetsForOrder(c, lines)
	if !ok {
		return
	}
	if len(sheets) == 0 {
		detail(c, http.StatusConflict,
			"None of this order's products have personalisation fields in the registry yet, "+
				"so there is nothing to approve against.")
		return
	}

	header, err := c.FormFile("file")
	if err != nil {
		detail(c, http.StatusBadRequest, "Attach the filled-in spreadsheet.")
		return
	}
	if header.Size > maxSheetUploadBytes {
		detail(c, http.StatusRequestEntityTooLarge, "That file is too large to be a spreadsheet.")
		return
	}
	opened, err := header.Open()
	if err != nil {
		detail(c, http.StatusBadRequest, "That file could not be read.")
		return
	}
	defer func() { _ = opened.Close() }()

	f, err := excelize.OpenReader(opened)
	if err != nil {
		detail(c, http.StatusUnprocessableEntity,
			"That is not a readable .xlsx workbook. Save it from Excel as .xlsx and try again.")
		return
	}
	defer func() { _ = f.Close() }()

	rowsBySheet, problems := validateWorkbook(f, sheets)
	if len(problems) > 0 {
		// 422 with the whole list: the operator fixes the file, not the request.
		c.JSON(http.StatusUnprocessableEntity, gin.H{
			"detail":   fmt.Sprintf("The spreadsheet has %d problem(s).", len(problems)),
			"problems": problems,
		})
		return
	}

	created, err := s.createJobsFromSheets(c, order, lines, rowsBySheet)
	if err != nil {
		detail(c, http.StatusInternalServerError, "Could not create the production jobs.")
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"quotation_number": order.QuotationNumber,
		"jobs_created":     created,
		"skipped_skus":     skipped,
	})
}

// createJobsFromSheets writes one production job per validated row.
//
// In one transaction with the status change, so an order is either approved
// with all its jobs or not approved at all. A partial approval would leave the
// floor holding work for a quotation that still says draft.
func (s *Server) createJobsFromSheets(
	c *gin.Context, order gen.BulkOrder, lines []gen.BulkOrderLine,
	rowsBySheet map[string][]sheetRow,
) (int, error) {
	ctx := c.Request.Context()

	// Which SKUs belong to which sheet, so a row can be attributed to the line
	// that ordered it. A sheet holds every SKU of its product in order, so the
	// rows are handed out in the order the lines appear.
	type pending struct {
		line gen.BulkOrderLine
		code string
	}
	var queue []pending
	for _, line := range lines {
		if line.VariantID == nil {
			continue
		}
		row, err := s.store.Q.GetVariantProduct(ctx, *line.VariantID)
		if err != nil {
			continue
		}
		queue = append(queue, pending{line: line, code: row.ProductCode})
	}

	cursor := map[string]int{}
	created := 0
	err := s.store.InTx(ctx, func(q *gen.Queries) error {
		for _, p := range queue {
			rows := rowsBySheet[p.code]
			for i := 0; i < int(p.line.Quantity); i++ {
				at := cursor[p.code]
				cursor[p.code] = at + 1
				if at >= len(rows) {
					return fmt.Errorf("sheet %s ran out of rows", p.code)
				}
				row := rows[at]
				if err := insertBulkJob(ctx, q, bulkJobInput{
					order: order, line: p.line, row: row, index: created,
				}); err != nil {
					return err
				}
				created++
			}
		}
		_, err := q.UpdateBulkOrderStatus(ctx, gen.UpdateBulkOrderStatusParams{
			ID: order.ID, Status: "accepted",
		})
		return err
	})
	if err != nil {
		return 0, err
	}
	return created, nil
}

// insertBulkJob writes one production job from one spreadsheet row.
//
// The personalisation lands in personalisation_properties keyed by the OpenSCAD
// VARIABLE - NAME_L, NAME_R - which is the shape the renderer's field maps
// already read for a Shopify order. A bulk job and a storefront job therefore
// reach the model generator looking the same, and nothing downstream has to
// know which it was.
func insertBulkJob(
	ctx context.Context, q *gen.Queries, in bulkJobInput,
) error {
	order, line, row := in.order, in.line, in.row
	props := make(map[string]string, len(row.Values))
	for key, value := range row.Values {
		// "role/VARIABLE" - the renderer keys on the variable alone.
		parts := strings.SplitN(key, "/", 2)
		props[parts[len(parts)-1]] = value
	}
	encoded, err := json.Marshal(props)
	if err != nil {
		return err
	}

	colour := strings.TrimSpace(row.Colour)
	name := order.CustomerName
	sku := line.Sku
	product := line.ProductName
	// The same numbering as a Shopify order's jobs, derived from the quotation
	// number instead: QUO-20818 gives JOB-20818, JOB-20818-2, and so on, so a
	// plank on the floor traces back to the document it was promised on.
	number := jobNumberForOrder(order.QuotationNumber, in.index)
	if number == "" {
		number = fmt.Sprintf("JOB-%s-%d", order.QuotationNumber, in.index+1)
	}
	return q.InsertBulkProductionJob(ctx, gen.InsertBulkProductionJobParams{
		ID: uuid.New(), JobNumber: number, BulkOrderID: &order.ID,
		// The serial is in the description so an operator holding a plank can
		// find the spreadsheet row it came from.
		Description: fmt.Sprintf("%s #%d - %s", order.QuotationNumber, row.Serial, product),
		Quantity:    1,
		Sku:         &sku, ProductName: &product, Colour: &colour, CustomerName: &name,
		PersonalisationProperties: encoded,
		Colours:                   []byte("[]"),
	})
}
