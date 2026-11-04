package api

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sidumandr/cisid/internal/db"
	"github.com/sidumandr/cisid/pkg/models"
)

// memArtifactReader serves the artifacts of job 5.
type memArtifactReader struct {
	content map[string]string
}

func (m memArtifactReader) Artifacts(_ context.Context, jobID int64) ([]models.Artifact, error) {
	if jobID != 5 {
		return nil, nil
	}
	return []models.Artifact{{JobID: 5, Name: "dist/app linux.bin", Size: 6, SHA256: "abc"}}, nil
}

func (m memArtifactReader) Artifact(_ context.Context, jobID int64, name string) (models.Artifact, error) {
	if c, ok := m.content[name]; ok && jobID == 5 {
		return models.Artifact{ID: 1, JobID: 5, Name: name, Size: int64(len(c)), SHA256: "abc"}, nil
	}
	return models.Artifact{}, db.ErrArtifactNotFound
}

func (m memArtifactReader) WriteArtifact(_ context.Context, a models.Artifact, w io.Writer) error {
	_, err := io.WriteString(w, m.content[a.Name])
	return err
}

func artifactsRouter(access *Access) http.Handler {
	store := memArtifactReader{content: map[string]string{"dist/app linux.bin": "<html>", "report.xml": "<ok/>"}}
	return NewRouter(Handlers{
		Agents:    http.NotFoundHandler(),
		Artifacts: NewArtifactsAPI(store, access, slog.New(slog.DiscardHandler)),
		Access:    access,
		DB:        &mockPinger{},
	}, slog.New(slog.DiscardHandler))
}

func TestArtifactsAPI(t *testing.T) {
	h := artifactsRouter(nil)

	rec := call(h, http.MethodGet, "/api/jobs/5/artifacts", "", "")
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), `"name":"dist/app linux.bin"`)
	assert.JSONEq(t, `[]`, call(h, http.MethodGet, "/api/jobs/6/artifacts", "", "").Body.String())

	rec = call(h, http.MethodGet, "/api/jobs/5/artifacts/dist/app%20linux.bin", "", "")
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "<html>", rec.Body.String())
	assert.Equal(t, "application/octet-stream", rec.Header().Get("Content-Type"), "never rendered, whatever it holds")
	assert.Equal(t, `attachment; filename*=UTF-8''app%20linux.bin`, rec.Header().Get("Content-Disposition"))
	assert.Equal(t, "nosniff", rec.Header().Get("X-Content-Type-Options"))
	assert.Contains(t, rec.Header().Get("Content-Security-Policy"), "sandbox")
	assert.Equal(t, "6", rec.Header().Get("Content-Length"))
	assert.Equal(t, `"abc"`, rec.Header().Get("ETag"))

	req := httptest.NewRequest(http.MethodGet, "/api/jobs/5/artifacts/report.xml", nil)
	req.Header.Set("If-None-Match", `"abc"`)
	cached := httptest.NewRecorder()
	h.ServeHTTP(cached, req)
	assert.Equal(t, http.StatusNotModified, cached.Code)

	assert.Equal(t, http.StatusNotFound, call(h, http.MethodGet, "/api/jobs/5/artifacts/missing", "", "").Code)
	assert.Equal(t, http.StatusBadRequest, call(h, http.MethodGet, "/api/jobs/x/artifacts", "", "").Code)
}

func TestArtifactsAPIFollowsReadAccess(t *testing.T) {
	h := artifactsRouter(tokenAccess(readToken))
	assert.Equal(t, http.StatusUnauthorized, call(h, http.MethodGet, "/api/jobs/5/artifacts", "", "").Code)
	assert.Equal(t, http.StatusUnauthorized, call(h, http.MethodGet, "/api/jobs/5/artifacts/report.xml", "", "").Code)
	assert.Equal(t, http.StatusOK, call(h, http.MethodGet, "/api/jobs/5/artifacts/report.xml", readToken, "").Code)
}
