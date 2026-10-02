-- +goose Up
-- Which brands a member may work in.
--
-- The frontend has offered this since the People page was built - "Assign the
-- brands each member may work in" - and there has never been anywhere to put
-- the answer. PUT /admin/users/:id/brands had no route and no table, so the
-- control saved nothing.
--
-- Rows are the ALLOW LIST for non-admins. A member with no rows sees no brand,
-- which is the safe default for somebody just invited: an admin grants access
-- deliberately rather than a new account arriving with the whole workspace.
-- Admins are not represented here at all - they see every brand by holding
-- brand:read, and writing thirteen rows per admin would only create a second
-- place for that truth to drift from.
--
-- user_id carries a Better Auth id with NO foreign key, exactly as user_roles
-- does: that table lives in the frontend's migration tool, and an orphaned row
-- here is harmless.
--
-- brand_slug references brands(slug) rather than its id, because every API
-- route and every URL is keyed by slug; storing the id would mean a join on
-- every access check to recover the thing the caller already had. ON DELETE
-- CASCADE so deleting a brand takes its grants with it.
CREATE TABLE IF NOT EXISTS user_brand_access (
    user_id     varchar(64) NOT NULL,
    brand_slug  text NOT NULL REFERENCES brands (slug) ON DELETE CASCADE,
    granted_by  varchar(64),
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, brand_slug)
);

-- Listing one member's brands is the hot read (the roster does it per row), and
-- the primary key already serves it. This one serves the other direction -
-- "who can see this brand" - which the brand-delete path needs.
CREATE INDEX IF NOT EXISTS ix_user_brand_access_brand ON user_brand_access (brand_slug);

-- Which brands the INVITE promised.
--
-- The invite form has always had a brand multi-select, and there was nowhere to
-- put its answer: the create-invite route accepted only an email and a role, so
-- the selection was posted and silently dropped. Grants cannot be written to
-- user_brand_access at invite time because the person has no user id until they
-- accept, so the intent is parked here and applied at acceptance.
--
-- Plain text[], not a join table: this is a short, write-once list that is read
-- exactly once, by AcceptInvite. A referenced brand deleted before acceptance
-- simply does not get granted - AcceptInvite skips what no longer exists rather
-- than failing the acceptance, because somebody is standing at a password form.
ALTER TABLE user_invites
    ADD COLUMN IF NOT EXISTS brand_slugs text[] NOT NULL DEFAULT '{}';

-- +goose Down
ALTER TABLE user_invites DROP COLUMN IF EXISTS brand_slugs;
DROP TABLE IF EXISTS user_brand_access;
