// Package storage wraps the S3-compatible object store (MinIO in dev) where the
// pipeline keeps uploaded STL files and the G-code the slicer produces. Handlers
// use it to stream an upload straight to storage without buffering it in memory.
package storage

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

// responseHeaderTimeout bounds how long an operation waits for the store to
// start responding. Without it, a stalled MinIO could hang a request for as long
// as its context allows; the full transfer is still bounded by the caller's ctx.
const responseHeaderTimeout = 30 * time.Second

// Config is everything New needs to reach the object store. It replaces a long
// positional argument list so call sites read clearly and new options (Prefix,
// AutoCreate, Region) do not keep widening the signature.
type Config struct {
	Endpoint  string // host[:port], e.g. "localhost:9100" or "s3.ap-south-1.amazonaws.com"
	Region    string // optional; for AWS it is otherwise derived from the endpoint host
	AccessKey string
	SecretKey string
	Bucket    string
	// Prefix is prepended to every object key, so a shared bucket can be
	// partitioned per project (e.g. "Tensor/"). The keys stored in the database
	// stay bare; the prefix is applied only at the storage boundary. Empty means
	// no prefix (the dev MinIO layout).
	Prefix string
	Secure bool
	// AutoCreate makes New ensure the bucket exists (HeadBucket + MakeBucket on
	// first run). Correct for the dev MinIO where the app owns the bucket; must be
	// false against a shared, externally-managed bucket where the app's IAM user
	// is prefix-scoped and may not HeadBucket or CreateBucket at all.
	AutoCreate bool
}

// Client is a bucket-bound object store handle. Every key it reads or writes is
// transparently prefixed with cfg.Prefix.
type Client struct {
	mc     *minio.Client
	bucket string
	prefix string
}

// New builds the client. When cfg.AutoCreate is set it also ensures the bucket
// exists (created on first run) - only safe when the app owns the bucket.
func New(ctx context.Context, cfg Config) (*Client, error) {
	transport, err := minio.DefaultTransport(cfg.Secure)
	if err != nil {
		return nil, fmt.Errorf("minio transport: %w", err)
	}
	transport.ResponseHeaderTimeout = responseHeaderTimeout

	mc, err := minio.New(cfg.Endpoint, &minio.Options{
		Creds:     credentials.NewStaticV4(cfg.AccessKey, cfg.SecretKey, ""),
		Secure:    cfg.Secure,
		Region:    cfg.Region,
		Transport: transport,
	})
	if err != nil {
		return nil, fmt.Errorf("minio: %w", err)
	}
	if cfg.AutoCreate {
		exists, err := mc.BucketExists(ctx, cfg.Bucket)
		if err != nil {
			return nil, fmt.Errorf("minio bucket check: %w", err)
		}
		if !exists {
			if err := mc.MakeBucket(ctx, cfg.Bucket, minio.MakeBucketOptions{}); err != nil {
				return nil, fmt.Errorf("minio make bucket: %w", err)
			}
		}
	}
	return &Client{mc: mc, bucket: cfg.Bucket, prefix: normalisePrefix(cfg.Prefix)}, nil
}

// normalisePrefix returns the prefix with exactly one trailing slash (or empty),
// so joining with a bare key never produces "" or a doubled slash.
func normalisePrefix(p string) string {
	p = strings.Trim(p, "/")
	if p == "" {
		return ""
	}
	return p + "/"
}

// key applies the configured prefix to a bare object key.
func (c *Client) key(k string) string {
	return c.prefix + k
}

// Put streams size bytes from r to the given object key.
func (c *Client) Put(ctx context.Context, key string, r io.Reader, size int64, contentType string) error {
	_, err := c.mc.PutObject(ctx, c.bucket, c.key(key), r, size, minio.PutObjectOptions{ContentType: contentType})
	if err != nil {
		return fmt.Errorf("minio put %s: %w", key, err)
	}
	return nil
}

// Download fetches the object at key to a local file (used by the slice worker).
func (c *Client) Download(ctx context.Context, key, destPath string) error {
	if err := c.mc.FGetObject(ctx, c.bucket, c.key(key), destPath, minio.GetObjectOptions{}); err != nil {
		return fmt.Errorf("minio download %s: %w", key, err)
	}
	return nil
}

// Upload stores a local file at the given object key (used by the slice worker).
func (c *Client) Upload(ctx context.Context, key, srcPath, contentType string) error {
	_, err := c.mc.FPutObject(ctx, c.bucket, c.key(key), srcPath, minio.PutObjectOptions{ContentType: contentType})
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
	obj, err := c.mc.GetObject(ctx, c.bucket, c.key(key), minio.GetObjectOptions{})
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
