package api

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/sidumandr/cisid/internal/db"
	"github.com/sidumandr/cisid/pkg/models"
)

type mockJobReader struct {
	mock.Mock
}

func (m *mockJobReader) ListJobs(ctx context.Context, limit int) ([]models.Job, error) {
	args := m.Called(ctx, limit)
	jobs, _ := args.Get(0).([]models.Job)
	return jobs, args.Error(1)
}

func (m *mockJobReader) GetJob(ctx context.Context, id int64) (models.Job, error) {
	args := m.Called(ctx, id)
	return args.Get(0).(models.Job), args.Error(1)
}

func (m *mockJobReader) JobLogsAfter(ctx context.Context, id, after int64, limit int) ([]models.JobLog, error) {
	args := m.Called(ctx, id, after, limit)
	logs, _ := args.Get(0).([]models.JobLog)
	return logs, args.Error(1)
}

// fakeEvents is a JobEvents whose signals the test triggers.
type fakeEvents struct {
	mu   sync.Mutex
	subs map[int64]chan struct{}
}

func (f *fakeEvents) Subscribe(jobID int64) (<-chan struct{}, func()) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.subs == nil {
		f.subs = map[int64]chan struct{}{}
	}
	ch := make(chan struct{}, 1)
	f.subs[jobID] = ch
	return ch, func() {
		f.mu.Lock()
		defer f.mu.Unlock()
		delete(f.subs, jobID)
	}
}

func (f *fakeEvents) publish(jobID int64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if ch, ok := f.subs[jobID]; ok {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

func (f *fakeEvents) subscribed(jobID int64) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	_, ok := f.subs[jobID]
	return ok
}

func newJobsAPI(t *testing.T, store *mockJobReader, ev *fakeEvents) (*JobsAPI, http.Handler) {
	t.Helper()
	jobs := NewJobsAPI(store, ev, slog.New(slog.DiscardHandler))
	jobs.pollInterval = time.Hour // tests drive the stream with events
	t.Cleanup(jobs.Close)
	return jobs, NewRouter(Handlers{
		Webhook: http.NotFoundHandler(),
		Agents:  http.NotFoundHandler(),
		Jobs:    jobs,
		DB:      &mockPinger{},
	}, slog.New(slog.DiscardHandler))
}

func get(t *testing.T, h http.Handler, target string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, target, nil))
	return rec
}

func runningJob(id int64) models.Job {
	return models.Job{ID: id, Repo: "sidumandr/cisid", Branch: "main", Status: models.JobStatusRunning}
}

func finishedJob(id int64) models.Job {
	j := runningJob(id)
	j.Status = models.JobStatusSuccess
	return j
}

func logLine(id, seq int64, data string) models.JobLog {
	return models.JobLog{JobID: id, Seq: seq, Stream: models.StreamStdout, Data: data}
}

func TestListJobs(t *testing.T) {
	tests := []struct {
		query string
		limit int
	}{
		{"", 50},
		{"?limit=5", 5},
		{"?limit=1000", 200},
	}
	for _, tt := range tests {
		t.Run(tt.query, func(t *testing.T) {
			store := &mockJobReader{}
			store.On("ListJobs", mock.Anything, tt.limit).Return([]models.Job{runningJob(2), finishedJob(1)}, nil).Once()
			_, h := newJobsAPI(t, store, &fakeEvents{})

			rec := get(t, h, "/api/jobs"+tt.query)

			assert.Equal(t, http.StatusOK, rec.Code)
			assert.Equal(t, "application/json", rec.Header().Get("Content-Type"))
			var got []models.Job
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
			assert.Equal(t, []int64{2, 1}, []int64{got[0].ID, got[1].ID})
			store.AssertExpectations(t)
		})
	}
}

func TestListJobsBadLimit(t *testing.T) {
	_, h := newJobsAPI(t, &mockJobReader{}, &fakeEvents{})
	assert.Equal(t, http.StatusBadRequest, get(t, h, "/api/jobs?limit=-1").Code)
	assert.Equal(t, http.StatusBadRequest, get(t, h, "/api/jobs?limit=lots").Code)
}

func TestGetJob(t *testing.T) {
	store := &mockJobReader{}
	store.On("GetJob", mock.Anything, int64(42)).Return(finishedJob(42), nil)
	store.On("GetJob", mock.Anything, int64(7)).Return(models.Job{}, fmt.Errorf("get: %w", db.ErrJobNotFound))
	_, h := newJobsAPI(t, store, &fakeEvents{})

	rec := get(t, h, "/api/jobs/42")
	assert.Equal(t, http.StatusOK, rec.Code)
	var got models.Job
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	assert.Equal(t, finishedJob(42), got)

	assert.Equal(t, http.StatusNotFound, get(t, h, "/api/jobs/7").Code)
	assert.Equal(t, http.StatusBadRequest, get(t, h, "/api/jobs/abc").Code)
	assert.Equal(t, http.StatusBadRequest, get(t, h, "/api/jobs/0").Code)
}

func TestJobLogs(t *testing.T) {
	store := &mockJobReader{}
	store.On("GetJob", mock.Anything, int64(42)).Return(runningJob(42), nil)
	store.On("JobLogsAfter", mock.Anything, int64(42), int64(10), maxLogPage).
		Return([]models.JobLog{logLine(42, 11, "a\n"), logLine(42, 12, "b\n")}, nil).Once()
	_, h := newJobsAPI(t, store, &fakeEvents{})

	rec := get(t, h, "/api/jobs/42/logs?after=10")

	assert.Equal(t, http.StatusOK, rec.Code)
	var got []models.JobLog
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	assert.Equal(t, []models.JobLog{logLine(42, 11, "a\n"), logLine(42, 12, "b\n")}, got)

	assert.Equal(t, http.StatusBadRequest, get(t, h, "/api/jobs/42/logs?after=-3").Code)
}

func TestJobLogsOfMissingJob(t *testing.T) {
	store := &mockJobReader{}
	store.On("GetJob", mock.Anything, int64(7)).Return(models.Job{}, db.ErrJobNotFound)
	_, h := newJobsAPI(t, store, &fakeEvents{})

	assert.Equal(t, http.StatusNotFound, get(t, h, "/api/jobs/7/logs").Code)
	assert.Equal(t, http.StatusNotFound, get(t, h, "/api/jobs/7/logs/stream").Code)
}

// sseEvent is one parsed server-sent event.
type sseEvent struct {
	ID, Event, Data string
}

// openStream starts a stream request and returns a channel of its events,
// closed when the server ends the stream.
func openStream(t *testing.T, h http.Handler, target string, header http.Header) <-chan sseEvent {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, srv.URL+target, nil)
	require.NoError(t, err)
	for k, v := range header {
		req.Header[k] = v
	}
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, "text/event-stream", resp.Header.Get("Content-Type"))

	events := make(chan sseEvent, 100)
	go func() {
		defer close(events)
		defer resp.Body.Close()
		var ev sseEvent
		sc := bufio.NewScanner(resp.Body)
		for sc.Scan() {
			line := sc.Text()
			switch {
			case line == "":
				if ev.Event != "" {
					events <- ev
				}
				ev = sseEvent{}
			case strings.HasPrefix(line, "id: "):
				ev.ID = strings.TrimPrefix(line, "id: ")
			case strings.HasPrefix(line, "event: "):
				ev.Event = strings.TrimPrefix(line, "event: ")
			case strings.HasPrefix(line, "data: "):
				ev.Data = strings.TrimPrefix(line, "data: ")
			}
		}
	}()
	return events
}

func next(t *testing.T, events <-chan sseEvent) sseEvent {
	t.Helper()
	select {
	case ev, ok := <-events:
		require.True(t, ok, "stream ended early")
		return ev
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for an event")
		return sseEvent{}
	}
}

func expectEnd(t *testing.T, events <-chan sseEvent) {
	t.Helper()
	select {
	case ev, ok := <-events:
		require.False(t, ok, "unexpected event %+v", ev)
	case <-time.After(5 * time.Second):
		t.Fatal("stream did not end")
	}
}

func (e sseEvent) log(t *testing.T) models.JobLog {
	t.Helper()
	require.Equal(t, "log", e.Event)
	var l models.JobLog
	require.NoError(t, json.Unmarshal([]byte(e.Data), &l))
	assert.Equal(t, fmt.Sprint(l.Seq), e.ID, "the event ID is the log seq, for resuming")
	return l
}

func (e sseEvent) job(t *testing.T) models.Job {
	t.Helper()
	require.Equal(t, "status", e.Event)
	var j models.Job
	require.NoError(t, json.Unmarshal([]byte(e.Data), &j))
	return j
}

func TestStreamFollowsJobUntilItFinishes(t *testing.T) {
	store := &mockJobReader{}
	ev := &fakeEvents{}
	// Backlog first, then a new line, then the job finishes.
	store.On("GetJob", mock.Anything, int64(42)).Return(runningJob(42), nil).Times(2)
	store.On("JobLogsAfter", mock.Anything, int64(42), int64(0), maxLogPage).
		Return([]models.JobLog{logLine(42, 1, "one\n"), logLine(42, 2, "two\n")}, nil).Once()
	store.On("JobLogsAfter", mock.Anything, int64(42), int64(2), maxLogPage).
		Return([]models.JobLog{logLine(42, 3, "three\n")}, nil).Once()
	store.On("GetJob", mock.Anything, int64(42)).Return(finishedJob(42), nil)
	store.On("JobLogsAfter", mock.Anything, int64(42), int64(3), maxLogPage).Return(nil, nil)
	_, h := newJobsAPI(t, store, ev)

	events := openStream(t, h, "/api/jobs/42/logs/stream", nil)

	assert.Equal(t, models.JobStatusRunning, next(t, events).job(t).Status)
	assert.Equal(t, "one\n", next(t, events).log(t).Data)
	assert.Equal(t, "two\n", next(t, events).log(t).Data)

	ev.publish(42)
	assert.Equal(t, "three\n", next(t, events).log(t).Data)

	ev.publish(42)
	assert.Equal(t, models.JobStatusSuccess, next(t, events).job(t).Status)
	expectEnd(t, events)
	assert.Eventually(t, func() bool { return !ev.subscribed(42) }, time.Second, 10*time.Millisecond)
}

func TestStreamResumesFromLastEventID(t *testing.T) {
	store := &mockJobReader{}
	store.On("GetJob", mock.Anything, int64(42)).Return(finishedJob(42), nil)
	store.On("JobLogsAfter", mock.Anything, int64(42), int64(17), maxLogPage).
		Return([]models.JobLog{logLine(42, 18, "rest\n")}, nil).Once()
	store.On("JobLogsAfter", mock.Anything, int64(42), int64(18), maxLogPage).Return(nil, nil)
	_, h := newJobsAPI(t, store, &fakeEvents{})

	events := openStream(t, h, "/api/jobs/42/logs/stream", http.Header{"Last-Event-Id": {"17"}})

	next(t, events).job(t)
	assert.Equal(t, int64(18), next(t, events).log(t).Seq)
	assert.Equal(t, models.JobStatusSuccess, next(t, events).job(t).Status)
	expectEnd(t, events)
}

func TestStreamEndsOnShutdown(t *testing.T) {
	store := &mockJobReader{}
	store.On("GetJob", mock.Anything, int64(42)).Return(runningJob(42), nil)
	store.On("JobLogsAfter", mock.Anything, int64(42), int64(0), maxLogPage).Return(nil, nil)
	jobs, h := newJobsAPI(t, store, &fakeEvents{})

	events := openStream(t, h, "/api/jobs/42/logs/stream", nil)
	next(t, events).job(t)

	jobs.Close()
	expectEnd(t, events)
}
