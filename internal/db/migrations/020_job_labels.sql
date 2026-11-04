-- The labels an agent needs to run the job, from the pipeline's labels.
-- NULL until the job is planned on an agent that lacks them: it then goes
-- back to the queue with them, and only agents that have every one of
-- them claim it.
ALTER TABLE jobs ADD COLUMN labels TEXT[];
