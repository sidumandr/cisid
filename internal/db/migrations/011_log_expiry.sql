-- When a finished job's logs were deleted after the retention period
-- (CISID_LOG_RETENTION). The job itself is kept.
ALTER TABLE jobs ADD COLUMN logs_expired_at TIMESTAMPTZ;

CREATE INDEX jobs_logs_expiry_idx ON jobs (finished_at)
    WHERE finished_at IS NOT NULL AND logs_expired_at IS NULL;
