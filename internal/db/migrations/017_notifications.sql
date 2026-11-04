-- Where a repository's build results are announced: a Slack-compatible
-- chat, a webhook or email. target holds the URL or the addresses, sealed
-- with CISID_SECRET_KEY like secrets; hint is what may be shown instead
-- (the URL's host, or the addresses).
CREATE TABLE notifications (
    id            BIGSERIAL   PRIMARY KEY,
    repo          TEXT        NOT NULL,
    name          TEXT        NOT NULL,
    kind          TEXT        NOT NULL CHECK (kind IN ('slack', 'webhook', 'email')),
    target        BYTEA       NOT NULL,
    hint          TEXT        NOT NULL,
    events        TEXT[]      NOT NULL, -- failed, broken, fixed, passed
    branches      TEXT[]      NOT NULL DEFAULT '{}', -- glob patterns; empty: every branch
    pull_requests BOOLEAN     NOT NULL DEFAULT false,
    last_sent_at  TIMESTAMPTZ,
    last_error    TEXT        NOT NULL DEFAULT '',
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (repo, name)
);

-- When a server took the job's result to announce it. A server claims a
-- finished job by setting this, so servers sharing the database announce
-- each result once. Jobs that finished before notifications existed are
-- not announced.
ALTER TABLE jobs ADD COLUMN notified_at TIMESTAMPTZ;
UPDATE jobs SET notified_at = now() WHERE status IN ('success', 'failed', 'canceled');
CREATE INDEX jobs_unnotified_idx ON jobs (id) WHERE notified_at IS NULL;

-- The latest builds of a branch, for badges and for telling whether a
-- result is a change.
CREATE INDEX jobs_branch_idx ON jobs (repo, branch, id) WHERE event <> 'pull_request';
