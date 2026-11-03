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

// Job is a single pipeline run triggered by a push.
type Job struct {
	ID        int64     `json:"id"`
	Repo      string    `json:"repo"` // owner/name
	CloneURL  string    `json:"clone_url"`
	Branch    string    `json:"branch"`
	CommitSHA string    `json:"commit_sha"`
	Status    JobStatus `json:"status"`
	CreatedAt time.Time `json:"created_at"`
}
