-- name: ClaimDamageLink :one
-- Takes the right to send this person a damage link, or reports that they
-- already have one.
--
-- Written BEFORE the message is sent, like the personalisation claim: a row
-- written afterwards leaves a window in which the process dies with the
-- customer's phone buzzing and nothing recorded. A crash then costs a missed
-- link, which is recoverable, rather than a duplicate, which is not.
--
-- No conflict target, because the index this races is PARTIAL - "one live
-- link per person" is only enforced over the open statuses, and a partial
-- index cannot be named as an ON CONFLICT target without restating its
-- predicate. DO NOTHING catches it either way, and the caller reads an empty
-- result as "they already hold one".
INSERT INTO damage_links (id, brand_slug, wa_id, token_hash, status, expires_at)
VALUES (
    sqlc.arg('id'), sqlc.arg('brand_slug'), sqlc.arg('wa_id'),
    sqlc.arg('token_hash'), 'pending', sqlc.arg('expires_at')
)
ON CONFLICT DO NOTHING
RETURNING *;

-- name: SettleDamageLink :one
-- Records how the send went.
--
-- CAST on the status, for the same reason SettlePersonalisationLink carries
-- one: the placeholder is both assigned to a varchar column and compared with
-- a literal, and without a type Postgres deduces two and refuses the whole
-- statement at runtime.
UPDATE damage_links
SET status     = sqlc.arg('status')::varchar,
    sent_at    = CASE WHEN sqlc.arg('status')::varchar = 'sent' THEN now() ELSE sent_at END,
    message_id = coalesce(sqlc.narg('message_id'), message_id),
    detail     = sqlc.narg('detail')
WHERE id = sqlc.arg('id')
RETURNING *;

-- name: ReleaseDamageLink :exec
-- Gives the person back, for a link whose message never left.
--
-- Guarded on a null message_id, which is the proof nothing was sent. A
-- permanent refusal is settled as 'failed' and KEPT - that row is the only
-- record of a customer who asked for help and did not get a link.
DELETE FROM damage_links
WHERE id = sqlc.arg('id') AND message_id IS NULL;

-- name: GetLiveDamageLinkForWaID :one
-- The link this person already holds, if any.
--
-- Open statuses only, and the expiry judged here rather than in Go because
-- this one is not a public read: the caller is the bot deciding whether to
-- mint or to resend, and a closed window means mint.
SELECT * FROM damage_links
WHERE brand_slug = sqlc.arg('brand_slug')
  AND wa_id = sqlc.arg('wa_id')
  AND status IN ('sent', 'opened')
  AND expires_at > now();

-- name: GetDamageLinkByTokenHash :one
-- The public page's own read. Expiry is judged in Go, not here, so an expired
-- link and an unknown one can be refused with the same sentence.
SELECT * FROM damage_links WHERE token_hash = sqlc.arg('token_hash');

-- name: MarkDamageLinkOpened :exec
-- First open only: "when did they look" is answered by the first time they
-- did, and a refresh is not a new visit.
UPDATE damage_links
SET opened_at = coalesce(opened_at, now()),
    status    = CASE WHEN status = 'sent' THEN 'opened' ELSE status END
WHERE id = sqlc.arg('id');

-- name: SubmitDamageLink :exec
-- Marks the form sent. The link is deliberately NOT closed: the customer has
-- 24 hours, and somebody who finds another photograph an hour later should be
-- able to add it to the report they already filed rather than start again.
UPDATE damage_links
SET submitted_at = coalesce(submitted_at, now())
WHERE id = sqlc.arg('id');

-- name: ExpireDamageLinks :execrows
-- Closes windows that have run out, so the partial unique index lets the same
-- person ask again.
UPDATE damage_links
SET status = 'expired'
WHERE status IN ('pending', 'sent', 'opened') AND expires_at <= now();

-- name: CountDamageFiles :one
-- How many files this link already holds, for the per-link cap.
SELECT count(*)::int FROM damage_files WHERE link_id = sqlc.arg('link_id');

-- name: AttachDamageFile :exec
-- Links one uploaded file to the link it was uploaded against.
--
-- The position is computed here rather than passed in, so two uploads landing
-- at once cannot claim the same slot.
INSERT INTO damage_files (link_id, file_id, position)
SELECT sqlc.arg('link_id'), sqlc.arg('file_id'),
       coalesce(max(position) + 1, 0)
FROM damage_files WHERE link_id = sqlc.arg('link_id')
ON CONFLICT DO NOTHING;

-- name: ListDamageLinkFiles :many
-- What this link holds, in upload order. Used by the public page to show the
-- customer what they have already sent, and by the dashboard to show the
-- report's photographs.
SELECT f.id, f.filename, f.content_type, f.size_bytes, d.position, d.created_at
FROM damage_files d
JOIN file_assets f ON f.id = d.file_id
WHERE d.link_id = sqlc.arg('link_id')
ORDER BY d.position;

-- name: GetDamageReportFile :one
-- One file of one report, reached through the report's own link.
--
-- THE JOIN IS THE AUTHORISATION. A file id on its own means nothing: it has to
-- belong to this report, and the report has to belong to this brand, or the
-- route would serve any file_asset in the database by id.
SELECT f.id, f.filename, f.content_type, f.size_bytes, f.storage_key
FROM damage_reports r
JOIN damage_files d ON d.link_id = r.link_id
JOIN file_assets f ON f.id = d.file_id
WHERE r.id = sqlc.arg('report_id')
  AND r.brand_slug = sqlc.arg('brand_slug')
  AND f.id = sqlc.arg('file_id');
