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
	"strings"

	"github.com/sidumandr/cisid/internal/forge"
	"github.com/sidumandr/cisid/internal/secrets"
	"github.com/sidumandr/cisid/pkg/models"
)

// DefaultCloneImage runs the clone step. It needs /bin/sh and git.
const DefaultCloneImage = "alpine/git:2.52.0"

const (
	cloneStepName       = "clone"
	cloneTimeoutSeconds = 600
	// artifactsStepName collects the artifacts once the other steps pass.
	artifactsStepName       = "artifacts"
	artifactsTimeoutSeconds = 600
)

var (
	// ErrInvalidJob is returned for a job that lacks what planning needs.
	ErrInvalidJob = errors.New("invalid job")
	// ErrNoConfig is returned when the commit has no .cisid.yml.
	ErrNoConfig = errors.New("no " + ConfigFile + " in repository")
	// ErrTemporary marks planning failures worth retrying later, such as
	// the forge being unreachable or rate limited.
	ErrTemporary = errors.New("temporary planning failure")
)

// ConfigSource reads a file from a repository at a commit.
// *forge.Set satisfies it.
type ConfigSource interface {
	FileContent(ctx context.Context, repo, ref, path string) ([]byte, error)
}

var _ ConfigSource = (*forge.Set)(nil)

// CloneAuth returns the credentials git clones a repository with; an empty
// token clones anonymously. *forge.Set satisfies it.
type CloneAuth interface {
	CloneCredentials(repo string) (user, token string)
}

var _ CloneAuth = (*forge.Set)(nil)

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
	// Clones authenticates the clone step, for private repositories. When
	// nil, repositories are cloned anonymously.
	Clones CloneAuth
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
	case errors.Is(err, forge.ErrNotFound):
		return Plan{}, fmt.Errorf("%w (%s@%s)", ErrNoConfig, job.Repo, job.CommitSHA)
	case errors.Is(err, forge.ErrTemporary), err != nil && ctx.Err() != nil:
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
	if cfg.Parallel {
		setDependencies(cfg, steps)
	}
	if len(cfg.Artifacts) > 0 {
		collect := p.artifactsStep(job, cfg)
		if cfg.Parallel {
			// It needs every other step to have passed.
			for i := range steps {
				collect.DependsOn = append(collect.DependsOn, i)
			}
		}
		steps = append(steps, collect)
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

// setDependencies turns the depends_on of the planned steps into step
// indexes. A step skipped by when passes its own dependencies on, and a
// step with none waits for the clone only.
func setDependencies(cfg Config, steps []models.StepSpec) {
	planned := map[string]int{}
	for i, s := range steps {
		planned[s.Name] = i
	}
	byName := map[string]Step{}
	for _, s := range cfg.Steps {
		byName[s.Name] = s
	}
	// Dependencies name earlier steps only, so this ends.
	var resolve func(names []string) []int
	resolve = func(names []string) []int {
		var out []int
		for _, n := range names {
			if i, ok := planned[n]; ok {
				out = append(out, i)
			} else {
				out = append(out, resolve(byName[n].DependsOn)...)
			}
		}
		return out
	}
	for i := 1; i < len(steps); i++ {
		deps := resolve(byName[steps[i].Name].DependsOn)
		if len(deps) == 0 {
			deps = []int{0}
		}
		slices.Sort(deps)
		steps[i].DependsOn = slices.Compact(deps)
	}
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

// credentialHelper answers git's request for credentials from the clone
// step's environment, so the token never lands in .git/config.
const credentialHelper = `!f() { test "$1" = get && echo "username=$CISID_CLONE_USER" && echo "password=$CISID_CLONE_TOKEN"; }; f`

// cloneStep fetches exactly the pushed commit into the workspace. Values
// from the webhook payload are passed as environment variables and only
// referenced quoted in the script, so they cannot inject shell code.
//
// Servers that do not serve a commit by its SHA alone get the branch or
// pull request ref instead, which must still contain the commit. With the
// forge's token, private repositories clone too; the token is a secret of
// this step only, masked in its output.
func (p Planner) cloneStep(job models.Job) models.StepSpec {
	image := p.CloneImage
	if image == "" {
		image = DefaultCloneImage
	}
	env := jobEnv(job)
	env["CISID_CLONE_URL"] = job.CloneURL
	env["CISID_FETCH_REF"] = fetchRef(job)
	env["GIT_TERMINAL_PROMPT"] = "0"
	step := models.StepSpec{
		Name:  cloneStepName,
		Image: image,
		Commands: []string{
			"git init -q .",
			`git remote add origin "$CISID_CLONE_URL"`,
			`git fetch -q --depth=1 origin "$CISID_COMMIT_SHA" || git fetch -q --depth=50 origin "+$CISID_FETCH_REF:refs/remotes/origin/cisid"`,
			`git checkout -q -B "$CISID_BRANCH" "$CISID_COMMIT_SHA"`,
			"git log -1 --format='%H %s'",
		},
		Env:            env,
		TimeoutSeconds: cloneTimeoutSeconds,
	}
	if p.Clones == nil {
		return step
	}
	if user, token := p.Clones.CloneCredentials(job.Repo); token != "" {
		// Configuration from the environment applies to every git command
		// of the step and is not written anywhere.
		env["GIT_CONFIG_COUNT"] = "1"
		env["GIT_CONFIG_KEY_0"] = "credential.helper"
		env["GIT_CONFIG_VALUE_0"] = credentialHelper
		env["CISID_CLONE_USER"] = user
		step.Secrets = map[string]string{"CISID_CLONE_TOKEN": token}
	}
	return step
}

// artifactsScript writes a tar stream of the files the paths in
// CISID_ARTIFACT_PATHS (one per line) match to stdout. The paths come from
// the environment, never the script, and only newlines split them. Without
// CISID_ARTIFACT_UPLOAD=1, set by agents that upload, it only lists them.
var artifactsScript = []string{
	"IFS='\n'",
	"list=$(mktemp)",
	`for match in $CISID_ARTIFACT_PATHS; do if [ -d "$match" ]; then find "$match" -type f >> "$list"; elif [ -f "$match" ]; then printf '%s\n' "$match" >> "$list"; else echo "no file matches $match" >&2; fi; done`,
	`if [ ! -s "$list" ]; then echo "no artifacts found" >&2; exit 0; fi`,
	`if [ "$CISID_ARTIFACT_UPLOAD" != 1 ]; then echo "this agent does not upload artifacts; update it" >&2; cat "$list" >&2; exit 0; fi`,
	`tar -cf - -T "$list"`,
}

// artifactsStep collects the artifacts once every other step has passed.
func (p Planner) artifactsStep(job models.Job, cfg Config) models.StepSpec {
	image := p.CloneImage
	if image == "" {
		image = DefaultCloneImage
	}
	env := jobEnv(job)
	env["CISID_ARTIFACT_PATHS"] = strings.Join(cfg.Artifacts, "\n")
	step := models.StepSpec{
		Name:           artifactsStepName,
		Image:          image,
		Commands:       artifactsScript,
		Env:            env,
		TimeoutSeconds: artifactsTimeoutSeconds,
		Artifacts:      true,
	}
	return step
}

// fetchRef is the ref that holds job's commit: the pull request's head ref
// or the branch.
func fetchRef(job models.Job) string {
	if job.PullRequest > 0 {
		name, _, err := forge.SplitKey(job.Repo)
		if err != nil {
			name = forge.GitHub
		}
		return forge.PullRequestRef(name, job.PullRequest, job.Branch)
	}
	return "refs/heads/" + job.Branch
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
