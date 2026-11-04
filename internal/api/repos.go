package api

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/sidumandr/cisid/internal/auth"
	"github.com/sidumandr/cisid/internal/db"
	"github.com/sidumandr/cisid/internal/forge"
	"github.com/sidumandr/cisid/internal/forgeauth"
	"github.com/sidumandr/cisid/internal/pipeline"
	"github.com/sidumandr/cisid/pkg/models"
)

// RepoStore keeps the repositories connected to cisid. *db.Store
// satisfies it.
type RepoStore interface {
	PutRepository(ctx context.Context, r models.Repository) (bool, error)
	DeleteRepository(ctx context.Context, repo string) (bool, error)
	Repositories(ctx context.Context) ([]models.Repository, error)
	PutWatch(ctx context.Context, w models.Watch) (bool, error)
}

var _ RepoStore = (*db.Store)(nil)

// RepoForges looks repositories up on their forges and lists the ones the
// forges' tokens reach. *forge.Set satisfies it.
type RepoForges interface {
	Repository(ctx context.Context, key string) (forge.Repository, error)
	BranchHead(ctx context.Context, key, branch string) (string, error)
	FileContent(ctx context.Context, key, ref, file string) ([]byte, error)
	Repositories(ctx context.Context) forge.Listing
}

var _ RepoForges = (*forge.Set)(nil)

// ReposAPI connects repositories to cisid, so they build before any
// webhook arrives for them:
//
//	GET    /api/repos                    the connected repositories
//	POST   /api/repos                    connect one, and poll its default branch (maintainer)
//	DELETE /api/repos/{owner}/{repo}     disconnect one: no more polling or schedules (maintainer)
//	GET    /api/forges/repositories      the repositories the forges' tokens reach, to pick from (maintainer)
type ReposAPI struct {
	store   RepoStore
	forges  RepoForges
	access  *Access
	log     *slog.Logger
	polling bool
}

// NewReposAPI returns a ReposAPI; polling says whether the server polls
// branches, which connecting one asks for.
func NewReposAPI(store RepoStore, forges RepoForges, access *Access, log *slog.Logger, polling bool) *ReposAPI {
	if log == nil {
		log = slog.Default()
	}
	if access == nil {
		access = openAccess()
	}
	return &ReposAPI{store: store, forges: forges, access: access, log: log, polling: polling}
}

func (a *ReposAPI) register(mux *http.ServeMux) {
	maintainer := func(h http.HandlerFunc) http.HandlerFunc { return a.access.require(auth.RoleMaintainer, h) }
	mux.HandleFunc("GET /api/repos", a.access.wrapFunc(a.list))
	mux.HandleFunc("POST /api/repos", maintainer(a.connect))
	mux.HandleFunc("DELETE /api/repos/{owner}/{repo}", maintainer(a.disconnect))
	mux.HandleFunc("GET /api/forges/repositories", maintainer(a.reachable))
}

func (a *ReposAPI) list(w http.ResponseWriter, r *http.Request) {
	repos, err := a.store.Repositories(r.Context())
	if err != nil {
		a.internalError(w, r, "list repositories", err)
		return
	}
	if repos == nil {
		repos = []models.Repository{}
	}
	writeJSON(w, http.StatusOK, repos)
}

// connected is the answer to connecting a repository.
type connected struct {
	models.Repository
	// HasPipeline says the default branch has a .cisid.yml: without one,
	// nothing builds until it gets one.
	HasPipeline bool `json:"has_pipeline"`
}

// connect looks the repository up on its forge, connects it and, with
// "poll", polls its default branch: the first poll builds its head. It
// answers 201 for a new connection, 200 for one there was already.
func (a *ReposAPI) connect(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Repo string `json:"repo"`
		Poll bool   `json:"poll"`
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, `body must be {"repo": "owner/name", "poll": true}`)
		return
	}
	repo := strings.TrimSpace(body.Repo)
	if _, _, err := forge.SplitKey(repo); err != nil {
		writeError(w, http.StatusBadRequest, "write the repository as owner/name on GitHub, or forge:path, such as gitlab:group/project")
		return
	}
	if body.Poll && !a.polling {
		writeError(w, http.StatusConflict, "polling is off on this server (CISID_POLL_INTERVAL=0): connect it with a webhook instead")
		return
	}

	// The caller's own access to the forge, when they connected it, finds
	// their private repositories, and builds the repository from now on.
	ctx, via := a.userAccess(r.Context(), r, repo)
	info, err := a.forges.Repository(ctx, repo)
	if err != nil {
		a.resolveError(w, r, err)
		return
	}
	head, err := a.forges.BranchHead(ctx, repo, info.DefaultBranch)
	if err != nil {
		a.resolveError(w, r, err)
		return
	}
	// A pipeline that cannot be read now is not a reason to refuse: only
	// one that is not there is worth saying.
	_, err = a.forges.FileContent(ctx, repo, head, pipeline.ConfigFile)
	hasPipeline := !errors.Is(err, forge.ErrNotFound)

	by := "API token"
	if p, ok := caller(r); ok && p.name != "" {
		by = p.name
	}
	rec := models.Repository{Repo: repo, CloneURL: info.CloneURL, DefaultBranch: info.DefaultBranch, AddedBy: by, ViaUserID: via}
	if via != nil {
		rec.Via = by
	}
	created, err := a.store.PutRepository(ctx, rec)
	if err != nil {
		a.internalError(w, r, "connect repository", err)
		return
	}
	if body.Poll {
		if _, err := a.store.PutWatch(ctx, models.Watch{Repo: repo, Branch: info.DefaultBranch, CloneURL: info.CloneURL}); err != nil {
			a.internalError(w, r, "poll repository", err)
			return
		}
		rec.Polled = true
	}
	a.log.InfoContext(ctx, "repository connected", "repo", repo, "by", by, "polled", body.Poll, "has_pipeline", hasPipeline)
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	writeJSON(w, status, connected{Repository: rec, HasPipeline: hasPipeline})
}

func (a *ReposAPI) disconnect(w http.ResponseWriter, r *http.Request) {
	repo, ok := repoParam(w, r)
	if !ok {
		return
	}
	found, err := a.store.DeleteRepository(r.Context(), repo)
	switch {
	case err != nil:
		a.internalError(w, r, "disconnect repository", err)
	case !found:
		writeError(w, http.StatusNotFound, "repository not connected")
	default:
		a.log.InfoContext(r.Context(), "repository disconnected", "repo", repo)
		w.WriteHeader(http.StatusNoContent)
	}
}

// reachable lists the repositories the caller's own forge access reaches,
// and the server's tokens on the forges they have not connected.
func (a *ReposAPI) reachable(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if p, ok := caller(r); ok && p.user != nil && a.access.cfg.ForgeAccounts != nil {
		for _, name := range a.access.cfg.ForgeAccounts.Forges() {
			if token, err := a.access.cfg.ForgeAccounts.Token(ctx, p.user.ID, name); err == nil {
				ctx = forge.WithAccess(ctx, name, token)
			}
		}
	}
	writeJSON(w, http.StatusOK, a.forges.Repositories(ctx))
}

// userAccess returns ctx with the caller's own access to the forge of repo,
// and their user ID, when they connected that forge; else ctx as it is.
func (a *ReposAPI) userAccess(ctx context.Context, r *http.Request, repo string) (context.Context, *int64) {
	p, ok := caller(r)
	if !ok || p.user == nil || a.access.cfg.ForgeAccounts == nil {
		return ctx, nil
	}
	name, _, err := forge.SplitKey(repo)
	if err != nil {
		return ctx, nil
	}
	token, err := a.access.cfg.ForgeAccounts.Token(ctx, p.user.ID, name)
	if err != nil {
		if !errors.Is(err, forgeauth.ErrNotConnected) {
			a.log.WarnContext(ctx, "user's forge token unusable, using the server's", "user", p.user.Username, "forge", name, "error", err)
		}
		return ctx, nil
	}
	id := p.user.ID
	return forge.WithAccess(ctx, name, token), &id
}

// resolveError answers a failure to look a repository up on its forge.
func (a *ReposAPI) resolveError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, forge.ErrNotFound):
		writeError(w, http.StatusNotFound, "repository not found, or private and the server's token for its forge cannot see it")
	case errors.Is(err, forge.ErrUnknownForge):
		writeError(w, http.StatusNotFound, "the server is not set up for this repository's forge")
	case errors.Is(err, forge.ErrTemporary):
		writeError(w, http.StatusServiceUnavailable, "the forge is unavailable, try again")
	default:
		a.internalError(w, r, "look the repository up", err)
	}
}

func (a *ReposAPI) internalError(w http.ResponseWriter, r *http.Request, what string, err error) {
	a.log.ErrorContext(r.Context(), what, "error", err)
	writeError(w, http.StatusInternalServerError, "internal server error")
}
