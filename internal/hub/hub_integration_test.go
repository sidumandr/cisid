package hub

import (
	"context"
	"io"
	"log/slog"
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

// fakeRunner stands in for Docker. Each Run writes the step's commit SHA and
// exits with the next queued exit code; if gate is set, Run first waits on it.
type fakeRunner struct {
	mu      sync.Mutex
	codes   []int
	steps   []runner.Step
	gate    chan struct{}
	started chan struct{}
}

func (r *fakeRunner) Run(ctx context.Context, step runner.Step, stdout, stderr io.Writer) (int, error) {
	r.mu.Lock()
	r.steps = append(r.steps, step)
	code := 0
	if len(r.codes) > 0 {
		code, r.codes = r.codes[0], r.codes[1:]
	}
	gate, started := r.gate, r.started
	r.mu.Unlock()

	_, _ = io.WriteString(stdout, "checked out "+step.Env["CISID_COMMIT_SHA"]+"\n")
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

func (r *fakeRunner) RemoveVolume(context.Context, string) error { return nil }

// system is a database, a restartable hub behind one URL and an agent.
type system struct {
	store  *db.Store
	runner *fakeRunner
	hub    atomic.Pointer[Hub]
	stop   context.CancelFunc
	done   chan struct{}
}

func newSystem(t *testing.T, r *fakeRunner) *system {
	t.Helper()
	pool := dbtest.New(t)
	_, err := db.Migrate(t.Context(), pool)
	require.NoError(t, err)

	s := &system{store: db.New(pool), runner: r}
	s.startHub(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		s.hub.Load().ServeHTTP(w, req)
	}))
	t.Cleanup(srv.Close)

	a, err := agent.New(agent.Config{
		ServerURL:    "ws" + strings.TrimPrefix(srv.URL, "http"),
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
	h := New(s.store, pipeline.Planner{}, WithLogger(slog.New(slog.DiscardHandler)), WithPollInterval(time.Hour))
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
