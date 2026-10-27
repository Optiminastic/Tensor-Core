-- name: FindOrderByNumber :one
-- The Tensor order behind whatever the customer typed, when there is one.
--
-- TWO WAYS OF SAYING THE SAME ORDER. They are asked for "your order ID" and
-- the confirmation email shows T3DPS-115766, so some type that and some type
-- 115766 - and orders.order_number holds the full prefixed form. Matching on
-- the exact text alone finds the first group and not the second.
--
-- The digit comparison is a scan, and that is affordable here: it runs once
-- per damage report, of which there are tens a month, and only when the
-- exact match has already missed.
--
-- Best effort throughout. A customer can name an order Tensor has never
-- imported - a brand new one, one from a shop whose sync is behind, or a
-- number they mistyped - and the report is filed either way.
-- THE TRAILING RUN OF DIGITS, not every digit in the string.
--
-- Stripping all non-digits looks equivalent and is not: this shop's order
-- numbers are T3DPS-115766, and the PREFIX CONTAINS A 3, so that reading
-- produced 3115766 and never matched the 115766 the customer typed. The
-- fallback existed for exactly this case and had never once worked - every
-- report filed with the short number was left unlinked, which is why their
-- Order tab was empty and why no courier could be booked for a reprint.
--
-- The trailing run is what a customer reads off their confirmation email, and
-- it is right for a bare number, a prefixed one and a twice-prefixed one
-- alike (T3DPS-DNP-115902 -> 115902).
SELECT id, customer_name FROM orders
WHERE order_number = sqlc.arg('typed')
   OR (sqlc.arg('digits')::text <> ''
       AND (regexp_match(order_number, '(\d+)$'))[1] = sqlc.arg('digits')::text)
ORDER BY (order_number = sqlc.arg('typed')) DESC, created_at DESC
LIMIT 1;

-- name: UpsertDamageReportForLink :one
-- Files the report behind one link, or updates the one already there.
--
-- ONE REPORT PER LINK, which is the whole deduplication story now that the
-- old wamid key is gone. A customer who reopens their link inside its 24
-- hours and sends another photograph is correcting the report they already
-- filed, not making a second one - so a resubmission updates the order
-- number they typed and the tally, and leaves the status and the resolution
-- note alone. Somebody in the shop may already have moved it to in_review.
INSERT INTO damage_reports (
    brand_slug, wa_id, order_number, order_id, customer_name,
    photo_count, link_id
) VALUES (
    sqlc.arg('brand_slug'), sqlc.arg('wa_id'), sqlc.arg('order_number'),
    sqlc.narg('order_id'), sqlc.arg('customer_name'),
    sqlc.arg('photo_count'), sqlc.arg('link_id')
)
ON CONFLICT (link_id) WHERE link_id IS NOT NULL DO UPDATE
SET order_number  = excluded.order_number,
    order_id      = coalesce(excluded.order_id, damage_reports.order_id),
    customer_name = CASE WHEN excluded.customer_name <> ''
                         THEN excluded.customer_name ELSE damage_reports.customer_name END,
    photo_count   = excluded.photo_count,
    updated_at    = now()
RETURNING *;

-- name: ListDamageReports :many
-- The brand's damage reports, newest first, with how many files each holds.
--
-- ONE TALLY, not four. The old list carried stored/pending/failed because a
-- WhatsApp attachment had to be fetched out of Meta and could still be in
-- flight or have failed. A file the customer uploaded to the form is simply
-- there.
SELECT r.*,
       coalesce(c.files, 0)::int AS file_count,
       o.order_number IS NOT NULL AS order_imported
FROM damage_reports r
LEFT JOIN orders o ON o.id = r.order_id
LEFT JOIN LATERAL (
    SELECT count(*) AS files FROM damage_files d WHERE d.link_id = r.link_id
) c ON true
WHERE r.brand_slug = sqlc.arg('brand_slug')
ORDER BY r.reported_at DESC
LIMIT sqlc.arg('row_limit');

-- name: CountDamageReportsByStatus :many
-- The tab counts, over the whole brand rather than the page.
SELECT status, count(*)::int AS total
FROM damage_reports WHERE brand_slug = sqlc.arg('brand_slug')
GROUP BY status;

-- name: GetDamageReport :one
SELECT * FROM damage_reports
WHERE id = sqlc.arg('id') AND brand_slug = sqlc.arg('brand_slug');

-- name: ListDamageReportFiles :many
-- The photographs and videos a report holds, in the order they were uploaded.
--
-- Reached through the report's own link, which is also what scopes them: the
-- files belong to the link, and the link belongs to the brand.
SELECT f.id, f.filename, f.content_type, f.size_bytes, d.position, d.created_at
FROM damage_reports r
JOIN damage_files d ON d.link_id = r.link_id
JOIN file_assets f ON f.id = d.file_id
WHERE r.id = sqlc.arg('report_id')
ORDER BY d.position;

-- name: UpdateDamageReportStatus :one
-- Moves a report along, records a resolution, or both.
--
-- Each field is optional and coalesced, so sending only a note does not reset
-- the status and sending only a status does not wipe the note.
UPDATE damage_reports
SET status            = coalesce(sqlc.narg('status'), status),
    resolution_note   = coalesce(sqlc.narg('resolution_note'), resolution_note),
    status_changed_at = now(),
    status_changed_by = sqlc.arg('status_changed_by'),
    updated_at        = now()
WHERE id = sqlc.arg('id') AND brand_slug = sqlc.arg('brand_slug')
RETURNING *;

-- name: GetDamageReportByLink :one
-- The report already filed against this link, if any.
--
-- Read by the public form so that reopening the link shows what the customer
-- already sent, rather than an empty box asking for the order number again.
SELECT * FROM damage_reports WHERE link_id = sqlc.arg('link_id');

-- name: SetDamageResolution :one
-- Records what the shop decided to do about a report, once.
--
-- COMPARE-AND-SET on the empty kind, which is what makes it once. Two admins
-- on the same board, or one admin double-clicking, would otherwise both offer
-- the reverse pickup and the customer would get the Rs 100 message twice. No
-- row back means somebody already decided, and the caller re-reads to say
-- which way.
UPDATE damage_reports
SET resolution_kind   = sqlc.arg('resolution_kind'),
    reverse_state     = sqlc.arg('reverse_state'),
    offered_at        = CASE WHEN sqlc.arg('is_offer')::boolean
                             THEN now() ELSE offered_at END,
    -- THE STATUS FOLLOWS FROM THE DECISION rather than being set by hand.
    -- A reprint is the whole of what the shop does about the complaint - we
    -- make it again and tell them - so the report is finished. A reverse has
    -- days of negotiation ahead of it and is only under way.
    status            = sqlc.arg('next_status'),
    status_changed_at = now(),
    status_changed_by = sqlc.arg('decided_by'),
    updated_at        = now()
WHERE id = sqlc.arg('id')
  AND brand_slug = sqlc.arg('brand_slug')
  AND resolution_kind = ''
RETURNING *;

-- name: AdvanceDamageReverse :one
-- Moves a reverse report from one state to the next.
--
-- The expected state is in the WHERE, not checked and then written: between a
-- read and a write a customer can tap a button and a support person can
-- confirm a payment. Whoever gets there first wins and the other gets no row,
-- which is the honest answer rather than a last-write-wins overwrite of a step
-- somebody already took.
UPDATE damage_reports
SET reverse_state      = sqlc.arg('next_state'),
    payment_claimed_at = CASE WHEN sqlc.arg('mark_claimed')::boolean
                              THEN now() ELSE payment_claimed_at END,
    updated_at         = now()
WHERE id = sqlc.arg('id')
  AND brand_slug = sqlc.arg('brand_slug')
  AND resolution_kind = 'reverse'
  AND reverse_state = sqlc.arg('expected_state')
RETURNING *;

-- name: ConfirmDamagePayment :one
-- Support confirms the Rs 100 arrived.
--
-- From 'offered' OR 'payment_claimed', because the screenshot is evidence and
-- not a gate: a customer may pay and never send one, and support who can see
-- the money in the bank should not be blocked by a missing picture.
--
-- 'offered' means "asked, waiting to be paid". It used to be 'agreed', which
-- stopped existing in migration 0130 when the ask and the QR became one
-- message - a WHERE still naming it would match nothing, for ever, silently.
--
-- WHO and WHEN are recorded. This is the step that unlocks sending a courier,
-- so "who said the money was there" has to stay answerable months later.
UPDATE damage_reports
SET reverse_state        = 'paid',
    payment_confirmed_at = now(),
    payment_confirmed_by = sqlc.arg('confirmed_by'),
    updated_at           = now()
WHERE id = sqlc.arg('id')
  AND brand_slug = sqlc.arg('brand_slug')
  AND resolution_kind = 'reverse'
  AND reverse_state IN ('offered', 'payment_claimed')
RETURNING *;

-- name: BookDamagePickup :one
-- An admin booked the reverse pickup with the courier.
--
-- ONLY FROM 'paid', in the WHERE rather than in Go alone. This is the
-- transition that costs the shop a courier and sends a van to somebody's
-- door; a guard that lives only in the handler is a guard one new caller
-- forgets.
UPDATE damage_reports
SET reverse_state    = 'pickup_booked',
    -- The last step anybody takes on a reverse: the courier is booked, the
    -- money is in, and nothing further is waiting on the shop.
    status           = 'resolved',
    pickup_waybill   = sqlc.arg('pickup_waybill'),
    pickup_booked_at = now(),
    pickup_booked_by = sqlc.arg('booked_by'),
    updated_at       = now()
WHERE id = sqlc.arg('id')
  AND brand_slug = sqlc.arg('brand_slug')
  AND resolution_kind = 'reverse'
  AND reverse_state = 'paid'
RETURNING *;

-- name: AttachDamagePaymentProof :one
-- Links the screenshot the customer sent to the report.
--
-- So support reads one page instead of hunting a chat for a picture. Moves the
-- report to payment_claimed in the same write - the arrival of the proof IS
-- the claim, and two statements could leave a file attached to a report that
-- never left 'agreed'.
--
-- Accepts a second screenshot: customers send them, and overwriting with the
-- newer one is kinder than refusing.
--
-- From 'offered' as well as 'payment_claimed'. 'agreed' is gone - see
-- ConfirmDamagePayment above and migration 0130.
UPDATE damage_reports
SET payment_proof_file_id = sqlc.arg('payment_proof_file_id'),
    reverse_state         = 'payment_claimed',
    payment_claimed_at    = now(),
    updated_at            = now()
WHERE id = sqlc.arg('id')
  AND brand_slug = sqlc.arg('brand_slug')
  AND resolution_kind = 'reverse'
  AND reverse_state IN ('offered', 'payment_claimed')
RETURNING *;

-- name: GetOpenReverseReportForWaID :one
-- The reverse report this customer is in the middle of, if any.
--
-- How an inbound WhatsApp message - a Yes button, a payment screenshot - finds
-- the report it belongs to. The customer's number is the only thing those
-- carry; nothing in a tapped button or an attached image names a report.
--
-- Only states where the shop is WAITING ON THE CUSTOMER. A report already
-- paid or booked is not expecting anything from them, and matching it would
-- let a stray photograph months later reopen a settled case. 'agreed' is gone
-- with migration 0130 - the ask is one message now, so paying is agreeing.
--
-- Newest first: a customer who damaged two parcels has two reports, and the
-- one they are answering about is the one just offered to them.
SELECT * FROM damage_reports
WHERE brand_slug = sqlc.arg('brand_slug')
  AND wa_id = sqlc.arg('wa_id')
  AND resolution_kind = 'reverse'
  AND reverse_state IN ('offered', 'payment_claimed')
ORDER BY reported_at DESC
LIMIT 1;

-- name: RecordDamageStage :exec
-- Writes one step of a damaged order's progress.
--
-- APPEND-ONLY, never an upsert. A failed attempt stays in the table when a
-- retry succeeds, because "the courier refused us twice before accepting" is
-- what somebody wants to know in three months and an UPDATE would erase it.
INSERT INTO damage_stages (report_id, stage, state, detail, actor)
VALUES (
    sqlc.arg('report_id'), sqlc.arg('stage'), sqlc.arg('state'),
    sqlc.arg('detail'), sqlc.narg('actor')
);

-- name: SetDamageStage :exec
-- Moves the report's own stage column, which is what the BOARD reads.
--
-- A copy of the furthest row in damage_stages, written in the same transaction
-- as the row it reflects so the list and the drawer cannot disagree. Separate
-- from RecordDamageStage because a FAILED step is recorded without advancing
-- the report: a failure is not progress.
UPDATE damage_reports
SET stage = sqlc.arg('stage'), updated_at = now()
WHERE id = sqlc.arg('id') AND brand_slug = sqlc.arg('brand_slug');

-- name: ListDamageStages :many
-- One report's steps, oldest first - the drawer's timeline.
SELECT * FROM damage_stages
WHERE report_id = sqlc.arg('report_id')
ORDER BY at, id;

-- name: SetDamagePickupWaybill :exec
-- The forward shipment Delhivery accepted for a reprint.
--
-- Shares pickup_waybill with the reverse route, which is not a conflation: a
-- report takes one route or the other, so the column means "the waybill this
-- report's parcel travels on" either way, and resolution_kind says which
-- direction that is.
UPDATE damage_reports
SET pickup_waybill = sqlc.arg('pickup_waybill'), updated_at = now()
WHERE id = sqlc.arg('id') AND brand_slug = sqlc.arg('brand_slug');

-- name: RequeueJobsForOrder :many
-- Puts an order's production jobs back in the print queue for a reprint.
--
-- The jobs ALREADY EXIST - the order was printed once - so this re-queues them
-- rather than creating a second set, which would double the work on the floor
-- and the count on every board that totals jobs per order.
--
-- Deliberately NOT touching a job already queued or in production: a reprint
-- decided while the original is still on a bed would otherwise reset a job
-- somebody is in the middle of printing.
UPDATE production_jobs
SET status = 'queued',
    assembly_status = CASE WHEN assembly_status = 'completed'
                           THEN 'pending' ELSE assembly_status END,
    qc_status = CASE WHEN qc_status = 'completed' THEN 'pending' ELSE qc_status END,
    packaging_status = CASE WHEN packaging_status = 'completed'
                            THEN 'pending' ELSE packaging_status END,
    batch_id = NULL,
    updated_at = now()
WHERE order_id = sqlc.arg('order_id')
  AND status IN ('completed', 'failed')
RETURNING id, job_number;

-- name: GetDamagePaymentProofFile :one
-- The payment screenshot a customer sent, if this report has one.
--
-- NOT REACHED THROUGH damage_files, unlike every other file on a report.
-- Those are the photographs of the breakage, uploaded through the public
-- form; this arrived later, over WhatsApp, and answers a different question -
-- "did they pay" rather than "what broke". Mixing it into the photo grid
-- would put a bank screenshot among pictures of a cracked diya holder.
--
-- THE JOIN IS STILL THE AUTHORISATION: the file has to be the one this
-- report points at, and the report has to belong to this brand.
SELECT f.id, f.filename, f.content_type, f.size_bytes, f.storage_key, f.created_at
FROM damage_reports r
JOIN file_assets f ON f.id = r.payment_proof_file_id
WHERE r.id = sqlc.arg('report_id')
  AND r.brand_slug = sqlc.arg('brand_slug');

-- name: DeleteDamageReport :execrows
-- Removes one report from the board.
--
-- ITS TIMELINE GOES WITH IT, by damage_stages.report_id ON DELETE CASCADE.
--
-- THE PHOTOGRAPHS DO NOT. They hang off the damage_link, not the report, and
-- that link expires on its own - so deleting a report does not reach into
-- object storage and does not race anything still uploading to the form.
--
-- Brand-scoped like every other read here: a report id on its own is not
-- authority to delete somebody else's row.
DELETE FROM damage_reports
WHERE id = sqlc.arg('id') AND brand_slug = sqlc.arg('brand_slug');
