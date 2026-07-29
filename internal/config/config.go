// Package config loads the backend's settings from the environment, mirroring
// app/core/config.py. Defaults match the Python Settings so behaviour is
// identical when a variable is unset.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// minIODevSecret is the built-in development MinIO secret. Running with it in
// production is a misconfiguration, so Validate rejects it there.
const minIODevSecret = "tensor_local_dev"

// Settings holds every environment-driven value the backend needs.
type Settings struct {
	Environment       string
	Port              string
	LogLevel          string
	DatabaseURL       string
	AuthJWKSURL       string
	AuthIssuer        string
	AuthAudience      string
	InternalAPISecret string
	JWKSCacheSeconds  int
	CORSOrigins       []string

	// PermissionFreshnessTTL bounds how long a revoked/changed permission can
	// linger in an already-minted token before the guard layer rejects it. Zero
	// disables the per-request freshness check (tokens then remain valid until
	// they expire, the previous behaviour).
	PermissionFreshnessTTL time.Duration

	// HTTP server timeouts. ReadTimeout is deliberately omitted: the design
	// upload path streams large multipart bodies and a short read timeout would
	// truncate it. Query latency is bounded by the DB statement timeout instead.
	HTTPReadHeaderTimeout time.Duration
	HTTPWriteTimeout      time.Duration
	HTTPIdleTimeout       time.Duration
	ShutdownTimeout       time.Duration

	// Connection pool tuning and a per-statement timeout applied to every query
	// on the pool. Zero values fall back to pgx defaults / no statement timeout.
	DBMaxConns         int32
	DBMinConns         int32
	DBMaxConnLifetime  time.Duration
	DBMaxConnIdleTime  time.Duration
	DBHealthCheck      time.Duration
	DBConnectTimeout   time.Duration
	DBStatementTimeout time.Duration

	// Design pipeline: object storage for STL/G-code.
	MinIOEndpoint  string
	MinIOAccessKey string
	MinIOSecretKey string
	MinIOBucket    string
	MinIOSecure    bool

	// Slice worker (cmd/sliceworker): the Bambu Studio install, per-slice timeout,
	// the calibratable average printer draw used to estimate energy, and how many
	// slices run at once.
	BambuRoot           string
	SliceTimeoutSeconds int
	PrinterAvgPowerKW   float64
	SliceConcurrency    int

	// Orientation analysis (advisory least-support recommendation): the
	// self-support overhang limit in degrees, and a cap on mesh triangles scored
	// so very large models stay fast.
	OrientationOverhangDeg  float64
	OrientationMaxTriangles int

	// Shopify Admin API version used when publishing an approved design, and the
	// per-request timeout for the outbound Admin API calls.
	ShopifyAPIVersion string
	ShopifyTimeout    time.Duration
}

// Load reads settings from the process environment. It never fails: missing
// values fall back to the same defaults as the Python backend, and the guards
// that need a value (JWKS URL, issuer, internal secret) fail closed at use.
func Load() Settings {
	return Settings{
		Environment:       envOr("ENVIRONMENT", "development"),
		Port:              envOr("PORT", "8001"),
		LogLevel:          envOr("LOG_LEVEL", "info"),
		DatabaseURL:       normaliseDSN(os.Getenv("DATABASE_URL")),
		AuthJWKSURL:       os.Getenv("AUTH_JWKS_URL"),
		AuthIssuer:        os.Getenv("AUTH_ISSUER"),
		AuthAudience:      envOr("AUTH_AUDIENCE", "tensor-core"),
		InternalAPISecret: os.Getenv("INTERNAL_API_SECRET"),
		JWKSCacheSeconds:  intEnvOr("JWKS_CACHE_SECONDS", 300),
		CORSOrigins:       corsOrigins(envOr("CORS_ORIGINS", `["http://localhost:3000"]`)),

		// A revoked permission takes effect within this window instead of lingering
		// until the token expires. Safe with the current frontend: Better Auth's
		// getToken re-mints a fresh token (running definePayload -> fetchUserAuthz)
		// on every server-action call, so a legitimate request always carries the
		// current version and the 401 only rejects a genuinely stale token. Set 0
		// to disable.
		PermissionFreshnessTTL: secondsEnvOr("PERMISSION_FRESHNESS_TTL_SECONDS", 30),

		HTTPReadHeaderTimeout: secondsEnvOr("HTTP_READ_HEADER_TIMEOUT_SECONDS", 10),
		HTTPWriteTimeout:      secondsEnvOr("HTTP_WRITE_TIMEOUT_SECONDS", 60),
		HTTPIdleTimeout:       secondsEnvOr("HTTP_IDLE_TIMEOUT_SECONDS", 120),
		ShutdownTimeout:       secondsEnvOr("HTTP_SHUTDOWN_TIMEOUT_SECONDS", 10),

		DBMaxConns:         int32(intEnvOr("DB_MAX_CONNS", 10)),
		DBMinConns:         int32(intEnvOr("DB_MIN_CONNS", 0)),
		DBMaxConnLifetime:  secondsEnvOr("DB_MAX_CONN_LIFETIME_SECONDS", 3600),
		DBMaxConnIdleTime:  secondsEnvOr("DB_MAX_CONN_IDLE_SECONDS", 1800),
		DBHealthCheck:      secondsEnvOr("DB_HEALTHCHECK_SECONDS", 60),
		DBConnectTimeout:   secondsEnvOr("DB_CONNECT_TIMEOUT_SECONDS", 10),
		DBStatementTimeout: secondsEnvOr("DB_STATEMENT_TIMEOUT_SECONDS", 15),

		MinIOEndpoint:  envOr("MINIO_ENDPOINT", "localhost:9100"),
		MinIOAccessKey: envOr("MINIO_ACCESS_KEY", "tensor"),
		MinIOSecretKey: envOr("MINIO_SECRET_KEY", "tensor_local_dev"),
		MinIOBucket:    envOr("MINIO_BUCKET", "designs"),
		MinIOSecure:    boolEnvOr("MINIO_SECURE", false),

		BambuRoot:           envOr("BAMBU_ROOT", "/opt/bambu/squashfs-root"),
		SliceTimeoutSeconds: intEnvOr("SLICE_TIMEOUT_SECONDS", 300),
		PrinterAvgPowerKW:   floatEnvOr("PRINTER_AVG_POWER_KW", 0.11),
		SliceConcurrency:    intEnvOr("SLICE_CONCURRENCY", 2),

		OrientationOverhangDeg:  floatEnvOr("ORIENTATION_OVERHANG_DEG", 45),
		OrientationMaxTriangles: intEnvOr("ORIENTATION_MAX_TRIANGLES", 500_000),

		ShopifyAPIVersion: envOr("SHOPIFY_API_VERSION", "2024-10"),
		ShopifyTimeout:    secondsEnvOr("SHOPIFY_TIMEOUT_SECONDS", 15),
	}
}

// IsProduction reports whether the service is running outside development, where
// missing secrets must fail fast rather than silently fall back to dev defaults.
func (s Settings) IsProduction() bool {
	return !strings.EqualFold(strings.TrimSpace(s.Environment), "development")
}

// Validate fails closed on a misconfigured production deploy. In development it
// is a no-op so local runs keep working with the built-in defaults. It is kept
// separate from Load so Load itself never fails (mirroring the Python Settings).
func (s Settings) Validate() error {
	if !s.IsProduction() {
		return nil
	}
	var missing []string
	require := func(name, value string) {
		if strings.TrimSpace(value) == "" {
			missing = append(missing, name)
		}
	}
	require("AUTH_JWKS_URL", s.AuthJWKSURL)
	require("AUTH_ISSUER", s.AuthIssuer)
	require("INTERNAL_API_SECRET", s.InternalAPISecret)
	require("DATABASE_URL", s.DatabaseURL)
	if len(missing) > 0 {
		return fmt.Errorf("config: required in production but unset: %s", strings.Join(missing, ", "))
	}
	if s.MinIOSecretKey == minIODevSecret {
		return errors.New("config: MINIO_SECRET_KEY is the development default in production; set a real secret")
	}
	return nil
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

// secondsEnvOr reads an integer number of seconds and returns it as a Duration,
// falling back to the given default (also in seconds).
func secondsEnvOr(key string, fallbackSeconds int) time.Duration {
	return time.Duration(intEnvOr(key, fallbackSeconds)) * time.Second
}

func floatEnvOr(key string, fallback float64) float64 {
	if v := os.Getenv(key); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			return f
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
