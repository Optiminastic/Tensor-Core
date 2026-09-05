package main

import (
	"bytes"
	"context"
	"fmt"
	"os"

	"github.com/joho/godotenv"

	"github.com/Optiminastic/tensor-core/internal/config"
	"github.com/Optiminastic/tensor-core/internal/storage"
)

func main() {
	_ = godotenv.Load("env/local.env")
	cfg := config.Load()
	ctx := context.Background()
	st, err := storage.New(ctx, storage.Options{
		Endpoint: cfg.S3Endpoint, AccessKey: cfg.S3AccessKey, SecretKey: cfg.S3SecretKey,
		Bucket: cfg.S3Bucket, KeyPrefix: cfg.S3KeyPrefix, Secure: cfg.S3Secure,
		AssumeBucketExists: cfg.S3AssumeBucketExists,
	})
	if err != nil {
		panic(err)
	}
	obj, err := st.Get(ctx, os.Args[1])
	if err != nil {
		panic(err)
	}
	defer func() { _ = obj.Body.Close() }()
	buf := &bytes.Buffer{}
	_, _ = buf.ReadFrom(obj.Body)
	if err := os.WriteFile(os.Args[2], buf.Bytes(), 0o600); err != nil {
		panic(err)
	}
	fmt.Printf("saved %d bytes to %s\n", buf.Len(), os.Args[2])
}
