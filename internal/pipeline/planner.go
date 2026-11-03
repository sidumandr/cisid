// Package pipeline turns a job into the steps an agent runs.
package pipeline

import (
	"context"
	"errors"
	"fmt"

	"github.com/sidumandr/cisid/pkg/models"
)

// DefaultCloneImage runs the clone step. It needs /bin/sh and git.
const DefaultCloneImage = "alpine/git:2.52.0"

const cloneTimeoutSeconds = 600

// ErrInvalidJob is returned for a job that lacks what planning needs.
var ErrInvalidJob = errors.New("invalid job")

// Planner builds the steps of a job.
//
// For now every job consists of a single clone step; reading the
// repository's .cisid.yml and appending its steps comes next.
type Planner struct {
	// CloneImage overrides DefaultCloneImage when set.
	CloneImage string
}

// Plan returns the steps for job.
func (p Planner) Plan(ctx context.Context, job models.Job) ([]models.StepSpec, error) {
	switch {
	case job.CloneURL == "":
		return nil, fmt.Errorf("%w %d: clone URL is empty", ErrInvalidJob, job.ID)
	case job.CommitSHA == "":
		return nil, fmt.Errorf("%w %d: commit SHA is empty", ErrInvalidJob, job.ID)
	case job.Branch == "":
		return nil, fmt.Errorf("%w %d: branch is empty", ErrInvalidJob, job.ID)
	}
	return []models.StepSpec{p.cloneStep(job)}, nil
}

// cloneStep fetches exactly the pushed commit into the workspace. Values
// from the webhook payload are passed as environment variables and only
// referenced quoted in the script, so they cannot inject shell code.
func (p Planner) cloneStep(job models.Job) models.StepSpec {
	image := p.CloneImage
	if image == "" {
		image = DefaultCloneImage
	}
	return models.StepSpec{
		Name:  "clone",
		Image: image,
		Commands: []string{
			"git init -q .",
			`git remote add origin "$CISID_CLONE_URL"`,
			`git fetch -q --depth=1 origin "$CISID_COMMIT_SHA"`,
			`git checkout -q -B "$CISID_BRANCH" FETCH_HEAD`,
			"git log -1 --format='%H %s'",
		},
		Env: map[string]string{
			"CISID_REPO":          job.Repo,
			"CISID_CLONE_URL":     job.CloneURL,
			"CISID_BRANCH":        job.Branch,
			"CISID_COMMIT_SHA":    job.CommitSHA,
			"GIT_TERMINAL_PROMPT": "0",
		},
		TimeoutSeconds: cloneTimeoutSeconds,
	}
}
