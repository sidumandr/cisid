package api

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/sidumandr/cisid/internal/auth"
	"github.com/sidumandr/cisid/internal/notify"
	"github.com/sidumandr/cisid/pkg/models"
)

// NotificationService keeps and sends notifications. *notify.Service
// satisfies it.
type NotificationService interface {
	Put(ctx context.Context, n models.Notification, t notify.Target) error
	Delete(ctx context.Context, repo, name string) error
	List(ctx context.Context, repo string) ([]models.Notification, error)
	Test(ctx context.Context, repo, name string) error
}

var _ NotificationService = (*notify.Service)(nil)

// NotificationsAPI manages where a repository's build results are
// announced. Every endpoint needs the maintainer role; a target's URL can
// be written but never read back:
//
//	GET    /api/repos/{owner}/{repo}/notifications              the notifications
//	PUT    /api/repos/{owner}/{repo}/notifications/{name}       create or replace one
//	DELETE /api/repos/{owner}/{repo}/notifications/{name}       remove one
//	POST   /api/repos/{owner}/{repo}/notifications/{name}/test  send a test message
type NotificationsAPI struct {
	service NotificationService
	access  *Access
	log     *slog.Logger
}

// NewNotificationsAPI returns a NotificationsAPI. A nil service (no
// CISID_SECRET_KEY) disables the endpoints; a nil log uses slog.Default().
func NewNotificationsAPI(service NotificationService, access *Access, log *slog.Logger) *NotificationsAPI {
	if log == nil {
		log = slog.Default()
	}
	if access == nil {
		access = openAccess()
	}
	return &NotificationsAPI{service: service, access: access, log: log}
}

func (a *NotificationsAPI) register(mux *http.ServeMux) {
	maintainer := func(h http.HandlerFunc) http.HandlerFunc { return a.access.require(auth.RoleMaintainer, a.enabled(h)) }
	mux.HandleFunc("GET /api/repos/{owner}/{repo}/notifications", maintainer(a.list))
	mux.HandleFunc("PUT /api/repos/{owner}/{repo}/notifications/{name}", maintainer(a.put))
	mux.HandleFunc("DELETE /api/repos/{owner}/{repo}/notifications/{name}", maintainer(a.delete))
	mux.HandleFunc("POST /api/repos/{owner}/{repo}/notifications/{name}/test", maintainer(a.test))
}

func (a *NotificationsAPI) enabled(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if a.service == nil {
			writeError(w, http.StatusForbidden, "notifications disabled: CISID_SECRET_KEY is not set")
			return
		}
		next(w, r)
	}
}

func (a *NotificationsAPI) list(w http.ResponseWriter, r *http.Request) {
	repo, ok := repoParam(w, r)
	if !ok {
		return
	}
	list, err := a.service.List(r.Context(), repo)
	if err != nil {
		a.internalError(w, r, "list notifications", err)
		return
	}
	if list == nil {
		list = []models.Notification{}
	}
	writeJSON(w, http.StatusOK, list)
}

// notificationBody is what PUT takes.
type notificationBody struct {
	Kind         string   `json:"kind"`
	URL          string   `json:"url"`
	Secret       string   `json:"secret"`
	To           []string `json:"to"`
	On           []string `json:"on"`
	Branches     []string `json:"branches"`
	PullRequests bool     `json:"pull_requests"`
}

func (a *NotificationsAPI) put(w http.ResponseWriter, r *http.Request) {
	repo, ok := repoParam(w, r)
	if !ok {
		return
	}
	var body notificationBody
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest,
			`body must be {"kind": "slack|webhook|email", "url": "...", "secret": "...", "to": [...], "on": [...], "branches": [...], "pull_requests": false}`)
		return
	}
	name := r.PathValue("name")
	n := models.Notification{Repo: repo, Name: name, Kind: body.Kind, On: body.On, Branches: body.Branches, PullRequests: body.PullRequests}
	err := a.service.Put(r.Context(), n, notify.Target{URL: body.URL, Secret: body.Secret, To: body.To})
	switch {
	case errors.Is(err, notify.ErrInvalid):
		writeError(w, http.StatusBadRequest, err.Error())
	case err != nil:
		a.internalError(w, r, "store notification", err)
	default:
		a.log.InfoContext(r.Context(), "notification stored", "repo", repo, "name", name, "kind", body.Kind)
		w.WriteHeader(http.StatusNoContent)
	}
}

func (a *NotificationsAPI) delete(w http.ResponseWriter, r *http.Request) {
	repo, ok := repoParam(w, r)
	if !ok {
		return
	}
	name := r.PathValue("name")
	err := a.service.Delete(r.Context(), repo, name)
	switch {
	case errors.Is(err, notify.ErrNotFound):
		writeError(w, http.StatusNotFound, "notification not found")
	case err != nil:
		a.internalError(w, r, "delete notification", err)
	default:
		a.log.InfoContext(r.Context(), "notification deleted", "repo", repo, "name", name)
		w.WriteHeader(http.StatusNoContent)
	}
}

// test sends a test message and answers 204 once it went through, or 502
// with why it did not.
func (a *NotificationsAPI) test(w http.ResponseWriter, r *http.Request) {
	repo, ok := repoParam(w, r)
	if !ok {
		return
	}
	err := a.service.Test(r.Context(), repo, r.PathValue("name"))
	switch {
	case errors.Is(err, notify.ErrNotFound):
		writeError(w, http.StatusNotFound, "notification not found")
	case errors.Is(err, notify.ErrSend):
		writeError(w, http.StatusBadGateway, err.Error())
	case err != nil:
		a.internalError(w, r, "test notification", err)
	default:
		w.WriteHeader(http.StatusNoContent)
	}
}

func (a *NotificationsAPI) internalError(w http.ResponseWriter, r *http.Request, what string, err error) {
	a.log.ErrorContext(r.Context(), what, "path", r.URL.Path, "error", err)
	writeError(w, http.StatusInternalServerError, "internal server error")
}
