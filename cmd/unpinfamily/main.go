// Command unpinfamily clears the printer family stamped on generated jobs, so
// they can run on any machine in the fleet.
//
// Every plank and frame Tensor rendered used to be stamped with one configured
// family - "A2L" in production - because the planner refused to bed a job that
// named no family. The result was that five of thirteen printers did all the
// plank work while eight stood idle. The stamp is gone from the render path
// now, and an unset family means "any online machine", but the jobs already in
// the queue still carry the value they were given.
//
// Scoped to the stamped value, not to every family. A design-matched job's
// family is a real fact about its printer profile - an H2C plate belongs on an
// H2C - and clearing those would let a plate reach a machine it was not sliced
// for. Only the value this tool is told to unpin is removed.
//
// Printed and in-progress work is never touched: a job on a locked or running
// bed has had its plate built and its filament reserved for the machine it is
// on. Draft beds are included, because a Draft is a proposal the next planning
// pass can dissolve anyway.
//
//	go run ./cmd/unpinfamily -dry-run
//	go run ./cmd/unpinfamily -family A2L
package main

import (
	"context"
	"flag"
	"fmt"
	"log"

	"github.com/joho/godotenv"

	"github.com/Optiminastic/tensor-core/internal/config"
	"github.com/Optiminastic/tensor-core/internal/db"
	"github.com/Optiminastic/tensor-core/internal/production"
)

// where selects the jobs that may be unpinned: queued, and either unbatched or
// sitting on a bed that is still only a proposal.
//
// issue_reason is cleared only when it is exactly 'profile_missing' - the flag
// the old rule stamped on a familyless job - so a job held for any other reason
// stays held. That mirrors how SetProductionJobPrintFile clears 'stl_missing'
// and nothing else.
const where = `
  j.status = 'queued'
  AND (j.batch_id IS NULL
       OR j.batch_id IN (SELECT id FROM batches WHERE status = 'pending_approval'))
  AND (j.machine_family = $1 OR j.issue_reason = $2)`

func main() {
	dryRun := flag.Bool("dry-run", false, "report what would be unpinned without writing")
	family := flag.String("family", "A2L", "the stamped machine family to clear")
	flag.Parse()

	_ = godotenv.Load("env/local.env")
	cfg := config.Load()
	ctx := context.Background()

	store, err := db.Open(ctx, cfg.DatabaseURL, db.Options{})
	if err != nil {
		log.Fatalf("connect to database: %v", err)
	}
	defer store.Close()

	rows, err := store.Pool.Query(ctx, `
		SELECT j.job_number, coalesce(j.machine_family, ''), coalesce(j.issue_reason, '')
		  FROM production_jobs j
		 WHERE `+where+`
		 ORDER BY j.job_number`, *family, production.IssueProfileMissing)
	if err != nil {
		log.Fatalf("list jobs: %v", err)
	}
	type job struct{ number, family, issue string }
	var jobs []job
	for rows.Next() {
		var j job
		if err := rows.Scan(&j.number, &j.family, &j.issue); err != nil {
			log.Fatalf("scan: %v", err)
		}
		jobs = append(jobs, j)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		log.Fatalf("read jobs: %v", err)
	}

	fmt.Printf("%d job(s) pinned to %s\n", len(jobs), *family)
	if len(jobs) == 0 {
		return
	}
	for _, j := range jobs[:min(10, len(jobs))] {
		fmt.Printf("   %s  family=%q issue=%q\n", j.number, j.family, j.issue)
	}
	if len(jobs) > 10 {
		fmt.Printf("   … and %d more\n", len(jobs)-10)
	}
	if *dryRun {
		fmt.Println("\ndry run: nothing was changed")
		return
	}

	tag, err := store.Pool.Exec(ctx, `
		UPDATE production_jobs j
		   SET machine_family = NULL,
		       issue_reason = CASE WHEN j.issue_reason = $2 THEN NULL ELSE j.issue_reason END,
		       updated_at = now()
		 WHERE `+where, *family, production.IssueProfileMissing)
	if err != nil {
		log.Fatalf("unpin: %v", err)
	}
	fmt.Printf("\nunpinned %d job(s)\n", tag.RowsAffected())
}
