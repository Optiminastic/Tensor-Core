// Command markprinted records that a list of orders has been printed.
//
// The floor keeps a paper list of the plates that came off the printers, by
// order number. This is that list, applied: the orders' jobs are completed, a
// bed whose whole contents printed is marked Done, and a bed that printed only
// partly is unlocked back to a Draft so the planner refills it to four.
//
//	go run ./cmd/markprinted 114762 114778 ...
//	go run ./cmd/markprinted -file printed.txt      # one number per line
//	go run ./cmd/markprinted -dry-run -file printed.txt
//
// Numbers may be written with or without the store prefix: 114762 and
// T3DPS-114762 both match.
package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"strings"

	"github.com/joho/godotenv"

	"github.com/Optiminastic/tensor-core/internal/auth"
	"github.com/Optiminastic/tensor-core/internal/config"
	"github.com/Optiminastic/tensor-core/internal/db"
	"github.com/Optiminastic/tensor-core/internal/httpapi"
	"github.com/Optiminastic/tensor-core/internal/obs"
	"github.com/Optiminastic/tensor-core/internal/storage"
)

func main() {
	file := flag.String("file", "", "read order numbers from this file, one per line")
	dryRun := flag.Bool("dry-run", false, "report what would change without writing anything")
	flag.Parse()

	numbers, err := collect(*file, flag.Args())
	if err != nil {
		log.Fatal(err)
	}
	if len(numbers) == 0 {
		log.Fatal("no order numbers given")
	}

	_ = godotenv.Load("env/local.env")
	cfg := config.Load()
	logger := obs.New("info")
	ctx := context.Background()

	store, err := db.Open(ctx, cfg.DatabaseURL, db.Options{})
	if err != nil {
		log.Fatalf("connect to database: %v", err)
	}
	defer store.Close()

	server := httpapi.NewServer(cfg, store, auth.NewGuards(nil, ""), logger)
	// Object storage is what lets a reopened bed's preview plate be rebuilt.
	// Not fatal without it: the reconciliation itself only writes rows, and a
	// stale preview is cleared rather than rebuilt anyway.
	if objects, err := storage.New(ctx, storage.Options{
		Endpoint: cfg.S3Endpoint, AccessKey: cfg.S3AccessKey, SecretKey: cfg.S3SecretKey,
		Bucket: cfg.S3Bucket, KeyPrefix: cfg.S3KeyPrefix, Secure: cfg.S3Secure,
		AssumeBucketExists: cfg.S3AssumeBucketExists,
	}); err != nil {
		log.Printf("object storage unavailable, previews will be rebuilt on demand: %v", err)
	} else {
		server.EnablePipeline(objects, nil)
	}

	if *dryRun {
		report, err := server.PreviewOrdersPrinted(ctx, numbers)
		if err != nil {
			log.Fatalf("preview: %v", err)
		}
		fmt.Print(report)
		return
	}

	out, err := server.MarkOrdersPrinted(ctx, numbers)
	if err != nil {
		log.Fatalf("mark printed: %v", err)
	}
	fmt.Printf("orders matched: %d\njobs completed: %d\nbeds marked done: %d %v\nbeds reopened to refill: %d %v\n",
		len(out.Matched), out.JobsCompleted,
		len(out.BatchesCompleted), out.BatchesCompleted,
		len(out.BatchesReopened), out.BatchesReopened)
	if len(out.Missing) > 0 {
		// Named, not counted: a number that matched nothing is usually a
		// misread digit, and knowing which one is the whole point.
		fmt.Printf("NOT FOUND in Tensor (%d): %v\n", len(out.Missing), out.Missing)
	}
}

// collect gathers order numbers from a file and/or the command line.
func collect(file string, args []string) ([]string, error) {
	numbers := append([]string{}, args...)
	if file == "" {
		return numbers, nil
	}
	f, err := os.Open(file)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", file, err)
	}
	defer f.Close()
	scan := bufio.NewScanner(f)
	for scan.Scan() {
		// Blank lines and #-comments so the list can be kept as a file with the
		// date and the machine names the floor wrote beside each group.
		line := strings.TrimSpace(scan.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		numbers = append(numbers, strings.Fields(line)...)
	}
	return numbers, scan.Err()
}
