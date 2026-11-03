ALTER TABLE jobs
    ADD COLUMN agent       TEXT,
    ADD COLUMN exit_code   INT,
    ADD COLUMN failed_step INT,
    ADD COLUMN error       TEXT,
    ADD COLUMN started_at  TIMESTAMPTZ,
    ADD COLUMN finished_at TIMESTAMPTZ;

CREATE INDEX jobs_running_agent_idx ON jobs (agent) WHERE status = 'running';

CREATE TABLE job_logs (
    job_id     BIGINT      NOT NULL REFERENCES jobs (id) ON DELETE CASCADE,
    seq        BIGINT      NOT NULL,
    step       INT         NOT NULL,
    stream     TEXT        NOT NULL CHECK (stream IN ('stdout', 'stderr')),
    data       TEXT        NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (job_id, seq)
);
