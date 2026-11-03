// Package pipeline turns a job into the steps an agent runs: a clone step
// followed by the steps of the repository's .cisid.yml.
package pipeline

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"

	"github.com/sidumandr/cisid/internal/github"
	"github.com/sidumandr/cisid/internal/secrets"
	"github.com/sidumandr/cisid/pkg/models"
)

// DefaultCloneImage runs the clone step. It needs /bin/sh and git.
const DefaultCloneImage = "alpine/git:2.52.0"

const (
	cloneStepName       = "clone"
	cloneTimeoutSeconds = 600
)

var (
	// ErrInvalidJob is returned for a job that lacks what planning needs.
	ErrInvalidJob = errors.New("invalid job")
	// ErrNoConfig is returned when the commit has no .cisid.yml.
	ErrNoConfig = errors.New("no " + ConfigFile + " in repository")
	// ErrTemporary marks planning failures worth retrying later, such as
	// GitHub being unreachable or rate limited.
	ErrTemporary = errors.New("temporary planning failure")
)

// ConfigSource reads a file from a repository at a commit.
// *github.Client satisfies it.
type ConfigSource interface {
	FileContent(ctx context.Context, repo, ref, path string) ([]byte, error)
}

var _ ConfigSource = (*github.Client)(nil)

// SecretSource returns the values of repository secrets.
// *secrets.Vault satisfies it.
type SecretSource interface {
	Secrets(ctx context.Context, repo string, names []string) (map[string]string, error)
}

var _ SecretSource = (*secrets.Vault)(nil)

// Planner builds the steps of a job.
type Planner struct {
	// CloneImage overrides DefaultCloneImage when set.
	CloneImage string
	// Configs reads .cisid.yml. When nil, a job only clones the repository.
	Configs ConfigSource
	// Secrets provides the secrets steps ask for. When nil, a pipeline that
	// uses secrets fails.
	Secrets SecretSource
}

// Plan returns the steps for job: the clone step, then the steps of the
// .cisid.yml at the pushed commit.
func (p Planner) Plan(ctx context.Context, job models.Job) ([]models.StepSpec, error) {
	switch {
	case job.CloneURL == "":
		return nil, fmt.Errorf("%w %d: clone URL is empty", ErrInvalidJob, job.ID)
	case job.CommitSHA == "":
		return nil, fmt.Errorf("%w %d: commit SHA is empty", ErrInvalidJob, job.ID)
	case job.Branch == "":
		return nil, fmt.Errorf("%w %d: branch is empty", ErrInvalidJob, job.ID)
	}

	steps := []models.StepSpec{p.cloneStep(job)}
	if p.Configs == nil {
		return steps, nil
	}

	data, err := p.Configs.FileContent(ctx, job.Repo, job.CommitSHA, ConfigFile)
	switch {
	case errors.Is(err, github.ErrNotFound):
		return nil, fmt.Errorf("%w (%s@%s)", ErrNoConfig, job.Repo, job.CommitSHA)
	case errors.Is(err, github.ErrTemporary), err != nil && ctx.Err() != nil:
		return nil, fmt.Errorf("%w: %w", ErrTemporary, err)
	case err != nil:
		return nil, fmt.Errorf("read %s: %w", ConfigFile, err)
	}

	cfg, err := ParseConfig(data)
	if err != nil {
		return nil, err
	}
	var run []Step
	for _, s := range cfg.Steps {
		if s.RunsOn(job.Branch) {
			run = append(run, s)
		}
	}
	values, err := p.secretsFor(ctx, job, run)
	if err != nil {
		return nil, err
	}
	for _, s := range run {
		steps = append(steps, userStep(job, s, values))
	}
	return steps, nil
}

// secretsFor fetches the secrets the steps ask for.
func (p Planner) secretsFor(ctx context.Context, job models.Job, steps []Step) (map[string]string, error) {
	var names []string
	for _, s := range steps {
		names = append(names, s.Secrets...)
	}
	if len(names) == 0 {
		return nil, nil
	}
	if p.Secrets == nil {
		return nil, fmt.Errorf("%w: steps use secrets but the server has none configured (CISID_SECRET_KEY)", ErrInvalidConfig)
	}
	slices.Sort(names)
	values, err := p.Secrets.Secrets(ctx, job.Repo, slices.Compact(names))
	switch {
	case errors.Is(err, secrets.ErrNotFound), errors.Is(err, secrets.ErrCorrupt):
		return nil, err
	case err != nil:
		return nil, fmt.Errorf("%w: read secrets: %w", ErrTemporary, err)
	}
	return values, nil
}

// cloneStep fetches exactly the pushed commit into the workspace. Values
// from the webhook payload are passed as environment variables and only
// referenced quoted in the script, so they cannot inject shell code.
func (p Planner) cloneStep(job models.Job) models.StepSpec {
	image := p.CloneImage
	if image == "" {
		image = DefaultCloneImage
	}
	env := jobEnv(job)
	env["CISID_CLONE_URL"] = job.CloneURL
	env["GIT_TERMINAL_PROMPT"] = "0"
	return models.StepSpec{
		Name:  cloneStepName,
		Image: image,
		Commands: []string{
			"git init -q .",
			`git remote add origin "$CISID_CLONE_URL"`,
			`git fetch -q --depth=1 origin "$CISID_COMMIT_SHA"`,
			`git checkout -q -B "$CISID_BRANCH" FETCH_HEAD`,
			"git log -1 --format='%H %s'",
		},
		Env:            env,
		TimeoutSeconds: cloneTimeoutSeconds,
	}
}

func userStep(job models.Job, s Step, secretValues map[string]string) models.StepSpec {
	env := maps.Clone(s.Environment)
	if env == nil {
		env = map[string]string{}
	}
	maps.Copy(env, jobEnv(job)) // ParseConfig rejects CISID_* keys anyway
	spec := models.StepSpec{
		Name:           s.Name,
		Image:          s.Image,
		Commands:       s.Commands,
		Env:            env,
		TimeoutSeconds: int(s.Timeout.Seconds()),
	}
	if len(s.Secrets) > 0 {
		spec.Secrets = make(map[string]string, len(s.Secrets))
		for _, name := range s.Secrets {
			spec.Secrets[name] = secretValues[name]
		}
	}
	return spec
}

// jobEnv describes the pushed commit to every step.
func jobEnv(job models.Job) map[string]string {
	return map[string]string{
		"CISID_REPO":       job.Repo,
		"CISID_BRANCH":     job.Branch,
		"CISID_COMMIT_SHA": job.CommitSHA,
	}
}
