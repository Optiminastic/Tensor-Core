// Package obs is the backend's observability layer: a structured slog logger
// and helpers to carry a request-scoped logger through context.
//
// The rest of the codebase logs through a logger pulled from the request
// context (obs.FromContext), so every line carries the request id set by the
// HTTP middleware. Boot-time code uses the logger returned by New directly.
package obs

import (
	"context"
	"log/slog"
	"os"
	"strings"
)

// ctxKey is unexported so only this package can set or read the context logger.
type ctxKey struct{}

var loggerKey = ctxKey{}

// New builds the process logger: a JSON handler writing to stderr at the given
// level. Unknown or empty levels default to info. The returned logger is also
// installed as slog's default so stray slog.Info calls stay structured.
func New(level string) *slog.Logger {
	handler := slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: parseLevel(level)})
	logger := slog.New(handler)
	slog.SetDefault(logger)
	return logger
}

// parseLevel maps a config string to a slog level, defaulting to info.
func parseLevel(level string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(level)) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

// WithLogger returns a copy of ctx carrying l, so downstream code can log with
// the request-scoped fields already attached.
func WithLogger(ctx context.Context, l *slog.Logger) context.Context {
	return context.WithValue(ctx, loggerKey, l)
}

// FromContext returns the logger stored on ctx, or slog's default when none is
// present (boot code, tests). It never returns nil.
func FromContext(ctx context.Context) *slog.Logger {
	if l, ok := ctx.Value(loggerKey).(*slog.Logger); ok && l != nil {
		return l
	}
	return slog.Default()
}
