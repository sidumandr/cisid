package hub

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/coder/websocket"
	"golang.org/x/sync/errgroup"

	"github.com/sidumandr/cisid/internal/db"
	"github.com/sidumandr/cisid/internal/metrics"
	"github.com/sidumandr/cisid/pkg/models"
)

const (
	helloTimeout    = 10 * time.Second
	writeTimeout    = 10 * time.Second
	pingInterval    = 30 * time.Second
	maxMessageBytes = 1 << 20
	outBuffer       = 16
	// maxLabels bounds the labels an agent offers.
	maxLabels = 20
)

var (
	errHubClosed = errors.New("hub closed")
	errBadHello  = errors.New("expected agent_hello")
)

// validToken compares got with every token in constant time.
func validToken(got string, tokens []string) bool {
	ok := 0
	for _, t := range tokens {
		ok |= subtle.ConstantTimeCompare([]byte(got), []byte(t))
	}
	return ok == 1
}

// ServeHTTP accepts an agent's WebSocket connection and serves it until the
// agent disconnects, a newer connection with the same name replaces it, or
// the hub shuts down.
func (h *Hub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if len(h.agentTokens) > 0 {
		got, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !ok || !validToken(got, h.agentTokens) {
			h.log.WarnContext(r.Context(), "rejected agent with missing or wrong token", "remote_addr", r.RemoteAddr)
			w.Header().Set("WWW-Authenticate", `Bearer realm="cisid-agents"`)
			http.Error(w, "missing or wrong agent token", http.StatusUnauthorized)
			return
		}
	}

	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		http.Error(w, "server shutting down", http.StatusServiceUnavailable)
		return
	}
	h.conns.Add(1)
	h.mu.Unlock()
	defer h.conns.Done()

	conn, err := websocket.Accept(w, r, nil)
	if err != nil {
		h.log.WarnContext(r.Context(), "agent handshake failed", "remote_addr", r.RemoteAddr, "error", err)
		return
	}
	defer conn.CloseNow()
	conn.SetReadLimit(maxMessageBytes)

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	hello, err := readHello(ctx, conn)
	if err != nil {
		h.log.WarnContext(ctx, "rejecting agent connection", "remote_addr", r.RemoteAddr, "error", err)
		_ = conn.Close(websocket.StatusPolicyViolation, errBadHello.Error())
		return
	}

	a, err := h.register(ctx, hello)
	if err != nil {
		h.log.WarnContext(ctx, "rejecting agent connection", "agent", hello.Name, "error", err)
		_ = conn.Close(websocket.StatusTryAgainLater, "server unavailable")
		return
	}
	defer h.unregister(a)
	h.log.InfoContext(ctx, "agent connected", "agent", a.name, "remote_addr", r.RemoteAddr,
		"running_job", hello.RunningJobID)

	// On shutdown let the agent close the connection, so messages it already
	// sent are still read (Close would discard them); close it ourselves only
	// if it does not. A replaced connection is closed right away.
	watched := make(chan struct{})
	go func() {
		defer close(watched)
		select {
		case <-h.closing:
			close(a.drain)
			select {
			case <-ctx.Done():
			case <-time.After(h.drainTimeout):
				_ = conn.Close(websocket.StatusGoingAway, "server shutting down")
			}
		case <-a.kick:
			_ = conn.Close(websocket.StatusNormalClosure, "replaced by a newer connection")
		case <-ctx.Done():
		}
	}()

	g, gctx := errgroup.WithContext(ctx)
	g.Go(func() error { return h.writeLoop(gctx, conn, a) })
	g.Go(func() error { return h.readLoop(gctx, conn, a) })
	err = g.Wait()

	cancel()
	<-watched
	h.log.InfoContext(ctx, "agent disconnected", "agent", a.name, "reason", err)
}

func readHello(ctx context.Context, conn *websocket.Conn) (models.AgentHello, error) {
	ctx, cancel := context.WithTimeout(ctx, helloTimeout)
	defer cancel()

	_, data, err := conn.Read(ctx)
	if err != nil {
		return models.AgentHello{}, fmt.Errorf("read hello: %w", err)
	}
	var msg models.Message
	if err := json.Unmarshal(data, &msg); err != nil {
		return models.AgentHello{}, fmt.Errorf("%w: %w", errBadHello, err)
	}
	if msg.Type != models.MsgAgentHello {
		return models.AgentHello{}, fmt.Errorf("%w, got %q", errBadHello, msg.Type)
	}
	var hello models.AgentHello
	if err := msg.Decode(&hello); err != nil {
		return models.AgentHello{}, fmt.Errorf("%w: %w", errBadHello, err)
	}
	if hello.Name == "" {
		return models.AgentHello{}, fmt.Errorf("%w: name is empty", errBadHello)
	}
	if len(hello.Labels) > maxLabels {
		return models.AgentHello{}, fmt.Errorf("%w: more than %d labels", errBadHello, maxLabels)
	}
	for _, l := range hello.Labels {
		if !models.ValidLabel(l) {
			return models.AgentHello{}, fmt.Errorf("%w: invalid label %q", errBadHello, l)
		}
	}
	return hello, nil
}

// register makes a the connection for its name, replacing an older one.
// The job the agent reports is kept on it (even if its lease expired, as
// long as nobody else took it); jobs the database assigns to the agent but
// the agent no longer holds (a lost assignment, an agent restart) go back
// to the queue.
func (h *Hub) register(ctx context.Context, hello models.AgentHello) (*agentConn, error) {
	if id := hello.RunningJobID; id != 0 {
		err := h.store.AdoptJob(ctx, hello.Name, id, h.lease)
		switch {
		case errors.Is(err, db.ErrJobConflict):
			h.log.WarnContext(ctx, "agent reports a job that finished or moved on; its result will be dropped",
				"agent", hello.Name, "job_id", id)
		case err != nil:
			return nil, err
		}
	}

	n, err := h.store.RequeueAgentJobs(ctx, hello.Name, hello.RunningJobID)
	if err != nil {
		return nil, err
	}
	if n > 0 {
		h.log.InfoContext(ctx, "requeued jobs the agent no longer holds", "agent", hello.Name, "jobs", n)
	}

	a := &agentConn{
		name:     hello.Name,
		labels:   hello.Labels,
		out:      make(chan models.Message, outBuffer),
		kick:     make(chan struct{}),
		drain:    make(chan struct{}),
		ackReady: make(chan struct{}, 1),
		jobID:    hello.RunningJobID,
	}

	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return nil, errHubClosed
	}
	if old := h.agents[a.name]; old != nil {
		close(old.kick)
	}
	h.agents[a.name] = a
	h.mu.Unlock()

	h.Wake()
	return a, nil
}

// unregister forgets a unless a newer connection already replaced it. A
// job the agent was running stays assigned: the agent keeps running it and
// reports it when it reconnects.
func (h *Hub) unregister(a *agentConn) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.agents[a.name] == a {
		delete(h.agents, a.name)
	}
}

// writeLoop is the only goroutine that sends messages on conn. It
// acknowledges processed agent messages, coalescing acks under load, and
// pings the agent so dead connections are noticed.
func (h *Hub) writeLoop(ctx context.Context, conn *websocket.Conn, a *agentConn) error {
	ping := time.NewTicker(pingInterval)
	defer ping.Stop()

	drain := a.drain
	var acked int64
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-a.ackReady:
			id := a.processed.Load()
			if id <= acked {
				continue
			}
			msg, err := models.NewMessage(models.MsgAck, models.Ack{ID: id})
			if err != nil {
				return err
			}
			if err := write(ctx, conn, msg); err != nil {
				return err
			}
			acked = id
		case <-drain:
			drain = nil // announce once
			if err := write(ctx, conn, models.Message{Type: models.MsgServerClosing, Payload: []byte("{}")}); err != nil {
				return err
			}
		case msg := <-a.out:
			if err := write(ctx, conn, msg); err != nil {
				return err
			}
		case <-ping.C:
			pctx, cancel := context.WithTimeout(ctx, writeTimeout)
			err := conn.Ping(pctx)
			cancel()
			if err != nil {
				return fmt.Errorf("ping agent %s: %w", a.name, err)
			}
		}
	}
}

func write(ctx context.Context, conn *websocket.Conn, msg models.Message) error {
	data, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("encode %s message: %w", msg.Type, err)
	}
	ctx, cancel := context.WithTimeout(ctx, writeTimeout)
	defer cancel()
	if err := conn.Write(ctx, websocket.MessageText, data); err != nil {
		return fmt.Errorf("write %s message: %w", msg.Type, err)
	}
	return nil
}

func (h *Hub) readLoop(ctx context.Context, conn *websocket.Conn, a *agentConn) error {
	for {
		_, data, err := conn.Read(ctx)
		if err != nil {
			return fmt.Errorf("read from agent %s: %w", a.name, err)
		}

		var msg models.Message
		if err := json.Unmarshal(data, &msg); err != nil {
			h.log.WarnContext(ctx, "ignoring malformed message", "agent", a.name, "error", err)
			continue
		}
		h.handle(ctx, a, msg)

		// Acknowledge once handled (stored, or deliberately dropped), so the
		// agent can forget it. Duplicates are acknowledged again.
		if msg.ID > 0 {
			a.processed.Store(msg.ID)
			select {
			case a.ackReady <- struct{}{}:
			default:
			}
		}
	}
}

func (h *Hub) handle(ctx context.Context, a *agentConn, msg models.Message) {
	switch msg.Type {
	case models.MsgJobLog:
		var l models.JobLog
		if err := msg.Decode(&l); err != nil {
			h.log.WarnContext(ctx, "ignoring malformed message", "agent", a.name, "error", err)
			return
		}
		h.handleLog(ctx, a, l)
	case models.MsgJobResult:
		var res models.JobResult
		if err := msg.Decode(&res); err != nil {
			h.log.WarnContext(ctx, "ignoring malformed message", "agent", a.name, "error", err)
			return
		}
		h.handleResult(ctx, a, res)
	case models.MsgJobRejected:
		var rej models.JobRejected
		if err := msg.Decode(&rej); err != nil {
			h.log.WarnContext(ctx, "ignoring malformed message", "agent", a.name, "error", err)
			return
		}
		h.handleRejected(ctx, a, rej)
	case models.MsgJobStep:
		var st models.JobStep
		if err := msg.Decode(&st); err != nil {
			h.log.WarnContext(ctx, "ignoring malformed message", "agent", a.name, "error", err)
			return
		}
		h.handleStep(ctx, a, st)
	default:
		h.log.DebugContext(ctx, "ignoring unknown message", "agent", a.name, "type", msg.Type)
	}
}

func (h *Hub) handleLog(ctx context.Context, a *agentConn, l models.JobLog) {
	if !h.holds(a.name, l.JobID) {
		h.log.WarnContext(ctx, "dropping log for a job the agent does not hold", "agent", a.name, "job_id", l.JobID)
		return
	}
	metrics.LogChunks.Inc()
	if err := h.store.AppendJobLog(ctx, l); err != nil {
		h.log.ErrorContext(ctx, "store job log", "job_id", l.JobID, "seq", l.Seq, "error", err)
	}
}

func (h *Hub) handleStep(ctx context.Context, a *agentConn, st models.JobStep) {
	if !h.holds(a.name, st.JobID) {
		h.log.WarnContext(ctx, "dropping step for a job the agent does not hold", "agent", a.name, "job_id", st.JobID)
		return
	}
	if err := h.store.RecordStep(ctx, st); err != nil {
		h.log.ErrorContext(ctx, "store job step", "job_id", st.JobID, "step", st.Step, "error", err)
	}
}

func (h *Hub) handleResult(ctx context.Context, a *agentConn, res models.JobResult) {
	if !h.holds(a.name, res.JobID) {
		h.log.WarnContext(ctx, "dropping result for a job the agent does not hold", "agent", a.name, "job_id", res.JobID)
		return
	}
	err := h.store.FinishJob(ctx, a.name, res)
	switch {
	case errors.Is(err, db.ErrJobConflict):
		// A resent result for a job already finished, or one that moved to
		// another agent after its lease expired.
		h.log.InfoContext(ctx, "ignoring result for a job no longer running here", "job_id", res.JobID, "agent", a.name)
	case err != nil:
		h.log.ErrorContext(ctx, "store job result", "job_id", res.JobID, "error", err)
	default:
		metrics.JobsFinished.WithLabelValues(string(res.Status)).Inc()
		h.log.InfoContext(ctx, "job finished", "job_id", res.JobID, "agent", a.name,
			"status", res.Status, "exit_code", res.ExitCode)
		h.jobChanged(res.JobID)
	}

	h.mu.Lock()
	if cur := h.agents[a.name]; cur != nil {
		if cur.jobID == res.JobID {
			cur.jobID = 0
		}
		cur.blocked = false
	}
	h.mu.Unlock()
	h.Wake()
}

// handleRejected requeues the job and stops offering work to the agent
// until it reports a result or reconnects.
func (h *Hub) handleRejected(ctx context.Context, a *agentConn, rej models.JobRejected) {
	h.log.WarnContext(ctx, "agent rejected job", "agent", a.name, "job_id", rej.JobID, "reason", rej.Reason)
	if err := h.store.RequeueJob(ctx, rej.JobID, a.name); err != nil {
		h.log.ErrorContext(ctx, "requeue rejected job", "job_id", rej.JobID, "error", err)
	}

	h.mu.Lock()
	if cur := h.agents[a.name]; cur != nil {
		if cur.jobID == rej.JobID {
			cur.jobID = 0
		}
		cur.blocked = true
	}
	h.mu.Unlock()
	h.Wake()
}
