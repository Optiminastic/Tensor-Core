-- Per-user brand access. user_id holds a Better Auth id (no FK). ListBrandsForUser
-- returns the same shape as ListBrands so handlers can share the row -> DTO mapping.

-- name: ListBrandsForUser :many
SELECT b.id, b.slug, b.name, b.logo_url, b.starting_price::float8 AS starting_price,
       b.shopify_url, b.description, b.is_active, b.ladder,
       b.cp_green_max::float8 AS cp_green_max, b.cp_yellow_max::float8 AS cp_yellow_max,
       b.entry_machine_hours, b.entry_rung, b.created_at, b.updated_at
FROM brands b
JOIN user_brands ub ON ub.brand_slug = b.slug
WHERE ub.user_id = $1
ORDER BY b.name;

-- name: ListUserBrandSlugs :many
SELECT brand_slug FROM user_brands WHERE user_id = $1 ORDER BY brand_slug;

-- name: UserHasBrand :one
SELECT EXISTS (
    SELECT 1 FROM user_brands WHERE user_id = $1 AND brand_slug = $2
) AS has_brand;

-- name: InsertUserBrand :exec
INSERT INTO user_brands (user_id, brand_slug, assigned_by)
VALUES ($1, $2, $3)
ON CONFLICT (user_id, brand_slug) DO NOTHING;

-- name: DeleteUserBrands :exec
DELETE FROM user_brands WHERE user_id = $1;

-- name: ListAllUserBrands :many
-- Every (user_id, brand_slug) assignment, for building the members overview
-- without an N+1 per-user query.
SELECT user_id, brand_slug FROM user_brands ORDER BY user_id, brand_slug;
