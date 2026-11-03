-- Branches a secret is limited to, as glob patterns like when.branch. Such
-- a secret only goes to jobs on those branches, never to pull requests.
-- NULL gives it to every branch.
ALTER TABLE secrets ADD COLUMN branches TEXT[];
