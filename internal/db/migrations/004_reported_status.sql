-- The job status last reported to GitHub as a commit status.
ALTER TABLE jobs ADD COLUMN reported_status TEXT;

-- Do not report jobs that existed before commit statuses were introduced.
UPDATE jobs SET reported_status = status;

CREATE INDEX jobs_unreported_idx ON jobs (id) WHERE reported_status IS DISTINCT FROM status;
