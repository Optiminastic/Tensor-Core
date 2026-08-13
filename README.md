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

## The slice worker

`cmd/sliceworker` consumes River `slice` jobs and needs a real Bambu Studio
install, which means Linux, xvfb and the extracted AppImage. It cannot run on a
Windows or macOS host, so it runs in a container:

```bash
docker compose -f docker-compose.sliceworker.yml up --build
```

The image (`sliceworker.Dockerfile`) downloads Bambu Studio's official AppImage,
extracts it to `BAMBU_ROOT=/opt/bambu/squashfs-root` and installs the GTK/GL
stack it links against. First build downloads ~220 MB and takes a few minutes;
the result is ~2 GB.

Without it, set `FAKE_SLICE=true` (as `env/local.env` does) and every design
gets fabricated metrics — enough to exercise order → job → batch → machine, but
**every design gets the same print time**, so nothing downstream that depends on
one design costing more than another is meaningful.

### Which printer profile

Slicing resolves against the **H2S** presets, not the shop's H2C. The H2C is
multi-extruder and its CLI refuses to assign a filament to an extruder headless
(`some filaments can not be mapped ... for multi extruder printer`, return
`-66`) — see the comment at the top of `internal/slicing/profiles.go` for what
was tried. The H2S is its single-extruder sibling: same generation, same 0.4
nozzle, same bed, complete preset coverage. A two-material design is therefore
estimated as if printed in one material.

### Proving it actually slices

The unit suite cannot tell a working slicer from a broken one. This can, and CI
runs it on every PR:

```bash
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go test -c -o slicing.test ./internal/slicing/
docker run --rm -v "$PWD:/t:ro" \
  -e BAMBU_ROOT=/opt/bambu/squashfs-root \
  -e SLICE_TEST_MODEL=/opt/bambu/squashfs-root/resources/model/Bambu_Cube.stl \
  tensor-slice-worker /t/slicing.test -test.run RealInstall -test.v
```

## Batches will not form on the default gate

**With realistic priorities, almost nothing batches until `BATCH_MAX_WAIT_HOURS`
(default 4) elapses.** Two things combine:

- `TargetBedUtilisationPercent` is 80, but utilisation counts footprint area
  against the full 330×320 bed while packing is confined to a 310×300 inset
  with 10 mm gaps. Measured against the real packer, the ceiling is **69% for
  parts up to 100 mm** and **74% up to 150 mm**; only a single part filling
  nearly the whole bed exceeds 80%. `BATCH_AGING_FLOOR_PERCENT` (73) is above
  the practical ceiling too.
- `shouldCreateBatch` treats `Priority <= 1` as urgent, and the column defaults
  to `0` — so any job whose priority is never set bypasses the gate entirely.
  Only once jobs carry real priorities does the backlog become visible at all.

Neither is a bug to fix here — changing `GapMM`, the target, or the urgent test
is a product decision. To watch batches form promptly, turn the gate down:

```bash
BATCH_MAX_WAIT_HOURS=0.02 BATCH_AGING_WINDOW_MINUTES=1 \
BATCH_AGING_FLOOR_PERCENT=20 BATCH_PLAN_INTERVAL_MINUTES=1 \
BATCH_PLAN_JOB_THRESHOLD=2 go run ./cmd/productionworker
```

Set `NEXT_PUBLIC_PRODUCTION_REFRESH_SECONDS=10` in the frontend too, or the
production pages render one snapshot and never update.

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
  go test -p 1 ./...
```

**`-p 1` matters.** Two packages have DB-backed tests — `internal/httpapi` and
`internal/slicing` — and both TRUNCATE shared tables in their setup. Go runs
different packages concurrently by default, so without it they wipe each
other's fixtures mid-run and fail with foreign-key or duplicate-key errors that
look like real bugs and move around between runs. Either package passes cleanly
on its own, which is what makes the flakiness confusing.

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
