-- Config: material profiles, machine profiles, cost assumption sets.
-- numeric columns are cast to/from float8 so pgx scans and binds float64
-- cleanly; brand is now a nullable free-form slug (plain text).

-- name: ListMaterials :many
SELECT id, name, material_type, cost_per_kg::float8 AS cost_per_kg,
       colour, is_active, created_at, updated_at
FROM material_profiles ORDER BY name;

-- name: GetMaterial :one
SELECT id, name, material_type, cost_per_kg::float8 AS cost_per_kg,
       colour, is_active, created_at, updated_at
FROM material_profiles WHERE id = $1;

-- name: InsertMaterial :one
INSERT INTO material_profiles (id, name, material_type, cost_per_kg, colour, is_active)
VALUES (sqlc.arg('id'), sqlc.arg('name'), sqlc.arg('material_type'),
        sqlc.arg('cost_per_kg')::float8, sqlc.narg('colour'), sqlc.arg('is_active'))
RETURNING id, name, material_type, cost_per_kg::float8 AS cost_per_kg,
          colour, is_active, created_at, updated_at;

-- name: UpdateMaterial :one
UPDATE material_profiles SET
    material_type = COALESCE(sqlc.narg('material_type'), material_type),
    cost_per_kg   = COALESCE(sqlc.narg('cost_per_kg')::float8, cost_per_kg),
    colour        = CASE WHEN sqlc.arg('set_colour')::bool THEN sqlc.narg('colour') ELSE colour END,
    is_active     = COALESCE(sqlc.narg('is_active'), is_active),
    updated_at    = now()
WHERE id = sqlc.arg('id')
RETURNING id, name, material_type, cost_per_kg::float8 AS cost_per_kg,
          colour, is_active, created_at, updated_at;

-- name: ListMachines :many
SELECT id, name, machine_hour_cost::float8 AS machine_hour_cost,
       is_active, created_at, updated_at
FROM machine_profiles ORDER BY name;

-- name: InsertMachine :one
INSERT INTO machine_profiles (id, name, machine_hour_cost, is_active)
VALUES (sqlc.arg('id'), sqlc.arg('name'), sqlc.arg('machine_hour_cost')::float8, sqlc.arg('is_active'))
RETURNING id, name, machine_hour_cost::float8 AS machine_hour_cost,
          is_active, created_at, updated_at;

-- Operational machine views for the print queue (machine_profiles.status). The
-- cost fields are omitted here; the config endpoints above own those.

-- name: ListMachineOps :many
SELECT id, name, status, is_active FROM machine_profiles ORDER BY name;

-- name: GetMachineOps :one
SELECT id, name, status, is_active FROM machine_profiles WHERE id = $1;

-- name: UpdateMachineStatus :one
UPDATE machine_profiles SET status = sqlc.arg('status'), updated_at = now()
WHERE id = sqlc.arg('id')
RETURNING id, name, status, is_active;

-- name: SuggestMachine :one
-- The least-loaded online machine: fewest active (open/in_progress) batches.
SELECT m.id, m.name, m.status
FROM machine_profiles m
LEFT JOIN batches b ON b.machine_id = m.id AND b.status IN ('open', 'in_progress')
WHERE m.status = 'online'
GROUP BY m.id, m.name, m.status
ORDER BY count(b.id) ASC, m.name ASC
LIMIT 1;

-- name: ListCostAssumptions :many
SELECT id, name, brand,
       filament_cost_per_kg::float8 AS filament_cost_per_kg,
       electricity_cost_per_unit::float8 AS electricity_cost_per_unit,
       machine_hour_cost::float8 AS machine_hour_cost,
       finishing_labour::float8 AS finishing_labour,
       consumables::float8 AS consumables,
       failure_pct::float8 AS failure_pct,
       fixed_costs, margins,
       is_default, created_at, updated_at
FROM cost_assumption_sets ORDER BY name;

-- name: GetCostAssumption :one
SELECT id, name, brand,
       filament_cost_per_kg::float8 AS filament_cost_per_kg,
       electricity_cost_per_unit::float8 AS electricity_cost_per_unit,
       machine_hour_cost::float8 AS machine_hour_cost,
       finishing_labour::float8 AS finishing_labour,
       consumables::float8 AS consumables,
       failure_pct::float8 AS failure_pct,
       fixed_costs, margins,
       is_default, created_at, updated_at
FROM cost_assumption_sets WHERE id = $1;

-- GetDefaultCostAssumptionForBrand returns the cost assumptions the pricing path
-- should use for a brand: the brand's own default set if it has one, else the
-- global default (brand IS NULL). Brand-specific rows sort first.
-- name: GetDefaultCostAssumptionForBrand :one
SELECT id, name, brand,
       filament_cost_per_kg::float8 AS filament_cost_per_kg,
       electricity_cost_per_unit::float8 AS electricity_cost_per_unit,
       machine_hour_cost::float8 AS machine_hour_cost,
       finishing_labour::float8 AS finishing_labour,
       consumables::float8 AS consumables,
       failure_pct::float8 AS failure_pct,
       fixed_costs, margins,
       is_default, created_at, updated_at
FROM cost_assumption_sets
WHERE is_default = true AND (brand = sqlc.narg('brand') OR brand IS NULL)
ORDER BY (brand IS NOT NULL) DESC
LIMIT 1;

-- name: InsertCostAssumption :one
INSERT INTO cost_assumption_sets (
    id, name, brand, filament_cost_per_kg, electricity_cost_per_unit,
    machine_hour_cost, finishing_labour, consumables, failure_pct,
    fixed_costs, margins, is_default
) VALUES (
    sqlc.arg('id'), sqlc.arg('name'), sqlc.narg('brand'),
    sqlc.arg('filament_cost_per_kg')::float8, sqlc.arg('electricity_cost_per_unit')::float8,
    sqlc.arg('machine_hour_cost')::float8, sqlc.arg('finishing_labour')::float8,
    sqlc.arg('consumables')::float8, sqlc.arg('failure_pct')::float8,
    sqlc.arg('fixed_costs')::json, sqlc.arg('margins')::json, sqlc.arg('is_default')
)
RETURNING id, name, brand,
          filament_cost_per_kg::float8 AS filament_cost_per_kg,
          electricity_cost_per_unit::float8 AS electricity_cost_per_unit,
          machine_hour_cost::float8 AS machine_hour_cost,
          finishing_labour::float8 AS finishing_labour,
          consumables::float8 AS consumables,
          failure_pct::float8 AS failure_pct,
          fixed_costs, margins,
          is_default, created_at, updated_at;

-- name: UpdateCostAssumption :one
UPDATE cost_assumption_sets SET
    name = COALESCE(sqlc.narg('name'), name),
    brand = CASE WHEN sqlc.arg('set_brand')::bool THEN sqlc.narg('brand') ELSE brand END,
    filament_cost_per_kg = COALESCE(sqlc.narg('filament_cost_per_kg')::float8, filament_cost_per_kg),
    electricity_cost_per_unit = COALESCE(sqlc.narg('electricity_cost_per_unit')::float8, electricity_cost_per_unit),
    machine_hour_cost = COALESCE(sqlc.narg('machine_hour_cost')::float8, machine_hour_cost),
    finishing_labour = COALESCE(sqlc.narg('finishing_labour')::float8, finishing_labour),
    consumables = COALESCE(sqlc.narg('consumables')::float8, consumables),
    failure_pct = COALESCE(sqlc.narg('failure_pct')::float8, failure_pct),
    fixed_costs = COALESCE(sqlc.narg('fixed_costs')::json, fixed_costs),
    margins = COALESCE(sqlc.narg('margins')::json, margins),
    is_default = COALESCE(sqlc.narg('is_default'), is_default),
    updated_at = now()
WHERE id = sqlc.arg('id')
RETURNING id, name, brand,
          filament_cost_per_kg::float8 AS filament_cost_per_kg,
          electricity_cost_per_unit::float8 AS electricity_cost_per_unit,
          machine_hour_cost::float8 AS machine_hour_cost,
          finishing_labour::float8 AS finishing_labour,
          consumables::float8 AS consumables,
          failure_pct::float8 AS failure_pct,
          fixed_costs, margins,
          is_default, created_at, updated_at;
