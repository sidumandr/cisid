// Package trigger starts builds without a push: by hand through the API,
// and on cron schedules kept in the database.
package trigger

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/robfig/cron/v3"

	"github.com/sidumandr/cisid/internal/github"
	"github.com/sidumandr/cisid/pkg/models"
)

// ErrInvalidCron is returned for a schedule that cannot be parsed.
var ErrInvalidCron = errors.New("invalid cron schedule")

// Next returns the first time after after that spec matches. spec has the
// five standard fields ("0 3 * * 1-5"), or is a descriptor like "@daily";
// it is in UTC unless prefixed with a zone, as in
// "CRON_TZ=Europe/Istanbul 0 3 * * *".
func Next(spec string, after time.Time) (time.Time, error) {
	s, err := cron.ParseStandard(spec)
	if err != nil {
		return time.Time{}, fmt.Errorf("%w: %w", ErrInvalidCron, err)
	}
	next := s.Next(after)
	if next.IsZero() {
		return time.Time{}, fmt.Errorf("%w: %q never matches", ErrInvalidCron, spec)
	}
	return next, nil
}

// Resolver finds what to build for a branch. *github.Client satisfies it.
type Resolver interface {
	Repository(ctx context.Context, repo string) (github.Repository, error)
	BranchHead(ctx context.Context, repo, branch string) (string, error)
}

var _ Resolver = (*github.Client)(nil)

// BranchJob returns a pending job, triggered by event, for the head commit
// of branch in repo, or of the default branch when branch is empty.
func BranchJob(ctx context.Context, r Resolver, repo, branch, event string) (models.Job, error) {
	info, err := r.Repository(ctx, repo)
	if err != nil {
		return models.Job{}, err
	}
	if branch == "" {
		branch = info.DefaultBranch
	}
	sha, err := r.BranchHead(ctx, repo, branch)
	if err != nil {
		return models.Job{}, err
	}
	return models.Job{
		Repo:      repo,
		CloneURL:  info.CloneURL,
		Branch:    branch,
		CommitSHA: sha,
		Status:    models.JobStatusPending,
		Event:     event,
	}, nil
}
