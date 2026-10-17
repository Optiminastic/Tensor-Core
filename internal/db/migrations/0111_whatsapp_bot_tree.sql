-- +goose Up
-- The bot as a tree, so a button can be added and told what to do.
--
-- 0109 made the WORDS editable: a button's label, a question and its answer.
-- It could not make the SHAPE editable. The three main-menu buttons were
-- fixed because the action behind each one was a case in a switch, so "add a
-- button that opens our returns policy" was still a code change, and the one
-- list of questions was the only second level there could ever be.
--
-- A node is a message with options under it; an option carries an action.
-- "Explore products opens the website" and "Other opens the questions list"
-- become rows rather than code.
--
-- THREE ACTIONS STAY IN CODE: damage, track and team each change the
-- conversation's state, and state belongs to the pure state machine, which
-- cannot read a table. Their option ids are reserved so the machine still
-- recognises them; everything about where they appear and what they are
-- called is editable. See internal/whatsappbot/content/tree.go.
CREATE TABLE IF NOT EXISTS whatsapp_bot_nodes (
    brand_slug  varchar(64) NOT NULL REFERENCES brands(slug) ON DELETE CASCADE,
    node_key    varchar(64) NOT NULL,
    -- 'buttons' (at most 3, shown under the message) or 'list' (at most 10
    -- rows behind one button). Meta's limits, and the reason both exist: a
    -- fourth button is not something WhatsApp can send.
    kind        varchar(16) NOT NULL DEFAULT 'buttons',
    body        text NOT NULL DEFAULT '',
    -- The word on the button that opens a list, e.g. "See questions".
    -- Unused by a buttons node. Meta caps it at 20 characters.
    list_button varchar(24) NOT NULL DEFAULT '',
    position    integer NOT NULL DEFAULT 0,
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (brand_slug, node_key)
);

CREATE TABLE IF NOT EXISTS whatsapp_bot_options (
    brand_slug  varchar(64) NOT NULL REFERENCES brands(slug) ON DELETE CASCADE,
    node_key    varchar(64) NOT NULL,
    -- What WhatsApp hands back when the customer taps. Reserved for a code
    -- action ('damage', 'track', 'team'); the shop's own otherwise.
    option_key  varchar(64) NOT NULL,
    -- Button label or list-row title. Meta caps these at 20 and 24 and
    -- refuses the WHOLE message over one that is too long, naming neither
    -- the row nor the rule - so the API checks them.
    title       varchar(64) NOT NULL DEFAULT '',
    description varchar(96) NOT NULL DEFAULT '',
    -- 'answer' | 'open' | 'url' | 'damage' | 'track' | 'team'
    action      varchar(16) NOT NULL DEFAULT 'answer',
    -- The message sent for action 'answer'. Also the lead-in text for 'url',
    -- which sends a message with a link button under it.
    answer      text NOT NULL DEFAULT '',
    -- The node key for 'open', or the web address for 'url'.
    target      varchar(500) NOT NULL DEFAULT '',
    -- What the link button says for 'url'. Meta caps it at 20 characters.
    button_label varchar(24) NOT NULL DEFAULT '',
    position    integer NOT NULL DEFAULT 0,
    -- Off rather than deleted, so a seasonal option can come back without
    -- being retyped.
    enabled     boolean NOT NULL DEFAULT true,
    updated_by  varchar(64),
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (brand_slug, node_key, option_key),
    FOREIGN KEY (brand_slug, node_key)
        REFERENCES whatsapp_bot_nodes (brand_slug, node_key) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS ix_whatsapp_bot_options_order
    ON whatsapp_bot_options (brand_slug, node_key, position);

-- Carry over anything a brand already wrote under 0109.
--
-- Only for a brand that has rows: one with none keeps behaving as the Go
-- defaults describe, which is the overlay rule this table inherits. The old
-- model had exactly two levels, so the shape of the carry-over is fixed - the
-- menu becomes the root node and the questions become the list behind it.
-- +goose StatementBegin
DO $$
DECLARE
    b record;
BEGIN
    IF to_regclass('public.whatsapp_bot_content') IS NULL THEN
        RETURN;
    END IF;

    FOR b IN SELECT DISTINCT brand_slug FROM whatsapp_bot_content LOOP
        INSERT INTO whatsapp_bot_nodes (brand_slug, node_key, kind, body, list_button, position)
        VALUES (b.brand_slug, 'main', 'buttons', '', '', 0),
               (b.brand_slug, 'questions', 'list', '', 'See questions', 1)
        ON CONFLICT DO NOTHING;

        -- Menu rows become the root's options. The action is recovered from
        -- the id, which is all the old model recorded: its behaviour WAS the
        -- id. An id the code never knew becomes a plain answer.
        INSERT INTO whatsapp_bot_options (
            brand_slug, node_key, option_key, title, action, target, position, enabled
        )
        SELECT b.brand_slug, 'main', c.content_key, c.title,
               CASE c.content_key
                   WHEN 'damage' THEN 'damage'
                   WHEN 'track'  THEN 'track'
                   WHEN 'team'   THEN 'team'
                   WHEN 'other'  THEN 'open'
                   WHEN 'explore' THEN 'url'
                   ELSE 'answer'
               END,
               CASE c.content_key
                   WHEN 'other'   THEN 'questions'
                   WHEN 'explore' THEN 'https://the3dprintingstore.in/'
                   ELSE ''
               END,
               c.position, c.enabled
        FROM whatsapp_bot_content c
        WHERE c.brand_slug = b.brand_slug AND c.kind = 'menu'
        ON CONFLICT DO NOTHING;

        INSERT INTO whatsapp_bot_options (
            brand_slug, node_key, option_key, title, description, action, answer, target, position, enabled
        )
        SELECT b.brand_slug, 'questions', c.content_key, c.title, c.description,
               CASE c.content_key
                   WHEN 'track' THEN 'track'
                   WHEN 'team'  THEN 'team'
                   ELSE 'answer'
               END,
               c.answer,
               '',
               c.position, c.enabled
        FROM whatsapp_bot_content c
        WHERE c.brand_slug = b.brand_slug AND c.kind = 'faq'
        ON CONFLICT DO NOTHING;
    END LOOP;
END $$;
-- +goose StatementEnd

DROP TABLE IF EXISTS whatsapp_bot_content;

-- +goose Down
CREATE TABLE IF NOT EXISTS whatsapp_bot_content (
    brand_slug  varchar(64) NOT NULL REFERENCES brands(slug) ON DELETE CASCADE,
    kind        varchar(16) NOT NULL,
    content_key varchar(64) NOT NULL,
    title       varchar(64) NOT NULL DEFAULT '',
    description varchar(96) NOT NULL DEFAULT '',
    answer      text NOT NULL DEFAULT '',
    position    integer NOT NULL DEFAULT 0,
    enabled     boolean NOT NULL DEFAULT true,
    updated_by  varchar(64),
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (brand_slug, kind, content_key)
);
DROP TABLE IF EXISTS whatsapp_bot_options;
DROP TABLE IF EXISTS whatsapp_bot_nodes;
