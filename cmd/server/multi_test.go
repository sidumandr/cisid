package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sidumandr/cisid/internal/agent"
	"github.com/sidumandr/cisid/internal/db/dbtest"
	"github.com/sidumandr/cisid/internal/runner"
)

// gatedRunner runs the clone step at once and holds the "test" step after
// its first line until released or canceled.
type gatedRunner struct {
	stepRunner
	started chan struct{}
	release chan struct{}
}

func (g gatedRunner) Run(ctx context.Context, step runner.Step, stdout, stderr io.Writer) (int, error) {
	if step.Env["CISID_STEP"] != "test" {
		return g.stepRunner.Run(ctx, step, stdout, stderr)
	}
	_, _ = io.WriteString(stdout, "testing\n")
	g.started <- struct{}{}
	select {
	case <-g.release:
		return 0, nil
	case <-ctx.Done():
		return -1, ctx.Err()
	}
}

// TestRunTwoServers runs two servers on one database with the only agent
// on the second. A push delivered to the first is run by the second's
// agent, the first streams its logs live and cancels it.
func TestRunTwoServers(t *testing.T) {
	dbURL := dbtest.NewURL(t)
	client := &http.Client{Transport: &http.Transport{DisableKeepAlives: true}}
	ghURL, statuses := fakeGitHub(t)
	const (
		secret     = "two-secret"
		agentToken = "two-agent-token-0123456789"
		apiToken   = "two-api-token-0123456789"
	)

	type server struct {
		base string
		logs *syncBuffer
		done chan error
	}
	srvCtx, stopServers := context.WithCancel(t.Context())
	defer stopServers()
	start := func() server {
		ln := listen(t)
		addr := ln.Addr().String()
		require.NoError(t, ln.Close())
		env := map[string]string{
			"CISID_HTTP_ADDR":             addr,
			"CISID_DATABASE_URL":          dbURL,
			"CISID_GITHUB_WEBHOOK_SECRET": secret,
			"CISID_SHUTDOWN_TIMEOUT":      "5s",
			"CISID_GITHUB_API_URL":        ghURL,
			"CISID_GITHUB_TOKEN":          "e2e-token",
			"CISID_AGENT_TOKEN":           agentToken,
			"CISID_API_TOKEN":             apiToken,
			"CISID_SECRET_KEY":            "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=",
			"CISID_READ_ACCESS":           "public", // the test reads without signing in
		}
		s := server{base: "http://" + addr, logs: &syncBuffer{}, done: make(chan error, 1)}
		go func() { s.done <- run(srvCtx, func(k string) string { return env[k] }, s.logs) }()
		require.Eventually(t, func() bool {
			resp, err := client.Get(s.base + "/readyz")
			if err != nil {
				return false
			}
			resp.Body.Close()
			return resp.StatusCode == http.StatusOK
		}, 3*testTimeout, 50*time.Millisecond, "server did not become ready")
		return s
	}
	first, second := start(), start()

	// The pipeline asks for a secret; one server stores it for both.
	put, err := http.NewRequestWithContext(t.Context(), http.MethodPut,
		first.base+"/api/repos/sidumandr/cisid/secrets/DEPLOY_TOKEN", strings.NewReader(`{"value":"dep_two"}`))
	require.NoError(t, err)
	put.Header.Set("Authorization", "Bearer "+apiToken)
	putResp, err := client.Do(put)
	require.NoError(t, err)
	putResp.Body.Close()
	require.Equal(t, http.StatusNoContent, putResp.StatusCode)

	steps := gatedRunner{started: make(chan struct{}, 1), release: make(chan struct{})}
	a, err := agent.New(agent.Config{
		ServerURL: "ws" + strings.TrimPrefix(second.base, "http") + "/agent/ws", Name: "two-agent", Token: agentToken,
		MinBackoff: 10 * time.Millisecond, MaxBackoff: 50 * time.Millisecond, FlushTimeout: 2 * time.Second,
	}, steps, discard)
	require.NoError(t, err)
	agentCtx, stopAgent := context.WithCancel(t.Context())
	agentDone := make(chan error, 1)
	go func() { agentDone <- a.Run(agentCtx) }()

	push := func(salt string) int64 {
		body, err := os.ReadFile(filepath.Join("..", "..", "internal", "api", "testdata", "push.json"))
		require.NoError(t, err)
		// A different body per push, or the second would be a duplicate.
		end := bytes.LastIndexByte(body, '}')
		body = append(body[:end:end], fmt.Sprintf(`, "salt": %q}`, salt)...)
		mac := hmac.New(sha256.New, []byte(secret))
		mac.Write(body)
		req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, first.base+"/webhooks/github", bytes.NewReader(body))
		require.NoError(t, err)
		req.Header.Set("X-GitHub-Event", "push")
		req.Header.Set("X-Hub-Signature-256", "sha256="+hex.EncodeToString(mac.Sum(nil)))
		resp, err := client.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()
		require.Equal(t, http.StatusAccepted, resp.StatusCode)
		var queued struct {
			JobID int64 `json:"job_id"`
		}
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&queued))
		return queued.JobID
	}
	waitStarted := func() {
		select {
		case <-steps.started:
		case <-time.After(3 * testTimeout):
			t.Fatalf("the second server's agent did not run the job; logs:\n%s\n%s", first.logs, second.logs)
		}
	}
	jobStatus := func(id int64) string {
		resp, err := client.Get(fmt.Sprintf("%s/api/jobs/%d", first.base, id))
		require.NoError(t, err)
		defer resp.Body.Close()
		var job struct {
			Status string `json:"status"`
			Agent  string `json:"agent"`
		}
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&job))
		return job.Status
	}

	// A push to the first server runs on the second's agent; the first
	// streams its logs while it runs.
	id := push("one")
	waitStarted()
	stream, err := client.Get(fmt.Sprintf("%s/api/jobs/%d/logs/stream", first.base, id))
	require.NoError(t, err)
	events := make(chan string, 100)
	go func() {
		defer close(events)
		defer stream.Body.Close()
		sc := bufio.NewScanner(stream.Body)
		for sc.Scan() {
			events <- sc.Text()
		}
	}()
	waitFor := func(substr string) {
		t.Helper()
		deadline := time.After(3 * testTimeout)
		for {
			select {
			case line, ok := <-events:
				require.True(t, ok, "stream ended before %q", substr)
				if strings.Contains(line, substr) {
					return
				}
			case <-deadline:
				t.Fatalf("no %q on the first server's stream", substr)
			}
		}
	}
	waitFor(`testing\n`)
	steps.release <- struct{}{} // the job finishes on the second server
	waitFor(`"status":"success"`)
	assert.Equal(t, "success", jobStatus(id))

	// A cancel sent to the first server stops the job on the second's agent.
	id = push("two")
	waitStarted()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, fmt.Sprintf("%s/api/jobs/%d/cancel", first.base, id), nil)
	require.NoError(t, err)
	req.Header.Set("Authorization", "Bearer "+apiToken)
	resp, err := client.Do(req)
	require.NoError(t, err)
	resp.Body.Close()
	require.Equal(t, http.StatusAccepted, resp.StatusCode)
	assert.Eventually(t, func() bool { return jobStatus(id) == "canceled" }, 3*testTimeout, 50*time.Millisecond,
		"the cancel reaches the agent through the other server")

	// Both servers report commit statuses; GitHub ends up with the last state.
	require.Eventually(t, func() bool {
		sts := statuses()
		return len(sts) > 0 && sts[len(sts)-1].State == "error"
	}, 3*testTimeout, 20*time.Millisecond)

	stopAgent()
	<-agentDone
	stopServers()
	for _, s := range []server{first, second} {
		select {
		case err := <-s.done:
			require.NoError(t, err)
		case <-time.After(2 * testTimeout):
			t.Fatalf("server did not shut down; log:\n%s", s.logs)
		}
	}
}
