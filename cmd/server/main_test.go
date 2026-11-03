package main

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"log/slog"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime/pprof"
	"strings"
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
	"github.com/sidumandr/cisid/internal/github"
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
	out := step.Env["CISID_STEP"] + " in " + step.Image + " at " + step.Env["CISID_COMMIT_SHA"] + "\n"
	if token := step.Env["DEPLOY_TOKEN"]; token != "" {
		out += "token=" + token + "\n" // a careless step printing its secret
	}
	_, err := io.WriteString(stdout, out)
	return 0, err
}

// fakeGitHub serves .cisid.yml for any repository and ref.
// fakeGitHub serves .cisid.yml and records the commit statuses it receives.
func fakeGitHub(t *testing.T) (url string, statuses func() []github.CommitStatus) {
	t.Helper()
	const sha = "6113728f27ae82c7b1a177c8d03f9e96e0adf246"
	var (
		mu       sync.Mutex
		received []github.CommitStatus
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "Bearer e2e-token", r.Header.Get("Authorization"))
		switch r.URL.Path {
		case "/repos/sidumandr/cisid/contents/.cisid.yml":
			assert.Equal(t, sha, r.URL.Query().Get("ref"))
			_, _ = io.WriteString(w, "steps:\n  - name: test\n    image: golang:1.27\n    commands: [go test ./...]\n    secrets: [DEPLOY_TOKEN]\n")
		case "/repos/sidumandr/cisid/statuses/" + sha:
			var st github.CommitStatus
			assert.NoError(t, json.NewDecoder(r.Body).Decode(&st))
			mu.Lock()
			received = append(received, st)
			mu.Unlock()
			w.WriteHeader(http.StatusCreated)
		default:
			t.Errorf("unexpected GitHub request %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv.URL, func() []github.CommitStatus {
		mu.Lock()
		defer mu.Unlock()
		return append([]github.CommitStatus(nil), received...)
	}
}

func (stepRunner) RemoveVolume(context.Context, string) error  { return nil }
func (stepRunner) CreateNetwork(context.Context, string) error { return nil }
func (stepRunner) RemoveNetwork(context.Context, string) error { return nil }
func (stepRunner) StopService(context.Context, string) error   { return nil }
func (stepRunner) StartService(_ context.Context, svc runner.Service) (string, error) {
	return "svc-" + svc.Name, nil
}

// TestRunEndToEnd starts the real server on a fresh database with a real
// agent, delivers a signed webhook, waits for the job to finish and shuts
// everything down. Skipped unless CISID_TEST_DATABASE_URL is set.
func TestRunEndToEnd(t *testing.T) {
	dbURL := dbtest.NewURL(t)
	// Without keep-alives the client never leaves a connected but idle
	// socket behind, which http.Server.Shutdown waits up to 5s for.
	client := &http.Client{Transport: &http.Transport{DisableKeepAlives: true}}
	const secret = "e2e-secret"

	ln := listen(t)
	addr := ln.Addr().String()
	require.NoError(t, ln.Close())

	ghURL, statuses := fakeGitHub(t)
	const apiToken = "e2e-api-token-0123456789"
	env := map[string]string{
		"CISID_HTTP_ADDR":             addr,
		"CISID_DATABASE_URL":          dbURL,
		"CISID_GITHUB_WEBHOOK_SECRET": secret,
		"CISID_SHUTDOWN_TIMEOUT":      "5s",
		"CISID_GITHUB_API_URL":        ghURL,
		"CISID_GITHUB_TOKEN":          "e2e-token",
		"CISID_PUBLIC_URL":            "https://ci.example.com",
		"CISID_API_TOKEN":             apiToken,
		"CISID_SECRET_KEY":            "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=", // 32 bytes
	}
	var logs syncBuffer
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- run(ctx, func(k string) string { return env[k] }, &logs) }()

	base := "http://" + addr
	require.Eventually(t, func() bool {
		resp, err := client.Get(base + "/readyz")
		if err != nil {
			return false
		}
		resp.Body.Close()
		return resp.StatusCode == http.StatusOK
	}, testTimeout, 50*time.Millisecond, "server did not become ready")

	// Store the secret the pipeline asks for.
	putReq, err := http.NewRequestWithContext(t.Context(), http.MethodPut,
		base+"/api/repos/sidumandr/cisid/secrets/DEPLOY_TOKEN", strings.NewReader(`{"value":"dep_e2e_s3cret"}`))
	require.NoError(t, err)
	putReq.Header.Set("Authorization", "Bearer "+apiToken)
	putResp, err := client.Do(putReq)
	require.NoError(t, err)
	putResp.Body.Close()
	require.Equal(t, http.StatusNoContent, putResp.StatusCode)

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
	resp, err := client.Do(req)
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
		"test in golang:1.27 at " + sha + "\ntoken=***\n",
	}, lines, "the clone step runs first, then the steps of .cisid.yml; the secret is masked")

	// GitHub sees the commit go from pending to success, linked to the job.
	require.Eventually(t, func() bool {
		sts := statuses()
		return len(sts) > 0 && sts[len(sts)-1].State == "success"
	}, testTimeout, 20*time.Millisecond, "success status not reported")
	sts := statuses()
	assert.Equal(t, "pending", sts[0].State)
	last := sts[len(sts)-1]
	assert.Equal(t, "cisid", last.Context)
	assert.Equal(t, fmt.Sprintf("https://ci.example.com/jobs/%d", queued.JobID), last.TargetURL)
	assert.True(t, strings.HasPrefix(last.Description, "Passed in "), last.Description)

	// The API serves the job and replays its logs as a finished stream.
	listResp, err := client.Get(base + "/api/jobs")
	require.NoError(t, err)
	var listed []models.Job
	require.NoError(t, json.NewDecoder(listResp.Body).Decode(&listed))
	listResp.Body.Close()
	require.Len(t, listed, 1)
	assert.Equal(t, queued.JobID, listed[0].ID)

	streamResp, err := client.Get(fmt.Sprintf("%s/api/jobs/%d/logs/stream", base, queued.JobID))
	require.NoError(t, err)
	stream, err := io.ReadAll(streamResp.Body)
	streamResp.Body.Close()
	require.NoError(t, err)
	assert.Equal(t, 2, strings.Count(string(stream), "event: log\n"))
	assert.Contains(t, string(stream), `"status":"success"`)

	// Stop the server first: it asks the agent to close, then exits.
	cancel()
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(2 * testTimeout): // CISID_SHUTDOWN_TIMEOUT plus slack
		var stacks strings.Builder
		_ = pprof.Lookup("goroutine").WriteTo(&stacks, 1)
		t.Fatalf("server did not shut down; log:\n%s\ngoroutines:\n%s", logs.String(), stacks.String())
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
	assert.NotContains(t, out, "dep_e2e_s3cret")
	assert.NotContains(t, out, apiToken)
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

// selfSignedCert writes a certificate for 127.0.0.1 and its key to dir and
// returns their paths and a pool trusting the certificate.
func selfSignedCert(t *testing.T, dir string) (certFile, keyFile string, roots *x509.CertPool) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "cisid test"},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	require.NoError(t, err)
	keyDER, err := x509.MarshalECPrivateKey(key)
	require.NoError(t, err)

	certFile, keyFile = filepath.Join(dir, "cert.pem"), filepath.Join(dir, "key.pem")
	require.NoError(t, os.WriteFile(certFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600))
	require.NoError(t, os.WriteFile(keyFile, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), 0o600))

	cert, err := x509.ParseCertificate(der)
	require.NoError(t, err)
	roots = x509.NewCertPool()
	roots.AddCert(cert)
	return certFile, keyFile, roots
}

// TestRunServesTLSToAuthenticatedAgents runs the server over HTTPS with an
// agent token; an agent with the token and the CA connects over wss:// and
// runs a job, one without the token is turned away.
func TestRunServesTLSToAuthenticatedAgents(t *testing.T) {
	dbURL := dbtest.NewURL(t)
	certFile, keyFile, roots := selfSignedCert(t, t.TempDir())
	const agentToken = "tls-agent-token-0123456789"
	gh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "steps:\n  - {name: test, image: alpine:3.22, commands: [true]}\n")
	}))
	defer gh.Close()
	ghURL := gh.URL

	ln := listen(t)
	addr := ln.Addr().String()
	require.NoError(t, ln.Close())
	env := map[string]string{
		"CISID_HTTP_ADDR":             addr,
		"CISID_DATABASE_URL":          dbURL,
		"CISID_GITHUB_WEBHOOK_SECRET": "tls-secret",
		"CISID_SHUTDOWN_TIMEOUT":      "5s",
		"CISID_AGENT_TOKEN":           agentToken,
		"CISID_TLS_CERT_FILE":         certFile,
		"CISID_TLS_KEY_FILE":          keyFile,
		"CISID_GITHUB_API_URL":        ghURL,
	}
	var logs syncBuffer
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- run(ctx, func(k string) string { return env[k] }, &logs) }()

	tlsConfig := &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}
	client := &http.Client{Transport: &http.Transport{TLSClientConfig: tlsConfig, DisableKeepAlives: true}}
	base := "https://" + addr
	require.Eventually(t, func() bool {
		resp, err := client.Get(base + "/readyz")
		if err != nil {
			return false
		}
		resp.Body.Close()
		return resp.StatusCode == http.StatusOK
	}, testTimeout, 50*time.Millisecond, "server did not become ready over HTTPS")

	startAgent := func(token string) (*agent.Agent, func()) {
		a, err := agent.New(agent.Config{
			ServerURL: "wss://" + addr + "/agent/ws", Name: "tls-agent", Token: token, TLS: tlsConfig,
			MinBackoff: 10 * time.Millisecond, MaxBackoff: 50 * time.Millisecond, FlushTimeout: 2 * time.Second,
		}, stepRunner{}, discard)
		require.NoError(t, err)
		actx, stop := context.WithCancel(t.Context())
		stopped := make(chan struct{})
		go func() { defer close(stopped); _ = a.Run(actx) }()
		return a, func() { stop(); <-stopped }
	}

	_, stopIntruder := startAgent("wrong-token-0123456789")
	require.Eventually(t, func() bool { return strings.Contains(logs.String(), "rejected agent with missing or wrong token") },
		testTimeout, 20*time.Millisecond)
	stopIntruder()

	pool, err := pgxpool.New(t.Context(), dbURL)
	require.NoError(t, err)
	defer pool.Close()
	store := db.New(pool)
	id, err := store.CreateJob(t.Context(), models.Job{
		Repo: "sidumandr/cisid", CloneURL: "https://github.com/sidumandr/cisid.git", Branch: "main",
		CommitSHA: "9999999999999999999999999999999999999999", Status: models.JobStatusPending,
	})
	require.NoError(t, err)

	// The agent registering wakes the dispatcher, which finds the job.
	_, stopAgent := startAgent(agentToken)
	defer stopAgent()

	var job models.Job
	require.Eventually(t, func() bool {
		job, err = store.GetJob(t.Context(), id)
		return err == nil && job.Status.Finished()
	}, 3*testTimeout, 50*time.Millisecond, "job did not finish")
	assert.Equal(t, models.JobStatusSuccess, job.Status)
	assert.Equal(t, "tls-agent", job.Agent)

	cancel()
	select {
	case err := <-done:
		require.NoError(t, err, logs.String())
	case <-time.After(2 * testTimeout):
		t.Fatalf("server did not shut down; log:\n%s", logs.String())
	}
	assert.NotContains(t, logs.String(), agentToken)
}
