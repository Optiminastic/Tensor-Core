# Tensor Backend (`be/`)

FastAPI service for Tensor — the costing & pricing engine, and (later) the data model, slicer worker, and integrations.

## Quickstart

```bash
cd be
uv sync                      # install deps into .venv
uv run uvicorn app.main:app --reload   # http://127.0.0.1:8000  (docs at /docs)
```

## Develop

```bash
uv run pytest                # tests
uv run ruff check .          # lint
uv run ruff format .         # format
```

## What's here today

The **costing & pricing engine** (`app/pricing/`), fully unit-tested against the spec's worked examples — no database required:

- `compute_design_cp()` — Design CP + breakdown from slicer metrics (spec §3)
- `compute_effective_machine_time()` — per-unit time after batching (spec §2)
- `generate_selling_price()` — reverse unit economics, snapped to the approved ladder, with a 40%-ad stress test and per-rung warnings (spec §8–§10)
- `evaluate_status()` — Green / Yellow / Red pre-check (spec §4–§5)

Exposed at `POST /pricing/design-cp`, `/pricing/selling-price`, `/pricing/status`.

> Note: the spec's §10 example (CP ₹240 → SP) snaps to **₹1,199** on the given ladder (raw ₹1,079). The doc's illustrative "₹1,299" implies an extra safety rung — a policy the team can layer on top of the pure economics.

## Next

Data model (SQLAlchemy + Alembic), JWT verification (Better Auth JWKS), then the Designer-upload → Project-Lead-approval flows.
