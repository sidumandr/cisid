-- Jobs from forks a maintainer must approve before any agent runs them
-- (CISID_BUILD_FORKS=approve), and who approved them.
ALTER TABLE jobs
    ADD COLUMN needs_approval BOOLEAN NOT NULL DEFAULT false,
    ADD COLUMN approved_by    TEXT,
    ADD COLUMN approved_at    TIMESTAMPTZ;
