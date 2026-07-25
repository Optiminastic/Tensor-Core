// Package storage wraps the S3-compatible object store (MinIO in dev) where the
// pipeline keeps uploaded STL files and the G-code the slicer produces. Handlers
// use it to stream an upload straight to storage without buffering it in memory.
package storage

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

// responseHeaderTimeout bounds how long an operation waits for the store to
// start responding. Without it, a stalled MinIO could hang a request for as long
// as its context allows; the full transfer is still bounded by the caller's ctx.
const responseHeaderTimeout = 30 * time.Second

// Client is a bucket-bound object store handle.
type Client struct {
	mc     *minio.Client
	bucket string
}

// New builds the client and ensures the bucket exists (created on first run).
func New(ctx context.Context, endpoint, accessKey, secretKey, bucket string, secure bool) (*Client, error) {
	transport, err := minio.DefaultTransport(secure)
	if err != nil {
		return nil, fmt.Errorf("minio transport: %w", err)
	}
	transport.ResponseHeaderTimeout = responseHeaderTimeout

	mc, err := minio.New(endpoint, &minio.Options{
		Creds:     credentials.NewStaticV4(accessKey, secretKey, ""),
		Secure:    secure,
		Transport: transport,
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

// Download fetches the object at key to a local file (used by the slice worker).
func (c *Client) Download(ctx context.Context, key, destPath string) error {
	if err := c.mc.FGetObject(ctx, c.bucket, key, destPath, minio.GetObjectOptions{}); err != nil {
		return fmt.Errorf("minio download %s: %w", key, err)
	}
	return nil
}

// Upload stores a local file at the given object key (used by the slice worker).
func (c *Client) Upload(ctx context.Context, key, srcPath, contentType string) error {
	_, err := c.mc.FPutObject(ctx, c.bucket, key, srcPath, minio.PutObjectOptions{ContentType: contentType})
	if err != nil {
		return fmt.Errorf("minio upload %s: %w", key, err)
	}
	return nil
}

// Object is a readable object plus the metadata a handler needs to stream it to
// a client (Content-Length and Content-Type).
type Object struct {
	Body        io.ReadCloser
	Size        int64
	ContentType string
}

// Get opens the object at key for streaming. The caller must Close Body. A
// missing key surfaces as an error for which IsNotFound reports true, so a
// handler can answer 404 rather than 500.
func (c *Client) Get(ctx context.Context, key string) (*Object, error) {
	obj, err := c.mc.GetObject(ctx, c.bucket, key, minio.GetObjectOptions{})
	if err != nil {
		return nil, fmt.Errorf("minio get %s: %w", key, err)
	}
	// GetObject is lazy; Stat is the first call that actually reaches the store,
	// so it is where a missing object is reported.
	info, err := obj.Stat()
	if err != nil {
		_ = obj.Close()
		return nil, fmt.Errorf("minio stat %s: %w", key, err)
	}
	return &Object{Body: obj, Size: info.Size, ContentType: info.ContentType}, nil
}

// IsNotFound reports whether err is MinIO's "object does not exist" error. It
// uses errors.As so it works through any depth of wrapping (Get/Stat wrap the
// underlying minio.ErrorResponse with fmt.Errorf), unlike a single Unwrap.
func IsNotFound(err error) bool {
	var resp minio.ErrorResponse
	if errors.As(err, &resp) {
		return resp.Code == "NoSuchKey"
	}
	return false
}
