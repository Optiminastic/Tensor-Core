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

	// Design pipeline: object storage for STL/G-code. The client is S3-compatible
	// (MinIO in dev, AWS S3 in shared/prod deployments).
	MinIOEndpoint  string
	MinIORegion    string
	MinIOAccessKey string
	MinIOSecretKey string
	MinIOBucket    string
	// MinIOPrefix partitions a shared bucket per project (e.g. "Tensor/"). Empty in
	// dev, where MinIO owns the whole bucket. Object keys in the DB stay bare.
	MinIOPrefix string
	MinIOSecure bool
	// MinIOAutoCreate ensures the bucket exists on startup. True for dev MinIO;
	// must be false against a shared bucket whose IAM user is prefix-scoped and
	// cannot HeadBucket/CreateBucket at the bucket root.
	MinIOAutoCreate bool

	// Slice worker (cmd/sliceworker): the Bambu Studio install, per-slice timeout,
	// the calibratable average printer draw used to estimate energy, and how many
	// slices run at once.
	BambuRoot           string
	SliceTimeoutSeconds int
	PrinterAvgPowerKW   float64
	SliceConcurrency    int

	// Production pipeline (cmd/productionworker). ProductionConcurrency is how
	// many orders are turned into jobs at once. BatchPlanDebounce collapses a
	// burst of triggers - a rush of orders landing together - into a single
	// replan, and doubles as the period of the safety-net tick that reconsiders
	// batchable jobs even if every event trigger were lost.
	// RiverQueuePrefix namespaces the River queues this process enqueues to and
	// consumes from. The dev database is shared between developers, so without a
	// prefix every machine running the stack competes for the same queue and a
	// colleague on an older build will claim and discard jobs it does not
	// recognise. Empty (the default) keeps the original queue names.
	RiverQueuePrefix string

	ProductionConcurrency int
	BatchPlanDebounce     time.Duration
	BatchPlanInterval     time.Duration

	// OpenSCADBin is the OpenSCAD executable used to render personalised text into
	// printable geometry (the API's personalise-preview route). Absent/failed
	// OpenSCAD just falls back to the plain model, so this is best-effort.
	OpenSCADBin string

	// Orientation analysis (advisory least-support recommendation): the
	// self-support overhang limit in degrees, and a cap on mesh triangles scored
	// so very large models stay fast.
	OrientationOverhangDeg  float64
	OrientationMaxTriangles int

	// Shopify Admin API version used when publishing an approved design, and the
	// per-request timeout for the outbound Admin API calls.
	ShopifyAPIVersion string
	ShopifyTimeout    time.Duration

	// Shopify order import (inbound): the single app's OAuth credentials, this
	// backend's own reachable URL (for the OAuth redirect + webhook callback), the
	// frontend URL to bounce back to after connecting, and the key used to encrypt
	// each store's access token at rest.
	ShopifyClientID     string
	ShopifyClientSecret string
	PublicBaseURL       string
	FrontendURL         string
	TokenEncryptionKey  string

	// BamBuddy, the printer manager on the shop LAN. BaseURL is the instance root
	// (no /api/v1 suffix); APIKey is a bb_-prefixed key that must carry the
	// can_manage_library, can_queue, can_read_status and can_control_printer
	// scopes - the last defaults to false when a key is created.
	BambuddyBaseURL string
	BambuddyAPIKey  string
	BambuddyTimeout time.Duration
	// BambuddyManualStart stages a print instead of starting it: the plate lands
	// on the printer's queue and waits for a person. Defaults ON - an unattended
	// print from a mis-mapped SKU wastes filament and machine time, and staging
	// costs nothing but a tap.
	BambuddyManualStart bool
	// BambuddyAutoPrint is the kill switch. False stops Tensor pushing anything to
	// BamBuddy at all, without unwinding any other automation.
	BambuddyAutoPrint bool
	// BambuddyPollInterval is how often Tensor asks BamBuddy how live prints are
	// going. Polling (rather than an inbound webhook) is what keeps the shop LAN
	// free of any public endpoint - see internal/httpapi/dispatch_sync.go.
	BambuddyPollInterval time.Duration

	// AI optimization advisor via OpenRouter (an OpenAI-compatible LLM gateway).
	// The key is required to run the advisor; absent, the endpoint fails closed
	// (503). The model slug is configurable so the shop can pick any OpenRouter
	// model (default a capable Claude).
	OpenRouterAPIKey  string
	OpenRouterModel   string
	OpenRouterTimeout time.Duration

	// SMTP for the email-report feature (net/smtp, STARTTLS on 587). Absent
	// host/from means the email endpoint fails closed (503).
	SMTPHost string
	SMTPPort int
	SMTPUser string
	SMTPPass string
	SMTPFrom string
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

		MinIOEndpoint:   envOr("MINIO_ENDPOINT", "localhost:9100"),
		MinIORegion:     os.Getenv("MINIO_REGION"),
		MinIOAccessKey:  envOr("MINIO_ACCESS_KEY", "tensor"),
		MinIOSecretKey:  envOr("MINIO_SECRET_KEY", "tensor_local_dev"),
		MinIOBucket:     envOr("MINIO_BUCKET", "designs"),
		MinIOPrefix:     os.Getenv("MINIO_PREFIX"),
		MinIOSecure:     boolEnvOr("MINIO_SECURE", false),
		MinIOAutoCreate: boolEnvOr("MINIO_AUTO_CREATE", true),

		BambuRoot:           envOr("BAMBU_ROOT", "/opt/bambu/squashfs-root"),
		SliceTimeoutSeconds: intEnvOr("SLICE_TIMEOUT_SECONDS", 300),
		PrinterAvgPowerKW:   floatEnvOr("PRINTER_AVG_POWER_KW", 0.11),
		SliceConcurrency:    intEnvOr("SLICE_CONCURRENCY", 2),
		OpenSCADBin:         envOr("OPENSCAD_BIN", "openscad"),

		RiverQueuePrefix:      os.Getenv("RIVER_QUEUE_PREFIX"),
		ProductionConcurrency: intEnvOr("PRODUCTION_CONCURRENCY", 2),
		BatchPlanDebounce:     secondsEnvOr("BATCH_PLAN_DEBOUNCE_SECONDS", 30),
		BatchPlanInterval:     secondsEnvOr("BATCH_PLAN_INTERVAL_SECONDS", 600),

		BambuddyBaseURL:      os.Getenv("BAMBUDDY_BASE_URL"),
		BambuddyAPIKey:       os.Getenv("BAMBUDDY_API_KEY"),
		BambuddyTimeout:      secondsEnvOr("BAMBUDDY_TIMEOUT_SECONDS", 30),
		BambuddyManualStart:  boolEnvOr("BAMBUDDY_MANUAL_START", true),
		BambuddyAutoPrint:    boolEnvOr("BAMBUDDY_AUTOPRINT", true),
		BambuddyPollInterval: secondsEnvOr("BAMBUDDY_POLL_SECONDS", 30),

		OrientationOverhangDeg:  floatEnvOr("ORIENTATION_OVERHANG_DEG", 45),
		OrientationMaxTriangles: intEnvOr("ORIENTATION_MAX_TRIANGLES", 500_000),

		ShopifyAPIVersion: envOr("SHOPIFY_API_VERSION", "2024-10"),
		ShopifyTimeout:    secondsEnvOr("SHOPIFY_TIMEOUT_SECONDS", 15),

		ShopifyClientID:     os.Getenv("SHOPIFY_CLIENT_ID"),
		ShopifyClientSecret: os.Getenv("SHOPIFY_CLIENT_SECRET"),
		PublicBaseURL:       strings.TrimRight(os.Getenv("PUBLIC_BASE_URL"), "/"),
		FrontendURL:         strings.TrimRight(envOr("FRONTEND_URL", "http://localhost:3001"), "/"),
		TokenEncryptionKey:  os.Getenv("TOKEN_ENCRYPTION_KEY"),

		OpenRouterAPIKey:  os.Getenv("OPENROUTER_API_KEY"),
		OpenRouterModel:   envOr("OPENROUTER_MODEL", "anthropic/claude-sonnet-4.5"),
		OpenRouterTimeout: secondsEnvOr("OPENROUTER_TIMEOUT_SECONDS", 60),

		SMTPHost: os.Getenv("SMTP_HOST"),
		SMTPPort: intEnvOr("SMTP_PORT", 587),
		SMTPUser: os.Getenv("SMTP_USER"),
		SMTPPass: os.Getenv("SMTP_PASS"),
		SMTPFrom: os.Getenv("SMTP_FROM"),
	}
}

// EmailConfigured reports whether the email-report feature can send. When false,
// the email endpoint fails closed with a 503.
func (s Settings) EmailConfigured() bool {
	return strings.TrimSpace(s.SMTPHost) != "" && strings.TrimSpace(s.SMTPFrom) != ""
}

// AdvisorConfigured reports whether the AI optimization advisor has an API key.
// When false, the optimize endpoint fails closed with a 503.
func (s Settings) AdvisorConfigured() bool {
	return strings.TrimSpace(s.OpenRouterAPIKey) != ""
}

// ShopifyImportConfigured reports whether the inbound Shopify order-import flow
// has everything it needs. When false, the integration endpoints fail closed with
// a 503 rather than half-working.
func (s Settings) ShopifyImportConfigured() bool {
	return s.ShopifyClientID != "" && s.ShopifyClientSecret != "" &&
		s.PublicBaseURL != "" && s.TokenEncryptionKey != ""
}

// Queue namespaces a River queue name with RiverQueuePrefix. Enqueue and consume
// sites must both go through this, or jobs land on a queue nothing is reading.
func (s Settings) Queue(base string) string {
	return s.RiverQueuePrefix + base
}

// BambuddyConfigured reports whether Tensor can reach BamBuddy. When false the
// print-dispatch path fails closed rather than half-working: a batch still slices
// and still shows its plate, it simply is not pushed to a printer.
func (s Settings) BambuddyConfigured() bool {
	return strings.TrimSpace(s.BambuddyBaseURL) != "" && strings.TrimSpace(s.BambuddyAPIKey) != ""
}

// AutoPrintEnabled reports whether a dispatch should actually be sent. It is
// BambuddyConfigured plus the kill switch, so turning BAMBUDDY_AUTOPRINT off
// stops every push without needing the credentials removed.
func (s Settings) AutoPrintEnabled() bool {
	return s.BambuddyConfigured() && s.BambuddyAutoPrint
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
