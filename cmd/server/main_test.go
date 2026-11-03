package main

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sidumandr/cisid/internal/agent"
	"github.com/sidumandr/cisid/internal/config"
	"github.com/sidumandr/cisid/internal/db"
	"github.com/sidumandr/cisid/internal/db/dbtest"
	"github.com/sidumandr/cisid/internal/runner"
	"github.com/sidumandr/cisid/pkg/models"
)

const testTimeout = 5 * time.Second

var discard = slog.New(slog.DiscardHandler)

func listen(t *testing.T) net.Listener {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	return ln
}

func TestServeGracefulShutdown(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		<-release
		_, _ = io.WriteString(w, "finished")
	})}
	ln := listen(t)
	url := "http://" + ln.Addr().String()

	ctx, cancel := context.WithCancel(t.Context())
	served := make(chan error, 1)
	go func() { served <- serve(ctx, srv, ln, testTimeout, discard) }()

	type result struct {
		body string
		err  error
	}
	inflight := make(chan result, 1)
	go func() {
		resp, err := http.Get(url)
		if err != nil {
			inflight <- result{err: err}
			return
		}
		defer resp.Body.Close()
		b, err := io.ReadAll(resp.Body)
		inflight <- result{string(b), err}
	}()
	<-started

	cancel()

	// New connections are refused once shutdown has begun...
	assert.Eventually(t, func() bool {
		conn, err := net.DialTimeout("tcp", ln.Addr().String(), 100*time.Millisecond)
		if err == nil {
			conn.Close()
		}
		return err != nil
	}, testTimeout, 10*time.Millisecond)

	// ...but the in-flight request still completes.
	close(release)
	res := <-inflight
	require.NoError(t, res.err)
	assert.Equal(t, "finished", res.body)

	select {
	case err := <-served:
		require.NoError(t, err)
	case <-time.After(testTimeout):
		t.Fatal("serve did not return")
	}
}

func TestServeShutdownTimeout(t *testing.T) {
	started := make(chan struct{})
	stuck := make(chan struct{})
	defer close(stuck)
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		<-stuck
	})}
	ln := listen(t)

	ctx, cancel := context.WithCancel(t.Context())
	served := make(chan error, 1)
	go func() { served <- serve(ctx, srv, ln, 50*time.Millisecond, discard) }()
	go func() {
		if resp, err := http.Get("http://" + ln.Addr().String()); err == nil {
			resp.Body.Close()
		}
	}()
	<-started

	cancel()

	select {
	case err := <-served:
		require.ErrorIs(t, err, context.DeadlineExceeded)
	case <-time.After(testTimeout):
		t.Fatal("serve did not give up after the shutdown timeout")
	}
}

func TestRunRejectsInvalidConfig(t *testing.T) {
	err := run(t.Context(), func(string) string { return "" }, io.Discard)
	assert.ErrorIs(t, err, config.ErrInvalidConfig)
}

// stepRunner stands in for Docker in the end-to-end test.
type stepRunner struct{}

func (stepRunner) Run(_ context.Context, step runner.Step, stdout, _ io.Writer) (int, error) {
	_, err := io.WriteString(stdout, step.Env["CISID_STEP"]+" in "+step.Image+" at "+step.Env["CISID_COMMIT_SHA"]+"\n")
	return 0, err
}

// fakeGitHub serves .cisid.yml for any repository and ref.
func fakeGitHub(t *testing.T) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/repos/sidumandr/cisid/contents/.cisid.yml", r.URL.Path)
		assert.Equal(t, "6113728f27ae82c7b1a177c8d03f9e96e0adf246", r.URL.Query().Get("ref"))
		_, _ = io.WriteString(w, "steps:\n  - name: test\n    image: golang:1.27\n    commands: [go test ./...]\n")
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

func (stepRunner) RemoveVolume(context.Context, string) error { return nil }

// TestRunEndToEnd starts the real server on a fresh database with a real
// agent, delivers a signed webhook, waits for the job to finish and shuts
// everything down. Skipped unless CISID_TEST_DATABASE_URL is set.
func TestRunEndToEnd(t *testing.T) {
	dbURL := dbtest.NewURL(t)
	const secret = "e2e-secret"

	ln := listen(t)
	addr := ln.Addr().String()
	require.NoError(t, ln.Close())

	env := map[string]string{
		"CISID_HTTP_ADDR":             addr,
		"CISID_DATABASE_URL":          dbURL,
		"CISID_GITHUB_WEBHOOK_SECRET": secret,
		"CISID_SHUTDOWN_TIMEOUT":      "5s",
		"CISID_GITHUB_API_URL":        fakeGitHub(t),
	}
	var logs syncBuffer
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- run(ctx, func(k string) string { return env[k] }, &logs) }()

	base := "http://" + addr
	require.Eventually(t, func() bool {
		resp, err := http.Get(base + "/readyz")
		if err != nil {
			return false
		}
		resp.Body.Close()
		return resp.StatusCode == http.StatusOK
	}, testTimeout, 50*time.Millisecond, "server did not become ready")

	a, err := agent.New(agent.Config{
		ServerURL:    "ws://" + addr + "/agent/ws",
		Name:         "e2e-agent",
		MinBackoff:   10 * time.Millisecond,
		MaxBackoff:   50 * time.Millisecond,
		FlushTimeout: 2 * time.Second,
	}, stepRunner{}, discard)
	require.NoError(t, err)
	agentCtx, stopAgent := context.WithCancel(t.Context())
	agentDone := make(chan error, 1)
	go func() { agentDone <- a.Run(agentCtx) }()

	body, err := os.ReadFile(filepath.Join("..", "..", "internal", "api", "testdata", "push.json"))
	require.NoError(t, err)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)

	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, base+"/webhooks/github", bytes.NewReader(body))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-GitHub-Event", "push")
	req.Header.Set("X-Hub-Signature-256", "sha256="+hex.EncodeToString(mac.Sum(nil)))
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusAccepted, resp.StatusCode)

	var queued struct {
		JobID int64 `json:"job_id"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&queued))

	pool, err := pgxpool.New(t.Context(), dbURL)
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	store := db.New(pool)

	var job models.Job
	require.Eventually(t, func() bool {
		job, err = store.GetJob(t.Context(), queued.JobID)
		return err == nil && job.Status.Finished()
	}, testTimeout, 20*time.Millisecond, "job did not finish")
	assert.Equal(t, models.JobStatusSuccess, job.Status)
	assert.Equal(t, "e2e-agent", job.Agent)
	assert.Equal(t, "main", job.Branch)

	jobLogs, err := store.JobLogs(t.Context(), queued.JobID)
	require.NoError(t, err)
	const sha = "6113728f27ae82c7b1a177c8d03f9e96e0adf246"
	var lines []string
	for _, l := range jobLogs {
		lines = append(lines, l.Data)
	}
	assert.Equal(t, []string{
		"clone in alpine/git:2.52.0 at " + sha + "\n",
		"test in golang:1.27 at " + sha + "\n",
	}, lines, "the clone step runs first, then the steps of .cisid.yml")

	// Stop the server first: it asks the agent to close, then exits.
	cancel()
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(testTimeout):
		t.Fatal("server did not shut down")
	}
	stopAgent()
	select {
	case err := <-agentDone:
		require.NoError(t, err)
	case <-time.After(testTimeout):
		t.Fatal("agent did not stop")
	}

	out := logs.String()
	assert.NotContains(t, out, secret, "secrets must not be logged")
	assert.Contains(t, out, "job assigned")
	assert.Contains(t, out, "job finished")
	assert.Contains(t, out, "server stopped")
}

// syncBuffer is a bytes.Buffer safe for concurrent writes from the logger.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}
