package models

import (
	"maps"
	"slices"
	"strings"
	"time"
)

// JobStatus is the lifecycle state of a job.
type JobStatus string

const (
	JobStatusPending  JobStatus = "pending"
	JobStatusRunning  JobStatus = "running"
	JobStatusSuccess  JobStatus = "success"
	JobStatusFailed   JobStatus = "failed"
	JobStatusCanceled JobStatus = "canceled"
)

// Events that trigger jobs.
const (
	EventPush        = "push"
	EventPullRequest = "pull_request"
	// EventManual is a build started through the API, EventSchedule one
	// started by a schedule; both build the head of a branch.
	EventManual   = "manual"
	EventSchedule = "schedule"
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

	// Event is what triggered the job, one of the Event constants.
	Event string `json:"event"`
	// For pull requests: the number, the branch it is to be merged into,
	// and whether the changes come from a fork. Branch is the pull
	// request's own branch and CommitSHA its head commit.
	PullRequest  int    `json:"pull_request,omitempty"`
	TargetBranch string `json:"target_branch,omitempty"`
	Fork         bool   `json:"fork,omitempty"`

	// Set once the job has been claimed by an agent. The job goes back to
	// the queue if its lease expires without being renewed.
	Agent          string     `json:"agent,omitempty"`
	StartedAt      *time.Time `json:"started_at,omitempty"`
	LeaseExpiresAt *time.Time `json:"lease_expires_at,omitempty"`
	// StepNames lists the planned steps; FailedStep and JobLog.Step index it.
	StepNames []string `json:"steps,omitempty"`

	// Matrix is the combination of matrix values this job runs; ParentID
	// is the job whose planning created it, for all but the first.
	Matrix   map[string]string `json:"matrix,omitempty"`
	ParentID *int64            `json:"parent_id,omitempty"`

	// TraceParent is the W3C traceparent of the span that queued the job;
	// dispatching and running it continue that trace. Empty without tracing.
	TraceParent string `json:"-"`

	// Set once the job has finished.
	ExitCode   *int       `json:"exit_code,omitempty"`
	FailedStep *int       `json:"failed_step,omitempty"`
	Error      string     `json:"error,omitempty"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`
	// LogsExpiredAt is when the logs were deleted after the retention period.
	LogsExpiredAt *time.Time `json:"logs_expired_at,omitempty"`
	// CurrentStep is, for a running job, the step that wrote output last.
	CurrentStep *int `json:"current_step,omitempty"`
	// StepResults says how each step in StepNames went, by index; absent
	// for jobs run by agents that do not report steps.
	StepResults []StepResult `json:"step_results,omitempty"`
	// Labels are what an agent needs to run the job, once a planning found
	// that the agent it was planned on lacks them.
	Labels []string `json:"labels,omitempty"`
	// NeedsApproval holds a pending job from a fork until a maintainer
	// approves it; ApprovedBy is who did.
	NeedsApproval bool   `json:"needs_approval,omitempty"`
	ApprovedBy    string `json:"approved_by,omitempty"`
}

// Schedule builds the head of a branch at the times its cron spec matches.
type Schedule struct {
	ID   int64  `json:"-"`
	Repo string `json:"repo"`
	Name string `json:"name"`
	Cron string `json:"cron"`
	// Branch is empty for the repository's default branch.
	Branch    string     `json:"branch,omitempty"`
	NextRunAt time.Time  `json:"next_run_at"`
	LastRunAt *time.Time `json:"last_run_at,omitempty"`
	LastJobID *int64     `json:"last_job_id,omitempty"`
}

// Watch is a branch the server polls, building each new head commit.
type Watch struct {
	ID       int64  `json:"-"`
	Repo     string `json:"repo"`
	Branch   string `json:"branch"`
	CloneURL string `json:"-"`
	// LastSHA is the head last seen; empty until the first poll, which
	// builds the head the branch has then.
	LastSHA    string     `json:"last_sha,omitempty"`
	LastPollAt *time.Time `json:"last_poll_at,omitempty"`
	LastJobID  *int64     `json:"last_job_id,omitempty"`
	// LastError is why the last poll failed, empty after a good one.
	LastError string    `json:"last_error,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

// User is an account. Its password hash never leaves the store.
type User struct {
	ID       int64  `json:"-"`
	Username string `json:"username"`
	Role     string `json:"role"`
	Disabled bool   `json:"disabled,omitempty"`
	// HasPassword is false for users who only sign in through a forge.
	HasPassword bool `json:"has_password"`
	// Identities are the user's forge logins, by forge name.
	Identities map[string]string `json:"identities,omitempty"`
	CreatedAt  time.Time         `json:"created_at"`
}

// APIToken describes a personal API token without the token.
type APIToken struct {
	ID         int64      `json:"id"`
	Name       string     `json:"name"`
	CreatedAt  time.Time  `json:"created_at"`
	LastUsedAt *time.Time `json:"last_used_at,omitempty"`
}

// SecretInfo describes a repository secret without its value.
type SecretInfo struct {
	Name      string    `json:"name"`
	UpdatedAt time.Time `json:"updated_at"`
	// Branches limits the secret to pushes to these branches; empty
	// means every branch.
	Branches []string `json:"branches,omitempty"`
}

// MatrixLabel renders a matrix combination as "A=1, B=2", axes in name
// order; empty for no matrix.
func MatrixLabel(m map[string]string) string {
	parts := make([]string, 0, len(m))
	for _, k := range slices.Sorted(maps.Keys(m)) {
		parts = append(parts, k+"="+m[k])
	}
	return strings.Join(parts, ", ")
}
