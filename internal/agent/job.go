package agent

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"maps"
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

// execute runs the steps of job in order on a shared workspace volume and
// stops at the first failing step.
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

	var seq atomic.Int64
	for i, spec := range job.Steps {
		// Secrets are masked before output leaves the agent.
		stdout := newMaskWriter(&logWriter{a: a, jobID: job.JobID, step: i, stream: models.StreamStdout, seq: &seq}, spec.Secrets)
		stderr := newMaskWriter(&logWriter{a: a, jobID: job.JobID, step: i, stream: models.StreamStderr, seq: &seq}, spec.Secrets)

		code, err := a.runStep(ctx, i, spec, toRunnerStep(job.JobID, spec, workspace, network), stdout, stderr)
		_ = stdout.Flush()
		_ = stderr.Flush()
		if err == nil && code == 0 {
			continue
		}

		res.FailedStep, res.ExitCode = &i, code
		switch {
		case ctx.Err() != nil:
			// The cause tells a cancel request from an agent shutdown.
			res.Status = models.JobStatusCanceled
			res.Error = context.Cause(ctx).Error()
		case err != nil:
			res.Status, res.Error = models.JobStatusFailed, err.Error()
		default:
			res.Status = models.JobStatusFailed
		}
		a.log.InfoContext(ctx, "job step failed", "job_id", job.JobID, "step", spec.Name,
			"exit_code", code, "error", err)
		return res
	}
	return res
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
	}
	for i, s := range job.Services {
		if s.Name == "" || s.Image == "" {
			return fmt.Errorf("%w %d: service %d needs a name and an image", ErrInvalidJob, job.JobID, i)
		}
	}
	return nil
}

// toRunnerStep runs the step's commands as one shell script that stops at
// the first failing command and echoes each command before running it.
func toRunnerStep(jobID int64, spec models.StepSpec, workspace, network string) runner.Step {
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
		Mounts:     []runner.Mount{{Type: runner.MountVolume, Source: workspace, Target: workspacePath}},
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
