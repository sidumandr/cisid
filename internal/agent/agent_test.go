package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/sidumandr/cisid/internal/runner"
	"github.com/sidumandr/cisid/pkg/models"
)

const testTimeout = 5 * time.Second

// mockRunner is a testify mock of StepRunner.
type mockRunner struct {
	mock.Mock
}

func (m *mockRunner) Run(ctx context.Context, step runner.Step, stdout, stderr io.Writer) (int, error) {
	args := m.Called(ctx, step, stdout, stderr)
	return args.Int(0), args.Error(1)
}

func (m *mockRunner) RemoveVolume(ctx context.Context, name string) error {
	return m.Called(ctx, name).Error(0)
}

// writeTo returns a mock Run hook that writes to the step's stdout/stderr.
func writeTo(stdout, stderr string) func(mock.Arguments) {
	return func(args mock.Arguments) {
		if stdout != "" {
			_, _ = io.WriteString(args.Get(2).(io.Writer), stdout)
		}
		if stderr != "" {
			_, _ = io.WriteString(args.Get(3).(io.Writer), stderr)
		}
	}
}

func notCanceled(ctx context.Context) bool { return ctx.Err() == nil }

func image(name string) any {
	return mock.MatchedBy(func(s runner.Step) bool { return s.Image == name })
}

// fakeServer is a WebSocket endpoint standing in for the cisid server.
type fakeServer struct {
	srv   *httptest.Server
	conns chan *serverConn
	// reject, if set, is called for every handshake; returning true answers 503.
	reject func(attempt int) bool
	dials  atomic.Int32
	// noAck stops the server from acknowledging messages.
	noAck atomic.Bool
}

type serverConn struct {
	conn   *websocket.Conn
	msgs   chan models.Message
	hello  models.AgentHello
	closed chan websocket.StatusCode
}

func newFakeServer(t *testing.T) *fakeServer {
	t.Helper()
	fs := &fakeServer{conns: make(chan *serverConn, 10)}
	fs.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempt := int(fs.dials.Add(1))
		if fs.reject != nil && fs.reject(attempt) {
			http.Error(w, "starting up", http.StatusServiceUnavailable)
			return
		}
		assert.Equal(t, "test-agent", r.Header.Get(agentHeader))

		conn, err := websocket.Accept(w, r, nil)
		if !assert.NoError(t, err) {
			return
		}
		sc := &serverConn{conn: conn, msgs: make(chan models.Message, 1000), closed: make(chan websocket.StatusCode, 1)}
		fs.conns <- sc

		defer close(sc.msgs)
		for {
			_, data, err := conn.Read(r.Context())
			if err != nil {
				sc.closed <- websocket.CloseStatus(err)
				return
			}
			var msg models.Message
			if !assert.NoError(t, json.Unmarshal(data, &msg)) {
				continue
			}
			sc.msgs <- msg
			if msg.ID > 0 && !fs.noAck.Load() {
				ack, _ := json.Marshal(models.Message{Type: models.MsgAck, Payload: []byte(fmt.Sprintf(`{"id":%d}`, msg.ID))})
				_ = conn.Write(r.Context(), websocket.MessageText, ack)
			}
		}
	}))
	t.Cleanup(fs.srv.Close)
	return fs
}

func (fs *fakeServer) url() string {
	return "ws" + strings.TrimPrefix(fs.srv.URL, "http") + "/agent/ws"
}

// accept waits for the next agent connection and consumes its hello.
func (fs *fakeServer) accept(t *testing.T) *serverConn {
	t.Helper()
	select {
	case sc := <-fs.conns:
		sc.expect(t, models.MsgAgentHello, &sc.hello)
		assert.Equal(t, "test-agent", sc.hello.Name)
		assert.Equal(t, 1, sc.hello.Capacity)
		return sc
	case <-time.After(testTimeout):
		t.Fatal("agent did not connect")
		return nil
	}
}

func (sc *serverConn) send(t *testing.T, typ models.MessageType, payload any) {
	t.Helper()
	msg, err := models.NewMessage(typ, payload)
	require.NoError(t, err)
	data, err := json.Marshal(msg)
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(t.Context(), testTimeout)
	defer cancel()
	require.NoError(t, sc.conn.Write(ctx, websocket.MessageText, data))
}

func (sc *serverConn) next(t *testing.T) models.Message {
	t.Helper()
	select {
	case msg, ok := <-sc.msgs:
		require.True(t, ok, "connection closed while waiting for a message")
		return msg
	case <-time.After(testTimeout):
		t.Fatal("timed out waiting for a message")
		return models.Message{}
	}
}

func (sc *serverConn) expect(t *testing.T, typ models.MessageType, v any) {
	t.Helper()
	msg := sc.next(t)
	require.Equal(t, typ, msg.Type)
	require.NoError(t, msg.Decode(v))
}

// collectJob reads job_log messages until the job_result for jobID arrives.
func (sc *serverConn) collectJob(t *testing.T, jobID int64) ([]models.JobLog, models.JobResult) {
	t.Helper()
	var logs []models.JobLog
	for {
		msg := sc.next(t)
		switch msg.Type {
		case models.MsgJobLog:
			var l models.JobLog
			require.NoError(t, msg.Decode(&l))
			logs = append(logs, l)
		case models.MsgJobResult:
			var res models.JobResult
			require.NoError(t, msg.Decode(&res))
			require.Equal(t, jobID, res.JobID)
			return logs, res
		default:
			t.Fatalf("unexpected message %q", msg.Type)
		}
	}
}

// startAgent runs an agent until the test ends. It returns the agent's cancel
// func and a wait func that returns Run's result.
func startAgent(t *testing.T, fs *fakeServer, r StepRunner) (context.CancelFunc, func() error) {
	t.Helper()
	a, err := New(Config{
		ServerURL:    fs.url(),
		Name:         "test-agent",
		MinBackoff:   10 * time.Millisecond,
		MaxBackoff:   50 * time.Millisecond,
		FlushTimeout: 2 * time.Second,
	}, r, slog.New(slog.DiscardHandler))
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(t.Context())
	var runErr error
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		runErr = a.Run(ctx)
	}()

	wait := func() error {
		select {
		case <-finished:
			return runErr
		case <-time.After(testTimeout):
			t.Error("agent did not stop")
			return context.DeadlineExceeded
		}
	}
	t.Cleanup(func() {
		cancel()
		_ = wait()
	})
	lastStarted = a
	return cancel, wait
}

// lastStarted is the agent most recently started by startAgent, for tests
// that need to observe its internals. Tests in this package do not run in
// parallel.
var lastStarted *Agent

func intPtr(i int) *int { return &i }

func TestNewValidatesConfig(t *testing.T) {
	for _, url := range []string{"", "http://localhost:8080/agent/ws", "localhost:8080"} {
		_, err := New(Config{ServerURL: url, Name: "a"}, &mockRunner{}, nil)
		assert.ErrorIs(t, err, ErrInvalidConfig, "url %q", url)
	}
	_, err := New(Config{ServerURL: "ws://localhost/agent/ws"}, &mockRunner{}, nil)
	assert.ErrorIs(t, err, ErrInvalidConfig, "empty name")
}

func TestAgentRunsAssignedJob(t *testing.T) {
	fs := newFakeServer(t)
	r := &mockRunner{}

	var workspace string
	r.On("Run", mock.Anything, mock.MatchedBy(func(s runner.Step) bool {
		if s.Image != "alpine/git:2.47" || len(s.Mounts) != 1 {
			return false
		}
		workspace = s.Mounts[0].Source
		assert.Equal(t, runner.Step{
			Image:      "alpine/git:2.47",
			Entrypoint: []string{"/bin/sh", "-c"},
			Cmd:        []string{"set -ex\ngit clone https://github.com/sidumandr/cisid.git .\ngit checkout 6113728"},
			Env: map[string]string{
				"GIT_TERMINAL_PROMPT": "0",
				"CI":                  "true",
				"CISID":               "true",
				"CISID_JOB_ID":        "42",
				"CISID_STEP":          "clone",
			},
			WorkingDir: "/workspace",
			Mounts:     []runner.Mount{{Type: runner.MountVolume, Source: workspace, Target: "/workspace"}},
			Timeout:    time.Minute,
		}, s)
		return true
	}), mock.Anything, mock.Anything).Run(writeTo("Cloning into '.'...\n", "")).Return(0, nil).Once()
	r.On("Run", mock.Anything, image("golang:1.25"), mock.Anything, mock.Anything).
		Run(writeTo("ok  \tgithub.com/sidumandr/cisid\n", "go: downloading testify\n")).Return(0, nil).Once()
	r.On("RemoveVolume", mock.MatchedBy(notCanceled), mock.MatchedBy(func(name string) bool {
		return name == workspace && strings.HasPrefix(name, "cisid-job-42-")
	})).Return(nil).Once()

	startAgent(t, fs, r)
	sc := fs.accept(t)
	sc.send(t, models.MsgJobAssigned, models.JobAssignment{
		JobID: 42,
		Steps: []models.StepSpec{
			{
				Name:           "clone",
				Image:          "alpine/git:2.47",
				Commands:       []string{"git clone https://github.com/sidumandr/cisid.git .", "git checkout 6113728"},
				Env:            map[string]string{"GIT_TERMINAL_PROMPT": "0", "CI": "false"},
				TimeoutSeconds: 60,
			},
			{Name: "test", Image: "golang:1.25", Commands: []string{"go test ./..."}},
		},
	})

	logs, res := sc.collectJob(t, 42)

	assert.Equal(t, []models.JobLog{
		{JobID: 42, Step: 0, Stream: models.StreamStdout, Seq: 1, Data: "Cloning into '.'...\n"},
		{JobID: 42, Step: 1, Stream: models.StreamStdout, Seq: 2, Data: "ok  \tgithub.com/sidumandr/cisid\n"},
		{JobID: 42, Step: 1, Stream: models.StreamStderr, Seq: 3, Data: "go: downloading testify\n"},
	}, logs)
	assert.Equal(t, models.JobResult{JobID: 42, Status: models.JobStatusSuccess}, res)
	r.AssertExpectations(t)
}

func TestAgentReportsFailedStep(t *testing.T) {
	fs := newFakeServer(t)
	r := &mockRunner{}
	r.On("Run", mock.Anything, image("golang:1.25"), mock.Anything, mock.Anything).
		Run(writeTo("", "FAIL\n")).Return(3, nil).Once()
	r.On("RemoveVolume", mock.Anything, mock.Anything).Return(nil).Once()

	startAgent(t, fs, r)
	sc := fs.accept(t)
	sc.send(t, models.MsgJobAssigned, models.JobAssignment{
		JobID: 7,
		Steps: []models.StepSpec{
			{Name: "test", Image: "golang:1.25", Commands: []string{"go test ./..."}},
			{Name: "deploy", Image: "alpine:3.22", Commands: []string{"echo deploy"}},
		},
	})

	logs, res := sc.collectJob(t, 7)

	assert.Len(t, logs, 1)
	assert.Equal(t, models.JobResult{
		JobID: 7, Status: models.JobStatusFailed, ExitCode: 3, FailedStep: intPtr(0),
	}, res)
	r.AssertExpectations(t)
	r.AssertNotCalled(t, "Run", mock.Anything, image("alpine:3.22"), mock.Anything, mock.Anything)
}

func TestAgentReportsRunnerError(t *testing.T) {
	fs := newFakeServer(t)
	r := &mockRunner{}
	pullErr := fmt.Errorf("%w: golang:9.99: manifest unknown", runner.ErrImagePull)
	r.On("Run", mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(-1, pullErr).Once()
	r.On("RemoveVolume", mock.Anything, mock.Anything).Return(nil).Once()

	startAgent(t, fs, r)
	sc := fs.accept(t)
	sc.send(t, models.MsgJobAssigned, models.JobAssignment{
		JobID: 8,
		Steps: []models.StepSpec{{Name: "test", Image: "golang:9.99", Commands: []string{"go test ./..."}}},
	})

	_, res := sc.collectJob(t, 8)

	assert.Equal(t, models.JobResult{
		JobID: 8, Status: models.JobStatusFailed, ExitCode: -1, FailedStep: intPtr(0),
		Error: "image pull failed: golang:9.99: manifest unknown",
	}, res)
	r.AssertExpectations(t)
}

func TestAgentFailsInvalidAssignment(t *testing.T) {
	tests := []struct {
		name  string
		steps []models.StepSpec
	}{
		{name: "no steps"},
		{name: "step without image", steps: []models.StepSpec{{Name: "x", Commands: []string{"true"}}}},
		{name: "step without commands", steps: []models.StepSpec{{Name: "x", Image: "alpine:3.22"}}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fs := newFakeServer(t)
			r := &mockRunner{}

			startAgent(t, fs, r)
			sc := fs.accept(t)
			sc.send(t, models.MsgJobAssigned, models.JobAssignment{JobID: 9, Steps: tt.steps})

			_, res := sc.collectJob(t, 9)

			assert.Equal(t, models.JobStatusFailed, res.Status)
			assert.Equal(t, -1, res.ExitCode)
			assert.Contains(t, res.Error, "invalid job")
			r.AssertNotCalled(t, "Run", mock.Anything, mock.Anything, mock.Anything, mock.Anything)
		})
	}
}

func TestAgentRejectsJobWhileBusy(t *testing.T) {
	fs := newFakeServer(t)
	r := &mockRunner{}
	release := make(chan struct{})
	r.On("Run", mock.Anything, mock.Anything, mock.Anything, mock.Anything).
		Run(func(mock.Arguments) { <-release }).Return(0, nil).Once()
	r.On("RemoveVolume", mock.Anything, mock.Anything).Return(nil).Once()

	startAgent(t, fs, r)
	sc := fs.accept(t)
	step := []models.StepSpec{{Name: "s", Image: "alpine:3.22", Commands: []string{"sleep 1"}}}
	sc.send(t, models.MsgJobAssigned, models.JobAssignment{JobID: 1, Steps: step})
	sc.send(t, models.MsgJobAssigned, models.JobAssignment{JobID: 2, Steps: step})

	var rejected models.JobRejected
	sc.expect(t, models.MsgJobRejected, &rejected)
	assert.Equal(t, models.JobRejected{JobID: 2, Reason: "agent busy"}, rejected)

	close(release)
	_, res := sc.collectJob(t, 1)
	assert.Equal(t, models.JobStatusSuccess, res.Status)
	r.AssertExpectations(t)
}

func TestAgentReconnectsAfterConnectionLoss(t *testing.T) {
	fs := newFakeServer(t)
	r := &mockRunner{}
	r.On("Run", mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(0, nil).Once()
	r.On("RemoveVolume", mock.Anything, mock.Anything).Return(nil).Once()

	startAgent(t, fs, r)
	first := fs.accept(t)
	require.NoError(t, first.conn.Close(websocket.StatusGoingAway, "server restarting"))

	second := fs.accept(t)
	second.send(t, models.MsgJobAssigned, models.JobAssignment{
		JobID: 5,
		Steps: []models.StepSpec{{Name: "s", Image: "alpine:3.22", Commands: []string{"true"}}},
	})
	_, res := second.collectJob(t, 5)
	assert.Equal(t, models.JobStatusSuccess, res.Status)
}

func TestAgentRetriesWhileServerUnavailable(t *testing.T) {
	fs := newFakeServer(t)
	fs.reject = func(attempt int) bool { return attempt <= 3 }

	startAgent(t, fs, &mockRunner{})
	fs.accept(t)

	assert.EqualValues(t, 4, fs.dials.Load())
}

func TestAgentDeliversResultAfterReconnect(t *testing.T) {
	fs := newFakeServer(t)
	r := &mockRunner{}
	release := make(chan struct{})
	r.On("Run", mock.Anything, mock.Anything, mock.Anything, mock.Anything).
		Run(func(mock.Arguments) { <-release }).Return(0, nil).Once()
	r.On("RemoveVolume", mock.Anything, mock.Anything).Return(nil).Once()

	startAgent(t, fs, r)
	first := fs.accept(t)
	assert.Zero(t, first.hello.RunningJobID)
	first.send(t, models.MsgJobAssigned, models.JobAssignment{
		JobID: 11,
		Steps: []models.StepSpec{{Name: "s", Image: "alpine:3.22", Commands: []string{"make"}}},
	})
	require.NoError(t, first.conn.Close(websocket.StatusGoingAway, "server restarting"))

	second := fs.accept(t)
	assert.Equal(t, int64(11), second.hello.RunningJobID, "hello must report the job still running")
	close(release)

	_, res := second.collectJob(t, 11)
	assert.Equal(t, models.JobStatusSuccess, res.Status)

	// Once the result has been sent, the agent no longer claims the job.
	require.NoError(t, second.conn.Close(websocket.StatusGoingAway, "server restarting"))
	third := fs.accept(t)
	assert.Zero(t, third.hello.RunningJobID)
}

func TestAgentHelloReportsUndeliveredResult(t *testing.T) {
	fs := newFakeServer(t)
	var serverDown atomic.Bool
	fs.reject = func(int) bool { return serverDown.Load() }

	r := &mockRunner{}
	release := make(chan struct{})
	r.On("Run", mock.Anything, mock.Anything, mock.Anything, mock.Anything).
		Run(func(mock.Arguments) { <-release }).Return(0, nil).Once()
	r.On("RemoveVolume", mock.Anything, mock.Anything).Return(nil).Once()

	startAgent(t, fs, r)
	a := lastStarted
	first := fs.accept(t)
	first.send(t, models.MsgJobAssigned, models.JobAssignment{
		JobID: 21,
		Steps: []models.StepSpec{{Name: "s", Image: "alpine:3.22", Commands: []string{"true"}}},
	})

	// The server goes away; the job finishes while the agent is disconnected,
	// so its result waits in the outbox.
	serverDown.Store(true)
	require.NoError(t, first.conn.Close(websocket.StatusGoingAway, "server restarting"))
	// A redial means the old session, including its writer, has ended.
	require.Eventually(t, func() bool { return fs.dials.Load() >= 2 }, testTimeout, 5*time.Millisecond)
	close(release)
	require.Eventually(t, func() bool { return a.out.pending.Load() > 0 && !a.busy.Load() },
		testTimeout, 5*time.Millisecond, "result was not queued")

	serverDown.Store(false)
	second := fs.accept(t)
	assert.Equal(t, int64(21), second.hello.RunningJobID, "an unreported job still counts as running")
	_, res := second.collectJob(t, 21)
	assert.Equal(t, models.JobStatusSuccess, res.Status)
}

func TestAgentShutdownCancelsRunningJob(t *testing.T) {
	fs := newFakeServer(t)
	r := &mockRunner{}
	started := make(chan struct{})
	r.On("Run", mock.Anything, mock.Anything, mock.Anything, mock.Anything).
		Run(func(args mock.Arguments) {
			close(started)
			<-args.Get(0).(context.Context).Done() // like the runner: stop on cancel
		}).Return(-1, context.Canceled).Once()
	r.On("RemoveVolume", mock.MatchedBy(notCanceled), mock.Anything).Return(nil).Once()

	cancel, wait := startAgent(t, fs, r)
	sc := fs.accept(t)
	sc.send(t, models.MsgJobAssigned, models.JobAssignment{
		JobID: 12,
		Steps: []models.StepSpec{{Name: "s", Image: "alpine:3.22", Commands: []string{"sleep 600"}}},
	})
	<-started

	cancel()

	_, res := sc.collectJob(t, 12)
	assert.Equal(t, models.JobResult{
		JobID: 12, Status: models.JobStatusCanceled, ExitCode: -1, FailedStep: intPtr(0),
		Error: "context canceled",
	}, res)

	require.NoError(t, wait())
	r.AssertExpectations(t)
}

func TestAgentIgnoresUnknownAndMalformedMessages(t *testing.T) {
	fs := newFakeServer(t)
	r := &mockRunner{}
	r.On("Run", mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(0, nil).Once()
	r.On("RemoveVolume", mock.Anything, mock.Anything).Return(nil).Once()

	startAgent(t, fs, r)
	sc := fs.accept(t)

	ctx, cancel := context.WithTimeout(t.Context(), testTimeout)
	defer cancel()
	require.NoError(t, sc.conn.Write(ctx, websocket.MessageText, []byte("not json")))
	sc.send(t, "server_announcement", map[string]string{"text": "hi"})
	require.NoError(t, sc.conn.Write(ctx, websocket.MessageText,
		[]byte(`{"type":"job_assigned","payload":{"job_id":"x"}}`)))

	sc.send(t, models.MsgJobAssigned, models.JobAssignment{
		JobID: 13,
		Steps: []models.StepSpec{{Name: "s", Image: "alpine:3.22", Commands: []string{"true"}}},
	})
	_, res := sc.collectJob(t, 13)
	assert.Equal(t, models.JobStatusSuccess, res.Status)
}

func TestAgentClosesConnectionWhenServerDrains(t *testing.T) {
	fs := newFakeServer(t)
	startAgent(t, fs, &mockRunner{})
	sc := fs.accept(t)

	sc.send(t, models.MsgServerClosing, struct{}{})

	select {
	case code := <-sc.closed:
		assert.Equal(t, websocket.StatusNormalClosure, code, "the agent closes cleanly")
	case <-time.After(testTimeout):
		t.Fatal("agent did not close the connection")
	}
	fs.accept(t) // and reconnects
}

func TestAgentResendsUnacknowledgedMessages(t *testing.T) {
	fs := newFakeServer(t)
	fs.noAck.Store(true)
	r := &mockRunner{}
	r.On("Run", mock.Anything, mock.Anything, mock.Anything, mock.Anything).
		Run(writeTo("compiling\n", "")).Return(0, nil).Once()
	r.On("RemoveVolume", mock.Anything, mock.Anything).Return(nil).Once()

	startAgent(t, fs, r)
	a := lastStarted
	first := fs.accept(t)
	first.send(t, models.MsgJobAssigned, models.JobAssignment{
		JobID: 31,
		Steps: []models.StepSpec{{Name: "s", Image: "alpine:3.22", Commands: []string{"make"}}},
	})

	// The server receives the log and the result but never acknowledges them.
	sent := []models.Message{first.next(t), first.next(t)}
	assert.Equal(t, []int64{1, 2}, []int64{sent[0].ID, sent[1].ID}, "IDs follow the send order")
	assert.Equal(t, []models.MessageType{models.MsgJobLog, models.MsgJobResult}, []models.MessageType{sent[0].Type, sent[1].Type})
	require.NoError(t, first.conn.Close(websocket.StatusGoingAway, "server crashed"))

	fs.noAck.Store(false)
	second := fs.accept(t)
	assert.Equal(t, int64(31), second.hello.RunningJobID, "an unacknowledged result keeps the job reported")
	resent := []models.Message{second.next(t), second.next(t)}
	assert.Equal(t, sent, resent, "unacknowledged messages are sent again, unchanged")

	require.Eventually(t, func() bool { return a.out.pending.Load() == 0 && a.runningJob.Load() == 0 },
		testTimeout, 5*time.Millisecond, "acknowledged messages are forgotten")
}

func TestOutboxAck(t *testing.T) {
	o := newOutbox(10)
	for _, typ := range []models.MessageType{models.MsgJobLog, models.MsgJobLog, models.MsgJobResult} {
		require.True(t, o.tryPut(models.Message{Type: typ}))
	}
	for range 3 {
		o.take(<-o.ch)
	}
	assert.EqualValues(t, 3, o.pending.Load())

	acked := o.ack(2)
	assert.Len(t, acked, 2)
	assert.EqualValues(t, 1, o.pending.Load())
	assert.Equal(t, int64(3), o.unacked()[0].ID)

	assert.Empty(t, o.ack(2), "acks are cumulative and idempotent")
	assert.Len(t, o.ack(99), 1)
	assert.Zero(t, o.pending.Load())
}

func TestOutboxDropsLogsWhenFull(t *testing.T) {
	o := newOutbox(2)
	require.True(t, o.tryPut(models.Message{Type: models.MsgJobLog}))
	o.take(<-o.ch) // sent but unacknowledged still counts
	require.True(t, o.tryPut(models.Message{Type: models.MsgJobLog}))

	assert.False(t, o.tryPut(models.Message{Type: models.MsgJobLog}))
	assert.EqualValues(t, 1, o.dropped.Load())
	assert.EqualValues(t, 2, o.pending.Load())
}

func TestAgentCancelsRunningJob(t *testing.T) {
	fs := newFakeServer(t)
	r := &mockRunner{}
	started := make(chan struct{})
	r.On("Run", mock.Anything, mock.Anything, mock.Anything, mock.Anything).
		Run(func(args mock.Arguments) {
			close(started)
			<-args.Get(0).(context.Context).Done() // the runner stops the container
		}).Return(-1, context.Canceled).Once()
	r.On("RemoveVolume", mock.Anything, mock.Anything).Return(nil).Once()

	startAgent(t, fs, r)
	sc := fs.accept(t)
	sc.send(t, models.MsgJobAssigned, models.JobAssignment{
		JobID: 51,
		Steps: []models.StepSpec{
			{Name: "build", Image: "alpine:3.22", Commands: []string{"sleep 600"}},
			{Name: "deploy", Image: "alpine:3.22", Commands: []string{"deploy"}},
		},
	})
	<-started

	sc.send(t, models.MsgJobCancel, models.JobCancel{JobID: 51})

	_, res := sc.collectJob(t, 51)
	assert.Equal(t, models.JobResult{
		JobID: 51, Status: models.JobStatusCanceled, ExitCode: -1, FailedStep: intPtr(0),
		Error: "canceled by request",
	}, res)
	r.AssertExpectations(t) // deploy never ran
}

func TestAgentIgnoresCancelForOtherJobs(t *testing.T) {
	a, err := New(Config{ServerURL: "ws://localhost/agent/ws", Name: "a"}, &mockRunner{}, slog.New(slog.DiscardHandler))
	require.NoError(t, err)
	ctx := a.startJob(t.Context(), 1)

	a.cancel(t.Context(), 2)

	assert.NoError(t, ctx.Err())
}

func TestAgentCancelsJobAcceptedButNotStarted(t *testing.T) {
	a, err := New(Config{ServerURL: "ws://localhost/agent/ws", Name: "a"}, &mockRunner{}, slog.New(slog.DiscardHandler))
	require.NoError(t, err)
	a.runningJob.Store(7) // accepted, waiting for the executor

	a.cancel(t.Context(), 7)
	ctx := a.startJob(t.Context(), 7)

	require.Error(t, ctx.Err())
	assert.Equal(t, errCanceledByServer, context.Cause(ctx))
	a.endJob(7)
	assert.Empty(t, a.cancelLater)
}

func TestAgentMasksSecretsInLogs(t *testing.T) {
	fs := newFakeServer(t)
	r := &mockRunner{}
	r.On("Run", mock.Anything, mock.MatchedBy(func(s runner.Step) bool {
		return s.Env["DEPLOY_TOKEN"] == "dep_s3cret_value"
	}), mock.Anything, mock.Anything).Run(func(args mock.Arguments) {
		stderr := args.Get(3).(io.Writer)
		_, _ = io.WriteString(stderr, "+ curl -H 'Authorization: dep_s3cr")
		_, _ = io.WriteString(stderr, "et_value' https://deploy\n")
		_, _ = io.WriteString(args.Get(2).(io.Writer), "token tail: dep_s3")
	}).Return(0, nil).Once()
	r.On("RemoveVolume", mock.Anything, mock.Anything).Return(nil).Once()

	startAgent(t, fs, r)
	sc := fs.accept(t)
	sc.send(t, models.MsgJobAssigned, models.JobAssignment{
		JobID: 61,
		Steps: []models.StepSpec{{
			Name: "deploy", Image: "alpine:3.22", Commands: []string{"./deploy.sh"},
			Secrets: map[string]string{"DEPLOY_TOKEN": "dep_s3cret_value"},
		}},
	})

	logs, res := sc.collectJob(t, 61)
	require.Equal(t, models.JobStatusSuccess, res.Status)
	var stdout, stderr strings.Builder
	for _, l := range logs {
		assert.NotContains(t, l.Data, "s3cret", "no chunk carries the secret")
		if l.Stream == models.StreamStdout {
			stdout.WriteString(l.Data)
		} else {
			stderr.WriteString(l.Data)
		}
	}
	assert.Equal(t, "+ curl -H 'Authorization: ***' https://deploy\n", stderr.String())
	assert.Equal(t, "token tail: dep_s3", stdout.String(), "a mere prefix is flushed as is when the step ends")
	r.AssertExpectations(t)
}
