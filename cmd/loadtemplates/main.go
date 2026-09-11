// Command loadtemplates registers .scad files from a directory as design
// templates, the same way an upload through the Registry page would.
//
// It exists because the three plank templates ship compiled into the binary.
// That made them invisible to the registry and changeable only by a developer
// and a deploy - which is exactly how they drifted five days behind the shop's
// own masters without anyone noticing. Loading them here turns each into a
// stored file with a version, so it can be swapped, mapped to any variant, and
// answered for afterwards.
//
// Idempotent in the sense that matters: re-running adds a NEW version rather
// than a duplicate, and the previous one is retired, not deleted, so "which
// file printed last Tuesday's batch" stays answerable.
//
//	go run ./cmd/loadtemplates "C:/path/to/scad/folder"
package main

import (
	"bytes"
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/google/uuid"
	"github.com/joho/godotenv"

	"github.com/Optiminastic/tensor-core/internal/config"
	"github.com/Optiminastic/tensor-core/internal/db"
	"github.com/Optiminastic/tensor-core/internal/db/gen"
	"github.com/Optiminastic/tensor-core/internal/personalise"
	"github.com/Optiminastic/tensor-core/internal/storage"
)

// maxTemplateBytes matches the HTTP upload limit. The shop's own masters are
// 45 KB; a megabyte is room for one that grows tenfold and still refuses a 3MF
// sent by mistake.
const maxTemplateBytes = 1 << 20

const uploadedBy = "loadtemplates"

func main() {
	_ = godotenv.Load("env/local.env")
	if len(os.Args) < 2 {
		log.Fatal("usage: loadtemplates <directory of .scad files>")
	}
	dir := os.Args[1]

	cfg := config.Load()
	if cfg.DatabaseURL == "" {
		log.Fatal("DATABASE_URL is not set")
	}
	ctx := context.Background()

	store, err := db.Open(ctx, cfg.DatabaseURL, db.Options{})
	if err != nil {
		log.Fatalf("connect to database: %v", err)
	}
	defer store.Close()

	objects, err := storage.New(ctx, storage.Options{
		Endpoint:           cfg.S3Endpoint,
		AccessKey:          cfg.S3AccessKey,
		SecretKey:          cfg.S3SecretKey,
		Bucket:             cfg.S3Bucket,
		KeyPrefix:          cfg.S3KeyPrefix,
		Secure:             cfg.S3Secure,
		AssumeBucketExists: cfg.S3AssumeBucketExists,
	})
	if err != nil {
		log.Fatalf("object storage unavailable: %v", err)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		log.Fatalf("read %s: %v", dir, err)
	}

	loaded := 0
	for _, entry := range entries {
		// Exact .scad only. The folder is a working directory full of .bak,
		// .bakC, .bakD ... and loading a backup as the live template would
		// silently reinstate whatever was wrong with it.
		if entry.IsDir() || !strings.EqualFold(filepath.Ext(entry.Name()), ".scad") {
			continue
		}
		key := strings.ToLower(strings.TrimSuffix(entry.Name(), filepath.Ext(entry.Name())))
		if err := loadOne(ctx, store, objects, filepath.Join(dir, entry.Name()), key); err != nil {
			log.Fatalf("%s: %v", entry.Name(), err)
		}
		loaded++
	}
	fmt.Printf("loaded %d template(s) from %s\n", loaded, dir)
}

func loadOne(
	ctx context.Context, store *db.Store, objects *storage.Client, path, key string,
) error {
	source, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read: %w", err)
	}
	if len(source) > maxTemplateBytes {
		return fmt.Errorf("larger than 1 MB (%d bytes)", len(source))
	}
	// Refused before it is stored, not after it has printed something: OpenSCAD
	// accepts an unknown -D silently, so a template missing NAME_L renders a
	// plank with no name and exits 0.
	if missing := personalise.MissingTemplateParams(source); len(missing) > 0 {
		return fmt.Errorf("does not declare %s", strings.Join(missing, ", "))
	}

	fileID := uuid.New()
	storageKey := fmt.Sprintf("templates/%s/%s.scad", key, fileID)
	if err := objects.Put(ctx, storageKey, bytes.NewReader(source),
		int64(len(source)), "application/x-openscad"); err != nil {
		return fmt.Errorf("store file: %w", err)
	}

	var version int32
	err = store.InTx(ctx, func(q *gen.Queries) error {
		asset, err := q.InsertFileAsset(ctx, gen.InsertFileAssetParams{
			ID: fileID, Filename: filepath.Base(path), ContentType: "application/x-openscad",
			SizeBytes: int64(len(source)), StorageKey: storageKey, UploadedBy: uploadedBy,
		})
		if err != nil {
			return err
		}
		next, err := q.NextTemplateVersion(ctx, key)
		if err != nil {
			return err
		}
		// Retire the old one inside the same transaction as inserting the new,
		// or a failure between them leaves a key with no active template and
		// every plank falling back to the embedded shape.
		if err := q.SupersedeTemplate(ctx, key); err != nil {
			return err
		}
		created, err := q.InsertTemplate(ctx, gen.InsertTemplateParams{
			ID: uuid.New(), TemplateKey: key, FileID: asset.ID,
			Version: next, UploadedBy: uploadedBy, Notes: nil,
		})
		version = created.Version
		return err
	})
	if err != nil {
		return fmt.Errorf("record: %w", err)
	}

	fmt.Printf("  %-22s v%d  %6d bytes\n", key, version, len(source))
	return nil
}
