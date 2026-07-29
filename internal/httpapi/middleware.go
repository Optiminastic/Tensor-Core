package httpapi

import (
	"log/slog"
	"net/http"
	"runtime/debug"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/Optiminastic/tensor-core/internal/obs"
)

// requestIDHeader is the inbound/outbound correlation header. An upstream proxy
// may set it; otherwise we mint one so every request is traceable end to end.
const requestIDHeader = "X-Request-Id"

// requestContext assigns each request a correlation id and a request-scoped
// logger, stashes the logger on the request context so handlers and DB/outbound
// calls log with the same id, echoes the id on the response, and emits one
// structured access line when the request completes.
func (s *Server) requestContext() gin.HandlerFunc {
	return func(c *gin.Context) {
		// A client-supplied id is echoed and logged, so reject an over-long or
		// non-token value and generate our own instead.
		reqID := c.GetHeader(requestIDHeader)
		if !validRequestID(reqID) {
			reqID = uuid.NewString()
		}
		c.Header(requestIDHeader, reqID)

		logger := s.baseLogger().With(
			slog.String("request_id", reqID),
			slog.String("method", c.Request.Method),
			slog.String("path", c.Request.URL.Path),
		)
		c.Request = c.Request.WithContext(obs.WithLogger(c.Request.Context(), logger))

		start := time.Now()
		c.Next()

		logger.Info("request",
			slog.Int("status", c.Writer.Status()),
			slog.Duration("latency", time.Since(start)),
		)
	}
}

const maxRequestIDLen = 128

// validRequestID accepts a non-empty, bounded token of URL-safe characters, so a
// client cannot inject arbitrary or oversized content into the response header
// and every log line for the request.
func validRequestID(id string) bool {
	if id == "" || len(id) > maxRequestIDLen {
		return false
	}
	for _, r := range id {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
		default:
			return false
		}
	}
	return true
}

// baseLogger returns the server's logger, falling back to slog's default when
// one was not injected (older wiring, tests).
func (s *Server) baseLogger() *slog.Logger {
	if s.logger != nil {
		return s.logger
	}
	return slog.Default()
}

// recoverPanic replaces gin.Recovery: it recovers from a handler panic, logs
// the cause and stack with the request id, and responds with the standard
// {"detail"} error body. gin.Recovery aborts with an empty body, which would
// break the frontend's Zod validators that always read body.detail.
func (s *Server) recoverPanic() gin.HandlerFunc {
	return func(c *gin.Context) {
		defer func() {
			if r := recover(); r != nil {
				obs.FromContext(c.Request.Context()).Error("panic recovered",
					slog.Any("panic", r),
					slog.String("stack", string(debug.Stack())),
				)
				// The response may have already started; only write if not.
				if !c.Writer.Written() {
					detail(c, http.StatusInternalServerError, "An unexpected error occurred.")
				}
				c.Abort()
			}
		}()
		c.Next()
	}
}
