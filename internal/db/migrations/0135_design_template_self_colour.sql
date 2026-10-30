-- +goose Up
-- A design file that states its own colours.
--
-- Colour has always been configured IN TENSOR: a scheme per variant naming
-- each piece a file draws and the filament it prints in. That fits a product
-- whose colourways are one geometry differing by filament - twenty planks
-- from one .scad - and fits the Diwali products badly. The lotus holder is
-- three colourways whose back, outline, text and cup are three different
-- palettes, which is nine rows typed by hand, and every new product is nine
-- more.
--
-- The shop's own answer is a .scad per child SKU with the colours already in
-- it. A row here says this file is one of those: its pieces and their colours
-- are read from the file's own `<PIECE>_COLOR` declarations - which
-- personalise.PartColours has parsed since the preview needed it - and nothing
-- about colour is configured for a SKU that prints from it.
--
-- IT STILL RENDERS ONE PASS PER PIECE. OpenSCAD's STL output carries no
-- colour, and meshio.Write3MF refuses a part without a hex, so colour reaches
-- the 3MF only because each piece is rendered separately and written with its
-- own. A file rendered once as one object would come out a single colour -
-- the lotus losing its gold text and its pink outline - so "the file holds
-- the colours" means Tensor READS them, not that it stops asking for pieces.
--
-- KEYED ON THE TEMPLATE KEY, not on a design_templates row, for migration
-- 0134's two reasons: that table is versioned, so a column on the row is lost
-- by the next upload - exactly when somebody is looking at it - and the
-- templates this binary ships with have no row at all.
--
-- PRESENCE IS THE FLAG. No boolean column: a `false` row and a missing row
-- would be the same state, and two ways to say one thing is one of them
-- eventually disagreeing. No hex column either - the colours are in the file,
-- and a copy here is a second answer that can drift from it.
--
-- NOT a column on design_template_sections. That table's whole stated
-- contract is "a label, and nothing else reads it"; this decides what a
-- customer receives.
CREATE TABLE IF NOT EXISTS design_template_self_colour (
    template_key varchar(64) PRIMARY KEY,
    updated_by   varchar(64) NOT NULL,
    updated_at   timestamptz NOT NULL DEFAULT now()
);

-- Readiness summarises render state per SKU, which means a grouped read over
-- production_jobs keyed on lower(sku). There is no index for that today and
-- the table is the largest in the schema.
CREATE INDEX IF NOT EXISTS ix_production_jobs_sku_lower
    ON production_jobs (lower(sku));

-- +goose Down
DROP INDEX IF EXISTS ix_production_jobs_sku_lower;
DROP TABLE IF EXISTS design_template_self_colour;
