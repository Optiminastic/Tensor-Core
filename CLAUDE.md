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
tests/               Pytest, mirrors app/ ; validates spec worked examples
```
Future: `app/db/` (SQLAlchemy 2 models + Alembic), `app/auth/` (verify Better Auth JWT via JWKS), `app/integrations/` (Shopify/Razorpay/Shiprocket/Meta), `worker/` (slicer, Redis).

## Hard rules
- **Pure domain logic stays pure** — costing/pricing functions take inputs and return Pydantic models; no I/O, no DB, no globals. They must be unit-testable without a database (the pricing engine already is).
- **Slicer output is the source of truth** for time/filament/cost. AI only produces suggestions/summaries — never costs.
- Full type hints on every function signature; use `X | None`, `list[...]`, `StrEnum`. Ruff rules: `E,F,I,UP,B`.
- Validate all external input with Pydantic models at the API boundary.
- Money: round to 2 decimals in outputs; percentages as fractions (0.25 = 25%).
- Keep functions small and single-purpose; extract helpers rather than growing one function.
- Every new engine rule or formula gets a test asserting the spec's numbers.

## Auth
FastAPI verifies the Better Auth-issued JWT (`Authorization: Bearer …`) against the frontend's JWKS (`AUTH_JWKS_URL`) and reads the role claim. Roles: `ADMIN`, `DESIGNER`, `PROJECT_LEAD`, `PERFORMANCE_MARKETER`, `OPERATOR`. Enforce RBAC here — never trust the client.

## After changes
Run `uv run ruff check .` and `uv run pytest` — both must be green before done.
