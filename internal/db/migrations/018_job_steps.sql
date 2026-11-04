-- How each step of a job went, as its agent reports it: while it runs
-- (job_step messages) and, authoritatively, with the job's result. A job
-- that is claimed again starts with no rows.
CREATE TABLE job_steps (
    job_id      BIGINT      NOT NULL REFERENCES jobs (id) ON DELETE CASCADE,
    step        INT         NOT NULL CHECK (step >= 0),
    state       TEXT        NOT NULL CHECK (state IN ('pending', 'running', 'success', 'failed', 'canceled', 'skipped')),
    exit_code   INT,
    started_at  TIMESTAMPTZ,
    finished_at TIMESTAMPTZ,
    PRIMARY KEY (job_id, step)
);
