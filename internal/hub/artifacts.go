package hub

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/sidumandr/cisid/internal/db"
	"github.com/sidumandr/cisid/pkg/models"
)

// agentHeader names the agent in its requests.
const agentHeader = "X-Cisid-Agent"

// ArtifactStore keeps uploaded artifacts. *db.Store satisfies it.
type ArtifactStore interface {
	PutArtifact(ctx context.Context, jobID int64, agent, name string, r io.Reader, limits db.ArtifactLimits) (models.Artifact, error)
}

var _ ArtifactStore = (*db.Store)(nil)

// ArtifactUploads returns the handler agents upload artifacts to:
//
//	PUT /agent/jobs/{id}/artifacts/{name...}
//
// It takes the agent tokens the WebSocket endpoint takes, and accepts an
// artifact only for a job running on the agent the X-Cisid-Agent header
// names.
func (h *Hub) ArtifactUploads(store ArtifactStore, limits db.ArtifactLimits) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if len(h.agentTokens) > 0 {
			got, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
			if !ok || !validToken(got, h.agentTokens) {
				h.log.WarnContext(r.Context(), "rejected artifact upload with missing or wrong token", "remote_addr", r.RemoteAddr)
				w.Header().Set("WWW-Authenticate", `Bearer realm="cisid-agents"`)
				http.Error(w, "missing or wrong agent token", http.StatusUnauthorized)
				return
			}
		}
		agent := r.Header.Get(agentHeader)
		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		name := r.PathValue("name")
		switch {
		case agent == "":
			http.Error(w, agentHeader+" is required", http.StatusBadRequest)
			return
		case err != nil || id < 1:
			http.Error(w, "job id must be a positive integer", http.StatusBadRequest)
			return
		case !models.ValidArtifactName(name):
			http.Error(w, "invalid artifact name", http.StatusBadRequest)
			return
		case r.ContentLength > limits.MaxSize:
			http.Error(w, "artifact is over "+strconv.FormatInt(limits.MaxSize, 10)+" bytes (CISID_ARTIFACT_MAX_SIZE)", http.StatusRequestEntityTooLarge)
			return
		}

		a, err := store.PutArtifact(r.Context(), id, agent, name, http.MaxBytesReader(w, r.Body, limits.MaxSize+1), limits)
		var tooBig *http.MaxBytesError
		switch {
		case errors.Is(err, db.ErrArtifactTooLarge), errors.As(err, &tooBig):
			http.Error(w, "artifact is over "+strconv.FormatInt(limits.MaxSize, 10)+" bytes (CISID_ARTIFACT_MAX_SIZE)", http.StatusRequestEntityTooLarge)
		case errors.Is(err, db.ErrTooManyArtifacts):
			http.Error(w, "the job has too many artifacts", http.StatusConflict)
		case errors.Is(err, db.ErrJobConflict):
			http.Error(w, "the job is not running on this agent", http.StatusConflict)
		case err != nil:
			h.log.ErrorContext(r.Context(), "store artifact", "job_id", id, "name", name, "error", err)
			http.Error(w, "could not store the artifact", http.StatusInternalServerError)
		default:
			h.log.InfoContext(r.Context(), "artifact uploaded", "job_id", id, "agent", agent, "name", name, "size", a.Size)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(a)
		}
	})
}
