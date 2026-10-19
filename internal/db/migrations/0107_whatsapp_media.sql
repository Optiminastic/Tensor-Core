-- +goose Up
-- The bytes a customer actually sent, not just the fact that they sent some.
--
-- A webhook carries a media ID, never the attachment. The parser did not even
-- keep that: rawMessage had no image/video/document field, so an inbound photo
-- was stored as kind='image', body='', and the inbox drew the literal text
-- "[image]". A damage report's photographs were therefore visible only by
-- opening the WhatsApp Business app on a phone - which the bot's own Shopify
-- note said out loud: "See the WhatsApp chat."
--
-- media_status is the whole state machine and it is deliberately a string, not
-- a boolean:
--   ''        this message has no attachment
--   pending   the id is stored, the download has not finished
--   stored    file_asset_id points at the bytes
--   failed    it will never arrive, and media_error says why
-- The difference between pending and failed is what lets a screen say "still
-- downloading" rather than "no photos", which are opposite answers to "did we
-- receive their pictures?".
--
-- media_id is kept after the download, not cleared. It is the only handle that
-- can ever fetch those bytes again, and Meta's url cannot be stored: it lapses
-- five minutes after it is issued.
ALTER TABLE whatsapp_messages
    ADD COLUMN IF NOT EXISTS media_id        varchar(256) NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS media_mime_type varchar(127) NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS media_sha256    varchar(64)  NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS media_status    varchar(16)  NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS media_error     text         NOT NULL DEFAULT '',
    -- ON DELETE SET NULL, not CASCADE: losing the file must not delete the
    -- message that proves the customer sent one.
    ADD COLUMN IF NOT EXISTS file_asset_id   uuid REFERENCES file_assets(id) ON DELETE SET NULL;

-- Partial, because the interesting set is tiny and shrinking: what is still
-- waiting for its bytes. Used to find downloads a lost deploy left behind.
CREATE INDEX IF NOT EXISTS ix_whatsapp_messages_media_pending
    ON whatsapp_messages (created_at) WHERE media_status = 'pending';

-- +goose Down
DROP INDEX IF EXISTS ix_whatsapp_messages_media_pending;
ALTER TABLE whatsapp_messages
    DROP COLUMN IF EXISTS file_asset_id,
    DROP COLUMN IF EXISTS media_error,
    DROP COLUMN IF EXISTS media_status,
    DROP COLUMN IF EXISTS media_sha256,
    DROP COLUMN IF EXISTS media_mime_type,
    DROP COLUMN IF EXISTS media_id;
