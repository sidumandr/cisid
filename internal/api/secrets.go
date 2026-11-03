package api

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/sidumandr/cisid/internal/auth"
	"github.com/sidumandr/cisid/internal/forge"
	"github.com/sidumandr/cisid/internal/secrets"
	"github.com/sidumandr/cisid/pkg/models"
)

// SecretVault stores repository secrets. *secrets.Vault satisfies it.
type SecretVault interface {
	Set(ctx context.Context, repo, name, value string, branches []string) error
	Delete(ctx context.Context, repo, name string) error
	List(ctx context.Context, repo string) ([]models.SecretInfo, error)
}

var _ SecretVault = (*secrets.Vault)(nil)

// SecretsAPI manages repository secrets. Every endpoint needs the
// maintainer role; values can be written but never read back:
//
//	GET    /api/repos/{owner}/{repo}/secrets          names and update times
//	PUT    /api/repos/{owner}/{repo}/secrets/{name}   {"value": "..."}
//	DELETE /api/repos/{owner}/{repo}/secrets/{name}
type SecretsAPI struct {
	vault  SecretVault
	access *Access
	log    *slog.Logger
}

// NewSecretsAPI returns a SecretsAPI; access says who is a maintainer. A
// nil vault (no CISID_SECRET_KEY) disables the endpoints; a nil log uses
// slog.Default().
func NewSecretsAPI(vault SecretVault, access *Access, log *slog.Logger) *SecretsAPI {
	if log == nil {
		log = slog.Default()
	}
	if access == nil {
		access = openAccess()
	}
	return &SecretsAPI{vault: vault, access: access, log: log}
}

func (s *SecretsAPI) register(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/repos/{owner}/{repo}/secrets", s.access.require(auth.RoleMaintainer, s.enabled(s.list)))
	mux.HandleFunc("PUT /api/repos/{owner}/{repo}/secrets/{name}", s.access.require(auth.RoleMaintainer, s.enabled(s.put)))
	mux.HandleFunc("DELETE /api/repos/{owner}/{repo}/secrets/{name}", s.access.require(auth.RoleMaintainer, s.enabled(s.delete)))
}

func (s *SecretsAPI) enabled(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.vault == nil {
			writeError(w, http.StatusForbidden, "secrets disabled: CISID_SECRET_KEY is not set")
			return
		}
		next(w, r)
	}
}

func (s *SecretsAPI) list(w http.ResponseWriter, r *http.Request) {
	repo, ok := repoParam(w, r)
	if !ok {
		return
	}
	infos, err := s.vault.List(r.Context(), repo)
	if err != nil {
		s.internalError(w, r, "list secrets", err)
		return
	}
	if infos == nil {
		infos = []models.SecretInfo{}
	}
	writeJSON(w, http.StatusOK, infos)
}

func (s *SecretsAPI) put(w http.ResponseWriter, r *http.Request) {
	repo, ok := repoParam(w, r)
	if !ok {
		return
	}
	var body struct {
		Value    *string  `json:"value"`
		Branches []string `json:"branches"`
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 2*secrets.MaxValueBytes))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&body); err != nil || body.Value == nil {
		writeError(w, http.StatusBadRequest, `body must be {"value": "...", "branches": [...]} with branches optional`)
		return
	}

	name := r.PathValue("name")
	err := s.vault.Set(r.Context(), repo, name, *body.Value, body.Branches)
	switch {
	case errors.Is(err, secrets.ErrInvalidName), errors.Is(err, secrets.ErrInvalidBranch):
		writeError(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, secrets.ErrTooLarge):
		writeError(w, http.StatusRequestEntityTooLarge, err.Error())
	case err != nil:
		s.internalError(w, r, "store secret", err)
	default:
		s.log.InfoContext(r.Context(), "secret stored", "repo", repo, "name", name, "branches", body.Branches)
		w.WriteHeader(http.StatusNoContent)
	}
}

func (s *SecretsAPI) delete(w http.ResponseWriter, r *http.Request) {
	repo, ok := repoParam(w, r)
	if !ok {
		return
	}
	name := r.PathValue("name")
	err := s.vault.Delete(r.Context(), repo, name)
	switch {
	case errors.Is(err, secrets.ErrNotFound):
		writeError(w, http.StatusNotFound, "secret not found")
	case err != nil:
		s.internalError(w, r, "delete secret", err)
	default:
		s.log.InfoContext(r.Context(), "secret deleted", "repo", repo, "name", name)
		w.WriteHeader(http.StatusNoContent)
	}
}

func (s *SecretsAPI) internalError(w http.ResponseWriter, r *http.Request, what string, err error) {
	s.log.ErrorContext(r.Context(), what, "path", r.URL.Path, "error", err)
	writeError(w, http.StatusInternalServerError, "internal server error")
}

// repoParam returns the repository key from the path, writing a 400 if
// invalid. The key's last segment is {repo} and the rest {owner}, with the
// forge and any slashes escaped: /api/repos/gitlab:group%2Fsub/project.
func repoParam(w http.ResponseWriter, r *http.Request) (string, bool) {
	key := r.PathValue("owner") + "/" + r.PathValue("repo")
	if _, _, err := forge.SplitKey(key); err != nil {
		writeError(w, http.StatusBadRequest, "invalid repository")
		return "", false
	}
	return key, true
}
