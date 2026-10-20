// Command modelcheck reports what a job's STORED model actually is.
//
// "The preview has not changed" is a claim about the file in object storage,
// and the job page cannot answer it: two templates that lay a plank out the
// same way produce pictures a person cannot tell apart, so the eye is not
// evidence either way. This reads the row, fetches the object and prints the
// things that do distinguish them - the recorded template, the bounding box
// and the triangle count.
//
// Read-only. Nothing is written to object storage or to the database.
//
//	go run ./cmd/modelcheck -job JOB-115942
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"time"

	"github.com/joho/godotenv"

	"github.com/Optiminastic/tensor-core/internal/config"
	"github.com/Optiminastic/tensor-core/internal/db"
	"github.com/Optiminastic/tensor-core/internal/meshio"
	"github.com/Optiminastic/tensor-core/internal/storage"
)

func main() {
	jobNumber := flag.String("job", "", "the job number to inspect, e.g. JOB-115942")
	flag.Parse()
	if *jobNumber == "" {
		log.Fatal("-job is required")
	}

	_ = godotenv.Load("env/local.env")
	cfg := config.Load()
	ctx := context.Background()

	store, err := db.Open(ctx, cfg.DatabaseURL, db.Options{})
	if err != nil {
		log.Fatalf("connect to database: %v", err)
	}
	defer store.Close()

	var (
		filename     string
		storageKey   string
		sizeBytes    int64
		renderParams []byte
		createdAt    time.Time
	)
	err = store.Pool.QueryRow(ctx, `
        SELECT f.filename, f.storage_key, f.size_bytes,
               coalesce(f.render_params, '{}'::jsonb), f.created_at
        FROM production_jobs j
        JOIN file_assets f ON f.id = j.print_file_id
        WHERE j.job_number = $1`, *jobNumber).
		Scan(&filename, &storageKey, &sizeBytes, &renderParams, &createdAt)
	if err != nil {
		log.Fatalf("read the job's model row: %v", err)
	}

	fmt.Printf("job         %s\n", *jobNumber)
	fmt.Printf("filename    %s\n", filename)
	fmt.Printf("storage key %s\n", storageKey)
	fmt.Printf("size        %d bytes\n", sizeBytes)
	fmt.Printf("stored at   %s\n", createdAt.Local().Format("2006-01-02 15:04:05"))

	var pretty map[string]any
	if err := json.Unmarshal(renderParams, &pretty); err == nil {
		out, _ := json.MarshalIndent(pretty, "            ", "  ")
		fmt.Printf("render_params %s\n", out)
	}

	objects, err := storage.New(ctx, storage.Options{
		Endpoint: cfg.S3Endpoint, AccessKey: cfg.S3AccessKey, SecretKey: cfg.S3SecretKey,
		Bucket: cfg.S3Bucket, KeyPrefix: cfg.S3KeyPrefix, Secure: cfg.S3Secure,
		AssumeBucketExists: cfg.S3AssumeBucketExists,
	})
	if err != nil {
		log.Fatalf("object storage: %v", err)
	}
	obj, err := objects.Get(ctx, storageKey)
	if err != nil {
		log.Fatalf("fetch %s: %v", storageKey, err)
	}
	defer func() { _ = obj.Body.Close() }()
	buf := &bytes.Buffer{}
	if _, err := io.Copy(buf, obj.Body); err != nil {
		log.Fatalf("read %s: %v", storageKey, err)
	}

	parts, err := meshio.Read3MF(buf.Bytes())
	if err != nil {
		log.Fatalf("read the 3MF: %v", err)
	}
	fmt.Printf("\n%d part(s) in the model:\n", len(parts))
	for _, p := range parts {
		lo := [3]float64{1e9, 1e9, 1e9}
		hi := [3]float64{-1e9, -1e9, -1e9}
		for _, t := range p.Triangles {
			for _, v := range [3][3]float64{
				{t.V0.X, t.V0.Y, t.V0.Z}, {t.V1.X, t.V1.Y, t.V1.Z}, {t.V2.X, t.V2.Y, t.V2.Z},
			} {
				for i := range v {
					if v[i] < lo[i] {
						lo[i] = v[i]
					}
					if v[i] > hi[i] {
						hi[i] = v[i]
					}
				}
			}
		}
		fmt.Printf("  %-22s colour=%-9s triangles=%-7d  %.2f x %.2f x %.2f mm  (z %.2f..%.2f)\n",
			p.Name, p.Colour, len(p.Triangles),
			hi[0]-lo[0], hi[1]-lo[1], hi[2]-lo[2], lo[2], hi[2])
	}
}
