# Tensor-Core

Go / Gin backend for Tensor - the costing and pricing engine, RBAC, and admin
config. It is the source of truth for all costing and pricing. The frontend
(`../Tensor`) only renders and calls this API.

## Stack

| Concern | Choice |
| --- | --- |
| HTTP | Gin |
| DB driver | pgx v5 (+ pgxpool) |
| Queries | sqlc (typed Go generated from SQL) - no ORM |
| Migrations | goose (embedded in the binary) |
| JWT + JWKS | lestrrat-go/jwx (EdDSA) |
| Validation | go-playground/validator (via Gin binding) |
| Tests | `testing` + real Postgres for the integration suite |

## Layout

```
cmd/
  api/       the HTTP service
  seed/      migrate + seed the RBAC catalog and the two brands (idempotent)
  migrate/   apply migrations only
internal/
  config/    settings from the environment
  pricing/   the pure costing/pricing engine (no DB, no I/O)
  brandpolicy/ the only DB read on the pricing path
  auth/      JWT/JWKS verification, permission catalog, RBAC guards, invites, seed
  httpapi/   Gin routers and handlers (one file per router)
  db/
    migrations/  goose SQL (0001 is the baseline, idempotent)
    queries/     sqlc source
    gen/         sqlc-generated code (checked in)
    schema.sql   plain DDL for sqlc codegen only
```

## Running locally

Requires Go 1.25+ and the shared Postgres (see the repo root compose file, port
5434). Copy `.env.example` to `.env` first.

```bash
go run ./cmd/seed     # applies migrations, then seeds roles/permissions/brands
go run ./cmd/api      # serves on :8001
```

`go run ./cmd/migrate` applies migrations without seeding.

## Code generation

After editing anything under `internal/db/queries` or `internal/db/schema.sql`,
regenerate:

```bash
docker run --rm -v "$PWD:/src" -w /src sqlc/sqlc:1.27.0 generate
```

`schema.sql` (plain DDL, codegen only) and `migrations/0001_baseline.sql` (the
runtime schema) must describe the same tables; the integration tests run the
goose migration and every sqlc query, so drift fails the tests.

## Tests

```bash
go test ./...                       # unit tests (pricing, auth)

# integration tests need a throwaway Postgres:
docker run -d --name pg -e POSTGRES_USER=tensor -e POSTGRES_PASSWORD=tensor \
  -e POSTGRES_DB=tensor_test -p 5599:5432 postgres:16
TENSOR_TEST_DB="postgres://tensor:tensor@localhost:5599/tensor_test?sslmode=disable" \
  go test ./internal/httpapi/... -run Integration
```

## Contract

Every route, its JSON shape, status codes, and error `detail` strings are
identical to the previous FastAPI backend, so the frontend needs no changes.
Domain responses are snake_case; the two internal fields `permissionsVersion`
and `adminExists` are camelCase. Roles: `ADMIN`, `DESIGNER`, `PROJECT_LEAD`,
`PERFORMANCE_MARKETER`, `OPERATOR`. Guards check permission strings, never roles.
`/internal/*` is service-to-service (shared secret, 503 when unset). The pricing
engine is pure and reads each brand's ladder/thresholds from the database, with
`internal/pricing/assumptions.go` as the built-in fallback.
```
