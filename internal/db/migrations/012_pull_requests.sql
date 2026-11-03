-- What triggered a job. Pull request jobs also record the pull request's
-- number, the branch it targets and whether its changes come from a fork,
-- which gets no secrets.
ALTER TABLE jobs
    ADD COLUMN event         TEXT    NOT NULL DEFAULT 'push',
    ADD COLUMN pull_request  INT,
    ADD COLUMN target_branch TEXT,
    ADD COLUMN fork          BOOLEAN NOT NULL DEFAULT false;

-- Finds a pull request's earlier jobs to cancel when it changes.
CREATE INDEX jobs_pull_request_idx ON jobs (repo, pull_request)
    WHERE pull_request IS NOT NULL AND status IN ('pending', 'running');
