-- Repositories someone connected to cisid: they are listed, and can be
-- polled and built, before any webhook arrives for them.
CREATE TABLE repositories (
    repo           TEXT        PRIMARY KEY, -- the repository key: owner/name, or forge:path
    clone_url      TEXT        NOT NULL,
    default_branch TEXT        NOT NULL,
    added_by       TEXT        NOT NULL DEFAULT '',
    added_at       TIMESTAMPTZ NOT NULL DEFAULT now()
);
