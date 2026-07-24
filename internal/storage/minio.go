// Package storage wraps the S3-compatible object store (MinIO in dev) where the
// pipeline keeps uploaded STL files and the G-code the slicer produces. Handlers
// use it to stream an upload straight to storage without buffering it in memory.
package storage

import (
	"context"
	"fmt"
	"io"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

// Client is a bucket-bound object store handle.
type Client struct {
	mc     *minio.Client
	bucket string
}

// New builds the client and ensures the bucket exists (created on first run).
func New(ctx context.Context, endpoint, accessKey, secretKey, bucket string, secure bool) (*Client, error) {
	mc, err := minio.New(endpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(accessKey, secretKey, ""),
		Secure: secure,
	})
	if err != nil {
		return nil, fmt.Errorf("minio: %w", err)
	}
	exists, err := mc.BucketExists(ctx, bucket)
	if err != nil {
		return nil, fmt.Errorf("minio bucket check: %w", err)
	}
	if !exists {
		if err := mc.MakeBucket(ctx, bucket, minio.MakeBucketOptions{}); err != nil {
			return nil, fmt.Errorf("minio make bucket: %w", err)
		}
	}
	return &Client{mc: mc, bucket: bucket}, nil
}

// Put streams size bytes from r to the given object key.
func (c *Client) Put(ctx context.Context, key string, r io.Reader, size int64, contentType string) error {
	_, err := c.mc.PutObject(ctx, c.bucket, key, r, size, minio.PutObjectOptions{ContentType: contentType})
	if err != nil {
		return fmt.Errorf("minio put %s: %w", key, err)
	}
	return nil
}
