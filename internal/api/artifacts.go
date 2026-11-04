package api

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"path"
	"strconv"

	"github.com/sidumandr/cisid/internal/db"
	"github.com/sidumandr/cisid/pkg/models"
)

// ArtifactReader reads the artifacts of jobs. *db.Store satisfies it.
type ArtifactReader interface {
	Artifacts(ctx context.Context, jobID int64) ([]models.Artifact, error)
	Artifact(ctx context.Context, jobID int64, name string) (models.Artifact, error)
	WriteArtifact(ctx context.Context, a models.Artifact, w io.Writer) error
}

var _ ArtifactReader = (*db.Store)(nil)

// ArtifactsAPI serves the files jobs' builds kept, to whoever may read
// jobs:
//
//	GET /api/jobs/{id}/artifacts          the job's artifacts
//	GET /api/jobs/{id}/artifacts/{name}   one artifact's content, as a download
type ArtifactsAPI struct {
	store  ArtifactReader
	access *Access
	log    *slog.Logger
}

// NewArtifactsAPI returns an ArtifactsAPI reading from store; access says
// who reads.
func NewArtifactsAPI(store ArtifactReader, access *Access, log *slog.Logger) *ArtifactsAPI {
	if log == nil {
		log = slog.Default()
	}
	if access == nil {
		access = openAccess()
	}
	return &ArtifactsAPI{store: store, access: access, log: log}
}

func (a *ArtifactsAPI) register(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/jobs/{id}/artifacts", a.access.wrapFunc(a.list))
	mux.HandleFunc("GET /api/jobs/{id}/artifacts/{name...}", a.access.wrapFunc(a.download))
}

func (a *ArtifactsAPI) list(w http.ResponseWriter, r *http.Request) {
	id, ok := jobID(w, r)
	if !ok {
		return
	}
	list, err := a.store.Artifacts(r.Context(), id)
	if err != nil {
		a.log.ErrorContext(r.Context(), "list artifacts", "job_id", id, "error", err)
		writeError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	if list == nil {
		list = []models.Artifact{}
	}
	writeJSON(w, http.StatusOK, list)
}

// download sends the artifact as an attachment that browsers save and
// never render, whatever is in it.
func (a *ArtifactsAPI) download(w http.ResponseWriter, r *http.Request) {
	id, ok := jobID(w, r)
	if !ok {
		return
	}
	art, err := a.store.Artifact(r.Context(), id, r.PathValue("name"))
	if errors.Is(err, db.ErrArtifactNotFound) {
		writeError(w, http.StatusNotFound, "artifact not found")
		return
	}
	if err != nil {
		a.log.ErrorContext(r.Context(), "read artifact", "job_id", id, "error", err)
		writeError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	h := w.Header()
	h.Set("Content-Type", "application/octet-stream")
	h.Set("Content-Length", strconv.FormatInt(art.Size, 10))
	h.Set("Content-Disposition", "attachment; filename*=UTF-8''"+url.PathEscape(path.Base(art.Name)))
	h.Set("Content-Security-Policy", "default-src 'none'; sandbox")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("ETag", `"`+art.SHA256+`"`)
	h.Set("Cache-Control", "private, max-age=0")
	if r.Header.Get("If-None-Match") == `"`+art.SHA256+`"` {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	if err := a.store.WriteArtifact(r.Context(), art, w); err != nil {
		// The status is sent; a short body tells the client.
		a.log.WarnContext(r.Context(), "send artifact", "job_id", id, "name", art.Name, "error", err)
	}
}
