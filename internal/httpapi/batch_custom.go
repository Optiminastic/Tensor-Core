package httpapi

// Building a bed by hand.
//
// The planner decides what shares a plate, and for the ordinary run of Dual
// Name Planks it decides well. It cannot decide everything: a customer rings up
// and wants their two planks together, a bed needs filling with whatever is
// blue to use up a spool before it runs out, a reprint should ride along with
// the order it belongs to. None of that is a rule worth teaching the planner,
// and all of it is obvious to the person looking at the orders.
//
// So this is the same bed the planner would build, assembled by a person: the
// SAME eligibility (ListBatchableJobs), the SAME compatibility key, the SAME
// unit cap, and the same Draft status at the end. What changes is who chose.
// The constraints are not relaxed for a human - a plate that mixes two colours
// prints one of them wrong whoever assembled it.

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/Optiminastic/tensor-core/internal/db/gen"
	"github.com/Optiminastic/tensor-core/internal/obs"
	"github.com/Optiminastic/tensor-core/internal/production"
)

// batchableJobsResponse is the pool a hand-built bed is chosen from.
//
// JOBS, one row each. This grouped them by order once, on the reasoning that
// somebody building a bed thinks "these four customers are waiting" - true when
// filling a bed from the top of the queue, and wrong for the case the dialog is
// actually opened for: a named plank that has to go on a plate now. An order
// row cannot say "that one of the three", and the job number is what the rest
// of the floor calls a plank - the queue, the issues board, the plate itself.
//
// Searched in the database, a page at a time. The pool is every job the shop
// has ever created, so sending all of it for the browser to hide most of would
// turn a search box into a download - and would get slower every week.
type batchableJobsResponse struct {
	Jobs []batchableJob `json:"jobs"`
	// More says the search hit the pool ceiling, so Total is a floor rather
	// than a count. Said plainly because "40 of 200" from a table of 369 is a
	// number somebody would otherwise take at face value.
	More bool `json:"more"`
	// Total is how many jobs matched, which can be more than were returned.
	// The dialog says so rather than quietly truncating: a search that shows 40
	// of 180 is a search that needs narrowing, and a list that simply stops
	// looks like the answer.
	Total int `json:"total"`
	// UnitsPerBed is how many products one plate holds, so the dialog can count
	// places rather than making somebody guess when to stop.
	UnitsPerBed int `json:"units_per_bed"`
	// MinUnitsPerBed is how empty a bed may be and still be worth a
	// machine-hour. Not enforced here - a smaller bed may be built and will
	// simply wait for company before it locks - but said, so nobody builds a
	// bed of two and wonders why it never goes.
	MinUnitsPerBed int `json:"min_units_per_bed"`
}

// batchableJob is one plank waiting, as the search offers it.
type batchableJob struct {
	// JobID is what is actually sent when the row is chosen. JobNumber is what
	// is shown and searched on; the two are never interchanged.
	JobID     string `json:"job_id"`
	JobNumber string `json:"job_number"`
	// OrderNumber is the customer's order, falling back to the job's own
	// numbering for a reprint or a plank added by hand - those belong to
	// nobody's shipment.
	OrderNumber string `json:"order_number"`
	Product     string `json:"product"`
	// Units is how many places on the bed this job takes: a job for three of
	// the same plank takes three, not one.
	Units int `json:"units"`
	// CompatibilityKey is an opaque string: two rows may share a bed exactly
	// when theirs match. Computed here rather than rebuilt in the browser
	// because it folds in NormalisedColourKey, and a second implementation of
	// that in another language is a rule that will drift - quietly, and into a
	// plate that prints in the wrong colour.
	CompatibilityKey string `json:"compatibility_key"`
	ColourLabel      string `json:"colour_label"`
	// Available reports whether this plank can go on a bed right now. False
	// ones are still listed, because somebody who searches a job number by hand
	// has asked a direct question about that plank, and omitting it answers
	// with silence.
	Available         bool   `json:"available"`
	UnavailableReason string `json:"unavailable_reason"`
	// OnBed names the bed this plank already sits on.
	OnBed string `json:"on_bed"`
	// BedLocked marks a plank on an approved bed. Taking one off is allowed and
	// is not free: that bed's plate comes out of BambuBuddy's queue and its
	// filament is given back before it is rebuilt without it.
	BedLocked bool `json:"bed_locked"`
	// Reprint marks a plank that has already printed. Choosing it puts the same
	// job back in the queue, which prints a second copy.
	Reprint bool `json:"reprint"`
	// FinishedStage says where an already-printed plank actually is - waiting
	// for QC, to be packed, to be dispatched - so "print it again" reads as a
	// deliberate choice rather than the only visible option.
	FinishedStage string `json:"finished_stage"`
}

// batchablePoolLimit is how many rows one search reads from the database.
//
// A ceiling, not a page: the pool is every job ever created and grows with the
// shop. Filtering in SQL and stopping at a couple of hundred keeps the dialog's
// cost flat, and anything past it is reached by typing rather than scrolling.
const batchablePoolLimit = 200

// batchableSearchLimit is how many rows are offered at once.
//
// A bed holds five. A list longer than this is not a list of candidates, it is
// a reason to narrow the search - which the response says, by carrying how many
// matched.
const batchableSearchLimit = 40

// listBatchableJobs answers the custom-batch dialog's search.
//
// Every job, not only the ones on an unfulfilled order. The restriction to
// outstanding orders read well - a bed clears work that is waiting - and it
// hid exactly the jobs somebody opens this dialog holding a number for: a plank
// that printed badly, a customer asking for a second copy, a job whose order
// shipped without it.
//
// A plank that cannot be bedded right now is returned with that as its reason
// rather than omitted: somebody typing a job number has asked about that plank,
// and a search that finds nothing cannot tell "on hold" from "no such job".
//
// Two query parameters, both optional:
//
//	q    - job number, order number or product name, matched loosely.
//	key  - the bed's compatibility key, once a first job has fixed it.
//
// `key` narrows the BROWSE list only. While searching, a job of the wrong
// colour is returned and the dialog greys it with the reason, because a search
// for a number that answers with silence is indistinguishable from a typo.
func (s *Server) listBatchableJobs(c *gin.Context) {
	ctx := c.Request.Context()
	search := strings.TrimSpace(c.Query("q"))
	key := c.Query("key")

	var term *string
	if search != "" {
		escaped := likeTerm(search)
		term = &escaped
	}
	jobs, err := s.store.Q.SearchJobsForCustomBatch(ctx, gen.SearchJobsForCustomBatchParams{
		Search: term, RowLimit: batchablePoolLimit,
	})
	if err != nil {
		detail(c, http.StatusInternalServerError, "Could not read the jobs waiting.")
		return
	}
	beds, err := s.bedsHolding(ctx, jobs)
	if err != nil {
		detail(c, http.StatusInternalServerError, "Could not read the beds those jobs are on.")
		return
	}
	numbers, err := s.orderNumbersFor(ctx, jobs)
	if err != nil {
		detail(c, http.StatusInternalServerError, "Could not read the orders.")
		return
	}

	// Browsing is a suggestion list and is kept tidy: planks already on a bed
	// somebody built by hand are left out, and once a colour is fixed only that
	// colour is offered. A SEARCH is a question about a particular plank and is
	// answered whatever the answer is.
	pool := jobs
	if search == "" {
		pool = stillToPlace(pool, beds)
	}
	rows := jobRows(pool, beds, numbers)
	if search == "" && key != "" {
		rows = onlyMatching(rows, key)
	}

	out := batchableJobsResponse{
		Jobs:           rows,
		Total:          len(rows),
		More:           len(jobs) == batchablePoolLimit,
		UnitsPerBed:    s.bedUnitCap(),
		MinUnitsPerBed: s.bedUnitFloor(),
	}
	if len(out.Jobs) > batchableSearchLimit {
		out.Jobs = out.Jobs[:batchableSearchLimit]
	}
	c.JSON(http.StatusOK, out)
}

// onlyMatching keeps the rows that may share a bed with what is already on it.
func onlyMatching(rows []batchableJob, key string) []batchableJob {
	out := make([]batchableJob, 0, len(rows))
	for _, r := range rows {
		if r.CompatibilityKey == key {
			out = append(out, r)
		}
	}
	return out
}

// likeTerm makes a typed string safe to put inside an ILIKE pattern.
//
// Not injection - the term is a bound parameter - but meaning. A customer's
// product name may hold a percent sign, and unescaped it becomes "match
// anything", which returns rows that have nothing to do with what was typed.
func likeTerm(s string) string {
	r := strings.NewReplacer(`\`, `\\`, "%", `\%`, "_", `\_`)
	return r.Replace(s)
}

// orderNumbersFor reads the store's number for each job's order, in one query.
func (s *Server) orderNumbersFor(
	ctx context.Context, jobs []gen.ProductionJob,
) (map[uuid.UUID]string, error) {
	out := map[uuid.UUID]string{}
	ids := dedupeIDs(jobs, func(j gen.ProductionJob) *uuid.UUID { return j.OrderID })
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := s.store.Q.ListOrderNumbersForIDs(ctx, ids)
	if err != nil {
		return nil, err
	}
	for _, r := range rows {
		out[r.ID] = r.OrderNumber
	}
	return out, nil
}

// stillToPlace drops the planks somebody has already put on a bed by hand.
//
// Not greyed with a reason, as the other exclusions are: gone. The rest of this
// list answers "where is my order" and is worth showing even when it cannot be
// picked, but an order somebody has just built a bed for is a question they
// have already answered - leaving it on screen invites them to answer it twice,
// and a list that keeps offering back what you just chose is tiresome to work
// through.
//
// Undone by deleting that bed, which is where the decision was made.
func stillToPlace(
	jobs []gen.ProductionJob, beds map[uuid.UUID]gen.ListBatchIdentityForIDsRow,
) []gen.ProductionJob {
	out := make([]gen.ProductionJob, 0, len(jobs))
	for _, j := range jobs {
		if j.BatchID != nil && beds[*j.BatchID].Manual {
			continue
		}
		out = append(out, j)
	}
	return out
}

// jobRows turns each plank into a row the search can offer.
//
// One row per job, in the order the query returned them - oldest customer order
// first - with the ones that cannot be bedded sorted to the back. That ordering
// only decides what is shown before anybody types; after that a search decides.
//
// Nothing is collapsed. Two planks of one order are two rows because they can
// be chosen apart: the whole point of picking by job number is that a bed takes
// one of them and not the other.
func jobRows(
	jobs []gen.ProductionJob,
	beds map[uuid.UUID]gen.ListBatchIdentityForIDsRow,
	numbers map[uuid.UUID]string,
) []batchableJob {
	out := make([]batchableJob, 0, len(jobs))
	for _, j := range jobs {
		var bed gen.ListBatchIdentityForIDsRow
		if j.BatchID != nil {
			bed = beds[*j.BatchID]
		}
		row := batchableJob{
			JobID:            j.ID.String(),
			JobNumber:        j.JobNumber,
			OrderNumber:      orderTagFor(orderNumberPtr(numbers, j.OrderID), j.JobNumber),
			Product:          deref(j.ProductName),
			Units:            int(jobQuantity(j.Quantity)),
			CompatibilityKey: compatibilityKeyString(j),
			ColourLabel:      jobColourKey(j),
			Available:        true,
			OnBed:            bed.BatchNumber,
			BedLocked:        bed.Status == production.BatchOpen,
		}
		if j.Status == production.StatusCompleted {
			row.Reprint = true
			row.FinishedStage = finishedStage(j)
		}
		if reason := unavailableBecause(j, bed); reason != "" {
			row.Available = false
			row.UnavailableReason = reason
		}
		out = append(out, row)
	}

	sort.SliceStable(out, func(i, j int) bool {
		return out[i].Available && !out[j].Available
	})
	return out
}

func orderNumberPtr(numbers map[uuid.UUID]string, orderID *uuid.UUID) *string {
	if orderID == nil {
		return nil
	}
	if n, ok := numbers[*orderID]; ok {
		return &n
	}
	return nil
}

// bedsHolding reads the beds these planks sit on, by id.
//
// One query for the whole list rather than one per plank, and separate from the
// job query so that one can return production_jobs rows unchanged.
func (s *Server) bedsHolding(
	ctx context.Context, jobs []gen.ProductionJob,
) (map[uuid.UUID]gen.ListBatchIdentityForIDsRow, error) {
	out := map[uuid.UUID]gen.ListBatchIdentityForIDsRow{}
	ids := dedupeIDs(jobs, func(j gen.ProductionJob) *uuid.UUID { return j.BatchID })
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := s.store.Q.ListBatchIdentityForIDs(ctx, ids)
	if err != nil {
		return nil, err
	}
	for _, r := range rows {
		out[r.ID] = r
	}
	return out, nil
}

// unavailableBecause explains why a plank cannot go on a bed right now, or
// returns empty when it can.
//
// The same rules the create path enforces, said in words rather than enforced
// in silence. Ordered most-actionable first: a hold and a flag are somebody's
// to clear, whereas "already on a locked bed" is simply how the floor works.
func unavailableBecause(j gen.ProductionJob, bed gen.ListBatchIdentityForIDsRow) string {
	switch {
	case j.Held:
		return "on hold"
	case j.IssueReason != nil:
		return "needs attention: " + *j.IssueReason
	case j.PersonalisationStatus != production.PersonalisationValidated &&
		j.PersonalisationStatus != production.PersonalisationNotRequired:
		return "personalisation not checked yet"
	case j.PrintFileID == nil:
		// The model is what goes on the plate. Without one there is nothing to
		// arrange, and a bed built around it would fail at plate-merge time
		// with a message about a file rather than about this order.
		return "no 3D model yet"
	case j.Status == production.StatusFailed:
		return "failed - reprint it from the job"
	case j.Status == production.StatusCompleted:
		// Offered. The plank printed and its order is still outstanding, which
		// is exactly the case somebody is looking at when they ask why.
		return ""
	case j.Status != production.StatusQueued:
		return "printing now"
	case j.BatchID == nil:
		return ""
	case bed.Status == "":
		// The bed vanished between the two reads. Treating an unknown bed as
		// movable would be the one guess here that prints something.
		return "on a bed Tensor cannot read"
	}
	// Draft AND Locked, matching editableBatch. A locked bed is not final:
	// editing one takes its plate back out of BambuBuddy's queue and gives its
	// filament back before the membership changes. Only a bed that is PRINTING
	// or DONE is beyond reach, because those two record what physically
	// happened to a plate.
	if ok, why := editableBatch(gen.Batch{Status: bed.Status}); !ok {
		return strings.ToLower(strings.TrimSuffix(why, "."))
	}
	return ""
}

// finishedStage says where a printed plank actually is, for an order still
// showing unfulfilled.
//
// This is the question "the batch is done but the order is not" - and the
// answer is almost never "print it again". The plank exists; it is waiting for
// somebody to check or pack it.
func finishedStage(j gen.ProductionJob) string {
	switch {
	case j.QcStatus == production.QcFailed:
		return "failed QC"
	case j.QcStatus == production.QcPending:
		return "printed, waiting for QC"
	case j.PackagingStatus == production.PackagingPending:
		return "printed, waiting to be packed"
	default:
		return "printed and packed, waiting to be dispatched"
	}
}

// errPlanksClaimed marks the one in-transaction failure that is somebody
// else's doing rather than a fault, so the caller can say so in those terms.
var errPlanksClaimed = errors.New("planks claimed by another bed")

type createCustomBatchRequest struct {
	JobIDs []string `json:"job_ids"`
}

// createCustomBatch builds a Draft bed from jobs somebody chose.
//
// Validated before anything is written, and refused whole rather than in part:
// a half-built bed is worse than none, because the jobs that did land are now
// off the pool and somebody has to find them again to understand why.
func (s *Server) createCustomBatch(c *gin.Context) {
	var req createCustomBatchRequest
	if !bindJSON(c, &req) {
		return
	}
	if len(req.JobIDs) == 0 {
		detail(c, http.StatusUnprocessableEntity, "Choose at least one product for this bed.")
		return
	}
	ctx := c.Request.Context()

	jobs, err := s.eligibleJobsFor(ctx, req.JobIDs)
	if err != nil {
		writeStatusError(c, err, "Could not read the chosen jobs.")
		return
	}
	if err := checkOneBedWorth(jobs, s.bedUnitCap()); err != nil {
		detail(c, http.StatusUnprocessableEntity, err.Error())
		return
	}
	// A bed always ends in a plate, so storage is required - checked before
	// anything is written rather than when the build reaches for it.
	if !s.plateableBatch(c, len(jobs)) {
		return
	}

	sources, err := s.sourceBeds(ctx, jobs)
	if err != nil {
		writeStatusError(c, err, "Could not read the beds those products are on.")
		return
	}
	// Undo what locking did to each bed we are taking a plank from, BEFORE the
	// membership changes: its plate comes out of BambuBuddy's queue and its
	// filament is given back. A Draft passes through untouched. Done first
	// because a plate withdrawn after the move would be withdrawn on behalf of
	// a bed that no longer describes it.
	for _, bed := range sources {
		if err := s.beginBatchEdit(ctx, bed); err != nil {
			writeStatusError(c, err, "Could not release a bed those products are on.")
			return
		}
	}

	number, err := s.store.Q.NextBatchNumber(ctx)
	if err != nil {
		detail(c, http.StatusInternalServerError, "Could not generate a batch number.")
		return
	}
	// Draft, like every auto-created bed. A hand-built bed is still a plan
	// until somebody locks it, and locking is what reserves the filament.
	batch, err := s.store.Q.InsertBatch(ctx, gen.InsertBatchParams{
		ID: uuid.New(), BatchNumber: number, Status: production.BatchPendingApproval,
		// Built by a person, so the planner leaves it alone. Without this it
		// would be dissolved on the next planning run - a seven-minute timer
		// plus several events - and its planks redistributed, with nothing on
		// screen to say why the bed had gone.
		Manual: true,
	})
	if err != nil {
		detail(c, http.StatusInternalServerError, "Could not create the batch.")
		return
	}

	// A finished plank is reprinted rather than moved: its row records a print
	// that really happened, and putting it on another bed would rewrite that
	// into a print that has not happened yet.
	ids := make([]uuid.UUID, 0, len(jobs))
	for _, j := range jobs {
		ids = append(ids, j.ID)
	}

	// The move and the requeue together, or neither.
	//
	// They were two steps, and a finished plank was put back in the queue
	// BEFORE the move that might refuse it. When the move moved nothing, the
	// requeue stood: planks on a completed bed were left reading "queued", the
	// bed's own record said it had printed, and the reprint action then refused
	// them - "only a job that is printing or printed can be failed" - because
	// by then that was true.
	if err := s.store.InTx(ctx, func(q *gen.Queries) error {
		moved, err := q.MoveJobsToBatch(ctx, gen.MoveJobsToBatchParams{
			BatchID: &batch.ID, JobIds: ids,
		})
		if err != nil {
			return err
		}
		// A partial move is a failure, not a smaller bed. It means something
		// claimed a plank between the dialog being read and this request, and
		// half of somebody's deliberate arrangement is not what they asked for.
		if int(moved) != len(ids) {
			return errPlanksClaimed
		}
		// Only now, when the planks are certainly on this bed.
		for _, j := range jobs {
			if j.Status != production.StatusCompleted {
				continue
			}
			if err := q.RequeueFinishedJob(ctx, j.ID); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		s.discardEmptyBatch(ctx, batch)
		if errors.Is(err, errPlanksClaimed) {
			detail(c, http.StatusConflict,
				"Some of those products were claimed by another bed while you were choosing. Reopen the dialog and pick again.")
			return
		}
		detail(c, http.StatusInternalServerError, "Could not put the chosen products on the batch.")
		return
	}

	s.rebuildSourceBeds(ctx, c, sources)

	updated, ok := s.recomputeBatchPlate(ctx, c, batch)
	if !ok {
		return
	}
	c.JSON(http.StatusCreated, s.batchDTO(updated))
}

// rebuildSourceBeds puts each bed we took a plank from back together.
//
// A bed describes the plate built from the products on it - its utilisation,
// its print time, its merged plate file - so removing one leaves all three
// describing a bed that no longer exists. Each source is rebuilt from what it
// still has, which for a locked bed also re-reserves filament for the new
// composition and sends the corrected plate back to a printer.
//
// A source emptied completely is deleted. An empty bed is a row that looks like
// work and is not, and for a Draft the planner would simply propose it again.
//
// Best-effort, and deliberately after the new bed exists. The products have
// already moved; failing here would report a failure for something that
// succeeded and leave the caller unsure whether to try again.
func (s *Server) rebuildSourceBeds(ctx context.Context, c *gin.Context, sources []gen.Batch) {
	log := obs.FromContext(ctx)
	for _, bed := range sources {
		remaining, err := s.store.Q.ListJobsForBatch(ctx, &bed.ID)
		if err != nil {
			log.Warn("could not re-read a bed products were taken from",
				"batch", bed.BatchNumber, "error", err)
			continue
		}
		if len(remaining) == 0 {
			if _, err := s.store.Q.DeleteBatch(ctx, bed.ID); err != nil {
				log.Warn("could not remove an emptied bed", "batch", bed.BatchNumber, "error", err)
			}
			continue
		}
		if _, ok := s.finishBatchEdit(ctx, c, bed); !ok {
			log.Warn("could not rebuild a bed products were taken from",
				"batch", bed.BatchNumber)
			return
		}
	}
}

// planksBeingMoved is the chosen products that will actually leave a bed.
//
// A finished plank is reprinted rather than moved - a new job is minted and the
// original stays where it is - so its bed is never touched. Separating the two
// matters because the bed a finished plank sits on is itself finished, and a
// finished bed cannot be edited: treating it as a source refused the whole
// request over beds nothing was going to change.
func planksBeingMoved(jobs []gen.ProductionJob) []gen.ProductionJob {
	out := make([]gen.ProductionJob, 0, len(jobs))
	for _, j := range jobs {
		if j.Status != production.StatusCompleted {
			out = append(out, j)
		}
	}
	return out
}

// discardEmptyBatch removes a bed that was created and never filled.
//
// Best-effort, and deliberate: an empty Draft looks like work, sits in the
// pending list and invites somebody to wonder what happened to it. Leaving it
// was the earlier choice here and it left exactly that - a bed with no jobs, no
// plate and no explanation.
func (s *Server) discardEmptyBatch(ctx context.Context, batch gen.Batch) {
	if _, err := s.store.Q.DeleteBatch(ctx, batch.ID); err != nil {
		obs.FromContext(ctx).Warn("could not remove a bed that was never filled",
			"batch", batch.BatchNumber, "error", err)
	}
}

// eligibleJobsFor loads the chosen jobs and refuses any that may not be batched.
//
// Re-read and re-checked server-side rather than trusted from the request. The
// dialog only ever offers eligible jobs, but a request is not a dialog: these
// ids arrive from a client, and the list may have been on screen for minutes
// while somebody chose, by which time another operator's bed may already have
// claimed one of them.
func (s *Server) eligibleJobsFor(ctx context.Context, raw []string) ([]gen.ProductionJob, error) {
	out := make([]gen.ProductionJob, 0, len(raw))
	seen := map[uuid.UUID]bool{}
	for _, id := range raw {
		jobID, err := uuid.Parse(id)
		if err != nil {
			return nil, statusErr(http.StatusUnprocessableEntity,
				"One of the chosen products is not a valid identifier.")
		}
		if seen[jobID] {
			continue
		}
		seen[jobID] = true

		job, err := s.store.Q.GetProductionJobByID(ctx, jobID)
		if err != nil {
			return nil, statusErr(http.StatusNotFound, "One of the chosen products no longer exists.")
		}
		if err := batchableNow(job); err != nil {
			return nil, statusErr(http.StatusUnprocessableEntity, err.Error())
		}
		out = append(out, job)
	}
	return out, nil
}

// sourceBeds loads the beds these products sit on, refusing any whose
// membership is settled.
//
// batchableNow cannot answer this: the job row carries a batch id and not that
// batch's status, and the difference is the whole rule. A Draft and a Locked
// bed can both give a plank up - locked costs more, because its plate has to
// come out of BambuBuddy's queue and its filament be given back first, which is
// what beginBatchEdit does. A bed that is PRINTING or DONE cannot: those record
// what physically happened to a plate.
func (s *Server) sourceBeds(ctx context.Context, jobs []gen.ProductionJob) ([]gen.Batch, error) {
	// Only the planks actually being MOVED. A finished one is reprinted - a new
	// job is minted and the original stays exactly where it is - so its bed is
	// never touched and must not be judged as though it were. Judging it
	// refused the whole request, because the bed a finished plank sits on is
	// itself finished, and a finished bed cannot be edited: four printed planks
	// could not be reprinted together because of beds nothing was going to
	// change.
	ids := dedupeIDs(planksBeingMoved(jobs), func(j gen.ProductionJob) *uuid.UUID { return j.BatchID })
	if len(ids) == 0 {
		return nil, nil
	}
	out := make([]gen.Batch, 0, len(ids))
	for _, id := range ids {
		bed, err := s.store.Q.GetBatchByID(ctx, id)
		if err != nil {
			return nil, statusErr(http.StatusInternalServerError,
				"Could not read a bed those products are on.")
		}
		if ok, why := editableBatch(bed); !ok {
			return nil, statusErr(http.StatusUnprocessableEntity,
				fmt.Sprintf("%s holds one of these products. %s", bed.BatchNumber, why))
		}
		out = append(out, bed)
	}
	return out, nil
}

// batchableNow mirrors ListBatchableJobs' WHERE clause, job by job.
//
// Deliberately a second statement of the same rule, because the first is SQL
// and cannot explain itself to an operator. Each refusal names the job and what
// is wrong with it, which "that job is not eligible" would not.
func batchableNow(j gen.ProductionJob) error {
	switch {
	// Being on a bed is NOT a refusal here: a Draft is a proposal and its
	// products can be moved onto a bed somebody is building by hand. Which
	// beds are still proposals is checked in refuseCommittedBeds, which can
	// see their status; this row cannot.
	// The raw quantity, not jobQuantity, which clamps a zero up to one. A
	// split job's original row stays at zero once every unit has been peeled
	// off into its own rows; clamping would offer that spent row as one more
	// plank to print.
	case j.Quantity <= 0:
		return fmt.Errorf("%s has nothing left to print", j.JobNumber)
	case j.Status == production.StatusFailed:
		return fmt.Errorf("%s failed; reprint it from the job rather than re-bedding it", j.JobNumber)
	case j.Status != production.StatusQueued && j.Status != production.StatusCompleted:
		return fmt.Errorf("%s is %s, so it cannot go on a bed", j.JobNumber, j.Status)
	case j.Held:
		return fmt.Errorf("%s is on hold", j.JobNumber)
	case j.IssueReason != nil:
		return fmt.Errorf("%s needs attention first: %s", j.JobNumber, *j.IssueReason)
	case j.PersonalisationStatus != production.PersonalisationValidated &&
		j.PersonalisationStatus != production.PersonalisationNotRequired:
		return fmt.Errorf("%s's personalisation has not been checked yet", j.JobNumber)
	}
	return nil
}

// checkOneBedWorth refuses a selection that could not physically be one bed.
//
// The same two rules the planner and the add-to-batch path enforce, for the
// same reason: a plate declares one filament per slot and holds a fixed number
// of places, and neither becomes negotiable because a person did the choosing.
func checkOneBedWorth(jobs []gen.ProductionJob, unitCap int) error {
	if len(jobs) == 0 {
		return fmt.Errorf("choose at least one product for this bed")
	}
	key := compatibilityKeyOf(jobs[0])
	if key.Colour == "" {
		return fmt.Errorf("%s records no filament colour, so nothing can be matched to it",
			jobs[0].JobNumber)
	}
	for _, j := range jobs[1:] {
		if compatibilityKeyOf(j) != key {
			return fmt.Errorf(
				"%s does not match %s - a bed holds one colour, material and nozzle setup",
				j.JobNumber, jobs[0].JobNumber)
		}
	}
	if units := unitsOf(jobs); units > unitCap {
		return fmt.Errorf("a bed holds %d products; this is %d", unitCap, units)
	}
	return nil
}

// compatibilityKeyString renders a compatibility key for the browser to compare.
//
// Opaque on purpose. The dialog needs to know only whether two jobs may share a
// bed, never why, and giving it the parts would invite it to start deciding for
// itself - at which point the rule lives in two languages and one of them is
// wrong. fmt's %v over the struct changes if the struct does, which is the
// correct coupling: a new field that splits beds must split them here too.
func compatibilityKeyString(j gen.ProductionJob) string {
	return fmt.Sprintf("%v", compatibilityKeyOf(j))
}
