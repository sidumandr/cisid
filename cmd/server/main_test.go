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
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sidumandr/cisid/internal/config"
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

// TestRunEndToEnd starts the real server against PostgreSQL, delivers a
// signed webhook and shuts down. Skipped unless CISID_TEST_DATABASE_URL is set.
func TestRunEndToEnd(t *testing.T) {
	dbURL := os.Getenv("CISID_TEST_DATABASE_URL")
	if dbURL == "" {
		t.Skip("CISID_TEST_DATABASE_URL not set; skipping end-to-end test")
	}
	const secret = "e2e-secret"

	ln := listen(t)
	addr := ln.Addr().String()
	require.NoError(t, ln.Close())

	env := map[string]string{
		"CISID_HTTP_ADDR":             addr,
		"CISID_DATABASE_URL":          dbURL,
		"CISID_GITHUB_WEBHOOK_SECRET": secret,
		"CISID_SHUTDOWN_TIMEOUT":      "5s",
	}
	var logs bytes.Buffer
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

	conn, err := pgx.Connect(t.Context(), dbURL)
	require.NoError(t, err)
	// Cleanups run last-in first-out: delete the row, then close the connection.
	t.Cleanup(func() { _ = conn.Close(context.Background()) })
	t.Cleanup(func() {
		_, err := conn.Exec(context.Background(), "DELETE FROM jobs WHERE id = $1", queued.JobID)
		assert.NoError(t, err)
	})
	var branch, status string
	require.NoError(t, conn.QueryRow(t.Context(),
		"SELECT branch, status FROM jobs WHERE id = $1", queued.JobID).Scan(&branch, &status))
	assert.Equal(t, "main", branch)
	assert.Equal(t, "pending", status)

	cancel()
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(testTimeout):
		t.Fatal("server did not shut down")
	}
	assert.NotContains(t, logs.String(), secret, "secrets must not be logged")
	assert.Contains(t, logs.String(), "server stopped")
}
