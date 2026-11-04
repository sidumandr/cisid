package db

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/sidumandr/cisid/pkg/models"
)

// maxStepIndex bounds the step index an agent may report: the clone step
// and at most 50 pipeline steps, with room to spare.
const maxStepIndex = 100

// RecordStep stores that a step of a running job started or ended, and
// notifies JobEventsChannel. An end is never overwritten by a late start,
// so resent messages are harmless.
func (s *Store) RecordStep(ctx context.Context, st models.JobStep) error {
	if st.Step < 0 || st.Step > maxStepIndex {
		return fmt.Errorf("%w: job %d step %d", ErrInvalidJobResult, st.JobID, st.Step)
	}
	const q = `
		WITH recorded AS (
			INSERT INTO job_steps (job_id, step, state, exit_code, started_at, finished_at)
			SELECT $1, $2, $3, $4, $5, $6
			WHERE EXISTS (SELECT 1 FROM jobs WHERE id = $1 AND status = 'running')
			ON CONFLICT (job_id, step) DO UPDATE SET
				state = EXCLUDED.state, exit_code = EXCLUDED.exit_code,
				started_at = COALESCE(EXCLUDED.started_at, job_steps.started_at),
				finished_at = EXCLUDED.finished_at
			WHERE job_steps.finished_at IS NULL OR EXCLUDED.finished_at IS NOT NULL
			RETURNING job_id
		)
		SELECT pg_notify('` + JobEventsChannel + `', job_id::text) FROM recorded`
	if _, err := s.pool.Exec(ctx, q, st.JobID, st.Step, st.State, st.ExitCode, st.StartedAt, st.FinishedAt); err != nil {
		return fmt.Errorf("record step %d of job %d: %w", st.Step, st.JobID, err)
	}
	return nil
}

// replaceSteps stores the step results a job finished with, replacing what
// was reported while it ran.
func replaceSteps(ctx context.Context, tx pgx.Tx, jobID int64, steps []models.StepResult) error {
	if len(steps) > maxStepIndex+1 {
		return fmt.Errorf("%w: job %d reports %d steps", ErrInvalidJobResult, jobID, len(steps))
	}
	if _, err := tx.Exec(ctx, "DELETE FROM job_steps WHERE job_id = $1", jobID); err != nil {
		return fmt.Errorf("clear steps of job %d: %w", jobID, err)
	}
	const q = `INSERT INTO job_steps (job_id, step, state, exit_code, started_at, finished_at) VALUES ($1, $2, $3, $4, $5, $6)`
	batch := &pgx.Batch{}
	for i, st := range steps {
		batch.Queue(q, jobID, i, st.State, st.ExitCode, st.StartedAt, st.FinishedAt)
	}
	if err := tx.SendBatch(ctx, batch).Close(); err != nil {
		return fmt.Errorf("store steps of job %d: %w", jobID, err)
	}
	return nil
}

// fillStepResults sets StepResults of the jobs whose agents reported
// steps: one entry per planned step, pending (or, once the job finished,
// skipped) where nothing was reported.
func (s *Store) fillStepResults(ctx context.Context, jobs []models.Job) error {
	if len(jobs) == 0 {
		return nil
	}
	ids := make([]int64, len(jobs))
	for i, j := range jobs {
		ids[i] = j.ID
	}
	rows, err := s.pool.Query(ctx, `
		SELECT job_id, step, state, exit_code, started_at, finished_at FROM job_steps
		WHERE job_id = ANY($1) ORDER BY job_id, step`, ids)
	if err != nil {
		return fmt.Errorf("read job steps: %w", err)
	}
	type row struct {
		jobID int64
		step  int
		res   models.StepResult
	}
	list, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (row, error) {
		var x row
		err := r.Scan(&x.jobID, &x.step, &x.res.State, &x.res.ExitCode, &x.res.StartedAt, &x.res.FinishedAt)
		return x, err
	})
	if err != nil {
		return fmt.Errorf("read job steps: %w", err)
	}

	byJob := map[int64][]row{}
	for _, x := range list {
		byJob[x.jobID] = append(byJob[x.jobID], x)
	}
	for i := range jobs {
		reported := byJob[jobs[i].ID]
		if len(reported) == 0 {
			continue
		}
		n := len(jobs[i].StepNames)
		if last := reported[len(reported)-1].step; last >= n {
			n = last + 1
		}
		missing := models.StepPending
		if jobs[i].Status.Finished() {
			missing = models.StepSkipped
		}
		results := make([]models.StepResult, n)
		for k := range results {
			results[k].State = missing
		}
		for _, x := range reported {
			results[x.step] = x.res
		}
		jobs[i].StepResults = results
	}
	return nil
}
