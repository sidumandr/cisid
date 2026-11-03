-- The matrix combination a job runs, and the job whose planning created it.
ALTER TABLE jobs
    ADD COLUMN matrix    JSONB,
    ADD COLUMN parent_id BIGINT REFERENCES jobs (id) ON DELETE SET NULL;

CREATE INDEX jobs_parent_idx ON jobs (parent_id) WHERE parent_id IS NOT NULL;
