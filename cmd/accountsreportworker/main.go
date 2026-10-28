// Command accountsreportworker mails the daily sales register, and does
// nothing else.
//
// ITS OWN PROCESS, ITS OWN TICKER. Like cmd/winbackworker and unlike the
// pipeline's queues, this is scheduled work with a deadline somebody notices:
// an accountant expects a register every afternoon. Behind the production
// worker it would queue behind plate renders and a Shopify pull, and a
// mailbox credential would have to be handed to a process that has no other
// use for one.
//
// NOT A RIVER PERIODIC JOB, deliberately, and for the reason written at length
// in internal/workerset: River runs periodic jobs on the elected leader alone,
// and on a shared database that lease belongs to whichever client took it
// first - routinely a container running a build that does not register this
// job at all, so the tick never happens. That is how the win-back sweep came
// to be scheduled nowhere. This ticks for itself.
//
// WAKING OFTEN IS SAFE. The window is noon-to-noon and the send is guarded by
// last_window_end in the database, so a tick inside an already-sent window
// finds nothing due. That is what makes a restart, a second replica and a
// manual Send now all harmless.
package main

import (
	"context"
	"log"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/joho/godotenv"

	"github.com/Optiminastic/tensor-core/internal/accounts"
	"github.com/Optiminastic/tensor-core/internal/auth"
	"github.com/Optiminastic/tensor-core/internal/config"
	"github.com/Optiminastic/tensor-core/internal/db"
	"github.com/Optiminastic/tensor-core/internal/db/gen"
	"github.com/Optiminastic/tensor-core/internal/httpapi"
	"github.com/Optiminastic/tensor-core/internal/obs"
)

// tick is how often the worker looks for a window that has closed.
//
// Five minutes, not once a day at noon: a process that acts on one instant
// has to be running at that instant, and a deploy, a restart or a host reboot
// over midday would lose the day silently. Checking often and being idempotent
// is cheaper than being punctual.
const tick = 5 * time.Minute

// grace is how long after noon the first attempt waits.
//
// MEASURED AGAINST THE ORDER SYNC, which runs every ten minutes. The register
// is built from Tensor's mirror of Shopify, so an order placed at 11:58 may
// not have been imported when noon strikes. Fifteen minutes is one full sync
// interval plus room for a slow pull; sending earlier risks a register that is
// short by a sale, which is the one error nobody would spot.
const grace = 15 * time.Minute

const stopTimeout = 30 * time.Second

func main() {
	_ = godotenv.Load("env/local.env")
	cfg := config.Load()
	logger := obs.New(cfg.LogLevel)

	if cfg.DatabaseURL == "" {
		log.Fatal("DATABASE_URL is not set")
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	store, err := db.Open(ctx, cfg.DatabaseURL, db.Options{})
	if err != nil {
		log.Fatalf("connect to database: %v", err)
	}
	defer store.Close()

	// Never serves HTTP, so a nil verifier is safe: NewGuards is a cheap
	// constructor and no guard is ever exercised here.
	server := httpapi.NewServer(cfg, store, auth.NewGuards(nil, ""), logger)

	// SAID AT STARTUP, not at noon. A worker with no mailbox will do nothing
	// at midday and log one line nobody is watching; saying it now means the
	// deployment that forgot the credentials is obvious on the first page of
	// the logs.
	if !server.MailboxReady() {
		logger.Warn("no mailbox is configured - this worker will send nothing. " +
			"Set SMTP_HOST, SMTP_USERNAME, SMTP_PASSWORD and SMTP_FROM")
	}
	logger.Info("accounts report worker running",
		"tick", tick.String(), "grace_after_noon", grace.String(),
		"mailbox_ready", server.MailboxReady(), "from", cfg.SMTPFrom)

	ticker := time.NewTicker(tick)
	defer ticker.Stop()

	// One pass immediately: a deploy at 1pm should not wait for the next
	// tick to notice that midday's register has not gone out.
	runOnce(ctx, server, store, logger)

	for {
		select {
		case <-ctx.Done():
			log.Println("shutting down")
			shutdown, cancel := context.WithTimeout(context.Background(), stopTimeout)
			defer cancel()
			<-shutdown.Done()
			return
		case <-ticker.C:
			runOnce(ctx, server, store, logger)
		}
	}
}

// runOnce sends every register that is due.
func runOnce(ctx context.Context, server *httpapi.Server, store *db.Store, logger *slog.Logger) {
	now := time.Now()
	from, to := accounts.Window(now, httpapi.ReportZone())

	// Inside the grace period the window has closed but the order sync may
	// not have caught up. Waiting costs a few minutes; not waiting costs a
	// sale missing from a register nobody will re-check.
	if now.Before(to.Add(grace)) {
		return
	}

	due, err := store.Q.ListDueAccountsReports(ctx, timestamptz(to))
	if err != nil {
		logger.Error("could not read which registers are due", "error", err)
		return
	}
	if len(due) == 0 {
		return
	}

	for _, settings := range due {
		if !server.MailboxReady() {
			logger.Error("a register is due but no mailbox is configured",
				"brand", settings.BrandSlug)
			continue
		}
		out, sendErr := server.SendAccountsReport(ctx, settings, from, to)
		if sendErr != nil {
			// The window is NOT advanced: the next tick tries the same day
			// again. A failure that moved the window on would lose that day's
			// register silently, and nobody would know until the accountant
			// asked.
			if recErr := store.Q.RecordAccountsReportFailure(ctx,
				gen.RecordAccountsReportFailureParams{
					BrandSlug: settings.BrandSlug, LastError: strptr(sendErr.Error()),
				}); recErr != nil {
				logger.Error("could not record the failure", "error", recErr)
			}
			logger.Error("could not send the sales register",
				"brand", settings.BrandSlug, "error", sendErr)
			continue
		}

		if err := store.Q.RecordAccountsReportSent(ctx, gen.RecordAccountsReportSentParams{
			BrandSlug: settings.BrandSlug,
			WindowEnd: timestamptz(to),
			RowCount:  int32ptr(int32(out.Rows)),
		}); err != nil {
			// Sent but not recorded: the next tick will send it again. Loud,
			// because a duplicate register is confusing and the cause is here.
			logger.Error("the register was SENT but the window was not recorded - "+
				"it may be sent again on the next tick",
				"brand", settings.BrandSlug, "error", err)
			continue
		}
		logger.Info("sales register sent",
			"brand", settings.BrandSlug, "orders", out.Orders, "rows", out.Rows,
			"recipients", out.Recipients, "file", out.Filename,
			"window_end", to.Format(time.RFC3339))
	}
}

func strptr(s string) *string { return &s }
func int32ptr(v int32) *int32 { return &v }

func timestamptz(t time.Time) pgtype.Timestamptz {
	return pgtype.Timestamptz{Time: t, Valid: true}
}
