-- Names of the steps planned for a job, in order; log chunks and
-- failed_step refer to them by index.
ALTER TABLE jobs ADD COLUMN step_names TEXT[];
