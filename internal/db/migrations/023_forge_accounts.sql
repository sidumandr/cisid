-- Users' own access to their forges: what the repositories they connect
-- are polled, cloned and reported with. The tokens are sealed with
-- CISID_SECRET_KEY; refresh tokens and expiry are for OAuth tokens that
-- expire, as GitLab's do.
CREATE TABLE forge_accounts (
    user_id    BIGINT      NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    forge      TEXT        NOT NULL,
    login      TEXT        NOT NULL,
    kind       TEXT        NOT NULL, -- oauth or token
    token      BYTEA       NOT NULL,
    refresh    BYTEA,
    expires_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, forge)
);

-- Whose access a connected repository is reached with; the server's
-- token for its forge when empty.
ALTER TABLE repositories ADD COLUMN via_user_id BIGINT REFERENCES users (id) ON DELETE SET NULL;
