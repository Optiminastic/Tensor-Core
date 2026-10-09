// Command registrycheck answers one question per SKU: would a job for it
// render from the registry, and if not, which piece is missing?
//
// Read-only. It runs the same three lookups registryRendersSKU does - find the
// product, count its design roles with templates, count its field maps - and
// prints which one answered no. The alternative is reading a "not rendering"
// symptom off a held job and guessing between four causes.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"sort"
	"strings"
	"time"

	"github.com/joho/godotenv"

	"github.com/Optiminastic/tensor-core/internal/config"
	"github.com/Optiminastic/tensor-core/internal/db"
)

func main() {
	// The order to inspect. A flag rather than a constant: this is a
	// diagnostic, and the order worth looking at is whichever one is wrong
	// today - hardcoding a real customer's order number both dates the tool
	// and puts somebody's order in the repository.
	orderNumber := flag.String("order", "", "an order number to inspect (optional)")
	flag.Parse()
	_ = godotenv.Load("env/local.env")
	cfg := config.Load()
	if cfg.DatabaseURL == "" {
		log.Fatal("DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	store, err := db.Open(ctx, cfg.DatabaseURL, db.Options{})
	if err != nil {
		log.Fatalf("open database: %v", err)
	}
	defer store.Pool.Close()

	products, err := store.Q.ListProducts(ctx)
	if err != nil {
		log.Fatalf("list products: %v", err)
	}
	fmt.Printf("=== registry products (%d) ===\n", len(products))
	for _, p := range products {
		designs, _ := store.Q.ListDesignsForProduct(ctx, p.ID)
		maps, _ := store.Q.ListAllProductFieldMaps(ctx, p.ID)
		variants, _ := store.Q.ListVariantsForProduct(ctx, p.ID)

		roles := map[string]bool{}
		withTemplate := 0
		for _, d := range designs {
			if d.TemplateKey != nil && strings.TrimSpace(*d.TemplateKey) != "" {
				withTemplate++
				r := strings.TrimSpace(d.Role)
				if r == "" {
					r = "body"
				}
				roles[r] = true
			}
		}
		names := make([]string, 0, len(roles))
		for r := range roles {
			names = append(names, r)
		}
		sort.Strings(names)

		verdict := "RENDERS from registry"
		switch {
		case withTemplate == 0:
			verdict = "no  - no design row names a template"
		case len(maps) == 0:
			verdict = "no  - no field maps"
		}
		fmt.Printf("\n%-14s %-34s variants=%-4d designs=%-3d (with template %d) fieldmaps=%-3d\n",
			p.Code, p.Name, len(variants), len(designs), withTemplate, len(maps))
		fmt.Printf("  roles: %v\n  -> %s\n", names, verdict)
		for _, v := range variants {
			sku := "<null>"
			if v.Sku != nil {
				sku = *v.Sku
			}
			fmt.Printf("     variant sku=%-18s status=%-10s %s\n", sku, v.Status, v.Name)
		}
		if len(maps) > 0 {
			fmt.Printf("     field maps:\n")
			for _, m := range maps {
				fixed := ""
				if m.FixedValue != nil {
					fixed = "  fixed=" + *m.FixedValue
				}
				fmt.Printf("       role=%-16s %-34s -> %-14s type=%-7s required=%v%s\n",
					m.Role, m.PropertyKey, m.ScadVariable, m.ValueType, m.Required, fixed)
			}
		}
		for _, d := range designs {
			tk := "<none>"
			if d.TemplateKey != nil {
				tk = *d.TemplateKey
			}
			fmt.Printf("     design role=%-16s template=%s\n", strings.TrimSpace(d.Role), tk)
		}
	}

	fmt.Printf("\n=== uploaded design templates ===\n")
	tpls, err := store.Q.ListActiveTemplates(ctx)
	if err != nil {
		fmt.Printf("  (could not list: %v)\n", err)
	}
	for _, t := range tpls {
		fmt.Printf("  %-22s v%-3d %s\n", t.TemplateKey, t.Version, t.Status)
	}

	fmt.Printf("\n=== production jobs for SC / SCWL ===\n")
	rows, err := store.Pool.Query(ctx, `
		SELECT job_number, coalesce(sku,''), coalesce(part_role,''), status,
		       coalesce(issue_reason,''), coalesce(model_error,''),
		       (print_file_id IS NOT NULL), coalesce(batch_id::text,''), created_at
		  FROM production_jobs
		 WHERE sku ILIKE 'SC-%' OR sku ILIKE 'SCWL-%'
		 ORDER BY created_at DESC LIMIT 40`)
	if err != nil {
		fmt.Printf("  query failed: %v\n", err)
	} else {
		defer rows.Close()
		n := 0
		for rows.Next() {
			var num, sku, role, status, issue, merr, batch string
			var hasFile bool
			var created time.Time
			if err := rows.Scan(&num, &sku, &role, &status, &issue, &merr, &hasFile, &batch, &created); err != nil {
				fmt.Printf("  scan: %v\n", err)
				break
			}
			n++
			fmt.Printf("  %-12s %-10s role=%-16s %-12s model=%v batch=%-8s issue=%-16s %s\n",
				num, sku, role, status, hasFile, firstN(batch, 8), issue, firstN(merr, 70))
		}
		if n == 0 {
			fmt.Println("  (none - no job has ever been created for a Soulmate SKU)")
		}
	}

	fmt.Printf("\n=== orders containing a Soulmate SKU ===\n")
	var orderCount int
	if err := store.Pool.QueryRow(ctx,
		`SELECT count(*) FROM order_line_items WHERE sku ILIKE 'SC-%' OR sku ILIKE 'SCWL-%'`,
	).Scan(&orderCount); err != nil {
		fmt.Printf("  count failed: %v\n", err)
	} else {
		fmt.Printf("  order lines with a Soulmate SKU: %d\n", orderCount)
	}

	fmt.Printf("\n=== batching reach, by SKU family (all jobs) ===\n")
	frows, err := store.Pool.Query(ctx, `
		SELECT CASE WHEN sku ILIKE 'SCWL-%' THEN 'SCWL'
		            WHEN sku ILIKE 'SC-%'   THEN 'SC'
		            WHEN sku ILIKE '%DNP%'  THEN 'DNP*'
		            ELSE 'other' END AS fam,
		       count(*),
		       count(*) FILTER (WHERE print_file_id IS NOT NULL) AS rendered,
		       count(*) FILTER (WHERE batch_id IS NOT NULL)      AS batched,
		       count(*) FILTER (WHERE status = 'completed')      AS completed,
		       count(*) FILTER (WHERE model_error IS NOT NULL)   AS errored
		  FROM production_jobs GROUP BY 1 ORDER BY 2 DESC`)
	if err != nil {
		fmt.Printf("  query failed: %v\n", err)
	} else {
		defer frows.Close()
		fmt.Printf("  %-8s %7s %9s %8s %10s %8s\n", "family", "jobs", "rendered", "batched", "completed", "errored")
		for frows.Next() {
			var fam string
			var total, rendered, batched, completed, errored int
			if err := frows.Scan(&fam, &total, &rendered, &batched, &completed, &errored); err != nil {
				break
			}
			fmt.Printf("  %-8s %7d %9d %8d %10d %8d\n", fam, total, rendered, batched, completed, errored)
		}
	}

	fmt.Printf("\n=== Soulmate order lines, and whether each became jobs ===\n")
	lrows, err := store.Pool.Query(ctx, `
		SELECT li.sku, li.product_name, li.order_id,
		       (SELECT count(*) FROM production_jobs j WHERE j.order_id = li.order_id AND j.sku = li.sku)
		  FROM order_line_items li
		 WHERE li.sku ILIKE 'SC-%' OR li.sku ILIKE 'SCWL-%'`)
	if err == nil {
		defer lrows.Close()
		for lrows.Next() {
			var sku, name string
			var oid any
			var jobs int
			if err := lrows.Scan(&sku, &name, &oid, &jobs); err != nil {
				break
			}
			fmt.Printf("  %-10s %-34s jobs=%d\n", sku, firstN(name, 34), jobs)
		}
	}

	fmt.Printf("\n=== slicer-pipeline mappings ===\n")
	prows, err := store.Pool.Query(ctx,
		`SELECT sku, machine_family, pipeline_id, coalesce(pipeline_name,'')
		   FROM sku_slicer_pipelines ORDER BY sku, machine_family`)
	if err != nil {
		fmt.Printf("  query failed: %v\n", err)
	} else {
		defer prows.Close()
		n := 0
		for prows.Next() {
			var sku, fam, name string
			var pid int
			if err := prows.Scan(&sku, &fam, &pid, &name); err != nil {
				break
			}
			n++
			fmt.Printf("  %-12s %-6s pipeline=%-4d %s\n", sku, fam, pid, name)
		}
		if n == 0 {
			fmt.Println("  (none)")
		}
	}

	fmt.Printf("\n=== why a Soulmate line made no jobs ===\n")
	nrows, err := store.Pool.Query(ctx, `
		SELECT li.sku, o.order_number, o.status, o.created_at,
		       (SELECT count(*) FROM production_jobs j WHERE j.order_id = o.id)
		  FROM order_line_items li JOIN orders o ON o.id = li.order_id
		 WHERE li.sku ILIKE 'SC%' ORDER BY o.created_at DESC`)
	if err == nil {
		defer nrows.Close()
		for nrows.Next() {
			var sku, num, status string
			var created time.Time
			var jobs int
			if err := nrows.Scan(&sku, &num, &status, &created, &jobs); err != nil {
				break
			}
			fmt.Printf("  %-10s order=%-12s status=%-12s jobs_on_order=%d  %s\n",
				sku, num, status, jobs, created.Format("2006-01-02"))
		}
	}

	fmt.Printf("\n=== job fields the planner needs: SC vs a batched DNP ===\n")
	wrows, err := store.Pool.Query(ctx, `
		SELECT job_number, coalesce(sku,''), coalesce(part_role,''),
		       coalesce(material,''), coalesce(colour,''), coalesce(machine_family,''),
		       coalesce(filament_grams_required,0), coalesce(estimated_print_time_minutes,0),
		       coalesce(bbox_x_mm,0), coalesce(bbox_y_mm,0), coalesce(colour_count,0),
		       (batch_id IS NOT NULL)
		  FROM production_jobs
		 WHERE sku ILIKE 'SC%'
		    OR (batch_id IS NOT NULL AND sku ILIKE '%DNP%')
		 ORDER BY (sku ILIKE 'SC%') DESC, job_number LIMIT 14`)
	if err != nil {
		fmt.Printf("  query failed: %v\n", err)
	} else {
		defer wrows.Close()
		fmt.Printf("  %-12s %-10s %-14s %-6s %-9s %-5s %7s %6s %7s %7s %3s %s\n",
			"job", "sku", "role", "matl", "colour", "fam", "grams", "mins", "bboxX", "bboxY", "col", "batched")
		for wrows.Next() {
			var num, sku, role, matl, col, fam string
			var grams, mins, bx, by float64
			var ccount int
			var batched bool
			if err := wrows.Scan(&num, &sku, &role, &matl, &col, &fam, &grams, &mins, &bx, &by, &ccount, &batched); err != nil {
				fmt.Printf("  scan: %v\n", err)
				break
			}
			fmt.Printf("  %-12s %-10s %-14s %-6s %-9s %-5s %7.1f %6.0f %7.1f %7.1f %3d %v\n",
				num, sku, firstN(role, 14), matl, firstN(col, 9), fam, grams, mins, bx, by, ccount, batched)
		}
	}

	fmt.Printf("\n=== the order that made no jobs ===\n")
	var li []byte
	var onum, ostatus string
	if err := store.Pool.QueryRow(ctx,
		`SELECT order_number, status, line_items FROM orders WHERE order_number = $1`,
		*orderNumber,
	).Scan(&onum, &ostatus, &li); err != nil {
		fmt.Printf("  %v\n", err)
	} else {
		fmt.Printf("  %s status=%s\n  line_items=%s\n", onum, ostatus, firstN(string(li), 900))
	}

	fmt.Printf("\n=== recent orders: did job creation run at all? ===\n")
	rr, err := store.Pool.Query(ctx, `
		SELECT o.order_number, o.status, o.created_at,
		       (SELECT count(*) FROM production_jobs j WHERE j.order_id = o.id) AS jobs
		  FROM orders o
		 WHERE o.created_at > now() - interval '7 days'
		 ORDER BY o.created_at DESC LIMIT 20`)
	if err != nil {
		fmt.Printf("  %v\n", err)
	} else {
		defer rr.Close()
		for rr.Next() {
			var num, status string
			var created time.Time
			var jobs int
			if err := rr.Scan(&num, &status, &created, &jobs); err != nil {
				break
			}
			flag := ""
			if jobs == 0 {
				flag = "   <-- no jobs"
			}
			fmt.Printf("  %-14s %-12s jobs=%-3d %s%s\n", num, status, jobs, created.Format("01-02 15:04"), flag)
		}
	}

	fmt.Printf("\n=== river job failures, last 7 days ===\n")
	jr, err := store.Pool.Query(ctx, `
		SELECT kind, state, count(*)
		  FROM river_job
		 WHERE created_at > now() - interval '7 days'
		 GROUP BY 1,2 ORDER BY 3 DESC LIMIT 15`)
	if err != nil {
		fmt.Printf("  %v\n", err)
	} else {
		defer jr.Close()
		for jr.Next() {
			var kind, state string
			var n int
			if err := jr.Scan(&kind, &state, &n); err != nil {
				break
			}
			fmt.Printf("  %-34s %-12s %d\n", kind, state, n)
		}
	}

	fmt.Printf("\n=== discarded worker errors ===\n")
	er, err := store.Pool.Query(ctx, `
		SELECT kind, args::text, errors::text, finalized_at
		  FROM river_job
		 WHERE state = 'discarded' AND created_at > now() - interval '7 days'
		 ORDER BY finalized_at DESC LIMIT 14`)
	if err != nil {
		fmt.Printf("  %v\n", err)
	} else {
		defer er.Close()
		for er.Next() {
			var kind, args, errs string
			var fin *time.Time
			if err := er.Scan(&kind, &args, &errs, &fin); err != nil {
				fmt.Printf("  scan: %v\n", err)
				break
			}
			when := ""
			if fin != nil {
				when = fin.Format("01-02 15:04")
			}
			fmt.Printf("\n  [%s] %s\n    args: %s\n    err:  %s\n",
				when, kind, firstN(args, 160), firstN(errs, 420))
		}
	}

	fmt.Printf("\n=== %s jobs now ===\n", *orderNumber)
	orows, err := store.Pool.Query(ctx, `
		SELECT j.job_number, coalesce(j.sku,''), coalesce(j.part_role,''), j.status,
		       (j.print_file_id IS NOT NULL), coalesce(j.issue_reason,''), coalesce(j.model_error,'')
		  FROM production_jobs j JOIN orders o ON o.id = j.order_id
		 WHERE o.order_number = $1 ORDER BY j.job_number`, *orderNumber)
	if err != nil {
		fmt.Printf("  %v\n", err)
	} else {
		defer orows.Close()
		n := 0
		for orows.Next() {
			var num, sku, role, status, issue, merr string
			var hasFile bool
			if err := orows.Scan(&num, &sku, &role, &status, &hasFile, &issue, &merr); err != nil {
				break
			}
			n++
			fmt.Printf("  %-13s %-10s role=%-16s %-10s model=%-5v issue=%-14s %s\n",
				num, sku, role, status, hasFile, issue, firstN(merr, 80))
		}
		if n == 0 {
			fmt.Println("  still zero jobs")
		}
	}

	fmt.Printf("\n=== do uploaded templates implement the colour convention? ===\n")
	trows, err := store.Pool.Query(ctx,
		`SELECT template_key, version, length(source), encode(source::bytea,'escape')
		   FROM design_templates WHERE status = 'active' ORDER BY template_key`)
	if err != nil {
		// source may not be text; fall back to a cast-free read
		trows, err = store.Pool.Query(ctx,
			`SELECT template_key, version, 0, '' FROM design_templates WHERE status='active' ORDER BY template_key`)
	}
	if err != nil {
		fmt.Printf("  %v\n", err)
	} else {
		defer trows.Close()
		for trows.Next() {
			var key, src string
			var ver, n int
			if err := trows.Scan(&key, &ver, &n, &src); err != nil {
				fmt.Printf("  scan: %v\n", err)
				break
			}
			up := strings.ToUpper(src)
			has := func(tok string) string {
				if strings.Contains(up, tok) {
					return "yes"
				}
				return "NO "
			}
			fmt.Printf("  %-20s v%-2d %6d bytes  PART=%s  \"text\"=%s  \"base\"=%s\n",
				key, ver, n, has("PART"), has("\"TEXT\""), has("\"BASE\""))
		}
	}
	fmt.Printf("\n=== SKU lookups ===\n")
	for _, sku := range flag.Args() {
		p, err := store.Q.FindProductBySKU(ctx, sku)
		if err != nil {
			fmt.Printf("%-18s -> no product found (%v)\n", sku, err)
			continue
		}
		maps, _ := store.Q.ListAllProductFieldMaps(ctx, p.ID)
		designs, _ := store.Q.ListDesignsForProduct(ctx, p.ID)
		fmt.Printf("%-18s -> %s (%s)  designs=%d fieldmaps=%d\n",
			sku, p.Code, p.Name, len(designs), len(maps))
	}
}

func firstN(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
