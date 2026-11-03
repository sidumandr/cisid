-- Per-repository secrets. value is sealed with AES-256-GCM (nonce first);
-- the plaintext never reaches the database.
CREATE TABLE secrets (
    repo       TEXT        NOT NULL,
    name       TEXT        NOT NULL,
    value      BYTEA       NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (repo, name)
);
