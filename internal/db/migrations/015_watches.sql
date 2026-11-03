-- Branches the server polls for new commits, for servers webhooks cannot
-- reach. next_poll_at moves on when a server claims a poll; last_sha moves
-- in the transaction that queues the build, so servers sharing the
-- database never build the same head twice.
CREATE TABLE watches (
    id           BIGSERIAL   PRIMARY KEY,
    repo         TEXT        NOT NULL,
    branch       TEXT        NOT NULL,
    clone_url    TEXT        NOT NULL,
    last_sha     TEXT        NOT NULL DEFAULT '', -- empty until the first poll
    next_poll_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_poll_at TIMESTAMPTZ,
    last_job_id  BIGINT      REFERENCES jobs (id) ON DELETE SET NULL,
    last_error   TEXT        NOT NULL DEFAULT '',
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (repo, branch)
);

CREATE INDEX watches_due_idx ON watches (next_poll_at);
