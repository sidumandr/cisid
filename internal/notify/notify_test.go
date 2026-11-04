package notify

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sidumandr/cisid/internal/db"
	"github.com/sidumandr/cisid/internal/secrets"
	"github.com/sidumandr/cisid/pkg/models"
)

// memStore is an in-memory Store.
type memStore struct {
	mu      sync.Mutex
	nextID  int64
	notes   map[string]db.SealedNotification // "repo name"
	jobs    []models.Job                     // finished, not claimed yet
	prev    map[int64]models.JobStatus       // the result before each job
	records map[string][]string              // problems by notification name, "" for sent
}

func newMemStore() *memStore {
	return &memStore{notes: map[string]db.SealedNotification{}, prev: map[int64]models.JobStatus{}, records: map[string][]string{}}
}

func (m *memStore) PutNotification(_ context.Context, n models.Notification, sealed []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.nextID++
	n.ID = m.nextID
	m.notes[n.Repo+" "+n.Name] = db.SealedNotification{Notification: n, Sealed: sealed}
	return nil
}

func (m *memStore) DeleteNotification(_ context.Context, repo, name string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.notes[repo+" "+name]
	delete(m.notes, repo+" "+name)
	return ok, nil
}

func (m *memStore) sealed(repo string) []db.SealedNotification {
	var out []db.SealedNotification
	for _, n := range m.notes {
		if n.Repo == repo {
			out = append(out, n)
		}
	}
	slices.SortFunc(out, func(a, b db.SealedNotification) int { return strings.Compare(a.Name, b.Name) })
	return out
}

func (m *memStore) Notifications(_ context.Context, repo string) ([]models.Notification, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []models.Notification
	for _, n := range m.sealed(repo) {
		out = append(out, n.Notification)
	}
	return out, nil
}

func (m *memStore) SealedNotifications(_ context.Context, repo string) ([]db.SealedNotification, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.sealed(repo), nil
}

func (m *memStore) SealedNotification(_ context.Context, repo, name string) (db.SealedNotification, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	n, ok := m.notes[repo+" "+name]
	if !ok {
		return n, db.ErrNotificationNotFound
	}
	return n, nil
}

func (m *memStore) RecordNotification(_ context.Context, id int64, problem string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, n := range m.notes {
		if n.ID == id {
			m.records[n.Name] = append(m.records[n.Name], problem)
		}
	}
	return nil
}

func (m *memStore) ClaimFinishedJobs(_ context.Context, limit int) ([]models.Job, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := min(limit, len(m.jobs))
	out := m.jobs[:n:n]
	m.jobs = m.jobs[n:]
	return out, nil
}

func (m *memStore) PreviousResult(_ context.Context, job models.Job) (models.JobStatus, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.prev[job.ID]
	return s, ok, nil
}

func (m *memStore) finish(jobs ...models.Job) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.jobs = append(m.jobs, jobs...)
}

func (m *memStore) recorded(name string) []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return slices.Clone(m.records[name])
}

func testCipher(t *testing.T) *secrets.Cipher {
	t.Helper()
	c, err := secrets.NewCipher(make([]byte, secrets.KeySize))
	require.NoError(t, err)
	return c
}

// sink is a chat or webhook that records what it is sent.
type sink struct {
	mu     sync.Mutex
	bodies []string
	header []http.Header
	status []int // answered in turn; then 200
}

func (s *sink) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.bodies = append(s.bodies, string(body))
	s.header = append(s.header, r.Header.Clone())
	if len(s.status) > 0 {
		w.WriteHeader(s.status[0])
		s.status = s.status[1:]
	}
}

func (s *sink) received() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.bodies)
}

func newSink(t *testing.T, status ...int) (*sink, string) {
	t.Helper()
	s := &sink{status: status}
	srv := httptest.NewServer(s)
	t.Cleanup(srv.Close)
	return s, srv.URL + "/services/T000/B000/secret-part"
}

func newService(t *testing.T, store Store, opts ...Option) *Service {
	t.Helper()
	opts = append([]Option{WithLogger(slog.New(slog.DiscardHandler)), WithPublicURL("https://ci.example.com"),
		WithRetryDelays(time.Millisecond, time.Millisecond)}, opts...)
	return New(store, testCipher(t), opts...)
}

const repo = "sidumandr/cisid"

func finished(id int64, status models.JobStatus, branch string) models.Job {
	now := time.Now()
	start := now.Add(-108 * time.Second)
	exit, step := 1, 1
	j := models.Job{ID: id, Repo: repo, Branch: branch, CommitSHA: "6113728f27ae82c7b1a177c8d03f9e96e0adf246",
		Status: status, Event: models.EventPush, StartedAt: &start, FinishedAt: &now, StepNames: []string{"clone", "test"}}
	if status == models.JobStatusFailed {
		j.ExitCode, j.FailedStep = &exit, &step
	}
	return j
}

func TestPutChecks(t *testing.T) {
	store := newMemStore()
	s := newService(t, store)
	ctx := t.Context()
	slack := Target{URL: "https://hooks.slack.com/services/T000/B000/XXXX"}

	for name, c := range map[string]struct {
		n models.Notification
		t Target
	}{
		"bad name":        {models.Notification{Name: "Chat Room", Kind: models.NotifySlack}, slack},
		"bad kind":        {models.Notification{Name: "chat", Kind: "teams"}, slack},
		"not a url":       {models.Notification{Name: "chat", Kind: models.NotifySlack}, Target{URL: "hooks.slack.com/x"}},
		"ftp":             {models.Notification{Name: "chat", Kind: models.NotifyWebhook}, Target{URL: "ftp://example.com/x"}},
		"slack secret":    {models.Notification{Name: "chat", Kind: models.NotifySlack}, Target{URL: slack.URL, Secret: "s"}},
		"slack addresses": {models.Notification{Name: "chat", Kind: models.NotifySlack}, Target{URL: slack.URL, To: []string{"a@example.com"}}},
		"unknown result":  {models.Notification{Name: "chat", Kind: models.NotifySlack, On: []string{"failure"}}, slack},
		"spaced branch":   {models.Notification{Name: "chat", Kind: models.NotifySlack, Branches: []string{"a b"}}, slack},
		"email off":       {models.Notification{Name: "mail", Kind: models.NotifyEmail}, Target{To: []string{"dev@example.com"}}},
		"long secret":     {models.Notification{Name: "hook", Kind: models.NotifyWebhook}, Target{URL: "https://example.com", Secret: strings.Repeat("s", 300)}},
		"webhook no url":  {models.Notification{Name: "hook", Kind: models.NotifyWebhook}, Target{}},
	} {
		c.n.Repo = repo
		assert.ErrorIs(t, s.Put(ctx, c.n, c.t), ErrInvalid, name)
	}
	assert.Empty(t, store.notes)

	require.NoError(t, s.Put(ctx, models.Notification{Repo: repo, Name: "chat", Kind: models.NotifySlack}, slack))
	require.NoError(t, s.Put(ctx, models.Notification{Repo: repo, Name: "hook", Kind: models.NotifyWebhook,
		On: []string{"passed", "broken", "passed"}, Branches: []string{"main", "release/*"}}, Target{URL: "http://10.0.0.5:8080/ci", Secret: "s3"}))

	list, err := s.List(ctx, repo)
	require.NoError(t, err)
	require.Len(t, list, 2)
	assert.Equal(t, "hooks.slack.com", list[0].Target, "only the host is shown")
	assert.Equal(t, DefaultOn, list[0].On)
	assert.Equal(t, "10.0.0.5:8080", list[1].Target)
	assert.Equal(t, []string{"broken", "passed"}, list[1].On, "in their usual order, once")

	sealed := store.notes[repo+" chat"].Sealed
	assert.NotContains(t, string(sealed), "XXXX", "the URL is sealed")
	plain, err := testCipher(t).Open(repo, "notification chat", sealed)
	require.NoError(t, err)
	assert.JSONEq(t, `{"url":"https://hooks.slack.com/services/T000/B000/XXXX"}`, plain)

	require.ErrorIs(t, s.Delete(ctx, repo, "missing"), ErrNotFound)
	require.NoError(t, s.Delete(ctx, repo, "chat"))
	list, err = s.List(ctx, repo)
	require.NoError(t, err)
	assert.Len(t, list, 1)
}

func TestPutEmail(t *testing.T) {
	store := newMemStore()
	m, err := NewMailer(MailConfig{Host: "mail.example.com", Port: 587, From: "ci@example.com", TLS: TLSStartTLS})
	require.NoError(t, err)
	s := newService(t, store, WithMailer(m))
	ctx := t.Context()

	n := models.Notification{Repo: repo, Name: "team", Kind: models.NotifyEmail}
	require.ErrorIs(t, s.Put(ctx, n, Target{To: []string{"not an address"}}), ErrInvalid)
	require.ErrorIs(t, s.Put(ctx, n, Target{}), ErrInvalid)
	require.ErrorIs(t, s.Put(ctx, n, Target{To: make([]string, maxRecipients+1)}), ErrInvalid)
	require.ErrorIs(t, s.Put(ctx, n, Target{URL: "https://example.com", To: []string{"a@example.com"}}), ErrInvalid)

	require.NoError(t, s.Put(ctx, n, Target{To: []string{"Dev Team <dev@example.com>", " ops@example.com", "dev@example.com"}}))
	list, err := s.List(ctx, repo)
	require.NoError(t, err)
	require.Len(t, list, 1)
	assert.Equal(t, "dev@example.com, ops@example.com", list[0].Target, "addresses alone, once each")
}

func TestAnnounce(t *testing.T) {
	store := newMemStore()
	chat, chatURL := newSink(t)
	everything, everythingURL := newSink(t)
	prs, prsURL := newSink(t)
	s := newService(t, store)
	ctx := t.Context()

	require.NoError(t, s.Put(ctx, models.Notification{Repo: repo, Name: "chat", Kind: models.NotifySlack,
		Branches: []string{"main"}}, Target{URL: chatURL}))
	require.NoError(t, s.Put(ctx, models.Notification{Repo: repo, Name: "everything", Kind: models.NotifySlack,
		On: []string{"failed", "passed"}}, Target{URL: everythingURL}))
	require.NoError(t, s.Put(ctx, models.Notification{Repo: repo, Name: "prs", Kind: models.NotifySlack,
		On: []string{"broken"}, PullRequests: true, Branches: []string{"main"}}, Target{URL: prsURL}))
	require.NoError(t, s.Put(ctx, models.Notification{Repo: "other/repo", Name: "chat", Kind: models.NotifySlack},
		Target{URL: chatURL}))

	pr := finished(4, models.JobStatusFailed, "feature")
	pr.Event, pr.PullRequest, pr.TargetBranch = models.EventPullRequest, 7, "main"
	stale := finished(6, models.JobStatusFailed, "main")
	old := time.Now().Add(-48 * time.Hour)
	stale.FinishedAt = &old
	store.prev[2] = models.JobStatusSuccess // job 2 breaks main
	store.prev[3] = models.JobStatusFailed  // job 3 fixes it
	store.finish(
		finished(1, models.JobStatusSuccess, "main"), // first build: passed, no change
		finished(2, models.JobStatusFailed, "main"),  // broken
		finished(3, models.JobStatusSuccess, "main"), // fixed
		pr, // a pull request into main breaks
		finished(5, models.JobStatusCanceled, "main"), // canceled jobs are not announced
		stale, // nor results from long ago
		finished(7, models.JobStatusFailed, "dev"), // chat watches main only
	)
	s.round(ctx)

	got := chat.received()
	require.Len(t, got, 2, "chat: failures and fixes on main")
	assert.Contains(t, got[0], "sidumandr/cisid: main is broken")
	assert.Contains(t, got[0], `Step \"test\" failed with exit code 1 after 1m48s.`)
	assert.Contains(t, got[0], "<https://ci.example.com/jobs/2|Build #2> · commit 6113728 · push")
	assert.Contains(t, got[1], "sidumandr/cisid: main is fixed")

	got = everything.received()
	require.Len(t, got, 4, "everything: each failure and pass of every branch, but not pull requests")
	assert.Contains(t, got[0], "main passed", "the first build of a branch is not a fix")
	assert.Contains(t, got[1], "main is broken", "every notification gets the most telling message")
	assert.Contains(t, got[2], "main is fixed")
	assert.Contains(t, got[3], "dev failed", "no notification of dev asks for changes")
	assert.NotContains(t, got[3], "again")

	got = prs.received()
	require.Len(t, got, 2)
	assert.Contains(t, got[0], "main is broken")
	assert.Contains(t, got[1], "pull request #7 (feature → main) is broken")

	assert.Equal(t, []string{"", ""}, store.recorded("chat"))
}

func TestAnnounceFailedAgain(t *testing.T) {
	store := newMemStore()
	chat, chatURL := newSink(t)
	s := newService(t, store)
	require.NoError(t, s.Put(t.Context(), models.Notification{Repo: repo, Name: "chat", Kind: models.NotifySlack,
		On: []string{"failed", "broken"}}, Target{URL: chatURL}))

	store.prev[1] = models.JobStatusFailed
	failed := finished(1, models.JobStatusFailed, "main")
	failed.Matrix = map[string]string{"go": "1.27"}
	store.finish(failed)
	s.round(t.Context())

	got := chat.received()
	require.Len(t, got, 1)
	assert.Contains(t, got[0], "sidumandr/cisid: main (go=1.27) failed again")
}

func TestWebhook(t *testing.T) {
	store := newMemStore()
	hook, hookURL := newSink(t)
	s := newService(t, store)
	require.NoError(t, s.Put(t.Context(), models.Notification{Repo: repo, Name: "hook", Kind: models.NotifyWebhook},
		Target{URL: hookURL, Secret: "shh"}))

	store.finish(finished(9, models.JobStatusFailed, "main"))
	s.round(t.Context())

	got := hook.received()
	require.Len(t, got, 1)
	var body WebhookBody
	require.NoError(t, json.Unmarshal([]byte(got[0]), &body))
	assert.Equal(t, "broken", body.Result, "the first build failed")
	assert.Equal(t, repo, body.Repo)
	assert.Equal(t, "https://ci.example.com/jobs/9", body.URL)
	require.NotNil(t, body.Job)
	assert.Equal(t, int64(9), body.Job.ID)
	h := hook.header[0]
	assert.Equal(t, "broken", h.Get("X-Cisid-Result"))
	assert.Equal(t, Signature("shh", []byte(got[0])), h.Get("X-Cisid-Signature"))
	assert.Equal(t, "application/json", h.Get("Content-Type"))
}

func TestRetries(t *testing.T) {
	store := newMemStore()
	flaky, flakyURL := newSink(t, http.StatusServiceUnavailable, http.StatusTooManyRequests)
	gone, goneURL := newSink(t, http.StatusNotFound, http.StatusNotFound)
	s := newService(t, store)
	ctx := t.Context()
	require.NoError(t, s.Put(ctx, models.Notification{Repo: repo, Name: "flaky", Kind: models.NotifySlack}, Target{URL: flakyURL}))
	require.NoError(t, s.Put(ctx, models.Notification{Repo: repo, Name: "gone", Kind: models.NotifySlack}, Target{URL: goneURL}))

	store.finish(finished(1, models.JobStatusFailed, "main"))
	s.round(ctx)

	assert.Len(t, flaky.received(), 3, "retried until it went through")
	assert.Equal(t, []string{""}, store.recorded("flaky"))
	assert.Len(t, gone.received(), 1, "a 404 is not retried")
	problems := store.recorded("gone")
	require.Len(t, problems, 1)
	assert.Contains(t, problems[0], "answered 404 Not Found")
	assert.NotContains(t, problems[0], "secret-part", "the URL's path is a credential")
}

func TestUnreachableKeepsURLSecret(t *testing.T) {
	store := newMemStore()
	srv := httptest.NewServer(http.NotFoundHandler())
	url := srv.URL + "/services/T000/B000/secret-part"
	srv.Close() // nothing listens there now
	s := newService(t, store)
	ctx := t.Context()
	require.NoError(t, s.Put(ctx, models.Notification{Repo: repo, Name: "chat", Kind: models.NotifySlack}, Target{URL: url}))

	err := s.Test(ctx, repo, "chat")
	require.ErrorIs(t, err, ErrSend)
	assert.NotContains(t, err.Error(), "secret-part")
	problems := store.recorded("chat")
	require.Len(t, problems, 1)
	assert.NotContains(t, problems[0], "secret-part")
}

func TestTest(t *testing.T) {
	store := newMemStore()
	chat, chatURL := newSink(t)
	s := newService(t, store)
	ctx := t.Context()
	require.NoError(t, s.Put(ctx, models.Notification{Repo: "gitlab:group/sub/app", Name: "chat", Kind: models.NotifySlack},
		Target{URL: chatURL}))

	require.NoError(t, s.Test(ctx, "gitlab:group/sub/app", "chat"))
	got := chat.received()
	require.Len(t, got, 1)
	assert.Contains(t, got[0], "gitlab:group/sub/app: test notification")
	assert.Contains(t, got[0], "<https://ci.example.com/repos/gitlab:group%2Fsub/app>")
	assert.Equal(t, []string{""}, store.recorded("chat"))

	assert.ErrorIs(t, s.Test(ctx, repo, "missing"), ErrNotFound)
}

func TestRunWakes(t *testing.T) {
	store := newMemStore()
	chat, chatURL := newSink(t)
	s := newService(t, store, WithInterval(time.Hour))
	require.NoError(t, s.Put(t.Context(), models.Notification{Repo: repo, Name: "chat", Kind: models.NotifySlack},
		Target{URL: chatURL}))

	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- s.Run(ctx) }()

	store.finish(finished(1, models.JobStatusFailed, "main"))
	s.Wake()
	require.Eventually(t, func() bool { return len(chat.received()) == 1 }, 5*time.Second, 10*time.Millisecond)
	cancel()
	require.NoError(t, <-done)
}

func TestSlackEscapes(t *testing.T) {
	job := finished(3, models.JobStatusFailed, "<!channel>&co")
	msg := jobMessage(job, models.ResultBroken, true, "")
	text := msg.Slack()
	assert.Contains(t, text, "&lt;!channel&gt;&amp;co")
	assert.NotContains(t, text, "<!channel>")
	assert.Contains(t, text, "Build #3 · commit", "no link without a public URL")
}

func TestRefuseAddresses(t *testing.T) {
	for _, addr := range []string{"169.254.169.254:80", "[fe80::1]:443", "0.0.0.0:80", "[::]:80", "224.0.0.1:80",
		"[fd00:ec2::254]:80", "100.100.100.200:80", "[::ffff:169.254.169.254]:80", "not-an-ip:80"} {
		err := refuseAddresses("tcp", addr, nil)
		assert.ErrorIs(t, err, errRefusedAddress, addr)
	}
	for _, addr := range []string{"127.0.0.1:8080", "10.0.0.5:443", "[2001:db8::1]:443", "203.0.113.7:443"} {
		assert.NoError(t, refuseAddresses("tcp", addr, nil), addr)
	}
}

func TestMetadataServiceIsRefused(t *testing.T) {
	store := newMemStore()
	s := newService(t, store)
	ctx := t.Context()
	require.NoError(t, s.Put(ctx, models.Notification{Repo: repo, Name: "meta", Kind: models.NotifyWebhook},
		Target{URL: "http://169.254.169.254/latest/meta-data/"}))
	err := s.Test(ctx, repo, "meta")
	require.Error(t, err)
	assert.True(t, errors.Is(err, errRefusedAddress), err.Error())
}
