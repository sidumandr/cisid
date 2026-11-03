// Package pipeline turns a job into the steps an agent runs: a clone step
// followed by the steps of the repository's .cisid.yml.
package pipeline

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strconv"

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
	Secrets(ctx context.Context, repo string, names []string) (map[string]secrets.Secret, error)
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

// Plan is the outcome of planning a job.
type Plan struct {
	// Assignment is what the agent runs; its JobID is left for the caller.
	Assignment models.JobAssignment
	// Matrix is the combination the job runs, nil without a matrix.
	Matrix map[string]string
	// Siblings are the other combinations of the matrix. They are set only
	// when the job had no combination yet, and each needs a job of its own.
	Siblings []map[string]string
}

// Plan returns what an agent runs for job: the clone step, then the steps
// of the .cisid.yml at the pushed commit, with its services. A job without
// a matrix combination takes the first one of the pipeline's matrix and
// gets the rest back as Siblings.
func (p Planner) Plan(ctx context.Context, job models.Job) (Plan, error) {
	switch {
	case job.CloneURL == "":
		return Plan{}, fmt.Errorf("%w %d: clone URL is empty", ErrInvalidJob, job.ID)
	case job.CommitSHA == "":
		return Plan{}, fmt.Errorf("%w %d: commit SHA is empty", ErrInvalidJob, job.ID)
	case job.Branch == "":
		return Plan{}, fmt.Errorf("%w %d: branch is empty", ErrInvalidJob, job.ID)
	}

	steps := []models.StepSpec{p.cloneStep(job)}
	if p.Configs == nil {
		return Plan{Assignment: models.JobAssignment{Steps: steps}}, nil
	}

	data, err := p.Configs.FileContent(ctx, job.Repo, job.CommitSHA, ConfigFile)
	switch {
	case errors.Is(err, github.ErrNotFound):
		return Plan{}, fmt.Errorf("%w (%s@%s)", ErrNoConfig, job.Repo, job.CommitSHA)
	case errors.Is(err, github.ErrTemporary), err != nil && ctx.Err() != nil:
		return Plan{}, fmt.Errorf("%w: %w", ErrTemporary, err)
	case err != nil:
		return Plan{}, fmt.Errorf("read %s: %w", ConfigFile, err)
	}

	cfg, err := ParseConfig(data)
	if err != nil {
		return Plan{}, err
	}

	plan := Plan{Matrix: job.Matrix}
	if combos := cfg.Matrix.Combinations(); len(combos) > 0 && job.Matrix == nil {
		plan.Matrix, plan.Siblings = combos[0], combos[1:]
	}
	steps, services, err := p.steps(ctx, job, cfg, plan.Matrix, steps)
	if err != nil {
		return Plan{}, err
	}
	plan.Assignment = models.JobAssignment{Steps: steps, Services: services, Cache: cacheSpec(job, cfg.Cache)}
	return plan, nil
}

// steps appends the steps of cfg that run for job, with combo applied, to
// steps and returns them with the services.
func (p Planner) steps(ctx context.Context, job models.Job, cfg Config, combo map[string]string,
	steps []models.StepSpec) ([]models.StepSpec, []models.ServiceSpec, error) {
	var run []Step
	for _, s := range cfg.Steps {
		if s.RunsFor(job) {
			run = append(run, s)
		}
	}
	values, err := p.secretsFor(ctx, job, run)
	if err != nil {
		return nil, nil, err
	}
	for _, s := range run {
		steps = append(steps, userStep(job, s, values, combo))
	}

	var services []models.ServiceSpec
	for _, s := range cfg.Services {
		env := expandAll(s.Environment, combo)
		if len(combo) > 0 {
			env = withMatrix(env, combo)
		}
		services = append(services, models.ServiceSpec{
			Name: s.Name, Image: expand(s.Image, combo), Env: env, Command: s.Command,
		})
	}
	return steps, services, nil
}

func withMatrix(env, combo map[string]string) map[string]string {
	if env == nil {
		env = map[string]string{}
	}
	maps.Copy(env, combo)
	return env
}

// secretsFor fetches the secrets the steps ask for.
func (p Planner) secretsFor(ctx context.Context, job models.Job, steps []Step) (map[string]string, error) {
	var names []string
	for _, s := range steps {
		names = append(names, s.Secrets...)
	}
	if len(names) == 0 || job.Fork {
		// Code from a fork never sees the repository's secrets.
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
	allowed := make(map[string]string, len(values))
	for name, s := range values {
		if secretAllowed(s.Branches, job) {
			allowed[name] = s.Value
		}
	}
	return allowed, nil
}

// cacheSpec names the repository's caches. Forks may read them but not
// write them, so they cannot plant files for the repository's own builds.
func cacheSpec(job models.Job, paths []string) *models.CacheSpec {
	if len(paths) == 0 {
		return nil
	}
	sum := sha256.Sum256([]byte(job.Repo))
	return &models.CacheSpec{Key: hex.EncodeToString(sum[:6]), Paths: paths, ReadOnly: job.Fork}
}

// secretAllowed reports whether a secret limited to branches may go to job:
// only to jobs on those branches, never to pull requests, whose changes are
// not on the branch yet. Unlike when.branch, this is decided by the server,
// so a branch's .cisid.yml cannot change it.
func secretAllowed(branches []string, job models.Job) bool {
	if len(branches) == 0 {
		return true
	}
	if job.Event == models.EventPullRequest {
		return false
	}
	m, err := newBranchMatcher(branches)
	return err == nil && m.Matches(job.Branch)
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

func userStep(job models.Job, s Step, secretValues, combo map[string]string) models.StepSpec {
	env := expandAll(s.Environment, combo)
	if env == nil {
		env = map[string]string{}
	}
	maps.Copy(env, combo)
	maps.Copy(env, jobEnv(job)) // ParseConfig rejects the reserved names anyway
	spec := models.StepSpec{
		Name:           s.Name,
		Image:          expand(s.Image, combo),
		Commands:       s.Commands,
		Env:            env,
		TimeoutSeconds: int(s.Timeout.Seconds()),
	}
	for _, name := range s.Secrets {
		// A secret withheld from the job (e.g. a fork's) is left out.
		if v, ok := secretValues[name]; ok {
			if spec.Secrets == nil {
				spec.Secrets = make(map[string]string, len(s.Secrets))
			}
			spec.Secrets[name] = v
		}
	}
	return spec
}

// jobEnv describes the commit being built to every step.
func jobEnv(job models.Job) map[string]string {
	env := map[string]string{
		"CISID_REPO":       job.Repo,
		"CISID_BRANCH":     job.Branch,
		"CISID_COMMIT_SHA": job.CommitSHA,
		"CISID_EVENT":      cmp.Or(job.Event, models.EventPush),
	}
	if job.PullRequest > 0 {
		env["CISID_PULL_REQUEST"] = strconv.Itoa(job.PullRequest)
		env["CISID_TARGET_BRANCH"] = job.TargetBranch
	}
	return env
}
