-- Accounts: users with a role, signed in with a password, through a forge
-- (identities) or with personal API tokens. Sessions and tokens are kept
-- only as SHA-256 digests.
CREATE TABLE users (
    id            BIGSERIAL   PRIMARY KEY,
    username      TEXT        NOT NULL UNIQUE,
    role          TEXT        NOT NULL CHECK (role IN ('viewer', 'maintainer', 'admin')),
    password_hash TEXT        NOT NULL DEFAULT '', -- empty: no password sign-in
    disabled      BOOLEAN     NOT NULL DEFAULT false,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- A user's account on a forge. An admin may name the login ahead of the
-- first sign-in; the forge's stable user id (subject) is bound then.
CREATE TABLE user_identities (
    user_id BIGINT NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    forge   TEXT   NOT NULL,
    login   TEXT   NOT NULL,
    subject TEXT   NOT NULL DEFAULT '',
    PRIMARY KEY (user_id, forge),
    UNIQUE (forge, login)
);

CREATE UNIQUE INDEX user_identities_subject_idx ON user_identities (forge, subject) WHERE subject <> '';

CREATE TABLE sessions (
    digest     TEXT        PRIMARY KEY,
    user_id    BIGINT      NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at TIMESTAMPTZ NOT NULL
);

CREATE INDEX sessions_expiry_idx ON sessions (expires_at);

CREATE TABLE api_tokens (
    id           BIGSERIAL   PRIMARY KEY,
    user_id      BIGINT      NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    name         TEXT        NOT NULL,
    digest       TEXT        NOT NULL UNIQUE,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_used_at TIMESTAMPTZ
);
