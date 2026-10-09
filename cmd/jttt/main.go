package main

import (
	"context"
	"fmt"
	"log"

	"github.com/joho/godotenv"

	"github.com/Optiminastic/tensor-core/internal/config"
	"github.com/Optiminastic/tensor-core/internal/db"
)

func main() {
	_ = godotenv.Load("env/local.env")
	cfg := config.Load()
	ctx := context.Background()
	store, err := db.Open(ctx, cfg.DatabaseURL, db.Options{})
	if err != nil {
		log.Fatal(err)
	}
	defer store.Close()
	rows, err := store.Pool.Query(ctx, `
		SELECT job_number,
		       (print_file_id IS NOT NULL) AS model,
		       coalesce(issue_reason,'-')  AS issue,
		       coalesce(model_error,'-')   AS err,
		       coalesce(batch_id::text,'-') AS batch,
		       coalesce(colour,'-')        AS colour
		FROM production_jobs
		WHERE bulk_order_id IS NOT NULL ORDER BY job_number`)
	if err != nil {
		log.Fatal(err)
	}
	defer rows.Close()
	models, errs := 0, 0
	for rows.Next() {
		var jn, issue, e, batch, colour string
		var model bool
		if err := rows.Scan(&jn, &model, &issue, &e, &batch, &colour); err != nil {
			log.Fatal(err)
		}
		if model {
			models++
		}
		if e != "-" {
			errs++
		}
		if len(e) > 64 {
			e = e[:64] + "…"
		}
		fmt.Printf("  %-9s model=%-5v colour=%-10s issue=%-14s batch=%-8s err=%s\n",
			jn, model, colour, issue, batch, e)
	}
	fmt.Printf("\n%d with a model, %d carrying an error\n", models, errs)
}
