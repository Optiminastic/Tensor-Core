-- +goose Up
-- The design review workflow: a designer submits a priced design to the Project
-- Lead, who comments and either approves it or sends it back. designs.notes is
-- the "Notes for Project Lead" captured at upload; design_reviews is the review
-- thread + audit trail (the spec's Approval Logs / Designer Feedback / Project
-- Lead Notes), one append-only row per event. Written idempotently.

ALTER TABLE designs ADD COLUMN IF NOT EXISTS notes varchar(2000);

-- One row per review event on a design. kind is comment | submit | approve |
-- reject; body carries the message (nullable for a bare event). author_id is a
-- Better Auth user id (no FK, like designs.created_by).
CREATE TABLE IF NOT EXISTS design_reviews (
    id         uuid PRIMARY KEY,
    design_id  uuid NOT NULL REFERENCES designs (id) ON DELETE CASCADE,
    author_id  varchar(64) NOT NULL,
    kind       varchar(16) NOT NULL,
    body       text,
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS ix_design_reviews_design ON design_reviews (design_id, created_at);

-- +goose Down
DROP TABLE IF EXISTS design_reviews;
ALTER TABLE designs DROP COLUMN IF EXISTS notes;
