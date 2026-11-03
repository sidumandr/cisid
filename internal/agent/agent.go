// Package agent connects a worker to the cisid server over WebSocket, runs
// the jobs it is assigned and streams their logs and results back.
package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
	"golang.org/x/sync/errgroup"

	"github.com/sidumandr/cisid/internal/runner"
	"github.com/sidumandr/cisid/pkg/models"
)

const (
	agentHeader     = "X-Cisid-Agent"
	maxMessageBytes = 1 << 20
	outboxSize      = 4096

	defaultMinBackoff   = time.Second
	defaultMaxBackoff   = time.Minute
	defaultFlushTimeout = 10 * time.Second
)

// ErrInvalidConfig is returned by New for an unusable Config.
var ErrInvalidConfig = errors.New("invalid agent config")

// StepRunner runs step containers. *runner.DockerRunner satisfies it.
type StepRunner interface {
	Run(ctx context.Context, step runner.Step, stdout, stderr io.Writer) (int, error)
	RemoveVolume(ctx context.Context, name string) error
}

var _ StepRunner = (*runner.DockerRunner)(nil)

// Config configures an Agent.
type Config struct {
	// ServerURL is the server's agent endpoint, e.g. ws://ci.example.com/agent/ws.
	ServerURL string
	// Name identifies the agent to the server.
	Name string
	// MinBackoff and MaxBackoff bound the reconnect delay (default 1s and 1m).
	MinBackoff time.Duration
	MaxBackoff time.Duration
	// FlushTimeout bounds how long shutdown waits to deliver pending
	// messages, such as the result of a canceled job (default 10s).
	FlushTimeout time.Duration
}

// Agent is a worker that runs one job at a time.
type Agent struct {
	cfg    Config
	runner StepRunner
	log    *slog.Logger
	out    *outbox
	busy   atomic.Bool
}

// New validates cfg and returns an Agent. A nil log uses slog.Default().
func New(cfg Config, r StepRunner, log *slog.Logger) (*Agent, error) {
	u, err := url.Parse(cfg.ServerURL)
	if err != nil {
		return nil, fmt.Errorf("%w: server URL: %w", ErrInvalidConfig, err)
	}
	if u.Scheme != "ws" && u.Scheme != "wss" {
		return nil, fmt.Errorf("%w: server URL %q must use ws:// or wss://", ErrInvalidConfig, cfg.ServerURL)
	}
	if cfg.Name == "" {
		return nil, fmt.Errorf("%w: name is empty", ErrInvalidConfig)
	}

	if cfg.MinBackoff <= 0 {
		cfg.MinBackoff = defaultMinBackoff
	}
	if cfg.MaxBackoff < cfg.MinBackoff {
		cfg.MaxBackoff = max(defaultMaxBackoff, cfg.MinBackoff)
	}
	if cfg.FlushTimeout <= 0 {
		cfg.FlushTimeout = defaultFlushTimeout
	}
	if log == nil {
		log = slog.Default()
	}
	return &Agent{cfg: cfg, runner: r, log: log, out: newOutbox(outboxSize)}, nil
}

// Run connects to the server and runs assigned jobs until ctx is canceled,
// reconnecting with exponential backoff whenever the connection is lost.
//
// On cancellation the running job is stopped, its result is delivered
// (bounded by Config.FlushTimeout) and then the connection is closed.
func (a *Agent) Run(ctx context.Context) error {
	// Buffered so the reader never blocks; busy guarantees one job in flight.
	jobs := make(chan models.JobAssignment, 1)

	// The connection outlives ctx so the final result can still be sent.
	connCtx, stopConn := context.WithCancel(context.WithoutCancel(ctx))
	defer stopConn()

	execDone := make(chan struct{})
	go func() {
		defer close(execDone)
		a.executeJobs(ctx, jobs)
	}()

	go func() {
		select {
		case <-execDone:
		case <-connCtx.Done():
			return
		}
		flushCtx, cancel := context.WithTimeout(connCtx, a.cfg.FlushTimeout)
		defer cancel()
		if err := a.out.flush(flushCtx); err != nil {
			a.log.WarnContext(flushCtx, "shutting down with undelivered messages", "error", err)
		}
		stopConn()
	}()

	a.connectLoop(connCtx, ctx.Done(), jobs)
	<-execDone
	return nil
}

// connectLoop keeps a session open until ctx is canceled.
func (a *Agent) connectLoop(ctx context.Context, stopping <-chan struct{}, jobs chan<- models.JobAssignment) {
	bo := newBackoff(a.cfg.MinBackoff, a.cfg.MaxBackoff)
	for {
		connected, err := a.session(ctx, stopping, jobs)
		if ctx.Err() != nil {
			return
		}
		if connected {
			bo.Reset()
		}

		delay := bo.Next()
		a.log.WarnContext(ctx, "connection to server lost, reconnecting",
			"server", a.cfg.ServerURL, "error", err, "retry_in", delay)

		t := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			t.Stop()
			return
		case <-t.C:
		}
	}
}

// session runs one WebSocket connection until it fails or ctx is canceled.
// connected reports whether the handshake succeeded.
func (a *Agent) session(ctx context.Context, stopping <-chan struct{}, jobs chan<- models.JobAssignment) (connected bool, err error) {
	header := http.Header{}
	header.Set(agentHeader, a.cfg.Name)

	conn, _, err := websocket.Dial(ctx, a.cfg.ServerURL, &websocket.DialOptions{HTTPHeader: header})
	if err != nil {
		return false, fmt.Errorf("dial %s: %w", a.cfg.ServerURL, err)
	}
	defer conn.CloseNow()
	conn.SetReadLimit(maxMessageBytes)

	a.log.InfoContext(ctx, "connected to server", "server", a.cfg.ServerURL)

	// Written before the writer goroutine starts, so there is still a single writer.
	hello, err := models.NewMessage(models.MsgAgentHello, models.AgentHello{Name: a.cfg.Name, Capacity: 1})
	if err != nil {
		return true, err
	}
	if err := writeMessage(ctx, conn, hello); err != nil {
		return true, err
	}

	g, gctx := errgroup.WithContext(ctx)
	g.Go(func() error { return a.writeLoop(gctx, conn) })
	g.Go(func() error { return a.readLoop(gctx, conn, stopping, jobs) })
	err = g.Wait()

	if ctx.Err() != nil {
		_ = conn.Close(websocket.StatusNormalClosure, "agent shutting down")
	}
	return true, err
}

// writeLoop is the only goroutine that writes to conn after the hello.
func (a *Agent) writeLoop(ctx context.Context, conn *websocket.Conn) error {
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case msg := <-a.out.ch:
			err := writeMessage(ctx, conn, msg)
			a.out.written()
			if err != nil {
				return err
			}
		}
	}
}

func (a *Agent) readLoop(ctx context.Context, conn *websocket.Conn, stopping <-chan struct{}, jobs chan<- models.JobAssignment) error {
	for {
		_, data, err := conn.Read(ctx)
		if err != nil {
			return fmt.Errorf("read from server: %w", err)
		}

		var msg models.Message
		if err := json.Unmarshal(data, &msg); err != nil {
			a.log.WarnContext(ctx, "ignoring malformed message", "error", err)
			continue
		}

		switch msg.Type {
		case models.MsgJobAssigned:
			var job models.JobAssignment
			if err := msg.Decode(&job); err != nil {
				a.log.WarnContext(ctx, "ignoring malformed job assignment", "error", err)
				continue
			}
			a.accept(ctx, stopping, jobs, job)
		default:
			a.log.DebugContext(ctx, "ignoring unknown message", "type", msg.Type)
		}
	}
}

// accept hands job to the executor or rejects it.
func (a *Agent) accept(ctx context.Context, stopping <-chan struct{}, jobs chan<- models.JobAssignment, job models.JobAssignment) {
	select {
	case <-stopping:
		a.reject(ctx, job.JobID, "agent shutting down")
		return
	default:
	}
	if !a.busy.CompareAndSwap(false, true) {
		a.reject(ctx, job.JobID, "agent busy")
		return
	}
	a.log.InfoContext(ctx, "job assigned", "job_id", job.JobID, "steps", len(job.Steps))
	jobs <- job
}

func (a *Agent) reject(ctx context.Context, jobID int64, reason string) {
	a.log.WarnContext(ctx, "rejecting job", "job_id", jobID, "reason", reason)
	a.enqueue(ctx, models.MsgJobRejected, models.JobRejected{JobID: jobID, Reason: reason})
}

// enqueue queues a message without blocking; it is dropped if the outbox is full.
func (a *Agent) enqueue(ctx context.Context, typ models.MessageType, payload any) {
	msg, err := models.NewMessage(typ, payload)
	if err != nil {
		a.log.ErrorContext(ctx, "encode message", "type", typ, "error", err)
		return
	}
	if !a.out.tryPut(msg) {
		a.log.WarnContext(ctx, "outbox full, dropping message", "type", typ)
	}
}

func writeMessage(ctx context.Context, conn *websocket.Conn, msg models.Message) error {
	data, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("encode %s message: %w", msg.Type, err)
	}
	if err := conn.Write(ctx, websocket.MessageText, data); err != nil {
		return fmt.Errorf("write %s message: %w", msg.Type, err)
	}
	return nil
}
