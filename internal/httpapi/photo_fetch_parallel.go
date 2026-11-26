package httpapi

// Fast photo downloads for the custom photo sheets.
//
// The customer photos live on DigitalOcean Spaces in New York. From the shop
// every connection to it runs at about 40 KB/s, whatever the line can do, so a
// 3.5 MB phone photo took 90 seconds and a ten-photo order went past every
// timeout. The limit is per connection - ten at once each still get 40 KB/s -
// so a photo is fetched as 512 KB byte ranges side by side, and many photos at
// once, under one cap on connections.
//
// The bytes are kept for an hour: the preview downloads every photo, and the
// print PDF asked for right after it must not download them all again.

import (
	"context"
	"errors"
	"fmt"
	"image"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/Optiminastic/tensor-core/internal/photoframe"
)

const (
	photoChunkBytes = 512 << 10
	// photoConnections caps the connections open to the photo host at once,
	// across every photo of a run.
	photoConnections = 24
	photoCacheTTL    = time.Hour
	photoCacheBytes  = 512 << 20
)

var photoConnSlots = make(chan struct{}, photoConnections)

// photoCache holds downloaded photo files by URL.
var photoCache = &bytesCache{entries: map[string]cacheEntry{}}

type cacheEntry struct {
	data []byte
	at   time.Time
}

type bytesCache struct {
	mu      sync.Mutex
	entries map[string]cacheEntry
	size    int
}

func (c *bytesCache) get(key string) ([]byte, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[key]
	if !ok || time.Since(e.at) > photoCacheTTL {
		return nil, false
	}
	return e.data, true
}

// put stores data, first dropping expired entries and then the oldest until
// it fits under the cap.
func (c *bytesCache) put(key string, data []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if old, ok := c.entries[key]; ok {
		c.size -= len(old.data)
		delete(c.entries, key)
	}
	for k, e := range c.entries {
		if time.Since(e.at) > photoCacheTTL {
			c.size -= len(e.data)
			delete(c.entries, k)
		}
	}
	for c.size+len(data) > photoCacheBytes && len(c.entries) > 0 {
		oldest := ""
		for k, e := range c.entries {
			if oldest == "" || e.at.Before(c.entries[oldest].at) {
				oldest = k
			}
		}
		c.size -= len(c.entries[oldest].data)
		delete(c.entries, oldest)
	}
	c.entries[key] = cacheEntry{data: data, at: time.Now()}
	c.size += len(data)
}

// fetchPhotoFast is fetchPhoto through the cache and parallel byte ranges.
func fetchPhotoFast(ctx context.Context, raw string) (image.Image, error) {
	if data, ok := photoCache.get(raw); ok {
		return photoframe.DecodePhoto(data)
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host == "" {
		return nil, errors.New("the photo link is not an https URL")
	}
	data, err := downloadRanged(ctx, u.String())
	if err != nil {
		return nil, err
	}
	img, err := photoframe.DecodePhoto(data)
	if err == nil {
		photoCache.put(raw, data)
	}
	return img, err
}

// downloadRanged asks for the first chunk; a 206 says how big the file is and
// the rest is fetched in parallel, a 200 is the whole file in one go.
func downloadRanged(ctx context.Context, u string) ([]byte, error) {
	first, total, err := getRange(ctx, u, 0, photoChunkBytes-1)
	if err != nil {
		return nil, err
	}
	if total <= len(first) {
		return first, nil
	}
	if total > maxPhotoBytes {
		return nil, errors.New("the photo is larger than 25 MB")
	}
	out := make([]byte, total)
	copy(out, first)
	var g errgroup.Group
	for start := len(first); start < total; start += photoChunkBytes {
		end := min(start+photoChunkBytes, total) - 1
		g.Go(func() error {
			part, _, err := getRange(ctx, u, start, end)
			if err == nil && len(part) != end-start+1 {
				err = errors.New("the photo download was cut short")
			}
			if err == nil {
				copy(out[start:], part)
			}
			return err
		})
	}
	if err := g.Wait(); err != nil {
		return nil, err
	}
	return out, nil
}

// getRange fetches bytes start..end, retrying once. total is the whole
// file's size: from Content-Range on a 206, the body's length on a 200.
func getRange(ctx context.Context, u string, start, end int) ([]byte, int, error) {
	var lastErr error
	for attempt := 0; attempt < 2 && ctx.Err() == nil; attempt++ {
		data, total, err := getRangeOnce(ctx, u, start, end)
		if err == nil {
			return data, total, nil
		}
		lastErr = err
	}
	if lastErr == nil {
		lastErr = ctx.Err()
	}
	return nil, 0, lastErr
}

func getRangeOnce(ctx context.Context, u string, start, end int) ([]byte, int, error) {
	select {
	case photoConnSlots <- struct{}{}:
	case <-ctx.Done():
		return nil, 0, ctx.Err()
	}
	defer func() { <-photoConnSlots }()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", start, end))
	resp, err := sheetPhotoClient.Do(req)
	if err != nil {
		return nil, 0, errors.New("the photo could not be downloaded")
	}
	defer func() { _ = resp.Body.Close() }()
	switch resp.StatusCode {
	case http.StatusPartialContent, http.StatusOK:
	default:
		return nil, 0, fmt.Errorf("the photo link answered %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxPhotoBytes+1))
	if err != nil {
		return nil, 0, errors.New("the photo could not be downloaded")
	}
	if resp.StatusCode == http.StatusOK {
		if start > 0 {
			return nil, 0, errors.New("the photo host ignored the byte range")
		}
		return data, len(data), nil
	}
	return data, contentRangeTotal(resp.Header.Get("Content-Range"), len(data)), nil
}

// contentRangeTotal reads the size from "bytes 0-524287/3555160"; fallback
// when it is missing or "*".
func contentRangeTotal(h string, fallback int) int {
	i := strings.LastIndex(h, "/")
	if i < 0 {
		return fallback
	}
	n, err := strconv.Atoi(strings.TrimSpace(h[i+1:]))
	if err != nil {
		return fallback
	}
	return n
}
