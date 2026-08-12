# Tensor-Core - Go / Gin Rules

The backend owns the domain: PostgreSQL, the costing and pricing engine, admin config, and (later) the slicer worker and integrations. It is the source of truth. The frontend (`../Tensor`) only renders and calls this API.

## Toolchain
- **Go 1.25+**. `go build ./...`, `go test ./...`, `go vet ./...`, `gofmt -w internal cmd`.
- **pgx v5** driver, **sqlc** for typed queries (no ORM), **goose** for migrations (embedded), **Gin** for HTTP, **lestrrat-go/jwx** for JWT/JWKS, **go-playground/validator** via Gin binding.
- Add deps with `go get`; ask before adding a new dependency.

## Structure
```
cmd/
  api/         HTTP entrypoint (health, routers, CORS, graceful shutdown); enqueues slices
  sliceworker/ River worker: consumes slice jobs, runs Bambu Studio, prices the design
  seed/        migrate (goose + River) + seed RBAC catalog and default cost set (idempotent; no brands - user-created)
  migrate/     apply migrations only (goose + River)
internal/
  config/    Settings from the environment
  pricing/   costing/pricing engine - PURE (no DB, no I/O, no globals beyond constants)
    models.go       structs (Brand, SlicerMetrics, CostAssumptions, ...)
    assumptions.go  ladders, thresholds, entry rule (built-in fallback)
    design_cp.go    ComputeDesignCP, ComputeEffectiveMachineTime
    selling_price.go GenerateSellingPrice, RemainingMargin, SnapUpToLadder
    status.go       EvaluateStatus -> Green / Yellow / Red
    round.go        Python-compatible round-half-to-even + formatting
  slicing/   the slice pipeline (Go, replaces the Python worker):
    profiles.go  material/quality -> Bambu H2S profile paths + filament density
    bambu.go     RunSlice: Bambu Studio headless under xvfb (subprocess)
    gcode.go     parse result.json + slice_info + plate gcode -> SliceMetrics (+ gcode_test.go)
    metrics.go   ToPerUnit: whole-plate SliceMetrics -> per-unit PerUnitMetrics
    queue.go     River SliceArgs + Enqueuer (transactional InsertTx) + insert-only client
    worker.go    SliceWorker: river.Worker[SliceArgs] - the STL-in, priced-design-out job
    pricing.go   ProcessSliceResult / priceDesign / FailJob (slice -> price, ctx+store only)
  orientation/ least-support resting-orientation recommender from mesh geometry (pure):
    mesh.go      Vec3 math + STL (binary/ASCII) and 3MF (zip+xml) loaders -> Mesh
    optimize.go  Recommend: score candidate "down" faces by downward-overhang area
                 (Tweaker-style); advisory only, never changes the costed slice
  brandpolicy/ the ONLY DB read on the pricing path; loads a brand's policy
  production/ the print-queue lifecycle rules (pure, ported from print-queue-be):
    lifecycle.go  job/assembly/qc/packaging/personalisation + batch/machine statuses,
                  role-gated PATCH field set, LineItem shape, personalisation auto-validate
    planner.go    batch planner: group by (material,colour,nozzle), due-date cluster,
                  multi-strategy pack search -> PlannedBatch + Unbatchable
  bedpack/   guillotine Best-Area-Fit bed packer (pure): Pack, UtilisationPercent
  meshio/    STL merge/write (pure): rotate/translate/normalise + binary STL, reuses
             orientation's loaders to merge placed jobs into one plate
  secretbox/ AES-256-GCM seal/open for Shopify access tokens at rest (pure)
  storage/   S3/MinIO client (STL in, G-code out) - Put/Get/Download/Upload
  integrations/shopify/  Admin GraphQL client. client.go publishes an approved design
             and reads/updates an existing one (GetProduct/UpdateProduct); media.go
             stages image uploads + add/delete product media; inventory.go resolves the
             primary location and sets stock quantities; oauth.go is the inbound
             order-import side: OAuth authorize/callback HMAC, code exchange, orders/paid
             webhook register/delete
  auth/      jwt.go (verify against JWKS, EdDSA), catalog.go (permissions + grants),
             service.go (ResolveUserAuthz, BumpPermissionsVersion), invites.go,
             seed.go (sync catalog into DB), middleware.go (Gin guards), models.go
  httpapi/   Gin routers/handlers, one file per router; errors.go is the {"detail"} shape
             design review: designs_review.go (submit/approve/reject/comment/reviews);
             lifecycle queued->slicing->priced->submitted->approved->published, with
             changes_requested on send-back. Approve is decoupled from publish
             (publish requires an approved design), guarded by design:submit/approve/reject.
             designs_publish.go creates the Shopify DRAFT product; designs_shopify_edit.go
             edits an already-published listing in place (GET/PATCH
             /designs/:id/shopify-product, multipart: reads the live product to prefill,
             then pushes title/description/tags/type/vendor/SEO/status via productUpdate,
             price via productVariantsBulkUpdate, images via productCreateMedia/
             productDeleteMedia, and stock via inventorySetQuantities; the design's
             lifecycle status is untouched), guarded by shopify:publish.
             production pipeline: orders.go, production_jobs.go, production_qc.go
             (assembly/qc/packaging), files.go, batches.go, filament.go,
             machines_ops.go, dispatch.go, shopify_oauth.go (store connect),
             webhooks.go (orders/paid import) (+ production_helpers.go); guarded by
             production/order/batch/filament/machine/qc/packaging/assembly/dispatch/integration permissions
  db/
    migrations/  goose SQL (0001 baseline, idempotent, never touches Better Auth tables)
    queries/     sqlc source
    gen/         sqlc-generated (checked in; never edit by hand)
    schema.sql   plain DDL for sqlc codegen ONLY (keep in sync with 0001)
    store.go convert.go migrate.go (Migrate = goose, MigrateRiver = River)
```

## Hard rules
- **Pure domain logic stays pure.** `internal/pricing/*` takes inputs and returns structs; no DB, no I/O, no globals. It is unit-testable without a database. The DB->engine bridge lives in `brandpolicy` + the handler, never in the engine.
- **Numeric parity.** Money uses `roundHalfEven` (matches Python `round()`). Every new formula gets a test asserting the spec's numbers. Percentages are fractions (0.25 = 25%).
- **Slicer output is the source of truth** for time/filament/cost. AI only produces suggestions, never costs.
- Explicit return types, small single-purpose functions, no `interface{}` in hand-written code except where sqlc forces it for enum/domain params.
- Validate all external input at the HTTP boundary (Gin binding tags + explicit business checks).
- Regenerate sqlc after editing `internal/db/queries` or `schema.sql`; `schema.sql` and the goose baseline must describe the same tables (the integration tests catch drift).

## Multi-part products

A product stays one design and one SKU, but a design may declare named **parts** (its recipe): body, lid, two legs. A design with no parts is a single-part product and behaves exactly as before - the whole feature is additive and legacy-safe.

- `design_parts` (migration 0027) is the recipe: one row per named `role` on a design (unique per design), with a `quantity` for identical duplicates and optional per-part `print_file_id` / material / colour / nozzle overrides that fall back to the design. Authored via `GET/POST /designs/:id/parts` and `PATCH/DELETE /designs/:id/parts/:partId` (`designs_parts.go`), gated by `design:read` / `design:update` / `design:delete`.
- At order time (`planJobsForOrder` in `production_jobs.go`), a line whose SKU resolves to a design **with** parts fans out into one `assembly_groups` row per ordered unit and one job per `(part, instance)`. Everything else stays one job per line.
- A part's identity is `(assembly_group_id, part_role, part_instance)`. `assembly_group_parts.part_uid` (`PART-XXXXXX`, minted once) is the stable, never-reused id for the slot; it is carried across reprints while `job_id` repoints to the current attempt. Both tables are migration 0028; `production_jobs` is unchanged (identity lives in the side tables, so no existing job query changed).
- Selective reprint: print-`/fail` and QC-`fail` already clone a reprint; `repointAssemblyPart` moves the failed part's slot onto that reprint, so only the failed part reprints and it stays the same tracked part. Siblings are untouched.
- Gating (`assembly_groups.go`): when a part passes QC it is **held** (`awaiting_sibling_parts`) until every part of its unit is built; then the unit becomes `ready_to_assemble` and its parts are released. `POST /assembly-groups/:id/assemble` fails closed unless every part is completed and QC-passed; `GET /assembly-groups/:id` reads a unit and its parts.
- Not yet done (follow-ups): packaging/dispatch are still per-job (should key off the group for a multi-part product); no reprint-attempt cap / `needs_design_review` escalation; slicing still costs a whole file, so per-part filament is left unset.

## Auth - `internal/auth/`

Authentication (who you are) belongs to Better Auth in the frontend. **This service owns authorization** (what you may do). Gin verifies the Better Auth JWT (`Authorization: Bearer ...`) against the frontend's JWKS (`AUTH_JWKS_URL`): signature, `exp`, `iss`, `aud`, algorithms pinned to EdDSA/RS256/ES256. A verified token is trusted data.

### Hard rules
- **Guard on permissions, never roles.** `RequirePermission(auth.ConfigManage.Key())`, never `if role == ADMIN`.
- **Take the `PermissionSpec` from `catalog.go`, not a bare string.**
- **The database is the runtime source of truth for grants.** `catalog.go` seeds `role_permissions` and is the default; `ResolveUserAuthz` reads the table. That is what makes `permissions_version` meaningful.
- **Anything that changes a user's roles must call `BumpPermissionsVersion`.**
- **Fail closed.** No roles means no permissions means every guard rejects. Never default to a role on error.
- **`/internal/*` is service-to-service only.** Shared secret, constant-time compare, guarded at the router level. No secret configured means 503, not open.
- Add a new permission to `AllPermissions` and the relevant `roleGrants` entry, then re-run the seed. `ADMIN` is the whole catalog by construction.

Roles: `ADMIN`, `DESIGNER`, `PROJECT_LEAD`, `PERFORMANCE_MARKETER`, `OPERATOR`, `PACKAGING_QC`, `MARKETING_HEAD`. The catalog holds 38 permissions across 7 roles (95 grants). `MARKETING_HEAD` writes product marketing copy only (`brand:read`, `design:read`, `design:content`) - it cannot approve, price or publish. Separation of duties is asserted in `internal/auth/catalog_test.go` (e.g. Operator and Packaging-QC must never see cost assumptions; Marketing-Head writes copy only) - those tests are the spec, do not relax them.

### Contract with the frontend
Every route, method, JSON shape, status code, and error `detail` string must stay identical - the frontend validates responses with Zod. Domain responses are snake_case; `permissionsVersion` and `adminExists` are camelCase. POST-create returns 201; revoke/bootstrap/accept return 204.

### Table ownership
One Postgres, two migration tools. The goose baseline is idempotent and never touches Better Auth's tables.

| Tables | Owner |
| --- | --- |
| `user`, `session`, `account`, `verification`, `jwks` | Better Auth (`pnpm auth:migrate`, from the frontend) |
| everything else, incl. `roles`, `permissions`, `user_roles` | goose (`go run ./cmd/migrate`) |

`user_roles.user_id` holds a Better Auth user id with **no foreign key** - an orphaned row is harmless.

## After changes
Run `gofmt -w internal cmd`, `go vet ./...`, and `go test ./...` (with `TENSOR_TEST_DB` set for the integration suite) - all must be green before done.
