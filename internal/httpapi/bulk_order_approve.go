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
	"sort"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/xuri/excelize/v2"

	"github.com/Optiminastic/tensor-core/internal/db/gen"
	"github.com/Optiminastic/tensor-core/internal/obs"
	"github.com/Optiminastic/tensor-core/internal/production"
)

// bulkJobInput is one job's worth of inputs. A struct because five positional
// arguments of which two are uuids invites a caller to transpose them.
type bulkJobInput struct {
	order gen.BulkOrder
	line  gen.BulkOrderLine
	row   sheetRow
	// index is 1-based and runs across the whole order, so the numbers read
	// OMPT-1, OMPT-2 in the order the lines were quoted.
	index int
	code  string
}

// maxSheetUploadBytes caps the upload.
//
// 10 MB is far more than a spreadsheet of names needs - a thousand rows is tens
// of kilobytes - and the cap is here so a mis-picked file fails immediately
// rather than after a minute of streaming.
const maxSheetUploadBytes = 10 << 20

// sheetsForOrder works out which sheets this order's file must contain.
//
// ONE SHEET PER PRODUCT, for every product on the order. An earlier version
// emitted sheets only for products whose SKUs matched a registry variant with
// field maps, and against a real order that is nothing: the registry holds 15
// SKUs where the store holds 507, so a quotation for two Shopify products
// produced an empty workbook and a 409 instead of a file.
//
// Where the product IS mapped, its columns are derived from the field maps -
// the same table the renderer reads - so SC asks for NAME_L, NAME_R and the
// rose's and keychain's own NAME. Where it is not, the sheet falls back to the
// plank shape the shop specified: serial, two names, colour, heart count. That
// is a starting point rather than a guess at the product's real fields, and the
// way to improve any particular sheet is to give its product field maps, at
// which point this derives them without anyone touching this code.
func (s *Server) sheetsForOrder(
	c *gin.Context, lines []gen.BulkOrderLine,
) ([]productSheet, []string, bool) {
	ctx := c.Request.Context()

	// Group the lines. A registry product when the SKU maps to one, otherwise
	// the Shopify product snapshotted on the line.
	type group struct {
		code      string
		name      string
		productID *uuid.UUID
		rows      int
	}
	order := []string{}
	groups := map[string]*group{}

	for _, line := range lines {
		code, productID, name := s.sheetKeyFor(ctx, line)
		g := group{code: code, name: name, productID: productID}
		key := code

		existing, seen := groups[key]
		if !seen {
			copied := g
			groups[key] = &copied
			order = append(order, key)
			existing = groups[key]
		}
		existing.rows += int(line.Quantity)
	}

	// Field maps for the products that have them, in one read.
	productIDs := make([]uuid.UUID, 0, len(groups))
	for _, key := range order {
		if id := groups[key].productID; id != nil {
			productIDs = append(productIDs, *id)
		}
	}
	mapsByProduct := map[uuid.UUID][]gen.ListFieldMapsForProductsRow{}
	if len(productIDs) > 0 {
		maps, err := s.store.Q.ListFieldMapsForProducts(ctx, productIDs)
		if err != nil {
			detail(c, http.StatusInternalServerError, "Could not read the product registry.")
			return nil, nil, false
		}
		for _, m := range maps {
			mapsByProduct[m.ProductID] = append(mapsByProduct[m.ProductID], m)
		}
	}

	sheets := make([]productSheet, 0, len(order))
	var defaulted []string
	for _, key := range order {
		g := groups[key]
		sheet := productSheet{Code: g.code, ProductName: g.name, Rows: g.rows}
		if g.productID != nil {
			sheet.ProductID = *g.productID
		}
		if rows := mapsByProduct[sheet.ProductID]; len(rows) > 0 {
			sheet.Columns = columnsFromMaps(rows)
		} else {
			sheet.Columns = defaultColumns()
			defaulted = append(defaulted, g.code)
		}
		sheets = append(sheets, sheet)
	}
	sort.Slice(sheets, func(i, j int) bool { return sheets[i].Code < sheets[j].Code })
	return sheets, defaulted, true
}

// sheetKeyFor is which sheet a line's rows live on.
//
// ONE definition, used by both the code that builds the sheets and the code
// that reads rows back out of them. They were briefly two copies of the same
// rule, which is a bug waiting for the first product whose registry code and
// Shopify title disagree: the sheet would be written under one name and read
// under another, and the order would approve to no jobs at all.
//
// The registry's product code wins when the SKU maps to a variant - it is
// short, stable and what the shop calls the product. Otherwise the Shopify
// product title, sanitised into something Excel will accept.
func (s *Server) sheetKeyFor(ctx context.Context, line gen.BulkOrderLine) (string, *uuid.UUID, string) {
	if line.VariantID != nil {
		if row, err := s.store.Q.GetVariantProduct(ctx, *line.VariantID); err == nil {
			id := row.ProductID
			return row.ProductCode, &id, row.ProductName
		}
	}
	group := strings.TrimSpace(line.ProductGroup)
	name := group
	if name == "" {
		name = line.ProductName
	}
	return sheetNameFor(group, line.Sku), nil, name
}

// sheetNameFor turns a product title into something Excel will accept.
//
// Excel refuses []:*?/\ in a sheet name and truncates past 31 characters, so a
// name it rejects is a workbook that cannot be opened at all. The SKU is the
// fallback for a line carrying no product title.
func sheetNameFor(title, sku string) string {
	name := strings.TrimSpace(title)
	if name == "" {
		name = strings.TrimSpace(sku)
	}
	name = strings.Map(func(r rune) rune {
		if strings.ContainsRune(`[]:*?/\`, r) {
			return '-'
		}
		return r
	}, name)
	if len(name) > 31 {
		name = strings.TrimSpace(name[:31])
	}
	if name == "" {
		return "Sheet"
	}
	return name
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
		// Only reachable for an order with no lines, which the create endpoint
		// refuses - so this is a guard, not a path anybody walks.
		detail(c, http.StatusConflict, "This order has no products to fill in.")
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
					// Within the column's own bound where it has one: a sample
					// showing 3 hearts is a sample that teaches somebody to
					// write a file every render then refuses.
					example := int64(2)
					if col.Max > 0 && example > int64(col.Max) {
						example = int64(col.Max)
					}
					_ = f.SetCellInt(sheet.Code, cell, example)
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
	sheets, defaulted, ok := s.sheetsForOrder(c, lines)
	if !ok {
		return
	}
	if len(sheets) == 0 {
		detail(c, http.StatusConflict, "This order has no products to approve against.")
		return
	}

	// The code this order's jobs are numbered from, e.g. OMPT for Optiminastic
	// Media Pvt Ltd, giving OMPT-1, OMPT-2. It arrives with the file because
	// approval is the moment a quotation becomes production work, and that is
	// the first time the number means anything.
	code, ok := s.jobCodeFrom(c, order)
	if !ok {
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

	jobs, err := s.createJobsFromSheets(c, bulkApproval{
		order: order, lines: lines, rowsBySheet: rowsBySheet, code: code,
	})
	if err != nil {
		detail(c, http.StatusInternalServerError, "Could not create the production jobs.")
		return
	}

	// After the commit, best-effort, exactly as the Shopify path does it: a job
	// whose render was never scheduled still exists, still says stl_missing and
	// is still visible - whereas failing the approval for a queue hiccup would
	// lose the whole upload.
	s.enqueueModelGeneration(c.Request.Context(), jobs)

	c.JSON(http.StatusOK, gin.H{
		"quotation_number": order.QuotationNumber,
		"job_code":         code,
		"jobs_created":     len(jobs),
		// Which sheets used the fallback column shape rather than derived one.
		// Worth saying: those columns are a starting point, and the way to make
		// them right is to give the product field maps in the registry.
		"default_shape_sheets": defaulted,
	})
}

// jobCodeFrom reads and checks the job code off the upload.
//
// Uppercased, letters and digits only, 2-12 characters: it becomes the visible
// half of a job number an operator reads off a plank, so it has to be short and
// unambiguous. Checked for collision here rather than relying on the unique
// index, so a clash is reported against the field somebody typed instead of
// surfacing as a failed insert partway through a hundred jobs.
func (s *Server) jobCodeFrom(c *gin.Context, order gen.BulkOrder) (string, bool) {
	raw := strings.ToUpper(strings.TrimSpace(c.PostForm("job_code")))
	if raw == "" {
		detail(c, http.StatusBadRequest,
			"Give this order a code - OMPT for Optiminastic Media Pvt Ltd, say - "+
				"so its jobs can be numbered OMPT-1, OMPT-2.")
		return "", false
	}
	if len(raw) < 2 || len(raw) > 12 {
		detail(c, http.StatusUnprocessableEntity, "The code must be 2 to 12 characters.")
		return "", false
	}
	for _, r := range raw {
		if (r < 'A' || r > 'Z') && (r < '0' || r > '9') {
			detail(c, http.StatusUnprocessableEntity,
				"The code may only contain letters and digits.")
			return "", false
		}
	}
	taken, err := s.store.Q.JobCodeTaken(c.Request.Context(), gen.JobCodeTakenParams{
		Upper: &raw, ID: order.ID,
	})
	if err != nil {
		detail(c, http.StatusInternalServerError, "Could not check that code.")
		return "", false
	}
	if taken {
		detail(c, http.StatusConflict, fmt.Sprintf(
			"Another bulk order already uses the code %s. Every order needs its own, "+
				"or their job numbers would collide.", raw))
		return "", false
	}
	return raw, true
}

// bulkApproval is everything one approval needs. A struct because six
// positional arguments, three of them maps and slices, is a call nobody can
// read at the call site.
type bulkApproval struct {
	order       gen.BulkOrder
	lines       []gen.BulkOrderLine
	rowsBySheet map[string][]sheetRow
	code        string
}

// createJobsFromSheets writes one production job per validated row.
//
// In one transaction with the status change and the job code, so an order is
// either approved with all its jobs or not approved at all. A partial approval
// would leave the floor holding work for a quotation that still says draft.
//
// Returns the jobs so the caller can schedule their renders - which has to
// happen after the commit, or a render could start against a job the
// transaction then rolls back.
func (s *Server) createJobsFromSheets(
	c *gin.Context, in bulkApproval,
) ([]gen.ProductionJob, error) {
	ctx := c.Request.Context()

	// Which sheet each line draws its rows from. Resolved by sheetKeyFor, the
	// same function that named the sheets, so a line cannot look for rows under
	// a key the sheet does not have.
	type pending struct {
		line gen.BulkOrderLine
		code string
	}
	var queue []pending
	for _, line := range in.lines {
		code, _, _ := s.sheetKeyFor(ctx, line)
		queue = append(queue, pending{line: line, code: code})
	}

	cursor := map[string]int{}
	created := make([]gen.ProductionJob, 0, 64)
	err := s.store.InTx(ctx, func(q *gen.Queries) error {
		created = created[:0]
		for _, p := range queue {
			rows := in.rowsBySheet[p.code]
			for i := 0; i < int(p.line.Quantity); i++ {
				at := cursor[p.code]
				cursor[p.code] = at + 1
				if at >= len(rows) {
					return fmt.Errorf("sheet %s ran out of rows", p.code)
				}
				job, err := insertBulkJob(ctx, q, bulkJobInput{
					order: in.order, line: p.line, row: rows[at],
					// 1-based and sequential across the whole order, which is
					// what "OMPT-1, OMPT-2 and so on, order wise" means: the
					// numbering follows the order's lines, not each sheet.
					index: len(created) + 1, code: in.code,
				})
				if err != nil {
					return err
				}
				created = append(created, job)
			}
		}
		_, err := q.ApproveBulkOrder(ctx, gen.ApproveBulkOrderParams{
			ID: in.order.ID, JobCode: &in.code,
		})
		return err
	})
	if err != nil {
		return nil, err
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
) (gen.ProductionJob, error) {
	order, line, row := in.order, in.line, in.row
	// A LIST of {name, value}, exactly as a Shopify line carries it - not a
	// map. jobLineProperties unmarshals into []production.LineProp, and a map
	// fails that decode silently, leaving the renderer to fall back to the
	// order this job does not have.
	props := make([]production.LineProp, 0, len(row.Values))
	for key, value := range row.Values {
		props = append(props, production.LineProp{Name: key, Value: value})
	}
	// Sorted, so two jobs built from the same spreadsheet row store their
	// properties identically and a diff between them means something.
	sort.Slice(props, func(i, j int) bool { return props[i].Name < props[j].Name })

	encoded, err := json.Marshal(props)
	if err != nil {
		return gen.ProductionJob{}, err
	}

	colour := strings.TrimSpace(row.Colour)
	name := order.CustomerName
	sku := line.Sku
	product := line.ProductName

	// colours as a JSON ARRAY, not just the colour column. The planner groups
	// beds by this - NormalisedColourKey reads it - so a job with the column
	// set and the array empty reaches batching as "no filament colour
	// recorded" and never joins a bed.
	colours := []byte("[]")
	if colour != "" {
		if raw, err := json.Marshal([]string{colour}); err == nil {
			colours = raw
		}
	}

	// stl_missing, exactly as a generated storefront job starts. It keeps the
	// job out of batching until its model exists and is what the model
	// generator looks for; the render clears it and the job batches normally.
	issue := production.IssueSTLMissing

	return q.InsertBulkProductionJob(ctx, gen.InsertBulkProductionJobParams{
		ID: uuid.New(), JobNumber: fmt.Sprintf("%s-%d", in.code, in.index),
		BulkOrderID: &order.ID,
		// The serial is in the description so an operator holding a plank can
		// find the spreadsheet row it came from.
		Description: fmt.Sprintf("%s #%d - %s", order.QuotationNumber, row.Serial, product),
		Quantity:    1,
		Sku:         &sku, ProductName: &product, Colour: &colour, CustomerName: &name,
		PersonalisationProperties: encoded,
		Colours:                   colours,
		IssueReason:               &issue,
	})
}
