ALTER TABLE jobs ADD COLUMN lease_expires_at TIMESTAMPTZ;

UPDATE jobs SET lease_expires_at = now() + interval '5 minutes' WHERE status = 'running';

CREATE INDEX jobs_running_lease_idx ON jobs (lease_expires_at) WHERE status = 'running';
