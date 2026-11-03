-- Set when a running job should be stopped; the agent's server delivers it.
ALTER TABLE jobs ADD COLUMN cancel_requested_at TIMESTAMPTZ;

CREATE INDEX jobs_cancel_requested_idx ON jobs (agent)
    WHERE status = 'running' AND cancel_requested_at IS NOT NULL;
