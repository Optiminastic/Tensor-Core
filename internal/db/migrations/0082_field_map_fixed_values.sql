-- +goose Up
-- A mapped field may carry a fixed value instead of an order field.
--
-- Not every variable a template needs comes from the customer. The plank
-- templates default OUT_X/Y/Z to 0, which means "natural size, whatever the
-- name needs", and the DNP path passes 200/50/40 to scale the finished model
-- to the product. The registry path builds its arguments from the field
-- mapping alone, so it passed nothing - and SC's plank rendered 377 mm wide
-- instead of 200 mm.
--
-- That is worse than it looks. The model was not merely too big to fit a bed,
-- which is how it surfaced; it was the wrong product, rendered successfully,
-- and the only reason anybody found out is that the packer could not place it.
--
-- So a row may name a value rather than a field. property_key stays NOT NULL
-- and is ignored on such a row - widening it to nullable would mean every
-- read has to handle a null that means something different from empty, for no
-- gain over a row whose key is simply unused.
ALTER TABLE product_field_maps
    ADD COLUMN IF NOT EXISTS fixed_value varchar(120);

-- A row is one or the other, never both, and never neither in a way that
-- leaves the variable unset without saying so. Enforced here rather than
-- trusted, because a row that is half a mapping renders a model missing what
-- it names and exits 0.
ALTER TABLE product_field_maps
    DROP CONSTRAINT IF EXISTS ck_product_field_map_source;
ALTER TABLE product_field_maps
    ADD CONSTRAINT ck_product_field_map_source CHECK (
        fixed_value IS NOT NULL OR btrim(property_key) <> ''
    );

-- +goose Down
ALTER TABLE product_field_maps DROP CONSTRAINT IF EXISTS ck_product_field_map_source;
ALTER TABLE product_field_maps DROP COLUMN IF EXISTS fixed_value;
