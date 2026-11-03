package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/sidumandr/cisid/internal/db"
	"github.com/sidumandr/cisid/pkg/models"
)

const (
	defaultJobsLimit   = 50
	maxJobsLimit       = 200
	maxLogPage         = 1000
	streamPollInterval = 5 * time.Second
	streamRetryMillis  = 3000
)

// JobReader reads jobs and their logs. *db.Store satisfies it.
type JobReader interface {
	ListJobs(ctx context.Context, limit int) ([]models.Job, error)
	GetJob(ctx context.Context, id int64) (models.Job, error)
	JobLogsAfter(ctx context.Context, id, after int64, limit int) ([]models.JobLog, error)
}

var _ JobReader = (*db.Store)(nil)

// JobEvents signals that a job may have changed. *events.Broker satisfies it.
type JobEvents interface {
	Subscribe(jobID int64) (<-chan struct{}, func())
}

// JobsAPI serves read access to jobs and their logs:
//
//	GET /api/jobs                    recent jobs, newest first (?limit=, max 200)
//	GET /api/jobs/{id}               one job
//	GET /api/jobs/{id}/logs          log chunks (?after=seq), up to 1000 per call
//	GET /api/jobs/{id}/logs/stream   live logs as server-sent events
type JobsAPI struct {
	store        JobReader
	events       JobEvents
	log          *slog.Logger
	pollInterval time.Duration

	closing   chan struct{}
	closeOnce sync.Once
}

// NewJobsAPI returns a JobsAPI. A nil log uses slog.Default().
func NewJobsAPI(store JobReader, events JobEvents, log *slog.Logger) *JobsAPI {
	if log == nil {
		log = slog.Default()
	}
	return &JobsAPI{
		store:        store,
		events:       events,
		log:          log,
		pollInterval: streamPollInterval,
		closing:      make(chan struct{}),
	}
}

// Close ends all open log streams. Register it with
// http.Server.RegisterOnShutdown so streams do not hold up a shutdown.
func (j *JobsAPI) Close() {
	j.closeOnce.Do(func() { close(j.closing) })
}

func (j *JobsAPI) register(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/jobs", j.list)
	mux.HandleFunc("GET /api/jobs/{id}", j.get)
	mux.HandleFunc("GET /api/jobs/{id}/logs", j.logs)
	mux.HandleFunc("GET /api/jobs/{id}/logs/stream", j.stream)
}

func (j *JobsAPI) list(w http.ResponseWriter, r *http.Request) {
	limit, err := queryInt(r, "limit", defaultJobsLimit)
	if err != nil || limit < 1 {
		writeError(w, http.StatusBadRequest, "limit must be a positive integer")
		return
	}
	jobs, err := j.store.ListJobs(r.Context(), min(limit, maxJobsLimit))
	if err != nil {
		j.internalError(w, r, "list jobs", err)
		return
	}
	if jobs == nil {
		jobs = []models.Job{}
	}
	writeJSON(w, http.StatusOK, jobs)
}

func (j *JobsAPI) get(w http.ResponseWriter, r *http.Request) {
	job, ok := j.lookup(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, job)
}

func (j *JobsAPI) logs(w http.ResponseWriter, r *http.Request) {
	after, err := queryInt(r, "after", 0)
	if err != nil || after < 0 {
		writeError(w, http.StatusBadRequest, "after must be a non-negative integer")
		return
	}
	job, ok := j.lookup(w, r)
	if !ok {
		return
	}
	logs, err := j.store.JobLogsAfter(r.Context(), job.ID, int64(after), maxLogPage)
	if err != nil {
		j.internalError(w, r, "read job logs", err)
		return
	}
	if logs == nil {
		logs = []models.JobLog{}
	}
	writeJSON(w, http.StatusOK, logs)
}

// stream sends the job's state and log chunks as server-sent events until
// the job finishes, the client goes away or the server shuts down:
//
//	event: status   the job (sent first and, once finished, last)
//	event: log      a log chunk; its event ID is the chunk's seq
//
// A client resumes after a disconnect with Last-Event-ID (or ?after=).
func (j *JobsAPI) stream(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	after, err := resumePoint(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	id, ok := jobID(w, r)
	if !ok {
		return
	}
	// Subscribe before the first read so no change slips in between.
	changed, unsubscribe := j.events.Subscribe(id)
	defer unsubscribe()
	job, ok := j.load(w, r, id)
	if !ok {
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	rc := http.NewResponseController(w)
	_, _ = fmt.Fprintf(w, "retry: %d\n\n", streamRetryMillis)
	if writeEvent(w, "", "status", job) != nil || rc.Flush() != nil {
		return
	}

	poll := time.NewTicker(j.pollInterval)
	defer poll.Stop()
	for {
		// The job is read before its logs: once it is seen finished, every
		// log chunk is already stored (agents send logs before the result).
		logs, err := j.store.JobLogsAfter(ctx, job.ID, after, maxLogPage)
		if err != nil {
			j.log.WarnContext(ctx, "stream job logs", "job_id", job.ID, "error", err)
			return
		}
		for _, l := range logs {
			if writeEvent(w, strconv.FormatInt(l.Seq, 10), "log", l) != nil {
				return
			}
			after = l.Seq
		}
		if len(logs) == maxLogPage {
			_ = rc.Flush()
			continue
		}
		if job.Status.Finished() {
			_ = writeEvent(w, "", "status", job)
			_ = rc.Flush()
			return
		}
		if rc.Flush() != nil {
			return
		}

		select {
		case <-ctx.Done():
			return
		case <-j.closing:
			return
		case <-changed:
		case <-poll.C:
			// Fallback in case a notification was missed; also keeps
			// proxies from closing an idle connection.
			if _, err := io.WriteString(w, ": keepalive\n\n"); err != nil {
				return
			}
		}

		if job, err = j.store.GetJob(ctx, job.ID); err != nil {
			j.log.WarnContext(ctx, "stream job status", "job_id", job.ID, "error", err)
			return
		}
	}
}

// lookup parses {id} and loads the job, writing an error response if that fails.
func (j *JobsAPI) lookup(w http.ResponseWriter, r *http.Request) (models.Job, bool) {
	id, ok := jobID(w, r)
	if !ok {
		return models.Job{}, false
	}
	return j.load(w, r, id)
}

// jobID parses {id}, writing a 400 response if it is not a valid job ID.
func jobID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id < 1 {
		writeError(w, http.StatusBadRequest, "job id must be a positive integer")
		return 0, false
	}
	return id, true
}

// load reads job id, writing a 404 or 500 response if that fails.
func (j *JobsAPI) load(w http.ResponseWriter, r *http.Request, id int64) (models.Job, bool) {
	job, err := j.store.GetJob(r.Context(), id)
	if errors.Is(err, db.ErrJobNotFound) {
		writeError(w, http.StatusNotFound, "job not found")
		return models.Job{}, false
	}
	if err != nil {
		j.internalError(w, r, "get job", err)
		return models.Job{}, false
	}
	return job, true
}

func (j *JobsAPI) internalError(w http.ResponseWriter, r *http.Request, what string, err error) {
	j.log.ErrorContext(r.Context(), what, "path", r.URL.Path, "error", err)
	writeError(w, http.StatusInternalServerError, "internal server error")
}

// resumePoint returns the seq after which a stream starts: Last-Event-ID,
// else ?after=, else 0.
func resumePoint(r *http.Request) (int64, error) {
	v := r.Header.Get("Last-Event-ID")
	if v == "" {
		v = r.URL.Query().Get("after")
	}
	if v == "" {
		return 0, nil
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil || n < 0 {
		return 0, fmt.Errorf("resume point %q must be a non-negative integer", v)
	}
	return n, nil
}

func queryInt(r *http.Request, name string, def int) (int, error) {
	v := r.URL.Query().Get(name)
	if v == "" {
		return def, nil
	}
	return strconv.Atoi(v)
}

func writeEvent(w io.Writer, id, event string, v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("encode %s event: %w", event, err)
	}
	if id != "" {
		if _, err := fmt.Fprintf(w, "id: %s\n", id); err != nil {
			return err
		}
	}
	_, err = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, data)
	return err
}
