package agent

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"maps"
	"path"
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"github.com/sidumandr/cisid/internal/runner"
	"github.com/sidumandr/cisid/internal/telemetry"
	"github.com/sidumandr/cisid/pkg/models"
)

const (
	workspacePath    = "/workspace"
	workspaceTimeout = 30 * time.Second
)

// ErrInvalidJob is reported for an assignment that cannot be run.
var ErrInvalidJob = errors.New("invalid job")

// executeJobs runs assigned jobs one at a time until ctx is canceled.
func (a *Agent) executeJobs(ctx context.Context, jobs <-chan models.JobAssignment) {
	for {
		select {
		case <-ctx.Done():
			// A job accepted just before shutdown still gets a result.
			select {
			case job := <-jobs:
				a.report(ctx, models.JobResult{
					JobID: job.JobID, Status: models.JobStatusCanceled, ExitCode: -1,
					Error: ctx.Err().Error(),
				})
			default:
			}
			return
		case job := <-jobs:
			jobCtx := a.startJob(ctx, job.JobID)
			res := a.run(jobCtx, job)
			a.endJob(job.JobID)
			a.report(ctx, res)
			a.busy.Store(false)
		}
	}
}

// startJob returns the context job jobID runs under; canceling it with a
// cause stops the job. A cancel that arrived before the start applies now.
func (a *Agent) startJob(ctx context.Context, jobID int64) context.Context {
	jobCtx, cancel := context.WithCancelCause(ctx)
	a.jobMu.Lock()
	defer a.jobMu.Unlock()
	a.jobID, a.cancelJob = jobID, cancel
	if a.cancelLater[jobID] {
		delete(a.cancelLater, jobID)
		cancel(errCanceledByServer)
	}
	return jobCtx
}

func (a *Agent) endJob(jobID int64) {
	a.jobMu.Lock()
	defer a.jobMu.Unlock()
	if a.cancelJob != nil {
		a.cancelJob(nil)
	}
	a.jobID, a.cancelJob = 0, nil
	delete(a.cancelLater, jobID)
}

// run executes job in a span that continues the trace the server sent.
func (a *Agent) run(ctx context.Context, job models.JobAssignment) models.JobResult {
	ctx, span := a.tracer.Start(telemetry.WithTraceParent(ctx, job.TraceParent), "run job",
		trace.WithAttributes(telemetry.AttrJobID.Int64(job.JobID), telemetry.AttrAgent.String(a.cfg.Name)))
	defer span.End()

	res := a.execute(ctx, job)
	span.SetAttributes(telemetry.AttrJobStatus.String(string(res.Status)))
	if res.Status == models.JobStatusFailed {
		span.SetStatus(codes.Error, failureText(res))
	}
	return res
}

// failureText describes why a failed job failed.
func failureText(res models.JobResult) string {
	if res.Error != "" {
		return res.Error
	}
	if res.FailedStep != nil {
		return fmt.Sprintf("step %d exited with %d", *res.FailedStep, res.ExitCode)
	}
	return "failed"
}

// execute runs the steps of job on a shared workspace volume, each once the
// steps it needs have passed, and stops at the first failing step.
func (a *Agent) execute(ctx context.Context, job models.JobAssignment) models.JobResult {
	res := models.JobResult{JobID: job.JobID, Status: models.JobStatusSuccess}
	if err := validateJob(job); err != nil {
		res.Status, res.ExitCode, res.Error = models.JobStatusFailed, -1, err.Error()
		return res
	}

	workspace := workspaceName(job.JobID)
	defer a.removeWorkspace(ctx, workspace)

	// Services share a per-job network with the steps. Deferred cleanup
	// runs in reverse: services, then the network, then the workspace.
	var network string
	if len(job.Services) > 0 {
		network = workspace
		if err := a.runner.CreateNetwork(ctx, network); err != nil {
			return a.setupFailed(ctx, res, "create job network", err)
		}
		defer a.cleanup(ctx, "remove job network", func(ctx context.Context) error {
			return a.runner.RemoveNetwork(ctx, network)
		})
		for _, svc := range job.Services {
			svcCtx, span := a.tracer.Start(ctx, "service "+svc.Name, trace.WithAttributes(
				telemetry.AttrService.String(svc.Name), telemetry.AttrStepImage.String(svc.Image)))
			id, err := a.runner.StartService(svcCtx, runner.Service{
				Name: svc.Name, Image: svc.Image, Env: svc.Env, Cmd: svc.Command, Network: network,
			})
			if err != nil {
				span.SetStatus(codes.Error, err.Error())
				span.End()
				return a.setupFailed(ctx, res, "start service "+svc.Name, err)
			}
			span.End()
			defer a.cleanup(ctx, "stop service "+svc.Name, func(ctx context.Context) error {
				return a.runner.StopService(ctx, id)
			})
		}
	}

	return a.runSteps(ctx, job, res, &stepEnv{workspace: workspace, network: network, caches: cacheMounts(job.Cache)})
}

// stepEnv is what every step of a job is run with.
type stepEnv struct {
	workspace, network string
	caches             []runner.Mount
	seq                atomic.Int64 // numbers the job's log chunks
}

// errStepFailed stops the other steps of a job once one has failed.
var errStepFailed = errors.New("another step failed")

// stepDone is how running one step ended.
type stepDone struct {
	i    int
	code int
	err  error
}

// runSteps starts every step whose needed steps have passed, several at a
// time when they do not need each other, and reports each start and end.
// The first failure stops the steps still running (canceled) and leaves
// the rest unstarted (skipped); a canceled job stops them all.
func (a *Agent) runSteps(ctx context.Context, job models.JobAssignment, res models.JobResult, env *stepEnv) models.JobResult {
	results := make([]models.StepResult, len(job.Steps))
	for i := range results {
		results[i].State = models.StepPending
	}
	stepsCtx, stop := context.WithCancelCause(ctx)
	defer stop(nil)

	done := make(chan stepDone)
	running, failed := 0, false
	for {
		for i := range job.Steps {
			if failed || ctx.Err() != nil {
				break
			}
			if results[i].State != models.StepPending || !needsPassed(job.Steps, results, i) {
				continue
			}
			now := time.Now().UTC()
			results[i] = models.StepResult{State: models.StepRunning, StartedAt: &now}
			a.reportStep(job.JobID, i, results[i])
			running++
			go func() {
				code, err := a.runOne(stepsCtx, job, i, env)
				done <- stepDone{i: i, code: code, err: err}
			}()
		}
		if running == 0 {
			break
		}
		d := <-done
		running--

		now := time.Now().UTC()
		r := &results[d.i]
		r.FinishedAt, r.ExitCode = &now, &d.code
		switch {
		case d.err == nil && d.code == 0:
			r.State = models.StepSuccess
		case ctx.Err() != nil, stepsCtx.Err() != nil:
			// Stopped by a cancel, a shutdown or another step failing.
			r.State = models.StepCanceled
		default:
			r.State = models.StepFailed
			failed = true
			res.Status, res.FailedStep, res.ExitCode = models.JobStatusFailed, &d.i, d.code
			if d.err != nil {
				res.Error = d.err.Error()
			}
			stop(errStepFailed)
			a.log.InfoContext(ctx, "job step failed", "job_id", job.JobID, "step", job.Steps[d.i].Name,
				"exit_code", d.code, "error", d.err)
		}
		a.reportStep(job.JobID, d.i, *r)
	}

	for i := range results {
		if results[i].State == models.StepPending {
			results[i].State = models.StepSkipped
		}
	}
	if !failed && ctx.Err() != nil {
		// The cause tells a cancel request from an agent shutdown.
		res.Status, res.Error = models.JobStatusCanceled, context.Cause(ctx).Error()
		for i, r := range results {
			if r.State == models.StepCanceled {
				res.FailedStep, res.ExitCode = &i, *r.ExitCode
				break
			}
		}
	}
	res.Steps = results
	return res
}

// needsPassed reports whether every step that step i needs has passed.
func needsPassed(steps []models.StepSpec, results []models.StepResult, i int) bool {
	for _, n := range models.Needs(steps, i) {
		if results[n].State != models.StepSuccess {
			return false
		}
	}
	return true
}

// runOne runs step i of job, sending its output as it comes.
func (a *Agent) runOne(ctx context.Context, job models.JobAssignment, i int, env *stepEnv) (int, error) {
	spec := job.Steps[i]
	// Secrets are masked before output leaves the agent.
	stdout := newMaskWriter(&logWriter{a: a, jobID: job.JobID, step: i, stream: models.StreamStdout, seq: &env.seq}, spec.Secrets)
	stderr := newMaskWriter(&logWriter{a: a, jobID: job.JobID, step: i, stream: models.StreamStderr, seq: &env.seq}, spec.Secrets)
	if spec.Artifacts {
		code, err := a.collectArtifacts(ctx, job, i, env, stderr)
		_ = stderr.Flush()
		return code, err
	}
	code, err := a.runStep(ctx, i, spec, toRunnerStep(job.JobID, spec, env.workspace, env.network, env.caches), stdout, stderr)
	_ = stdout.Flush()
	_ = stderr.Flush()
	return code, err
}

// reportStep tells the server that a step started or ended. Like log
// chunks it never blocks the job; the result repeats every step's end.
func (a *Agent) reportStep(jobID int64, i int, r models.StepResult) {
	msg, err := models.NewMessage(models.MsgJobStep, models.JobStep{JobID: jobID, Step: i, StepResult: r})
	if err == nil {
		a.out.tryPut(msg)
	}
}

// runStep runs one step in its own span.
func (a *Agent) runStep(ctx context.Context, i int, spec models.StepSpec, step runner.Step, stdout, stderr io.Writer) (int, error) {
	ctx, span := a.tracer.Start(ctx, "step "+spec.Name, trace.WithAttributes(telemetry.AttrStepIndex.Int(i),
		telemetry.AttrStepName.String(spec.Name), telemetry.AttrStepImage.String(spec.Image)))
	defer span.End()

	code, err := a.runner.Run(ctx, step, stdout, stderr)
	span.SetAttributes(telemetry.AttrExitCode.Int(code))
	switch {
	case err != nil:
		span.SetStatus(codes.Error, err.Error())
	case code != 0:
		span.SetStatus(codes.Error, fmt.Sprintf("exit code %d", code))
	}
	return code, err
}

// report queues the job result. It still works after ctx is canceled, so a
// canceled job's result reaches the server during shutdown.
func (a *Agent) report(ctx context.Context, res models.JobResult) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), a.cfg.FlushTimeout)
	defer cancel()

	msg, err := models.NewMessage(models.MsgJobResult, res)
	if err == nil {
		err = a.out.put(ctx, msg)
	}
	if err != nil {
		a.log.ErrorContext(ctx, "could not report job result", "job_id", res.JobID, "error", err)
		return
	}
	a.log.InfoContext(ctx, "job finished", "job_id", res.JobID, "status", res.Status, "exit_code", res.ExitCode)
}

// setupFailed reports a job whose services could not be set up; no step ran.
func (a *Agent) setupFailed(ctx context.Context, res models.JobResult, what string, err error) models.JobResult {
	res.ExitCode = -1
	if ctx.Err() != nil {
		res.Status, res.Error = models.JobStatusCanceled, context.Cause(ctx).Error()
		return res
	}
	res.Status, res.Error = models.JobStatusFailed, what+": "+err.Error()
	a.log.WarnContext(ctx, "job setup failed", "job_id", res.JobID, "error", res.Error)
	return res
}

// cleanup runs a cleanup action even when ctx is canceled, logging failures.
func (a *Agent) cleanup(ctx context.Context, what string, f func(context.Context) error) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), workspaceTimeout)
	defer cancel()
	if err := f(ctx); err != nil {
		a.log.WarnContext(ctx, what, "error", err)
	}
}

func (a *Agent) removeWorkspace(ctx context.Context, name string) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), workspaceTimeout)
	defer cancel()
	if err := a.runner.RemoveVolume(ctx, name); err != nil {
		a.log.WarnContext(ctx, "remove workspace", "volume", name, "error", err)
	}
}

func validateJob(job models.JobAssignment) error {
	if len(job.Steps) == 0 {
		return fmt.Errorf("%w %d: no steps", ErrInvalidJob, job.JobID)
	}
	for i, s := range job.Steps {
		switch {
		case s.Image == "":
			return fmt.Errorf("%w %d: step %d (%s) has no image", ErrInvalidJob, job.JobID, i, s.Name)
		case len(s.Commands) == 0:
			return fmt.Errorf("%w %d: step %d (%s) has no commands", ErrInvalidJob, job.JobID, i, s.Name)
		}
		// Needing only earlier steps rules out cycles.
		seen := map[int]bool{}
		for _, n := range s.DependsOn {
			if n < 0 || n >= i || seen[n] {
				return fmt.Errorf("%w %d: step %d (%s) depends on step %d", ErrInvalidJob, job.JobID, i, s.Name, n)
			}
			seen[n] = true
		}
	}
	for i, s := range job.Services {
		if s.Name == "" || s.Image == "" {
			return fmt.Errorf("%w %d: service %d needs a name and an image", ErrInvalidJob, job.JobID, i)
		}
	}
	if c := job.Cache; c != nil {
		if !cacheKeyRe.MatchString(c.Key) {
			return fmt.Errorf("%w %d: cache key %q", ErrInvalidJob, job.JobID, c.Key)
		}
		for _, p := range c.Paths {
			if !path.IsAbs(p) || path.Clean(p) != p || p == "/" || p == workspacePath {
				return fmt.Errorf("%w %d: cache path %q", ErrInvalidJob, job.JobID, p)
			}
		}
	}
	return nil
}

var cacheKeyRe = regexp.MustCompile(`^[0-9a-z]{1,32}$`)

// cacheMounts mounts a job's caches: per path, a volume named after the
// repository's key, kept between its jobs on this Docker host.
func cacheMounts(c *models.CacheSpec) []runner.Mount {
	if c == nil {
		return nil
	}
	mounts := make([]runner.Mount, 0, len(c.Paths))
	for _, p := range c.Paths {
		sum := sha256.Sum256([]byte(p))
		mounts = append(mounts, runner.Mount{
			Type:     runner.MountVolume,
			Source:   fmt.Sprintf("cisid-cache-%s-%x", c.Key, sum[:4]),
			Target:   p,
			ReadOnly: c.ReadOnly,
			Labels:   map[string]string{"cisid.cache": c.Key, "cisid.cache.path": p},
		})
	}
	return mounts
}

// toRunnerStep runs the step's commands as one shell script that stops at
// the first failing command and echoes each command before running it.
func toRunnerStep(jobID int64, spec models.StepSpec, workspace, network string, caches []runner.Mount) runner.Step {
	env := maps.Clone(spec.Env)
	if env == nil {
		env = map[string]string{}
	}
	maps.Copy(env, spec.Secrets)
	// Built-in variables cannot be overridden by the step.
	env["CI"] = "true"
	env["CISID"] = "true"
	env["CISID_JOB_ID"] = strconv.FormatInt(jobID, 10)
	env["CISID_STEP"] = spec.Name

	return runner.Step{
		Image:      spec.Image,
		Entrypoint: []string{"/bin/sh", "-c"},
		Cmd:        []string{"set -ex\n" + strings.Join(spec.Commands, "\n")},
		Env:        env,
		WorkingDir: workspacePath,
		Mounts:     append([]runner.Mount{{Type: runner.MountVolume, Source: workspace, Target: workspacePath}}, caches...),
		Network:    network,
		Timeout:    time.Duration(spec.TimeoutSeconds) * time.Second,
	}
}

// workspaceName returns a unique volume name, so a retried job never sees a
// stale workspace.
func workspaceName(jobID int64) string {
	return fmt.Sprintf("cisid-job-%d-%s", jobID, strings.ToLower(rand.Text()[:8]))
}

// logWriter turns step output into job_log messages. It never blocks: if
// the outbox is full the chunk is dropped and the gap shows in Seq.
type logWriter struct {
	a      *Agent
	jobID  int64
	step   int
	stream models.LogStream
	seq    *atomic.Int64
}

func (w *logWriter) Write(p []byte) (int, error) {
	msg, err := models.NewMessage(models.MsgJobLog, models.JobLog{
		JobID:  w.jobID,
		Step:   w.step,
		Stream: w.stream,
		Seq:    w.seq.Add(1),
		Data:   string(p),
	})
	if err != nil {
		return 0, err
	}
	w.a.out.tryPut(msg)
	return len(p), nil
}
