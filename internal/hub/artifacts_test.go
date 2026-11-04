package hub

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sidumandr/cisid/internal/db"
	"github.com/sidumandr/cisid/pkg/models"
)

// memArtifacts stores uploads in memory; job 7 runs on agent-1.
type memArtifacts struct {
	mu   sync.Mutex
	got  map[string]string
	fail error
}

func (m *memArtifacts) PutArtifact(_ context.Context, jobID int64, agent, name string, r io.Reader, limits db.ArtifactLimits) (models.Artifact, error) {
	if m.fail != nil {
		return models.Artifact{}, m.fail
	}
	if jobID != 7 || agent != "agent-1" {
		return models.Artifact{}, fmt.Errorf("%w: not running here", db.ErrJobConflict)
	}
	data, err := io.ReadAll(r)
	if err != nil {
		return models.Artifact{}, err
	}
	if int64(len(data)) > limits.MaxSize {
		return models.Artifact{}, db.ErrArtifactTooLarge
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.got == nil {
		m.got = map[string]string{}
	}
	m.got[name] = string(data)
	return models.Artifact{JobID: jobID, Name: name, Size: int64(len(data))}, nil
}

func uploadHandler(store ArtifactStore) http.Handler {
	h := New(nil, nil, WithLogger(slog.New(slog.DiscardHandler)), WithAgentTokens("agent-token-0123456789"))
	mux := http.NewServeMux()
	mux.Handle("PUT /agent/jobs/{id}/artifacts/{name...}", h.ArtifactUploads(store, db.ArtifactLimits{MaxSize: 16, MaxCount: 10}))
	return mux
}

func put(h http.Handler, target, agent, token, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPut, target, strings.NewReader(body))
	if agent != "" {
		req.Header.Set(agentHeader, agent)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestArtifactUploads(t *testing.T) {
	store := &memArtifacts{}
	h := uploadHandler(store)
	const token = "agent-token-0123456789"

	rec := put(h, "/agent/jobs/7/artifacts/dist/app%20linux.bin", "agent-1", token, "binary")
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	assert.Equal(t, "binary", store.got["dist/app linux.bin"])
	assert.Contains(t, rec.Body.String(), `"size":6`)

	for _, c := range []struct {
		target, agent, token string
		body                 string
		want                 int
	}{
		{"/agent/jobs/7/artifacts/a.txt", "agent-1", "", "x", http.StatusUnauthorized},
		{"/agent/jobs/7/artifacts/a.txt", "agent-1", "wrong-token-0123456789", "x", http.StatusUnauthorized},
		{"/agent/jobs/7/artifacts/a.txt", "", token, "x", http.StatusBadRequest},
		{"/agent/jobs/x/artifacts/a.txt", "agent-1", token, "x", http.StatusBadRequest},
		{"/agent/jobs/7/artifacts/a%2F..%2F..%2Fetc", "agent-1", token, "x", http.StatusBadRequest},
		{"/agent/jobs/7/artifacts/big.bin", "agent-1", token, strings.Repeat("x", 17), http.StatusRequestEntityTooLarge},
		{"/agent/jobs/8/artifacts/a.txt", "agent-1", token, "x", http.StatusConflict},
		{"/agent/jobs/7/artifacts/a.txt", "agent-2", token, "x", http.StatusConflict},
	} {
		rec := put(h, c.target, c.agent, c.token, c.body)
		assert.Equal(t, c.want, rec.Code, "%s by %q: %s", c.target, c.agent, rec.Body.String())
	}

	store.fail = db.ErrTooManyArtifacts
	assert.Equal(t, http.StatusConflict, put(h, "/agent/jobs/7/artifacts/b.txt", "agent-1", token, "x").Code)
}
