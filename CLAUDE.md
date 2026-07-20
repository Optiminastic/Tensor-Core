# Tensor Backend (be/) — Python / FastAPI Rules

The backend owns the domain: PostgreSQL, the costing & pricing engine, the slicer worker, and all integrations. It is the source of truth. The frontend (`../fe`) only renders and calls this API.

## Toolchain
- **uv** for everything: `uv sync`, `uv run pytest`, `uv run ruff check .`, `uv run uvicorn app.main:app --reload`.
- Python **3.12+**. Ruff is the linter/formatter. Pytest for tests.
- Never `pip install` directly — add deps via `uv add <pkg>` (or `uv add --dev` for dev deps). Ask before adding a new dependency.

## Structure
```
app/
  main.py            FastAPI entrypoint (health, routers, CORS)
  core/config.py     Settings (pydantic-settings); env optional until DB is wired
  pricing/           Costing & pricing engine (pure logic + a FastAPI router)
    models.py        Pydantic models (Brand, SlicerMetrics, CostAssumptions, …)
    assumptions.py   Defaults, price ladders, thresholds (spec §4/§8/§9)
    design_cp.py     compute_design_cp(), compute_effective_machine_time()
    selling_price.py generate_selling_price(), remaining_margin()
    status.py        evaluate_status() → Green / Yellow / Red
    api.py           /pricing/* endpoints
  auth/              Authorization — verify tokens, guard endpoints
    jwt.py           verify a Better Auth JWT against the frontend's JWKS
    catalog.py       permission catalog + default role grants
    dependencies.py  current_user, require_permission, require_internal_secret
    service.py       resolve_user_authz(), bump_permissions_version()
    seed.py          sync the catalog into the DB (uv run python -m app.auth.seed)
  api/
    config.py        /config/* admin CRUD (permission-guarded)
    internal.py      /internal/* service-to-service (shared secret, not in schema)
  db/models/
    authz.py         Role, Permission, RolePermission, UserRole, UserAuthzState
tests/               Pytest, mirrors app/ ; validates spec worked examples
```
Future: `app/integrations/` (Shopify/Razorpay/Shiprocket/Meta), `worker/` (slicer, Redis).

## Hard rules
- **Pure domain logic stays pure** — costing/pricing functions take inputs and return Pydantic models; no I/O, no DB, no globals. They must be unit-testable without a database (the pricing engine already is).
- **Slicer output is the source of truth** for time/filament/cost. AI only produces suggestions/summaries — never costs.
- Full type hints on every function signature; use `X | None`, `list[...]`, `StrEnum`. Ruff rules: `E,F,I,UP,B`.
- Validate all external input with Pydantic models at the API boundary.
- Money: round to 2 decimals in outputs; percentages as fractions (0.25 = 25%).
- Keep functions small and single-purpose; extract helpers rather than growing one function.
- Every new engine rule or formula gets a test asserting the spec's numbers.

## Auth — `app/auth/`

Authentication (who you are) belongs to Better Auth in the frontend. **This service owns authorization** (what you may do). The two are deliberately independent.

FastAPI verifies the Better Auth-issued JWT (`Authorization: Bearer …`) against the frontend's JWKS (`AUTH_JWKS_URL`), checking signature, `exp`, `iss` and `aud`. A verified token is *trusted data* — it is signed by the issuer, not supplied by the client — so reading its claims is enforcement, not trust.

```
jwt.py           verify a token against the JWKS (EdDSA)
catalog.py       the permission catalog + default role grants
dependencies.py  current_user, require_permission, require_internal_secret
service.py       resolve_user_authz, bump_permissions_version
seed.py          sync the catalog into the DB (idempotent)
models.py        TokenClaims, AuthenticatedUser, UserAuthzResponse
```

### Hard rules

- **Guard on permissions, never roles.** `Depends(require_permission(CONFIG_MANAGE))`, never `if user.role == "ADMIN"`. Adding a role must never require touching an endpoint.
- **Take the `PermissionSpec` from `catalog.py`, not a bare string.** A typo'd string is an endpoint that silently never grants; a typo'd import is an error.
- **The database is the runtime source of truth for grants.** `catalog.py` seeds `role_permissions` and is the default, but `resolve_user_authz` reads the table. That is what makes `permissions_version` meaningful — a grant can change without a deploy.
- **Anything that changes a user's roles must call `bump_permissions_version`.** Otherwise the change is invisible until the token expires (up to 15 min).
- **Fail closed.** No roles means no permissions means every guard rejects. Never default to a role on error.
- **`/internal/*` is service-to-service only.** Shared secret, constant-time compare, never in the OpenAPI schema, never reachable from a browser. No secret configured means 503, not open.
- Add a new permission to `ALL_PERMISSIONS` and the relevant `ROLE_PERMISSIONS` entry, then re-run the seed. `ADMIN` is defined as the whole catalog, so it picks it up automatically.

Roles: `ADMIN`, `DESIGNER`, `PROJECT_LEAD`, `PERFORMANCE_MARKETER`, `OPERATOR`. Separation of duties is asserted in `tests/test_auth_internal.py` (e.g. Operator must never see cost assumptions) — those tests are the spec, do not relax them to make something pass.

### Table ownership

One Postgres, two migration tools. `alembic/env.py`'s `include_object` keeps Alembic off Better Auth's tables.

| Tables | Owner |
| --- | --- |
| `user`, `session`, `account`, `verification`, `jwks` | Better Auth (`pnpm auth:migrate`, from the frontend) |
| everything else, incl. `roles`, `permissions`, `user_roles` | Alembic (`uv run alembic upgrade head`) |

`user_roles.user_id` holds a Better Auth user id with **no foreign key** — a constraint would couple Alembic to Better Auth's CLI and force an apply order. An orphaned row is harmless: no user, no token, no authorization.

## After changes
Run `uv run ruff check .` and `uv run pytest` — both must be green before done.
