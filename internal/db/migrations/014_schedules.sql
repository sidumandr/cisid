-- Cron schedules that queue a build of a branch's head (event 'schedule').
-- next_run_at moves on when a server claims a run, so servers sharing the
-- database never queue the same run twice.
CREATE TABLE schedules (
    id          BIGSERIAL   PRIMARY KEY,
    repo        TEXT        NOT NULL,
    name        TEXT        NOT NULL,
    cron        TEXT        NOT NULL,
    branch      TEXT        NOT NULL DEFAULT '', -- empty: the default branch
    next_run_at TIMESTAMPTZ NOT NULL,
    last_run_at TIMESTAMPTZ,
    last_job_id BIGINT      REFERENCES jobs (id) ON DELETE SET NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (repo, name)
);

CREATE INDEX schedules_due_idx ON schedules (next_run_at);
