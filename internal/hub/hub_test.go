package hub

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/sidumandr/cisid/internal/metrics"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"

	"github.com/sidumandr/cisid/internal/db"
	"github.com/sidumandr/cisid/internal/pipeline"
	"github.com/sidumandr/cisid/internal/telemetry"
	"github.com/sidumandr/cisid/pkg/models"
)

const (
	testTimeout = 5 * time.Second
	// quiet is how long a test waits to be confident something did not happen.
	quiet        = 150 * time.Millisecond
	drainTimeout = 300 * time.Millisecond
)

type mockStore struct {
	mock.Mock
	mu          sync.Mutex
	claimLabels [][]string
}

// ClaimJob records the labels it is called with; expectations match the
// other arguments.
func (m *mockStore) ClaimJob(ctx context.Context, agent string, labels []string, lease time.Duration) (models.Job, error) {
	m.mu.Lock()
	m.claimLabels = append(m.claimLabels, labels)
	m.mu.Unlock()
	args := m.Called(ctx, agent, lease)
	return args.Get(0).(models.Job), args.Error(1)
}

func (m *mockStore) RequeueForLabels(ctx context.Context, id int64, agent string, labels []string) error {
	return m.Called(ctx, id, agent, labels).Error(0)
}

func (m *mockStore) RenewLeases(ctx context.Context, agents []string, lease time.Duration) (int, error) {
	args := m.Called(ctx, agents, lease)
	return args.Int(0), args.Error(1)
}

func (m *mockStore) RequeueExpired(ctx context.Context) ([]int64, error) {
	args := m.Called(ctx)
	ids, _ := args.Get(0).([]int64)
	return ids, args.Error(1)
}

func (m *mockStore) CancelRequestedJobs(ctx context.Context, agents []string) ([]models.Job, error) {
	args := m.Called(ctx, agents)
	jobs, _ := args.Get(0).([]models.Job)
	return jobs, args.Error(1)
}

func (m *mockStore) SetJobSteps(ctx context.Context, id int64, names []string) error {
	return m.Called(ctx, id, names).Error(0)
}

func (m *mockStore) ExpandMatrix(ctx context.Context, id int64, combo map[string]string, siblings []map[string]string) ([]int64, error) {
	args := m.Called(ctx, id, combo, siblings)
	ids, _ := args.Get(0).([]int64)
	return ids, args.Error(1)
}

func (m *mockStore) AdoptJob(ctx context.Context, agent string, id int64, lease time.Duration) error {
	return m.Called(ctx, agent, id, lease).Error(0)
}

func (m *mockStore) RequeueJob(ctx context.Context, id int64, agent string) error {
	return m.Called(ctx, id, agent).Error(0)
}

func (m *mockStore) RequeueAgentJobs(ctx context.Context, agent string, keep int64) (int, error) {
	args := m.Called(ctx, agent, keep)
	return args.Int(0), args.Error(1)
}

func (m *mockStore) FinishJob(ctx context.Context, agent string, res models.JobResult) error {
	return m.Called(ctx, agent, res).Error(0)
}

func (m *mockStore) AppendJobLog(ctx context.Context, l models.JobLog) error {
	return m.Called(ctx, l).Error(0)
}

func (m *mockStore) RecordStep(ctx context.Context, st models.JobStep) error {
	return m.Called(ctx, st).Error(0)
}

type mockPlanner struct {
	mock.Mock
}

// Plan returns the plan given to Return, or wraps the steps given to it.
func (m *mockPlanner) Plan(ctx context.Context, job models.Job) (pipeline.Plan, error) {
	args := m.Called(ctx, job)
	if plan, ok := args.Get(0).(pipeline.Plan); ok {
		return plan, args.Error(1)
	}
	steps, _ := args.Get(0).([]models.StepSpec)
	return pipeline.Plan{Assignment: models.JobAssignment{Steps: steps}}, args.Error(1)
}

// signal returns a mock Run hook that reports each call on the channel.
func signal(ch chan<- struct{}) func(mock.Arguments) {
	return func(mock.Arguments) { ch <- struct{}{} }
}

func waitFor(t *testing.T, ch <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(testTimeout):
		t.Fatalf("timed out waiting for %s", what)
	}
}

func expectNone(t *testing.T, ch <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-ch:
		t.Fatalf("unexpected %s", what)
	case <-time.After(quiet):
	}
}

var cloneSteps = []models.StepSpec{{Name: "clone", Image: "alpine/git:2.52.0", Commands: []string{"git init -q ."}}}

type fixture struct {
	hub     *Hub
	store   *mockStore
	planner *mockPlanner
	url     string
	cancel  context.CancelFunc
	done    chan struct{}
}

// newFixture starts a hub backed by mocks. setup registers the test's own
// expectations; afterwards permissive defaults are added: no pending jobs,
// nothing to requeue, every job plans to cloneSteps.
func newFixture(t *testing.T, setup func(s *mockStore, p *mockPlanner), opts ...Option) *fixture {
	t.Helper()
	f := &fixture{store: &mockStore{}, planner: &mockPlanner{}, done: make(chan struct{})}
	if setup != nil {
		setup(f.store, f.planner)
	}
	f.store.On("ClaimJob", mock.Anything, mock.Anything, mock.Anything).Return(models.Job{}, db.ErrNoPendingJob).Maybe()
	f.store.On("RequeueAgentJobs", mock.Anything, mock.Anything, mock.Anything).Return(0, nil).Maybe()
	f.store.On("RenewLeases", mock.Anything, mock.Anything, mock.Anything).Return(0, nil).Maybe()
	f.store.On("RequeueExpired", mock.Anything).Return([]int64(nil), nil).Maybe()
	f.store.On("AdoptJob", mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(nil).Maybe()
	f.store.On("CancelRequestedJobs", mock.Anything, mock.Anything).Return([]models.Job(nil), nil).Maybe()
	f.store.On("SetJobSteps", mock.Anything, mock.Anything, mock.Anything).Return(nil).Maybe()
	f.planner.On("Plan", mock.Anything, mock.Anything).Return(cloneSteps, nil).Maybe()

	f.hub = New(f.store, f.planner, append([]Option{WithLogger(slog.New(slog.DiscardHandler)),
		WithPollInterval(time.Hour), WithDrainTimeout(drainTimeout)}, opts...)...)
	srv := httptest.NewServer(f.hub)
	t.Cleanup(srv.Close)
	f.url = "ws" + strings.TrimPrefix(srv.URL, "http")

	ctx, cancel := context.WithCancel(t.Context())
	f.cancel = cancel
	go func() {
		defer close(f.done)
		assert.NoError(t, f.hub.Run(ctx))
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-f.done:
		case <-time.After(testTimeout):
			t.Error("hub did not stop")
		}
	})
	return f
}

// testAgent is a raw WebSocket client speaking the agent protocol.
type testAgent struct {
	conn   *websocket.Conn
	msgs   chan models.Message
	closed chan websocket.StatusCode
}

func (f *fixture) dial(t *testing.T) *testAgent {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), testTimeout)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, f.url, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.CloseNow() })

	ta := &testAgent{conn: conn, msgs: make(chan models.Message, 100), closed: make(chan websocket.StatusCode, 1)}
	go func() {
		defer close(ta.msgs)
		for {
			_, data, err := conn.Read(context.Background())
			if err != nil {
				ta.closed <- websocket.CloseStatus(err)
				return
			}
			var msg models.Message
			if assert.NoError(t, json.Unmarshal(data, &msg)) {
				ta.msgs <- msg
			}
		}
	}()
	return ta
}

// connect dials and sends a hello for name.
func (f *fixture) connect(t *testing.T, name string, runningJob int64) *testAgent {
	t.Helper()
	ta := f.dial(t)
	ta.send(t, models.MsgAgentHello, models.AgentHello{Name: name, Capacity: 1, RunningJobID: runningJob})
	return ta
}

func (ta *testAgent) send(t *testing.T, typ models.MessageType, payload any) {
	t.Helper()
	msg, err := models.NewMessage(typ, payload)
	require.NoError(t, err)
	data, err := json.Marshal(msg)
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(t.Context(), testTimeout)
	defer cancel()
	require.NoError(t, ta.conn.Write(ctx, websocket.MessageText, data))
}

func (ta *testAgent) expectAssignment(t *testing.T) models.JobAssignment {
	t.Helper()
	select {
	case msg, ok := <-ta.msgs:
		require.True(t, ok, "connection closed while waiting for an assignment")
		require.Equal(t, models.MsgJobAssigned, msg.Type)
		var a models.JobAssignment
		require.NoError(t, msg.Decode(&a))
		return a
	case <-time.After(testTimeout):
		t.Fatal("timed out waiting for an assignment")
		return models.JobAssignment{}
	}
}

func (ta *testAgent) expectNoMessage(t *testing.T) {
	t.Helper()
	select {
	case msg, ok := <-ta.msgs:
		if ok {
			t.Fatalf("unexpected %s message", msg.Type)
		}
	case <-time.After(quiet):
	}
}

func (ta *testAgent) expectClosed(t *testing.T) websocket.StatusCode {
	t.Helper()
	select {
	case code := <-ta.closed:
		return code
	case <-time.After(testTimeout):
		t.Fatal("server did not close the connection")
		return 0
	}
}

func job(id int64) models.Job {
	return models.Job{ID: id, Repo: "sidumandr/cisid", CloneURL: "https://github.com/sidumandr/cisid.git",
		Branch: "main", CommitSHA: "6113728f27ae82c7b1a177c8d03f9e96e0adf246", Status: models.JobStatusRunning}
}

func TestHubRunsJobLifecycle(t *testing.T) {
	logged := make(chan struct{}, 1)
	finished := make(chan struct{}, 1)
	claimedAgain := make(chan struct{}, 10)
	logLine := models.JobLog{JobID: 42, Step: 0, Stream: models.StreamStdout, Seq: 1, Data: "Cloning\n"}
	result := models.JobResult{JobID: 42, Status: models.JobStatusSuccess}

	f := newFixture(t, func(s *mockStore, p *mockPlanner) {
		s.On("ClaimJob", mock.Anything, "agent-1", mock.Anything).Return(job(42), nil).Once()
		s.On("ClaimJob", mock.Anything, "agent-1", mock.Anything).Return(models.Job{}, db.ErrNoPendingJob).
			Run(signal(claimedAgain))
		p.On("Plan", mock.Anything, job(42)).Return(cloneSteps, nil).Once()
		s.On("AppendJobLog", mock.Anything, logLine).Return(nil).Run(signal(logged)).Once()
		s.On("FinishJob", mock.Anything, "agent-1", result).Return(nil).Run(signal(finished)).Once()
	})

	a := f.connect(t, "agent-1", 0)
	assert.Equal(t, models.JobAssignment{JobID: 42, Steps: cloneSteps}, a.expectAssignment(t))

	a.send(t, models.MsgJobLog, logLine)
	waitFor(t, logged, "log to be stored")

	a.send(t, models.MsgJobResult, result)
	waitFor(t, finished, "result to be stored")
	waitFor(t, claimedAgain, "agent to be offered the next job")

	f.store.AssertExpectations(t)
	f.planner.AssertExpectations(t)
}

// TestHubTracesDispatch checks that dispatching continues the trace stored
// with the job: the planner sees the span (so its GitHub requests join the
// trace) and the agent is told to continue under it.
func TestHubTracesDispatch(t *testing.T) {
	rec := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec))
	const pushTrace = "00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01"
	unplannable, traced := job(1), job(2)
	unplannable.TraceParent, traced.TraceParent = pushTrace, pushTrace
	inSpan := mock.MatchedBy(func(ctx context.Context) bool { return trace.SpanContextFromContext(ctx).IsValid() })

	f := newFixture(t, func(s *mockStore, p *mockPlanner) {
		s.On("ClaimJob", mock.Anything, "agent-1", mock.Anything).Return(unplannable, nil).Once()
		s.On("ClaimJob", mock.Anything, "agent-1", mock.Anything).Return(traced, nil).Once()
		p.On("Plan", inSpan, unplannable).Return(nil, errors.New("no steps")).Once()
		p.On("Plan", inSpan, traced).Return(cloneSteps, nil).Once()
		s.On("FinishJob", mock.Anything, "agent-1", mock.Anything).Return(nil).Once()
	}, WithTracerProvider(tp))

	asg := f.connect(t, "agent-1", 0).expectAssignment(t)
	require.Eventually(t, func() bool { return len(rec.Ended()) == 2 }, testTimeout, 5*time.Millisecond)
	spans := rec.Ended()

	for _, span := range spans {
		assert.Equal(t, "dispatch job", span.Name())
		assert.Equal(t, "0af7651916cd43dd8448eb211c80319c", span.SpanContext().TraceID().String(), "the push's trace")
		assert.Equal(t, "b7ad6b7169203331", span.Parent().SpanID().String())
		assert.Contains(t, span.Attributes(), telemetry.AttrAgent.String("agent-1"))
	}
	assert.Contains(t, spans[0].Attributes(), telemetry.AttrJobID.Int64(1))
	assert.Equal(t, codes.Error, spans[0].Status().Code, "a job that cannot be planned marks its span")
	assert.Contains(t, spans[1].Attributes(), telemetry.AttrJobID.Int64(2))
	assert.Equal(t, codes.Unset, spans[1].Status().Code)

	assert.Equal(t, int64(2), asg.JobID)
	assert.Equal(t, "00-0af7651916cd43dd8448eb211c80319c-"+spans[1].SpanContext().SpanID().String()+"-01", asg.TraceParent,
		"the agent continues under the dispatch span")
	f.planner.AssertExpectations(t)
}

func TestHubRequiresHelloFirst(t *testing.T) {
	f := newFixture(t, nil)

	a := f.dial(t)
	a.send(t, models.MsgJobLog, models.JobLog{JobID: 1, Seq: 1, Stream: models.StreamStdout, Data: "x"})

	assert.Equal(t, websocket.StatusPolicyViolation, a.expectClosed(t))
	f.store.AssertNotCalled(t, "RequeueAgentJobs", mock.Anything, mock.Anything, mock.Anything)
}

func TestHubReconnectKeepsRunningJob(t *testing.T) {
	requeued := make(chan struct{}, 1)
	claimed := make(chan struct{}, 10)
	finished := make(chan struct{}, 1)
	result := models.JobResult{JobID: 7, Status: models.JobStatusFailed, ExitCode: 1, FailedStep: new(int)}

	f := newFixture(t, func(s *mockStore, p *mockPlanner) {
		s.On("AdoptJob", mock.Anything, "agent-1", int64(7), defaultLease).Return(nil).Once()
		s.On("RequeueAgentJobs", mock.Anything, "agent-1", int64(7)).Return(2, nil).Run(signal(requeued)).Once()
		s.On("ClaimJob", mock.Anything, "agent-1", mock.Anything).Return(models.Job{}, db.ErrNoPendingJob).Run(signal(claimed))
		s.On("FinishJob", mock.Anything, "agent-1", result).Return(nil).Run(signal(finished)).Once()
	})

	a := f.connect(t, "agent-1", 7)
	waitFor(t, requeued, "other jobs of the agent to be requeued")
	f.hub.Wake()
	expectNone(t, claimed, "claim for an agent still running job 7")

	// Logs of the job the agent reported are accepted.
	logged := make(chan struct{}, 1)
	f.store.On("AppendJobLog", mock.Anything, mock.MatchedBy(func(l models.JobLog) bool { return l.JobID == 7 })).
		Return(nil).Run(signal(logged)).Once()
	a.send(t, models.MsgJobLog, models.JobLog{JobID: 7, Seq: 9, Stream: models.StreamStdout, Data: "still going\n"})
	waitFor(t, logged, "log of the running job")

	// So are its steps.
	stepped := make(chan struct{}, 1)
	step := models.JobStep{JobID: 7, Step: 1, StepResult: models.StepResult{State: models.StepRunning}}
	f.store.On("RecordStep", mock.Anything, step).Return(nil).Run(signal(stepped)).Once()
	a.send(t, models.MsgJobStep, step)
	waitFor(t, stepped, "step of the running job")

	a.send(t, models.MsgJobResult, result)
	waitFor(t, finished, "result")
	waitFor(t, claimed, "claim once the agent is idle")
}

func TestHubRejectedJobIsRequeued(t *testing.T) {
	requeued := make(chan struct{}, 1)
	claimed := make(chan struct{}, 10)

	f := newFixture(t, func(s *mockStore, p *mockPlanner) {
		s.On("ClaimJob", mock.Anything, "agent-1", mock.Anything).Return(job(5), nil).Once()
		s.On("ClaimJob", mock.Anything, "agent-1", mock.Anything).Return(models.Job{}, db.ErrNoPendingJob).Run(signal(claimed))
		s.On("RequeueJob", mock.Anything, int64(5), "agent-1").Return(nil).Run(signal(requeued)).Once()
	})

	a := f.connect(t, "agent-1", 0)
	assert.Equal(t, int64(5), a.expectAssignment(t).JobID)
	a.send(t, models.MsgJobRejected, models.JobRejected{JobID: 5, Reason: "agent busy"})
	waitFor(t, requeued, "rejected job to be requeued")

	// The agent gets no work until it reports a result.
	f.hub.Wake()
	expectNone(t, claimed, "claim for an agent that rejected work")
	a.expectNoMessage(t)
}

func TestHubDropsLogsForJobsTheAgentDoesNotHold(t *testing.T) {
	f := newFixture(t, func(s *mockStore, p *mockPlanner) {
		s.On("ClaimJob", mock.Anything, "agent-1", mock.Anything).Return(job(42), nil).Once()
	})

	a := f.connect(t, "agent-1", 0)
	a.expectAssignment(t)
	a.send(t, models.MsgJobLog, models.JobLog{JobID: 99, Seq: 1, Stream: models.StreamStdout, Data: "forged\n"})
	a.send(t, models.MsgJobStep, models.JobStep{JobID: 99, Step: 0, StepResult: models.StepResult{State: models.StepFailed}})
	a.send(t, models.MsgJobResult, models.JobResult{JobID: 99, Status: models.JobStatusSuccess})

	time.Sleep(quiet)
	f.store.AssertNotCalled(t, "AppendJobLog", mock.Anything, mock.Anything)
	f.store.AssertNotCalled(t, "RecordStep", mock.Anything, mock.Anything)
	f.store.AssertNotCalled(t, "FinishJob", mock.Anything, mock.Anything, mock.Anything)
}

func TestHubFailsJobThatCannotBePlanned(t *testing.T) {
	failed := make(chan struct{}, 1)
	planErr := errors.New("clone URL is empty")

	f := newFixture(t, func(s *mockStore, p *mockPlanner) {
		s.On("ClaimJob", mock.Anything, "agent-1", mock.Anything).Return(job(1), nil).Once()
		s.On("ClaimJob", mock.Anything, "agent-1", mock.Anything).Return(job(2), nil).Once()
		p.On("Plan", mock.Anything, job(1)).Return(nil, planErr).Once()
		s.On("FinishJob", mock.Anything, "agent-1", models.JobResult{
			JobID: 1, Status: models.JobStatusFailed, ExitCode: -1, Error: "plan job: clone URL is empty",
		}).Return(nil).Run(signal(failed)).Once()
	})

	a := f.connect(t, "agent-1", 0)
	waitFor(t, failed, "unplannable job to be failed")
	assert.Equal(t, int64(2), a.expectAssignment(t).JobID, "the agent moves on to the next job")
}

func TestHubRequeuesJobWhenPlanningFailsTemporarily(t *testing.T) {
	requeued := make(chan struct{}, 1)
	claims := make(chan struct{}, 10)
	f := newFixture(t, func(s *mockStore, p *mockPlanner) {
		s.On("ClaimJob", mock.Anything, "agent-1", mock.Anything).Return(job(3), nil).Run(signal(claims)).Once()
		p.On("Plan", mock.Anything, job(3)).
			Return(nil, fmt.Errorf("%w: GitHub temporarily unavailable", pipeline.ErrTemporary)).Once()
		s.On("RequeueJob", mock.Anything, int64(3), "agent-1").Return(nil).Run(signal(requeued)).Once()
	})

	a := f.connect(t, "agent-1", 0)
	waitFor(t, claims, "claim")
	waitFor(t, requeued, "job to be requeued")

	// The round stops instead of claiming the same job again right away.
	expectNone(t, claims, "immediate re-claim")
	a.expectNoMessage(t)
	f.store.AssertNotCalled(t, "FinishJob", mock.Anything, mock.Anything, mock.Anything)
}

func TestHubRenewsLeasesOfConnectedAgents(t *testing.T) {
	renewed := make(chan struct{}, 100)
	f := newFixture(t, func(s *mockStore, p *mockPlanner) {
		s.On("RenewLeases", mock.Anything, []string{"agent-1"}, 300*time.Millisecond).
			Return(1, nil).Run(signal(renewed))
	}, WithLease(300*time.Millisecond))

	f.connect(t, "agent-1", 0)
	waitFor(t, renewed, "lease renewal")
	waitFor(t, renewed, "a second lease renewal")
}

func TestHubDispatchesJobsWithExpiredLeases(t *testing.T) {
	f := newFixture(t, func(s *mockStore, p *mockPlanner) {
		s.On("RequeueExpired", mock.Anything).Return([]int64{9}, nil).Once()
		s.On("ClaimJob", mock.Anything, "agent-1", mock.Anything).Return(models.Job{}, db.ErrNoPendingJob).Once()
		s.On("ClaimJob", mock.Anything, "agent-1", mock.Anything).Return(job(9), nil).Once()
	}, WithLease(300*time.Millisecond))

	a := f.connect(t, "agent-1", 0)

	// Nothing is pending when the agent connects; the expired job is
	// dispatched as soon as the lease check requeues it.
	assert.Equal(t, int64(9), a.expectAssignment(t).JobID)
}

func TestHubReconnectWithJobTakenOver(t *testing.T) {
	claimed := make(chan struct{}, 10)
	f := newFixture(t, func(s *mockStore, p *mockPlanner) {
		s.On("AdoptJob", mock.Anything, "agent-1", int64(7), mock.Anything).
			Return(fmt.Errorf("adopt: %w", db.ErrJobConflict)).Once()
		s.On("ClaimJob", mock.Anything, "agent-1", mock.Anything).
			Return(models.Job{}, db.ErrNoPendingJob).Run(signal(claimed))
	})

	f.connect(t, "agent-1", 7)

	// The agent is still busy locally, so it gets no new work yet.
	f.hub.Wake()
	expectNone(t, claimed, "claim for an agent busy with a lost job")
}

func TestHubDispatchesToEveryIdleAgent(t *testing.T) {
	f := newFixture(t, func(s *mockStore, p *mockPlanner) {
		s.On("ClaimJob", mock.Anything, mock.Anything, mock.Anything).Return(job(1), nil).Once()
		s.On("ClaimJob", mock.Anything, mock.Anything, mock.Anything).Return(job(2), nil).Once()
	})

	a1 := f.connect(t, "agent-1", 0)
	a2 := f.connect(t, "agent-2", 0)

	ids := []int64{a1.expectAssignment(t).JobID, a2.expectAssignment(t).JobID}
	assert.ElementsMatch(t, []int64{1, 2}, ids)
	a1.expectNoMessage(t)
	a2.expectNoMessage(t)
}

func TestHubReplacesConnectionWithSameName(t *testing.T) {
	f := newFixture(t, nil)

	old := f.connect(t, "agent-1", 0)
	time.Sleep(quiet) // let the first hello register
	f.connect(t, "agent-1", 0)

	assert.Equal(t, websocket.StatusNormalClosure, old.expectClosed(t))
}

func (ta *testAgent) expectMessage(t *testing.T, typ models.MessageType) {
	t.Helper()
	select {
	case msg, ok := <-ta.msgs:
		require.True(t, ok, "connection closed while waiting for %s", typ)
		require.Equal(t, typ, msg.Type)
	case <-time.After(testTimeout):
		t.Fatalf("timed out waiting for %s", typ)
	}
}

func TestHubShutdownDrainsAgentMessages(t *testing.T) {
	logged := make(chan struct{}, 1)
	finished := make(chan struct{}, 1)
	f := newFixture(t, func(s *mockStore, p *mockPlanner) {
		s.On("ClaimJob", mock.Anything, "agent-1", mock.Anything).Return(job(42), nil).Once()
		s.On("AppendJobLog", mock.Anything, mock.Anything).Return(nil).Run(signal(logged)).Once()
		s.On("FinishJob", mock.Anything, "agent-1", mock.Anything).Return(nil).Run(signal(finished)).Once()
	})
	a := f.connect(t, "agent-1", 0)
	a.expectAssignment(t)

	f.cancel()
	a.expectMessage(t, models.MsgServerClosing)

	// Messages sent after the announcement, before the agent closes, are kept.
	a.send(t, models.MsgJobLog, models.JobLog{JobID: 42, Seq: 1, Stream: models.StreamStdout, Data: "last words\n"})
	a.send(t, models.MsgJobResult, models.JobResult{JobID: 42, Status: models.JobStatusSuccess})
	require.NoError(t, a.conn.Close(websocket.StatusNormalClosure, "server closing"))

	waitFor(t, logged, "log sent while draining")
	waitFor(t, finished, "result sent while draining")
	select {
	case <-f.done:
	case <-time.After(testTimeout):
		t.Fatal("Run did not return")
	}
}

func TestHubShutdownClosesAgentConnections(t *testing.T) {
	f := newFixture(t, nil)
	a := f.connect(t, "agent-1", 0)
	time.Sleep(quiet)

	f.cancel()

	// An agent that ignores server_closing is disconnected after the drain timeout.
	a.expectMessage(t, models.MsgServerClosing)
	assert.Equal(t, websocket.StatusGoingAway, a.expectClosed(t))
	select {
	case <-f.done:
	case <-time.After(testTimeout):
		t.Fatal("Run did not return")
	}

	ctx, cancel := context.WithTimeout(t.Context(), testTimeout)
	defer cancel()
	_, resp, err := websocket.Dial(ctx, f.url, nil)
	require.Error(t, err)
	if assert.NotNil(t, resp) {
		assert.Equal(t, http.StatusServiceUnavailable, resp.StatusCode)
	}
}

func (ta *testAgent) sendID(t *testing.T, id int64, typ models.MessageType, payload any) {
	t.Helper()
	msg, err := models.NewMessage(typ, payload)
	require.NoError(t, err)
	msg.ID = id
	data, err := json.Marshal(msg)
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(t.Context(), testTimeout)
	defer cancel()
	require.NoError(t, ta.conn.Write(ctx, websocket.MessageText, data))
}

// expectAck waits until the server has acknowledged up to id.
func (ta *testAgent) expectAck(t *testing.T, id int64) {
	t.Helper()
	deadline := time.After(testTimeout)
	for {
		select {
		case msg, ok := <-ta.msgs:
			require.True(t, ok, "connection closed while waiting for ack %d", id)
			require.Equal(t, models.MsgAck, msg.Type)
			var ack models.Ack
			require.NoError(t, msg.Decode(&ack))
			if ack.ID >= id {
				assert.Equal(t, id, ack.ID)
				return
			}
		case <-deadline:
			t.Fatalf("timed out waiting for ack %d", id)
		}
	}
}

func TestHubAcknowledgesMessages(t *testing.T) {
	finished := make(chan struct{}, 1)
	f := newFixture(t, func(s *mockStore, p *mockPlanner) {
		s.On("ClaimJob", mock.Anything, "agent-1", mock.Anything).Return(job(42), nil).Once()
		s.On("AppendJobLog", mock.Anything, mock.Anything).Return(nil).Once()
		s.On("FinishJob", mock.Anything, "agent-1", mock.Anything).Return(nil).Run(signal(finished)).Once()
	})
	a := f.connect(t, "agent-1", 0)
	a.expectAssignment(t)

	a.sendID(t, 5, models.MsgJobLog, models.JobLog{JobID: 42, Seq: 1, Stream: models.StreamStdout, Data: "x\n"})
	a.sendID(t, 6, models.MsgJobResult, models.JobResult{JobID: 42, Status: models.JobStatusSuccess})
	a.expectAck(t, 6)
	waitFor(t, finished, "result")

	// A resent result is dropped, but acknowledged so the agent stops resending.
	a.sendID(t, 6, models.MsgJobResult, models.JobResult{JobID: 42, Status: models.JobStatusSuccess})
	a.sendID(t, 7, models.MsgJobResult, models.JobResult{JobID: 42, Status: models.JobStatusSuccess})
	a.expectAck(t, 7)
	f.store.AssertNumberOfCalls(t, "FinishJob", 1)
}

func TestHubReportsJobChanges(t *testing.T) {
	var (
		mu      sync.Mutex
		changes []int64
	)
	finished := make(chan struct{}, 1)
	f := newFixture(t, func(s *mockStore, p *mockPlanner) {
		s.On("ClaimJob", mock.Anything, "agent-1", mock.Anything).Return(job(42), nil).Once()
		s.On("FinishJob", mock.Anything, "agent-1", mock.Anything).Return(nil).Run(signal(finished)).Once()
	}, WithJobChanged(func(id int64) {
		mu.Lock()
		defer mu.Unlock()
		changes = append(changes, id)
	}))

	a := f.connect(t, "agent-1", 0)
	a.expectAssignment(t)
	a.send(t, models.MsgJobResult, models.JobResult{JobID: 42, Status: models.JobStatusSuccess})
	waitFor(t, finished, "result")

	require.Eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(changes) == 2
	}, testTimeout, 5*time.Millisecond)
	assert.Equal(t, []int64{42, 42}, changes, "assigned, then finished")
}

func TestHubDeliversCancelRequestsOnce(t *testing.T) {
	running := job(42)
	running.Agent = "agent-1"
	f := newFixture(t, func(s *mockStore, p *mockPlanner) {
		s.On("ClaimJob", mock.Anything, "agent-1", mock.Anything).Return(job(42), nil).Once()
		s.On("CancelRequestedJobs", mock.Anything, mock.Anything).Return([]models.Job{running}, nil)
	})
	a := f.connect(t, "agent-1", 0)
	a.expectAssignment(t)

	f.hub.Wake()
	a.expectMessage(t, models.MsgJobCancel)

	f.hub.Wake()
	f.hub.Wake()
	a.expectNoMessage(t)
}

func TestHubQueuesMatrixSiblings(t *testing.T) {
	var (
		mu      sync.Mutex
		changed []int64
	)
	combo := map[string]string{"GO": "1.26"}
	siblings := []map[string]string{{"GO": "1.27"}}
	f := newFixture(t, func(s *mockStore, p *mockPlanner) {
		s.On("ClaimJob", mock.Anything, "agent-1", mock.Anything).Return(job(42), nil).Once()
		p.On("Plan", mock.Anything, job(42)).Return(pipeline.Plan{
			Assignment: models.JobAssignment{Steps: cloneSteps}, Matrix: combo, Siblings: siblings,
		}, nil).Once()
		s.On("ExpandMatrix", mock.Anything, int64(42), combo, siblings).Return([]int64{43}, nil).Once()
	}, WithJobChanged(func(id int64) {
		mu.Lock()
		defer mu.Unlock()
		changed = append(changed, id)
	}))

	a := f.connect(t, "agent-1", 0)
	assert.Equal(t, models.JobAssignment{JobID: 42, Steps: cloneSteps}, a.expectAssignment(t))
	f.store.AssertExpectations(t)
	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, []int64{43, 42}, changed, "the new sibling is reported, then the assignment")
}

func TestHubRequeuesWhenMatrixCannotBeExpanded(t *testing.T) {
	requeued := make(chan struct{}, 1)
	f := newFixture(t, func(s *mockStore, p *mockPlanner) {
		s.On("ClaimJob", mock.Anything, "agent-1", mock.Anything).Return(job(42), nil).Once()
		p.On("Plan", mock.Anything, job(42)).Return(pipeline.Plan{
			Assignment: models.JobAssignment{Steps: cloneSteps},
			Matrix:     map[string]string{"GO": "1"}, Siblings: []map[string]string{{"GO": "2"}},
		}, nil).Once()
		s.On("ExpandMatrix", mock.Anything, int64(42), mock.Anything, mock.Anything).Return(nil, errors.New("db down")).Once()
		s.On("RequeueJob", mock.Anything, int64(42), "agent-1").Return(nil).Run(signal(requeued)).Once()
	})

	a := f.connect(t, "agent-1", 0)
	waitFor(t, requeued, "requeue")
	a.expectNoMessage(t)
}

// TestHubAcceptsEveryAgentToken checks token rotation: agents with the new
// token and those still on the old one both connect.
func TestHubAcceptsEveryAgentToken(t *testing.T) {
	f := newFixture(t, nil, WithAgentTokens("new-token-0123456789", "old-token-0123456789"))

	for _, token := range []string{"new-token-0123456789", "old-token-0123456789"} {
		ctx, cancel := context.WithTimeout(t.Context(), testTimeout)
		conn, _, err := websocket.Dial(ctx, f.url, &websocket.DialOptions{
			HTTPHeader: http.Header{"Authorization": {"Bearer " + token}},
		})
		cancel()
		require.NoError(t, err, token)
		_ = conn.CloseNow()
	}
}

func TestHubRequiresAgentToken(t *testing.T) {
	f := newFixture(t, nil, WithAgentTokens("agent-token-0123456789"))

	for _, token := range []string{"", "wrong"} {
		header := http.Header{}
		if token != "" {
			header.Set("Authorization", "Bearer "+token)
		}
		ctx, cancel := context.WithTimeout(t.Context(), testTimeout)
		_, resp, err := websocket.Dial(ctx, f.url, &websocket.DialOptions{HTTPHeader: header})
		cancel()
		require.Error(t, err, "token %q", token)
		require.NotNil(t, resp)
		assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
	}
	f.store.AssertNotCalled(t, "RequeueAgentJobs", mock.Anything, mock.Anything, mock.Anything)

	ctx, cancel := context.WithTimeout(t.Context(), testTimeout)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, f.url, &websocket.DialOptions{
		HTTPHeader: http.Header{"Authorization": {"Bearer agent-token-0123456789"}},
	})
	require.NoError(t, err)
	_ = conn.CloseNow()
}

func TestHubCountsFinishedJobsAndAgents(t *testing.T) {
	finished := make(chan struct{}, 1)
	f := newFixture(t, func(s *mockStore, p *mockPlanner) {
		s.On("ClaimJob", mock.Anything, "agent-1", mock.Anything).Return(job(42), nil).Once()
		s.On("FinishJob", mock.Anything, "agent-1", mock.Anything).Return(nil).Run(signal(finished)).Once()
	})
	counter := metrics.JobsFinished.WithLabelValues("success")
	before := testutil.ToFloat64(counter)

	a := f.connect(t, "agent-1", 0)
	a.expectAssignment(t)
	assert.Equal(t, 1, f.hub.Connected())
	a.send(t, models.MsgJobResult, models.JobResult{JobID: 42, Status: models.JobStatusSuccess})
	waitFor(t, finished, "result")

	assert.Eventually(t, func() bool { return testutil.ToFloat64(counter) == before+1 }, testTimeout, 5*time.Millisecond)
}
