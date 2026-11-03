CREATE TABLE IF NOT EXISTS jobs (
    id          BIGSERIAL   PRIMARY KEY,
    repo        TEXT        NOT NULL,
    clone_url   TEXT        NOT NULL,
    branch      TEXT        NOT NULL,
    commit_sha  TEXT        NOT NULL,
    status      TEXT        NOT NULL DEFAULT 'pending'
                CHECK (status IN ('pending', 'running', 'success', 'failed', 'canceled')),
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS jobs_status_created_at_idx ON jobs (status, created_at);
