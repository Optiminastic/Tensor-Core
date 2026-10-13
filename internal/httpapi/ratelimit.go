package httpapi

// A bucket per caller, for the one group of routes a stranger can reach.
//
// Tensor-Core has never had rate limiting, and for every other route that was
// defensible: they all require a bearer token minted for a member of staff, so
// the worst case is an employee hammering their own dashboard. The
// personalisation link is the first address anybody on the internet can call,
// and it reads a database and streams a model file.
//
// DELIBERATELY SMALL, and in-process rather than in Redis. This is not a
// defence against a distributed flood - that belongs at the edge, and the
// proxy in front of this service is where it should eventually live. It
// defends against the two things that are actually likely: a script walking
// tokens, and one customer's page getting stuck in a polling loop. Both come
// from one address and both are stopped by a counter in memory.
//
// Per-process, so N replicas allow N times the rate. Named here rather than
// hidden, because the alternative - a shared store - is a dependency this
// service does not otherwise need, and the limit would still be approximate.

import (
	"net/http"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

const (
	// publicRateWindow and publicRateBurst allow a page that polls its preview
	// every couple of seconds, plus the model fetches, with room to spare -
	// and stop a loop or a scan well before either costs anything.
	publicRateWindow = time.Minute
	publicRateBurst  = 120
	// publicRateMaxKeys bounds the memory a scan can make this costs. Past it
	// the whole table is dropped rather than grown: the next window rebuilds
	// it, and an attacker who can afford to cycle through 50k addresses is
	// past what an in-process counter was ever going to stop.
	publicRateMaxKeys = 50_000
)

type rateBucket struct {
	count   int
	resetAt time.Time
}

type rateLimiter struct {
	mu      sync.Mutex
	window  time.Duration
	burst   int
	buckets map[string]*rateBucket
}

func newRateLimiter(window time.Duration, burst int) *rateLimiter {
	return &rateLimiter{
		window: window, burst: burst,
		buckets: make(map[string]*rateBucket),
	}
}

// allow reports whether this key may make one more request now.
func (l *rateLimiter) allow(key string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()

	if len(l.buckets) > publicRateMaxKeys {
		l.buckets = make(map[string]*rateBucket)
	}

	b, ok := l.buckets[key]
	if !ok || now.After(b.resetAt) {
		l.buckets[key] = &rateBucket{count: 1, resetAt: now.Add(l.window)}
		return true
	}
	if b.count >= l.burst {
		return false
	}
	b.count++
	return true
}

// publicRateLimit limits by client address.
//
// By ADDRESS, not by token. Limiting per token would let a scan through a
// million tokens past untouched, since each guess is a different bucket - and
// that scan is the attack this exists to make pointless.
func (s *Server) publicRateLimit() gin.HandlerFunc {
	limiter := newRateLimiter(publicRateWindow, publicRateBurst)
	return func(c *gin.Context) {
		if !limiter.allow(c.ClientIP(), time.Now()) {
			// Retry-After, because the customer whose page is polling should
			// be told when to come back rather than shown a dead end.
			c.Header("Retry-After", "60")
			c.AbortWithStatusJSON(http.StatusTooManyRequests,
				gin.H{"detail": "Too many requests. Please wait a moment and try again."})
			return
		}
		c.Next()
	}
}
