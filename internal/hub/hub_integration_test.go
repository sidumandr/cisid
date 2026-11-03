package hub

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sidumandr/cisid/internal/agent"
	"github.com/sidumandr/cisid/internal/db"
	"github.com/sidumandr/cisid/internal/db/dbtest"
	"github.com/sidumandr/cisid/internal/pipeline"
	"github.com/sidumandr/cisid/internal/runner"
	"github.com/sidumandr/cisid/pkg/models"
)

// fakeRunner stands in for Docker. Each Run writes the step's commit SHA,
// then lines more numbered lines, and exits with the next queued exit code;
// if gate is set, Run first waits on it.
type fakeRunner struct {
	mu      sync.Mutex
	codes   []int
	steps   []runner.Step
	gate    chan struct{}
	started chan struct{}
	lines   int
}

func (r *fakeRunner) Run(ctx context.Context, step runner.Step, stdout, stderr io.Writer) (int, error) {
	r.mu.Lock()
	r.steps = append(r.steps, step)
	code := 0
	if len(r.codes) > 0 {
		code, r.codes = r.codes[0], r.codes[1:]
	}
	gate, started, lines := r.gate, r.started, r.lines
	r.mu.Unlock()

	_, _ = io.WriteString(stdout, "checked out "+step.Env["CISID_COMMIT_SHA"]+"\n")
	for i := range lines {
		_, _ = fmt.Fprintf(stdout, "line %03d\n", i+1)
		time.Sleep(2 * time.Millisecond)
	}
	if started != nil {
		close(started)
	}
	if gate != nil {
		select {
		case <-gate:
		case <-ctx.Done():
			return -1, ctx.Err()
		}
		_, _ = io.WriteString(stderr, "done after restart\n")
	}
	return code, nil
}

func (r *fakeRunner) RemoveVolume(context.Context, string) error  { return nil }
func (r *fakeRunner) CreateNetwork(context.Context, string) error { return nil }
func (r *fakeRunner) RemoveNetwork(context.Context, string) error { return nil }
func (r *fakeRunner) StopService(context.Context, string) error   { return nil }
func (r *fakeRunner) StartService(_ context.Context, svc runner.Service) (string, error) {
	return "svc-" + svc.Name, nil
}

// system is a database, a restartable hub behind one URL and an agent.
type system struct {
	planner pipeline.Planner
	store   *db.Store
	runner  *fakeRunner
	proxy   *tcpProxy // between the agent and the server
	hub     atomic.Pointer[Hub]
	stop    context.CancelFunc
	done    chan struct{}
}

func newSystem(t *testing.T, r *fakeRunner, planner ...pipeline.Planner) *system {
	t.Helper()
	pool := dbtest.New(t)
	_, err := db.Migrate(t.Context(), pool)
	require.NoError(t, err)

	s := &system{store: db.New(pool), runner: r}
	if len(planner) > 0 {
		s.planner = planner[0]
	}
	s.startHub(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		s.hub.Load().ServeHTTP(w, req)
	}))
	t.Cleanup(srv.Close)
	s.proxy = newTCPProxy(t, strings.TrimPrefix(srv.URL, "http://"))

	a, err := agent.New(agent.Config{
		ServerURL:    "ws://" + s.proxy.addr(),
		Name:         "it-agent",
		MinBackoff:   10 * time.Millisecond,
		MaxBackoff:   50 * time.Millisecond,
		FlushTimeout: 2 * time.Second,
	}, r, slog.New(slog.DiscardHandler))
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(t.Context())
	agentDone := make(chan struct{})
	go func() {
		defer close(agentDone)
		assert.NoError(t, a.Run(ctx))
	}()
	t.Cleanup(func() {
		cancel()
		<-agentDone
		s.stopHub(t)
	})
	return s
}

func (s *system) startHub(t *testing.T) {
	t.Helper()
	h := New(s.store, s.planner, WithLogger(slog.New(slog.DiscardHandler)), WithPollInterval(time.Hour))
	ctx, cancel := context.WithCancel(t.Context())
	s.stop, s.done = cancel, make(chan struct{})
	done := s.done
	go func() {
		defer close(done)
		assert.NoError(t, h.Run(ctx))
	}()
	s.hub.Store(h)
}

func (s *system) stopHub(t *testing.T) {
	t.Helper()
	s.stop()
	select {
	case <-s.done:
	case <-time.After(testTimeout):
		t.Fatal("hub did not stop")
	}
}

func (s *system) queue(t *testing.T, sha string) int64 {
	t.Helper()
	id, err := s.store.CreateJob(t.Context(), models.Job{
		Repo: "sidumandr/cisid", CloneURL: "https://github.com/sidumandr/cisid.git",
		Branch: "main", CommitSHA: sha, Status: models.JobStatusPending,
	})
	require.NoError(t, err)
	s.hub.Load().Wake()
	return id
}

func (s *system) waitFinished(t *testing.T, id int64) models.Job {
	t.Helper()
	var job models.Job
	require.Eventually(t, func() bool {
		var err error
		job, err = s.store.GetJob(t.Context(), id)
		return err == nil && job.Status.Finished()
	}, testTimeout, 20*time.Millisecond, "job %d did not finish", id)
	return job
}

func TestIntegrationJobsRunEndToEnd(t *testing.T) {
	r := &fakeRunner{codes: []int{0, 3}}
	s := newSystem(t, r)
	const sha1, sha2 = "1111111111111111111111111111111111111111", "2222222222222222222222222222222222222222"

	ok := s.waitFinished(t, s.queue(t, sha1))
	assert.Equal(t, models.JobStatusSuccess, ok.Status)
	assert.Equal(t, "it-agent", ok.Agent)
	assert.Equal(t, []string{"clone"}, ok.StepNames)
	assert.Equal(t, 0, *ok.ExitCode)
	assert.Nil(t, ok.FailedStep)

	logs, err := s.store.JobLogs(t.Context(), ok.ID)
	require.NoError(t, err)
	assert.Equal(t, []models.JobLog{
		{JobID: ok.ID, Step: 0, Stream: models.StreamStdout, Seq: 1, Data: "checked out " + sha1 + "\n"},
	}, logs)

	failed := s.waitFinished(t, s.queue(t, sha2))
	assert.Equal(t, models.JobStatusFailed, failed.Status)
	assert.Equal(t, 3, *failed.ExitCode)
	assert.Equal(t, 0, *failed.FailedStep)

	r.mu.Lock()
	defer r.mu.Unlock()
	require.Len(t, r.steps, 2)
	assert.Equal(t, pipeline.DefaultCloneImage, r.steps[0].Image)
	assert.Equal(t, sha1, r.steps[0].Env["CISID_COMMIT_SHA"])
}

func TestIntegrationJobSurvivesServerRestart(t *testing.T) {
	r := &fakeRunner{gate: make(chan struct{}), started: make(chan struct{})}
	s := newSystem(t, r)
	const sha = "3333333333333333333333333333333333333333"

	id := s.queue(t, sha)
	select {
	case <-r.started:
	case <-time.After(testTimeout):
		t.Fatal("job did not start")
	}

	// Restart the server side while the agent is mid-job.
	s.stopHub(t)
	s.startHub(t)
	close(r.gate)

	job := s.waitFinished(t, id)
	assert.Equal(t, models.JobStatusSuccess, job.Status)
	assert.Equal(t, "it-agent", job.Agent)

	logs, err := s.store.JobLogs(t.Context(), id)
	require.NoError(t, err)
	var data []string
	for _, l := range logs {
		data = append(data, l.Data)
	}
	assert.Equal(t, []string{"checked out " + sha + "\n", "done after restart\n"}, data,
		"logs from before and after the restart are kept")
}

func TestIntegrationJobOfVanishedAgentIsRequeued(t *testing.T) {
	pool := dbtest.New(t)
	_, err := db.Migrate(t.Context(), pool)
	require.NoError(t, err)
	store := db.New(pool)

	h := New(store, pipeline.Planner{}, WithLogger(slog.New(slog.DiscardHandler)),
		WithPollInterval(time.Hour), WithLease(600*time.Millisecond))
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() {
		defer close(done)
		assert.NoError(t, h.Run(ctx))
	}()
	t.Cleanup(func() { cancel(); <-done })
	f := &fixture{url: "ws" + strings.TrimPrefix(srv.URL, "http")}

	dead := f.connect(t, "dead-agent", 0)
	id, err := store.CreateJob(t.Context(), models.Job{
		Repo: "sidumandr/cisid", CloneURL: "https://github.com/sidumandr/cisid.git",
		Branch: "main", CommitSHA: "4444444444444444444444444444444444444444", Status: models.JobStatusPending,
	})
	require.NoError(t, err)
	h.Wake()
	require.Equal(t, id, dead.expectAssignment(t).JobID)

	// The agent disappears without a result and never comes back.
	require.NoError(t, dead.conn.CloseNow())
	live := f.connect(t, "live-agent", 0)

	assert.Equal(t, id, live.expectAssignment(t).JobID, "the job moves on once its lease expires")
	job, err := store.GetJob(t.Context(), id)
	require.NoError(t, err)
	assert.Equal(t, models.JobStatusRunning, job.Status)
	assert.Equal(t, "live-agent", job.Agent)
}

// tcpProxy forwards TCP connections to target. cut drops every connection
// at once, like a network failure, without a WebSocket close handshake.
type tcpProxy struct {
	ln     net.Listener
	target string
	mu     sync.Mutex
	conns  []net.Conn
}

func newTCPProxy(t *testing.T, target string) *tcpProxy {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	p := &tcpProxy{ln: ln, target: target}
	go p.serve()
	t.Cleanup(func() {
		_ = ln.Close()
		p.cut()
	})
	return p
}

func (p *tcpProxy) addr() string { return p.ln.Addr().String() }

func (p *tcpProxy) serve() {
	for {
		down, err := p.ln.Accept()
		if err != nil {
			return
		}
		up, err := net.Dial("tcp", p.target)
		if err != nil {
			_ = down.Close()
			continue
		}
		p.mu.Lock()
		p.conns = append(p.conns, down, up)
		p.mu.Unlock()
		pipe := func(dst, src net.Conn) {
			_, _ = io.Copy(dst, src)
			_ = dst.Close()
			_ = src.Close()
		}
		go pipe(up, down)
		go pipe(down, up)
	}
}

func (p *tcpProxy) cut() {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, c := range p.conns {
		_ = c.Close()
	}
	p.conns = nil
}

func TestIntegrationLogsSurviveNetworkFailures(t *testing.T) {
	const lines = 300
	r := &fakeRunner{lines: lines}
	s := newSystem(t, r)
	const sha = "5555555555555555555555555555555555555555"

	id := s.queue(t, sha)

	// Drop the connection a few times while the job streams its logs.
	cuts := make(chan struct{})
	go func() {
		defer close(cuts)
		for range 3 {
			time.Sleep(120 * time.Millisecond)
			s.proxy.cut()
		}
	}()

	job := s.waitFinished(t, id)
	<-cuts
	assert.Equal(t, models.JobStatusSuccess, job.Status)

	logs, err := s.store.JobLogs(t.Context(), id)
	require.NoError(t, err)
	require.Len(t, logs, lines+1, "no log chunk is lost")
	for i, l := range logs {
		require.Equal(t, int64(i+1), l.Seq, "chunks are contiguous")
	}
	assert.Equal(t, fmt.Sprintf("line %03d\n", lines), logs[lines].Data)
}

func TestIntegrationCancelRunningJob(t *testing.T) {
	r := &fakeRunner{gate: make(chan struct{}), started: make(chan struct{})}
	s := newSystem(t, r)
	id := s.queue(t, "7777777777777777777777777777777777777777")
	<-r.started

	_, err := s.store.CancelJob(t.Context(), id)
	require.NoError(t, err)
	s.hub.Load().Wake()

	job := s.waitFinished(t, id)
	assert.Equal(t, models.JobStatusCanceled, job.Status)
	assert.Equal(t, db.CancelReason, job.Error)
	assert.Equal(t, 0, *job.FailedStep)
}

// configs serves a fixed .cisid.yml.
type configs string

func (c configs) FileContent(context.Context, string, string, string) ([]byte, error) {
	return []byte(c), nil
}

func TestIntegrationMatrixRunsEveryCombination(t *testing.T) {
	r := &fakeRunner{}
	s := newSystem(t, r, pipeline.Planner{Configs: configs(`
matrix:
  GO: ["1.25", "1.26", "1.27"]
steps:
  - name: test
    image: golang:${GO}
    commands: [go test ./...]
`)})

	first := s.queue(t, "8888888888888888888888888888888888888888")
	s.waitFinished(t, first)

	jobs, err := s.store.ListJobs(t.Context(), 10)
	require.NoError(t, err)
	require.Len(t, jobs, 3, "one job per combination")
	versions := map[string]bool{}
	for _, j := range jobs {
		j = s.waitFinished(t, j.ID)
		assert.Equal(t, models.JobStatusSuccess, j.Status)
		versions[j.Matrix["GO"]] = true
		if j.ID != first {
			assert.Equal(t, &first, j.ParentID)
		}
	}
	assert.Equal(t, map[string]bool{"1.25": true, "1.26": true, "1.27": true}, versions)

	r.mu.Lock()
	defer r.mu.Unlock()
	var images []string
	for _, st := range r.steps {
		if st.Env["CISID_STEP"] == "test" {
			images = append(images, st.Image)
		}
	}
	assert.ElementsMatch(t, []string{"golang:1.25", "golang:1.26", "golang:1.27"}, images)
}
