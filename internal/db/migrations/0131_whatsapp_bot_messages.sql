-- +goose Up
-- The shop's own wording for the bot's one-off messages.
--
-- AN OVERLAY, exactly like whatsapp_bot_nodes: a row REPLACES the constant
-- compiled into the binary, and no row means the binary's wording is used. So
-- a brand that never opens the page behaves as it always did, and deleting a
-- row undoes a bad edit without a deploy. That is what makes this safe to put
-- in front of a live bot.
--
-- NOT THE TREE. whatsapp_bot_nodes holds the menu - a body with options under
-- it, and a shape. These are the lines the bot composes on its own: the Rs 100
-- ask, "your pickup is booked", "type MENU". They have no options and no
-- shape, so they are a flat key/value rather than a second tree.
--
-- message_key IS THE CODE'S, NOT THE USER'S. It matches content.Notices(), and
-- a row whose key no longer exists there is simply never read - harmless, and
-- recoverable if the key comes back.
CREATE TABLE IF NOT EXISTS whatsapp_bot_messages (
    brand_slug  varchar(64) NOT NULL REFERENCES brands(slug) ON DELETE CASCADE,
    message_key varchar(64) NOT NULL,
    body        text        NOT NULL,
    updated_by  text,
    updated_at  timestamptz NOT NULL DEFAULT now(),
    created_at  timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (brand_slug, message_key)
);

-- Every read is "this brand's wording", so the primary key already serves it.
-- No second index is added for that reason.

-- +goose Down
DROP TABLE IF EXISTS whatsapp_bot_messages;
