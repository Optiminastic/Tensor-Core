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

// s3DevSecret is the built-in development object-storage secret. Running with
// it in production is a misconfiguration, so Validate rejects it there.
const s3DevSecret = "tensor_local_dev"

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

	// Design pipeline: object storage for STL/G-code, any S3-compatible store
	// (real AWS S3 in production, MinIO in dev - internal/storage talks the S3
	// API via minio-go, which works against both identically). S3KeyPrefix
	// namespaces every object key - required when S3Bucket is shared with other
	// apps. S3AssumeBucketExists skips the bucket-existence/creation check, for
	// when the credentials are scoped to the prefix and can't HeadBucket.
	S3Endpoint           string
	S3AccessKey          string
	S3SecretKey          string
	S3Bucket             string
	S3KeyPrefix          string
	S3Secure             bool
	S3AssumeBucketExists bool

	// Slice worker (cmd/sliceworker): the Bambu Studio install, per-slice timeout,
	// the calibratable average printer draw used to estimate energy, and how many
	// slices run at once.
	BambuRoot           string
	SliceTimeoutSeconds int
	PrinterAvgPowerKW   float64
	SliceConcurrency    int

	// FakeSlice fabricates plausible slice metrics instead of running Bambu
	// Studio (internal/slicing/fake.go) - dev-only, for testing the downstream
	// pipeline (pricing, batching, scheduling) on a machine that can't run the
	// real slicer. Never set true against a production deployment.
	FakeSlice bool

	// Production worker (cmd/productionworker): how many order's-worth of jobs
	// the Job Creation Worker builds at once.
	ProductionConcurrency int

	// Batch gate overrides (production.BatchGate, see planner.go): a batch
	// under the utilisation target is still created once any of its jobs has
	// been queued this long, or is due within this window of now - otherwise
	// it's held for more compatible volume to arrive. See also the
	// hard-coded urgent-priority override, which needs no config.
	BatchMaxWaitHours float64
	BatchDueSoonHours float64

	// Batch aging (production.BatchGate, see planner.go): rather than a hard
	// binary switch at BatchMaxWaitHours, the acceptable utilisation bar
	// relaxes linearly from the 80% target down to BatchAgingFloorPercent as
	// a partition's oldest job approaches BatchAgingWindowMinutes of age,
	// then holds flat at the floor until BatchMaxWaitHours' unconditional
	// override eventually fires regardless of utilisation.
	BatchAgingWindowMinutes float64
	BatchAgingFloorPercent  float64

	// Batch replan cadence (see batch_orchestrate.go/cmd/productionworker):
	// a replan runs periodically every BatchPlanIntervalMinutes regardless of
	// activity, on the two high-frequency per-job trigger sites only once at
	// least BatchPlanJobThreshold jobs have actually accumulated (instead of
	// once per single order/personalisation event), and unconditionally on
	// the two low-frequency, high-signal events (a batch completing, a
	// reprint being created) - all still collapsed within
	// BatchPlanDebounceSeconds of each other into one run.
	BatchPlanIntervalMinutes int
	BatchPlanJobThreshold    int
	BatchPlanDebounceSeconds int

	// BatchReplanMinImprovementPercent is how much better a new plan must be,
	// as a percentage of the current Drafts' combined score, before those
	// Drafts are dissolved and rebuilt.
	//
	// Without a floor here the planner churns: it re-plans every couple of
	// minutes, and any reshuffle scoring even 0.01 better destroys every Draft
	// and mints new ones. Batch numbers change under the operator, cached
	// preview plates are rebuilt, and the board rewrites itself for no
	// production benefit.
	//
	// The threshold applies only when the new plan batches no more work than
	// the current one. A plan that places a job nothing was placing before is
	// real progress and always applies, however small its score delta.
	BatchReplanMinImprovementPercent float64

	// BatchHorizonJobs caps how many jobs of one compatibility group a single
	// planning run considers (production.BatchGate.HorizonJobs). The packing
	// search is superlinear in group size, so an unbounded group is what turns
	// a growing backlog into a planner that never finishes. Zero uses
	// production.DefaultHorizonJobs.
	BatchHorizonJobs int

	// BambuBuddy is the local service holding the MQTT connection to each
	// physical printer. With BambuBuddyURL unset, Tensor's fleet is whatever
	// is in the machines table and nothing syncs - which is the correct
	// behaviour for an environment with no printers attached.
	//
	// The API key is a credential: it belongs in the environment, never in the
	// repository, and it is never logged or returned on any response.
	BambuBuddyURL    string
	BambuBuddyAPIKey string

	// BatchIdleWaitMinutes is how long a thin bed may be held back while a
	// printer is free, hoping a short wait produces a fuller plate
	// (production.BatchGate.IdleWaitWindow). Zero disables the wait: an idle
	// machine then takes the best available bed immediately.
	BatchIdleWaitMinutes float64
	// River cancels a job's context at its own JobTimeoutDefault (1 minute)
	// unless the worker overrides Timeout(). Neither of these has a natural
	// inner deadline to derive one from, the way the slice worker derives its
	// from SLICE_TIMEOUT_SECONDS, so they are configured directly.
	//
	// A replan's cost scales with the backlog: it lists every batchable job,
	// runs the multi-strategy pack search, then for each created batch
	// downloads its jobs' print files, merges the meshes and uploads the plate.
	// Twenty batches of sixteen jobs is minutes, not seconds.
	BatchPlanTimeoutMinutes   int
	JobCreationTimeoutMinutes int

	// Machine selection weighted scoring (production.MachineScoreWeights, see
	// machine_scheduler.go/scheduler.go): minutes-equivalent adjustments to a
	// candidate machine's raw free time - already-loaded material/colour
	// match make it look more free, a material change (different, KNOWN
	// material loaded) makes it look less free, likewise queue depth and
	// health. The change penalty deliberately exceeds the match bonus: an
	// actual changeover costs more operator time than a match saves.
	MachineMaterialMatchBonusMinutes    float64
	MachineColourMatchBonusMinutes      float64
	MachineMaterialChangePenaltyMinutes float64
	MachineQueueLengthPenaltyMinutes    float64
	// MachineDraftLoadFraction is how much of a machine's Draft minutes count
	// towards its apparent load when choosing where a batch goes. Below 1
	// because Draft work is not owed yet; above 0 because a machine already
	// holding eight hours of proposals is a worse home for a ninth.
	MachineDraftLoadFraction float64
	// Idle recency only ever separates otherwise-equal candidates, so a tie
	// does not always resolve to the same machine.
	MachineIdleRecencyBonusMinutes  float64
	MachineIdleRecencyWindowMinutes float64
	MachineHealthBonusMinutes       float64

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

		S3Endpoint:           envOr("S3_ENDPOINT", "localhost:9100"),
		S3AccessKey:          envOr("S3_ACCESS_KEY", "tensor"),
		S3SecretKey:          envOr("S3_SECRET_KEY", "tensor_local_dev"),
		S3Bucket:             envOr("S3_BUCKET", "designs"),
		S3KeyPrefix:          os.Getenv("S3_KEY_PREFIX"),
		S3Secure:             boolEnvOr("S3_SECURE", false),
		S3AssumeBucketExists: boolEnvOr("S3_ASSUME_BUCKET_EXISTS", false),

		BambuRoot:           envOr("BAMBU_ROOT", "/opt/bambu/squashfs-root"),
		SliceTimeoutSeconds: intEnvOr("SLICE_TIMEOUT_SECONDS", 300),
		PrinterAvgPowerKW:   floatEnvOr("PRINTER_AVG_POWER_KW", 0.11),
		SliceConcurrency:    intEnvOr("SLICE_CONCURRENCY", 2),
		FakeSlice:           boolEnvOr("FAKE_SLICE", false),

		ProductionConcurrency:   intEnvOr("PRODUCTION_CONCURRENCY", 5),
		BatchMaxWaitHours:       floatEnvOr("BATCH_MAX_WAIT_HOURS", 4),
		BatchDueSoonHours:       floatEnvOr("BATCH_DUE_SOON_HOURS", 24),
		BatchAgingWindowMinutes: floatEnvOr("BATCH_AGING_WINDOW_MINUTES", 60),
		BatchAgingFloorPercent:  floatEnvOr("BATCH_AGING_FLOOR_PERCENT", 73),

		BatchPlanIntervalMinutes: intEnvOr("BATCH_PLAN_INTERVAL_MINUTES", 7),
		BatchPlanJobThreshold:    intEnvOr("BATCH_PLAN_JOB_THRESHOLD", 5),

		BatchPlanTimeoutMinutes:          intEnvOr("BATCH_PLAN_TIMEOUT_MINUTES", 15),
		JobCreationTimeoutMinutes:        intEnvOr("JOB_CREATION_TIMEOUT_MINUTES", 5),
		BatchPlanDebounceSeconds:         intEnvOr("BATCH_PLAN_DEBOUNCE_SECONDS", 5),
		BatchReplanMinImprovementPercent: floatEnvOr("BATCH_REPLAN_MIN_IMPROVEMENT_PERCENT", 2),
		BatchHorizonJobs:                 intEnvOr("BATCH_HORIZON_JOBS", 250),
		BatchIdleWaitMinutes:             floatEnvOr("BATCH_IDLE_WAIT_MINUTES", 10),
		BambuBuddyURL:                    envOr("BAMBUBUDDY_URL", ""),
		BambuBuddyAPIKey:                 envOr("BAMBUBUDDY_API_KEY", ""),

		MachineMaterialMatchBonusMinutes:    floatEnvOr("MACHINE_MATERIAL_MATCH_BONUS_MINUTES", 30),
		MachineColourMatchBonusMinutes:      floatEnvOr("MACHINE_COLOUR_MATCH_BONUS_MINUTES", 15),
		MachineMaterialChangePenaltyMinutes: floatEnvOr("MACHINE_MATERIAL_CHANGE_PENALTY_MINUTES", 45),
		MachineQueueLengthPenaltyMinutes:    floatEnvOr("MACHINE_QUEUE_LENGTH_PENALTY_MINUTES", 10),
		MachineDraftLoadFraction:            floatEnvOr("MACHINE_DRAFT_LOAD_FRACTION", 0.5),
		MachineIdleRecencyBonusMinutes:      floatEnvOr("MACHINE_IDLE_RECENCY_BONUS_MINUTES", 5),
		MachineIdleRecencyWindowMinutes:     floatEnvOr("MACHINE_IDLE_RECENCY_WINDOW_MINUTES", 60),
		MachineHealthBonusMinutes:           floatEnvOr("MACHINE_HEALTH_BONUS_MINUTES", 10),

		OrientationOverhangDeg:  floatEnvOr("ORIENTATION_OVERHANG_DEG", 45),
		OrientationMaxTriangles: intEnvOr("ORIENTATION_MAX_TRIANGLES", 500_000),

		ShopifyAPIVersion: envOr("SHOPIFY_API_VERSION", "2024-10"),
		ShopifyTimeout:    secondsEnvOr("SHOPIFY_TIMEOUT_SECONDS", 15),

		ShopifyClientID:     os.Getenv("SHOPIFY_CLIENT_ID"),
		ShopifyClientSecret: os.Getenv("SHOPIFY_CLIENT_SECRET"),
		PublicBaseURL:       strings.TrimRight(os.Getenv("PUBLIC_BASE_URL"), "/"),
		FrontendURL:         strings.TrimRight(envOr("FRONTEND_URL", "http://localhost:3001"), "/"),
		TokenEncryptionKey:  os.Getenv("TOKEN_ENCRYPTION_KEY"),
	}
}

// ShopifyImportConfigured reports whether the inbound Shopify order-import flow
// has everything it needs. When false, the integration endpoints fail closed with
// a 503 rather than half-working.
func (s Settings) ShopifyImportConfigured() bool {
	return s.ShopifyClientID != "" && s.ShopifyClientSecret != "" &&
		s.PublicBaseURL != "" && s.TokenEncryptionKey != ""
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
	if s.S3SecretKey == s3DevSecret {
		return errors.New("config: S3_SECRET_KEY is the development default in production; set a real secret")
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
