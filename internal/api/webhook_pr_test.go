package api

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/sidumandr/cisid/pkg/models"
)

// prTime is shortly after testdata/pull_request.json was last updated.
var prTime = time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)

const prHead = "b5e4d3c2a1f0e9d8c7b6a5f4e3d2c1b0a9f8e7d6"

// prPayload returns testdata/pull_request.json with the given action and
// head repository; an empty headRepo is a deleted fork.
func prPayload(t *testing.T, action, headRepo string) []byte {
	t.Helper()
	var p map[string]any
	require.NoError(t, json.Unmarshal(loadPayload(t, "pull_request.json"), &p))
	p["action"] = action
	head := p["pull_request"].(map[string]any)["head"].(map[string]any)
	if headRepo == "" {
		head["repo"] = nil
	} else {
		head["repo"].(map[string]any)["full_name"] = headRepo
	}
	body, err := json.Marshal(p)
	require.NoError(t, err)
	return body
}

func deliver(t *testing.T, h http.Handler, event string, body []byte) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/webhooks/github", bytes.NewReader(body))
	req.Header.Set("X-GitHub-Event", event)
	req.Header.Set("X-Hub-Signature-256", sign(testSecret, body))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func prHandler(t *testing.T, store JobCreator, opts ...WebhookOption) *WebhookHandler {
	t.Helper()
	h, err := NewWebhookHandler([]byte(testSecret), store, slog.New(slog.DiscardHandler), opts...)
	require.NoError(t, err)
	h.now = func() time.Time { return prTime }
	return h
}

func TestWebhookPullRequestQueuesTheHeadCommit(t *testing.T) {
	want := models.Job{
		Repo:         "sidumandr/cisid",
		CloneURL:     "https://github.com/sidumandr/cisid.git",
		Branch:       "feature/pr-builds",
		CommitSHA:    prHead,
		Status:       models.JobStatusPending,
		Event:        models.EventPullRequest,
		PullRequest:  7,
		TargetBranch: "main",
	}
	for _, action := range []string{"opened", "synchronize", "reopened"} {
		t.Run(action, func(t *testing.T) {
			store := &mockJobCreator{}
			store.On("QueueJob", mock.Anything, want, mock.Anything, mock.Anything).Return(int64(9), nil).Once()

			rec := deliver(t, prHandler(t, store), "pull_request", prPayload(t, action, "sidumandr/cisid"))

			assert.Equal(t, http.StatusAccepted, rec.Code, rec.Body.String())
			assert.JSONEq(t, `{"status":"queued","job_id":9}`, rec.Body.String())
			store.AssertExpectations(t)
		})
	}
}

func TestWebhookPullRequestFromFork(t *testing.T) {
	store := &mockJobCreator{}
	rec := deliver(t, prHandler(t, store), "pull_request", prPayload(t, "opened", "someone/cisid"))
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.JSONEq(t, `{"status":"ignored","reason":"pull request from a fork (CISID_BUILD_FORKS is off)"}`,
		rec.Body.String(), "forks are not built unless enabled")
	store.AssertNotCalled(t, "QueueJob", mock.Anything, mock.Anything, mock.Anything, mock.Anything)

	// A deleted fork has no head repository; it is still a fork.
	for _, headRepo := range []string{"someone/cisid", ""} {
		store := &mockJobCreator{}
		store.On("QueueJob", mock.Anything, mock.MatchedBy(func(j models.Job) bool {
			return j.Fork && j.Repo == "sidumandr/cisid" && j.CloneURL == "https://github.com/sidumandr/cisid.git"
		}), mock.Anything, mock.Anything).Return(int64(10), nil).Once()

		rec := deliver(t, prHandler(t, store, WithForkBuilds(true)), "pull_request", prPayload(t, "opened", headRepo))

		assert.Equal(t, http.StatusAccepted, rec.Code, "head repo %q", headRepo)
		store.AssertExpectations(t)
	}
}

func TestWebhookPullRequestIgnoredEvents(t *testing.T) {
	store := &mockJobCreator{}
	h := prHandler(t, store)

	rec := deliver(t, h, "pull_request", prPayload(t, "labeled", "sidumandr/cisid"))
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.JSONEq(t, `{"status":"ignored","reason":"pull request action \"labeled\" does not build"}`, rec.Body.String())

	h.now = func() time.Time { return prTime.Add(8 * 24 * time.Hour) }
	rec = deliver(t, h, "pull_request", prPayload(t, "synchronize", "sidumandr/cisid"))
	assert.JSONEq(t, `{"status":"ignored","reason":"stale delivery"}`, rec.Body.String())

	store.AssertNotCalled(t, "QueueJob", mock.Anything, mock.Anything, mock.Anything, mock.Anything)
}

func TestWebhookClosedPullRequestCancelsItsJobs(t *testing.T) {
	store := &mockJobCreator{}
	store.On("CancelPullRequest", mock.Anything, "sidumandr/cisid", 7).Return([]int64{3, 4}, nil).Once()

	rec := deliver(t, prHandler(t, store), "pull_request", prPayload(t, "closed", "sidumandr/cisid"))

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.JSONEq(t, `{"status":"canceled","reason":"pull request closed","canceled":[3,4]}`, rec.Body.String())
	store.AssertExpectations(t)
}

func TestWebhookRejectsBadPullRequests(t *testing.T) {
	badSHA := bytes.Replace(prPayload(t, "opened", "sidumandr/cisid"), []byte(prHead), []byte("not-a-sha"), 1)
	for name, body := range map[string][]byte{
		"malformed JSON": []byte(`{"action":`),
		"bad head SHA":   badSHA,
	} {
		t.Run(name, func(t *testing.T) {
			store := &mockJobCreator{}
			rec := deliver(t, prHandler(t, store), "pull_request", body)
			assert.Equal(t, http.StatusBadRequest, rec.Code)
			assert.Contains(t, rec.Body.String(), "invalid pull request payload")
			store.AssertNotCalled(t, "QueueJob", mock.Anything, mock.Anything, mock.Anything, mock.Anything)
		})
	}
}
