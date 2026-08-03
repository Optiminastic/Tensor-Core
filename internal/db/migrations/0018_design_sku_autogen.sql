-- +goose Up
-- Auto-generated SKUs. Every design gets a unique, system-generated SKU at
-- creation; no one types one in. A global sequence supplies the numeric suffix so
-- the code is collision-free without a per-brand counter.
CREATE SEQUENCE IF NOT EXISTS designs_sku_seq;

-- Backfill existing designs that predate auto-generation. The format mirrors the
-- Go generator (generateSKU): BRAND(3)-MATERIAL(4)-COLOUR(3|NA)-NNNN, alnum only,
-- uppercased. One-time; new designs are stamped by the app.
UPDATE designs SET sku =
  upper(substring(regexp_replace(brand_slug, '[^a-zA-Z0-9]', '', 'g') from 1 for 3)) || '-' ||
  upper(substring(regexp_replace(material,   '[^a-zA-Z0-9]', '', 'g') from 1 for 4)) || '-' ||
  coalesce(nullif(upper(substring(regexp_replace(coalesce(colour, ''), '[^a-zA-Z0-9]', '', 'g') from 1 for 3)), ''), 'NA') || '-' ||
  lpad(nextval('designs_sku_seq')::text, 4, '0')
WHERE sku IS NULL;

-- +goose Down
DROP SEQUENCE IF EXISTS designs_sku_seq;
