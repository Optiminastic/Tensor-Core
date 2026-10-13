-- +goose Up
-- The link itself, recoverable for exactly as long as it is useful.
--
-- The token is stored as a SHA-256 hash so that a database dump opens nobody's
-- order, and that remains true: this column is the same token sealed with
-- TOKEN_ENCRYPTION_KEY, which is the treatment every other recoverable secret
-- in Tensor already gets - a Shopify access token, an integration's API key.
-- A dump still opens nothing without the key.
--
-- Why keep it at all: somebody on the floor takes the phone call. A customer
-- rings about a name while their window is open, and the person looking at the
-- job needs to be able to send them back to the page they are describing. The
-- alternative is minting a second link, which silently kills the one the
-- customer already has open in front of them.
--
-- CLEARED WHEN THE WINDOW CLOSES. The expiry sweep and the confirm both null
-- it, so the recoverable copy exists only while the link works - a closed
-- window leaves a row that records what happened and nothing that can be
-- replayed. That is the whole reason this is a separate column from the hash
-- rather than a change to it: the hash outlives the link, and this must not.
ALTER TABLE personalisation_links
    ADD COLUMN IF NOT EXISTS token_sealed text;

-- +goose Down
ALTER TABLE personalisation_links DROP COLUMN IF EXISTS token_sealed;
