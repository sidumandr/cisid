package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"time"
	"unicode"

	"github.com/sidumandr/cisid/internal/db"
	"github.com/sidumandr/cisid/internal/github"
	"github.com/sidumandr/cisid/internal/telemetry"
	"github.com/sidumandr/cisid/internal/trigger"
	"github.com/sidumandr/cisid/pkg/models"
)

// TriggerStore queues jobs and keeps schedules. *db.Store satisfies it.
type TriggerStore interface {
	CreateJob(ctx context.Context, job models.Job) (int64, error)
	GetJob(ctx context.Context, id int64) (models.Job, error)
	PutSchedule(ctx context.Context, s models.Schedule) error
	DeleteSchedule(ctx context.Context, repo, name string) (bool, error)
	Schedules(ctx context.Context, repo string) ([]models.Schedule, error)
}

var _ TriggerStore = (*db.Store)(nil)

var scheduleNameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,62}$`)

// TriggersAPI starts builds without a push:
//
//	POST   /api/repos/{owner}/{repo}/builds            build a branch's head now (needs the write token)
//	GET    /api/repos/{owner}/{repo}/schedules         list the repository's schedules
//	PUT    /api/repos/{owner}/{repo}/schedules/{name}  create or change a schedule (needs the write token)
//	DELETE /api/repos/{owner}/{repo}/schedules/{name}  remove a schedule (needs the write token)
type TriggersAPI struct {
	store    TriggerStore
	resolver trigger.Resolver
	token    string
	read     *ReadAccess
	changed  func(jobID int64)
	log      *slog.Logger
	now      func() time.Time
}

// NewTriggersAPI returns a TriggersAPI. token is the write token; read
// guards listing schedules; changed runs with each job it queues.
func NewTriggersAPI(store TriggerStore, resolver trigger.Resolver, token string, read *ReadAccess,
	changed func(jobID int64), log *slog.Logger) *TriggersAPI {
	if changed == nil {
		changed = func(int64) {}
	}
	if log == nil {
		log = slog.Default()
	}
	return &TriggersAPI{store: store, resolver: resolver, token: token, read: read, changed: changed, log: log, now: time.Now}
}

func (a *TriggersAPI) register(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/repos/{owner}/{repo}/builds", requireToken(a.token, a.build))
	mux.HandleFunc("GET /api/repos/{owner}/{repo}/schedules", a.read.wrapFunc(a.listSchedules))
	mux.HandleFunc("PUT /api/repos/{owner}/{repo}/schedules/{name}", requireToken(a.token, a.putSchedule))
	mux.HandleFunc("DELETE /api/repos/{owner}/{repo}/schedules/{name}", requireToken(a.token, a.deleteSchedule))
}

// validBranch accepts the branch names GitHub does, roughly.
func validBranch(b string) bool {
	return len(b) <= 255 && !strings.Contains(b, "..") && !strings.HasPrefix(b, "/") &&
		!strings.HasPrefix(b, "-") && !strings.ContainsFunc(b, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) })
}

// build queues the head of the requested branch, the default branch when
// none is given, and returns the job (201).
func (a *TriggersAPI) build(w http.ResponseWriter, r *http.Request) {
	repo, ok := repoParam(w, r)
	if !ok {
		return
	}
	var body struct {
		Branch string `json:"branch"`
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&body); (err != nil && !errors.Is(err, io.EOF)) || !validBranch(body.Branch) {
		writeError(w, http.StatusBadRequest, `body must be {"branch": "..."} with a valid branch, or empty`)
		return
	}

	ctx := r.Context()
	job, err := trigger.BranchJob(ctx, a.resolver, repo, body.Branch, models.EventManual)
	switch {
	case errors.Is(err, github.ErrNotFound):
		writeError(w, http.StatusNotFound, "repository or branch not found")
		return
	case errors.Is(err, github.ErrTemporary):
		writeError(w, http.StatusServiceUnavailable, "GitHub is unavailable, try again")
		return
	case err != nil:
		a.internalError(w, r, "resolve branch", err)
		return
	}
	job.TraceParent = telemetry.TraceParent(ctx)

	id, err := a.store.CreateJob(ctx, job)
	if err != nil {
		a.internalError(w, r, "queue job", err)
		return
	}
	queued, err := a.store.GetJob(ctx, id)
	if err != nil {
		a.internalError(w, r, "read job", err)
		return
	}
	a.log.InfoContext(ctx, "manual build queued", "job_id", id, "repo", repo, "branch", job.Branch, "commit", job.CommitSHA)
	a.changed(id)
	w.Header().Set("Location", fmt.Sprintf("/api/jobs/%d", id))
	writeJSON(w, http.StatusCreated, queued)
}

func (a *TriggersAPI) listSchedules(w http.ResponseWriter, r *http.Request) {
	repo, ok := repoParam(w, r)
	if !ok {
		return
	}
	schedules, err := a.store.Schedules(r.Context(), repo)
	if err != nil {
		a.internalError(w, r, "list schedules", err)
		return
	}
	if schedules == nil {
		schedules = []models.Schedule{}
	}
	writeJSON(w, http.StatusOK, schedules)
}

// putSchedule creates or changes a schedule and returns it with its next
// run.
func (a *TriggersAPI) putSchedule(w http.ResponseWriter, r *http.Request) {
	repo, ok := repoParam(w, r)
	if !ok {
		return
	}
	name := r.PathValue("name")
	if !scheduleNameRe.MatchString(name) {
		writeError(w, http.StatusBadRequest, "schedule name may only contain a-z, 0-9, '.', '_' and '-'")
		return
	}
	var body struct {
		Cron   string `json:"cron"`
		Branch string `json:"branch"`
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&body); err != nil || body.Cron == "" || !validBranch(body.Branch) {
		writeError(w, http.StatusBadRequest, `body must be {"cron": "...", "branch": "..."} with branch optional`)
		return
	}
	next, err := trigger.Next(body.Cron, a.now())
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	sch := models.Schedule{Repo: repo, Name: name, Cron: body.Cron, Branch: body.Branch, NextRunAt: next}
	if err := a.store.PutSchedule(r.Context(), sch); err != nil {
		a.internalError(w, r, "store schedule", err)
		return
	}
	a.log.InfoContext(r.Context(), "schedule stored", "repo", repo, "schedule", name, "cron", body.Cron, "next_run_at", next)
	writeJSON(w, http.StatusOK, sch)
}

func (a *TriggersAPI) deleteSchedule(w http.ResponseWriter, r *http.Request) {
	repo, ok := repoParam(w, r)
	if !ok {
		return
	}
	found, err := a.store.DeleteSchedule(r.Context(), repo, r.PathValue("name"))
	switch {
	case err != nil:
		a.internalError(w, r, "delete schedule", err)
	case !found:
		writeError(w, http.StatusNotFound, "schedule not found")
	default:
		w.WriteHeader(http.StatusNoContent)
	}
}

func (a *TriggersAPI) internalError(w http.ResponseWriter, r *http.Request, what string, err error) {
	a.log.ErrorContext(r.Context(), what, "error", err)
	writeError(w, http.StatusInternalServerError, "internal server error")
}
