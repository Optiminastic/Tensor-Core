// Package config loads the backend's settings from the environment, mirroring
// app/core/config.py. Defaults match the Python Settings so behaviour is
// identical when a variable is unset.
package config

import (
	"encoding/json"
	"os"
	"strconv"
	"strings"
)

// Settings holds every environment-driven value the backend needs.
type Settings struct {
	Environment       string
	DatabaseURL       string
	AuthJWKSURL       string
	AuthIssuer        string
	AuthAudience      string
	InternalAPISecret string
	JWKSCacheSeconds  int
	CORSOrigins       []string

	// Design pipeline: object storage for STL/G-code and the slicer dispatcher.
	MinIOEndpoint     string
	MinIOAccessKey    string
	MinIOSecretKey    string
	MinIOBucket       string
	MinIOSecure       bool
	SlicerDispatchURL string
}

// Load reads settings from the process environment. It never fails: missing
// values fall back to the same defaults as the Python backend, and the guards
// that need a value (JWKS URL, issuer, internal secret) fail closed at use.
func Load() Settings {
	return Settings{
		Environment:       envOr("ENVIRONMENT", "development"),
		DatabaseURL:       normaliseDSN(os.Getenv("DATABASE_URL")),
		AuthJWKSURL:       os.Getenv("AUTH_JWKS_URL"),
		AuthIssuer:        os.Getenv("AUTH_ISSUER"),
		AuthAudience:      envOr("AUTH_AUDIENCE", "tensor-core"),
		InternalAPISecret: os.Getenv("INTERNAL_API_SECRET"),
		JWKSCacheSeconds:  intEnvOr("JWKS_CACHE_SECONDS", 300),
		CORSOrigins:       corsOrigins(envOr("CORS_ORIGINS", `["http://localhost:3000"]`)),

		MinIOEndpoint:     envOr("MINIO_ENDPOINT", "localhost:9100"),
		MinIOAccessKey:    envOr("MINIO_ACCESS_KEY", "tensor"),
		MinIOSecretKey:    envOr("MINIO_SECRET_KEY", "tensor_local_dev"),
		MinIOBucket:       envOr("MINIO_BUCKET", "designs"),
		MinIOSecure:       boolEnvOr("MINIO_SECURE", false),
		SlicerDispatchURL: envOr("SLICER_DISPATCH_URL", "http://localhost:8090"),
	}
}

// boolEnvOr parses a boolean env var, accepting the common truthy spellings.
func boolEnvOr(key string, fallback bool) bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv(key)))
	switch v {
	case "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	default:
		return fallback
	}
}

// normaliseDSN strips a SQLAlchemy driver suffix so pgx accepts the URL:
// "postgresql+psycopg://..." becomes "postgresql://...". The frontend and the
// Python backend share a DATABASE_URL in the +psycopg form; pgx and goose want
// it without the driver.
func normaliseDSN(raw string) string {
	scheme := strings.Index(raw, "://")
	plus := strings.Index(raw, "+")
	if scheme > 0 && plus > 0 && plus < scheme {
		return raw[:plus] + raw[scheme:]
	}
	return raw
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func intEnvOr(key string, fallback int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return fallback
}

// corsOrigins parses CORS_ORIGINS. pydantic-settings accepts a JSON array; we
// accept that first, then fall back to a comma-separated list, so either form in
// .env works.
func corsOrigins(raw string) []string {
	raw = strings.TrimSpace(raw)
	if strings.HasPrefix(raw, "[") {
		var list []string
		if err := json.Unmarshal([]byte(raw), &list); err == nil {
			return list
		}
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
