package httpapi

// Uploaded design files, kept in memory once read.
//
// Every read of a template went to object storage: the registry's variable
// list (GET /registry/templates/:key/params), the self-colour check, and every
// job render. Each was a fresh S3 download, so opening a product's mapping or
// assigning a file took a second or more per call, and a render paid the same
// before OpenSCAD even started.
//
// Keyed by the object's STORAGE KEY, not the template key. An upload never
// overwrites: each version is a new object under a new key, and the active
// version is looked up in the database on every read. So a cached source can
// never go stale - replacing a file changes the key, and the old entry is
// simply never asked for again.
//
// Bounded by total size and cleared outright past it rather than swept: the
// registry holds a few dozen files of tens to hundreds of kilobytes.

import "sync"

const templateSourceCacheMaxBytes = 64 << 20

type templateSourceCache struct {
	mu      sync.Mutex
	bytes   int
	entries map[string][]byte
}

var templateSources = &templateSourceCache{entries: map[string][]byte{}}

func (c *templateSourceCache) get(storageKey string) ([]byte, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	src, ok := c.entries[storageKey]
	return src, ok
}

func (c *templateSourceCache) put(storageKey string, source []byte) {
	if len(source) > templateSourceCacheMaxBytes {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.entries[storageKey]; ok {
		return
	}
	if c.bytes+len(source) > templateSourceCacheMaxBytes {
		c.entries, c.bytes = map[string][]byte{}, 0
	}
	c.entries[storageKey] = source
	c.bytes += len(source)
}
