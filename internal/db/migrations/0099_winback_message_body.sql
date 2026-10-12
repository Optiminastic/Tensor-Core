-- +goose Up
-- What the customer was actually sent, kept beside the fact that it was sent.
--
-- The ledger recorded a wamid and the word "accepted", which answers "did it
-- go" and nothing else. The question an operator actually arrives with is
-- "what did we say to this person" - before replying to them, before honouring
-- a discount they are quoting, before believing a complaint. Answering it
-- meant opening WhatsApp Manager for the copy, the ledger for the parameters
-- and Shopify for the code, and assembling the message by hand.
--
-- FROZEN AT SEND TIME, not rendered on read. The template copy lives at Meta
-- and is edited there; rendering the log from today's copy would redraw last
-- week's messages in this week's words, which is precisely the kind of quiet
-- rewriting a log exists to prevent. These two columns are what was composed,
-- and they do not change afterwards.
--
-- Nullable, because every row written before this migration has no body to
-- record and every voice row never will - a call has no text. A page that
-- shows nothing for those is correct; one that invented something would not
-- be.
ALTER TABLE abandoned_checkout_calls
    -- The body with its placeholders filled: the words, in order, as sent.
    ADD COLUMN IF NOT EXISTS message_body text,
    -- The recovery link behind the button, whole and tappable - host, discount
    -- code and the encoded redirect back to this customer's own basket. Stored
    -- separately from the body because Meta keeps the button out of the body
    -- text, so a reader who only had the body would see a message referring to
    -- a link that is not there.
    ADD COLUMN IF NOT EXISTS message_link text;

-- +goose Down
ALTER TABLE abandoned_checkout_calls DROP COLUMN IF EXISTS message_link;
ALTER TABLE abandoned_checkout_calls DROP COLUMN IF EXISTS message_body;
