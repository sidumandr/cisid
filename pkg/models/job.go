package models

import "time"

// JobStatus is the lifecycle state of a job.
type JobStatus string

const (
	JobStatusPending  JobStatus = "pending"
	JobStatusRunning  JobStatus = "running"
	JobStatusSuccess  JobStatus = "success"
	JobStatusFailed   JobStatus = "failed"
	JobStatusCanceled JobStatus = "canceled"
)

// Finished reports whether s is a terminal status.
func (s JobStatus) Finished() bool {
	return s == JobStatusSuccess || s == JobStatusFailed || s == JobStatusCanceled
}

// Job is a single pipeline run triggered by a push.
type Job struct {
	ID        int64     `json:"id"`
	Repo      string    `json:"repo"` // owner/name
	CloneURL  string    `json:"clone_url"`
	Branch    string    `json:"branch"`
	CommitSHA string    `json:"commit_sha"`
	Status    JobStatus `json:"status"`
	CreatedAt time.Time `json:"created_at"`

	// Set once the job has been claimed by an agent. The job goes back to
	// the queue if its lease expires without being renewed.
	Agent          string     `json:"agent,omitempty"`
	StartedAt      *time.Time `json:"started_at,omitempty"`
	LeaseExpiresAt *time.Time `json:"lease_expires_at,omitempty"`
	// StepNames lists the planned steps; FailedStep and JobLog.Step index it.
	StepNames []string `json:"steps,omitempty"`

	// Set once the job has finished.
	ExitCode   *int       `json:"exit_code,omitempty"`
	FailedStep *int       `json:"failed_step,omitempty"`
	Error      string     `json:"error,omitempty"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`
}
