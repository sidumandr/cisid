package api

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/sidumandr/cisid/internal/forge"
	"github.com/sidumandr/cisid/pkg/models"
)

const (
	forgeSHA = "6113728f27ae82c7b1a177c8d03f9e96e0adf246"
	otherSHA = "0d1a26e67d8f5eaf1f6ba5c57fc3c7d91ac0fd1c"
)

func forgeHandler(t *testing.T, name string, store JobCreator, opts ...WebhookOption) *WebhookHandler {
	t.Helper()
	opts = append([]WebhookOption{WithForge(name), WithCommitResolver(stubCommits{})}, opts...)
	h, err := NewWebhookHandler([]byte(testSecret), store, slog.New(slog.DiscardHandler), opts...)
	require.NoError(t, err)
	return h
}

func deliverTo(h http.Handler, header map[string]string, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/webhooks/x", bytes.NewReader([]byte(body)))
	for k, v := range header {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func hexHMAC(body string) string {
	mac := hmac.New(sha256.New, []byte(testSecret))
	mac.Write([]byte(body))
	return hex.EncodeToString(mac.Sum(nil))
}

// queued expects one job like want and answers with id.
func queued(store *mockJobCreator, want models.Job, id int64) {
	store.On("QueueJob", mock.Anything, mock.MatchedBy(func(j models.Job) bool {
		j.TraceParent = ""
		return assert.ObjectsAreEqual(want, j)
	}), mock.Anything, mock.Anything).Return(id, nil).Once()
}

type stubCommits struct{}

func (stubCommits) Commit(_ context.Context, repo, rev string) (string, error) {
	if rev == "6113728f27ae" {
		return forgeSHA, nil
	}
	return "", fmt.Errorf("commit %s in %s: %w", rev, repo, forge.ErrTemporary)
}

func TestNewWebhookHandlerForges(t *testing.T) {
	_, err := NewWebhookHandler([]byte(testSecret), &mockJobCreator{}, nil, WithForge("svn"))
	assert.ErrorIs(t, err, ErrUnknownWebhookForge)
	_, err = NewWebhookHandler([]byte(testSecret), &mockJobCreator{}, nil, WithForge(forge.Bitbucket))
	assert.ErrorIs(t, err, ErrUnknownWebhookForge, "Bitbucket needs a commit resolver")
}

func giteaPushBody(after string) string {
	return `{"ref":"refs/heads/main","after":"` + after + `","repository":{"full_name":"office/app","clone_url":"https://git.example.com/office/app.git"}}`
}

func TestGiteaWebhook(t *testing.T) {
	for _, name := range []string{forge.Gitea, forge.Forgejo} {
		t.Run(name, func(t *testing.T) {
			store := &mockJobCreator{}
			h := forgeHandler(t, name, store)
			body := giteaPushBody(forgeSHA)

			queued(store, models.Job{
				Repo: name + ":office/app", CloneURL: "https://git.example.com/office/app.git",
				Branch: "main", CommitSHA: forgeSHA, Status: models.JobStatusPending, Event: models.EventPush,
			}, 1)
			rec := deliverTo(h, map[string]string{"X-Gitea-Event": "push", "X-Gitea-Signature": hexHMAC(body)}, body)
			assert.Equal(t, http.StatusAccepted, rec.Code, rec.Body.String())

			rec = deliverTo(h, map[string]string{"X-Gitea-Event": "push", "X-Gitea-Signature": hexHMAC(body + " ")}, body)
			assert.Equal(t, http.StatusUnauthorized, rec.Code)

			deleted := giteaPushBody("0000000000000000000000000000000000000000")
			rec = deliverTo(h, map[string]string{"X-Forgejo-Event": "push", "X-Forgejo-Signature": hexHMAC(deleted)}, deleted)
			assert.JSONEq(t, `{"status":"ignored","reason":"branch deleted"}`, rec.Body.String(), "Forgejo's own headers work too")
			store.AssertExpectations(t)
		})
	}
}

func TestGiteaPullRequest(t *testing.T) {
	pr := func(action, headRepo string) string {
		return `{"action":"` + action + `","number":5,"pull_request":{"head":{"ref":"fix","sha":"` + forgeSHA +
			`","repo":{"full_name":"` + headRepo + `"}},"base":{"ref":"main"}},` +
			`"repository":{"full_name":"office/app","clone_url":"https://git.example.com/office/app.git"}}`
	}
	store := &mockJobCreator{}
	h := forgeHandler(t, forge.Gitea, store)
	send := func(body string) *httptest.ResponseRecorder {
		return deliverTo(h, map[string]string{"X-Gitea-Event": "pull_request", "X-Gitea-Signature": hexHMAC(body)}, body)
	}

	queued(store, models.Job{
		Repo: "gitea:office/app", CloneURL: "https://git.example.com/office/app.git", Branch: "fix", CommitSHA: forgeSHA,
		Status: models.JobStatusPending, Event: models.EventPullRequest, PullRequest: 5, TargetBranch: "main",
	}, 2)
	assert.Equal(t, http.StatusAccepted, send(pr("synchronized", "office/app")).Code)

	rec := send(pr("opened", "someone/app"))
	assert.JSONEq(t, `{"status":"ignored","reason":"`+forkReason+`"}`, rec.Body.String())

	store.On("CancelPullRequest", mock.Anything, "gitea:office/app", 5).Return([]int64{2}, nil).Once()
	rec = send(pr("closed", "office/app"))
	assert.JSONEq(t, `{"status":"canceled","reason":"pull request closed","canceled":[2]}`, rec.Body.String())

	rec = send(pr("labeled", "office/app"))
	assert.Equal(t, http.StatusOK, rec.Code)
	store.AssertExpectations(t)
}

func gitlabPushBody(path, after string) string {
	return `{"object_kind":"push","ref":"refs/heads/main","after":"` + after + `","project":{"path_with_namespace":"` + path +
		`","git_http_url":"https://gitlab.com/` + path + `.git"}}`
}

func TestGitLabWebhook(t *testing.T) {
	store := &mockJobCreator{}
	h := forgeHandler(t, forge.GitLab, store)
	body := gitlabPushBody("group/sub/project", forgeSHA)

	queued(store, models.Job{
		Repo: "gitlab:group/sub/project", CloneURL: "https://gitlab.com/group/sub/project.git",
		Branch: "main", CommitSHA: forgeSHA, Status: models.JobStatusPending, Event: models.EventPush,
	}, 1)
	rec := deliverTo(h, map[string]string{"X-Gitlab-Event": "Push Hook", "X-Gitlab-Token": testSecret}, body)
	assert.Equal(t, http.StatusAccepted, rec.Code, rec.Body.String())

	rec = deliverTo(h, map[string]string{"X-Gitlab-Event": "Push Hook", "X-Gitlab-Token": "wrong"}, body)
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
	rec = deliverTo(h, map[string]string{"X-Gitlab-Event": "Push Hook"}, body)
	assert.Equal(t, http.StatusUnauthorized, rec.Code)

	deleted := gitlabPushBody("group/project", "0000000000000000000000000000000000000000")
	rec = deliverTo(h, map[string]string{"X-Gitlab-Event": "Push Hook", "X-Gitlab-Token": testSecret}, deleted)
	assert.JSONEq(t, `{"status":"ignored","reason":"branch deleted"}`, rec.Body.String())

	rec = deliverTo(h, map[string]string{"X-Gitlab-Event": "Tag Push Hook", "X-Gitlab-Token": testSecret}, body)
	assert.Equal(t, http.StatusOK, rec.Code)
	store.AssertExpectations(t)
}

func TestGitLabMergeRequest(t *testing.T) {
	mr := func(action, oldrev string, source int) string {
		return `{"object_kind":"merge_request","project":{"path_with_namespace":"group/project","git_http_url":"https://gitlab.com/group/project.git"},` +
			`"object_attributes":{"iid":9,"action":"` + action + `","source_branch":"fix","target_branch":"main",` +
			fmt.Sprintf(`"source_project_id":%d,"target_project_id":1,"oldrev":"%s",`, source, oldrev) +
			`"last_commit":{"id":"` + forgeSHA + `"}}}`
	}
	store := &mockJobCreator{}
	h := forgeHandler(t, forge.GitLab, store, WithForkBuilds(true))
	send := func(body string) *httptest.ResponseRecorder {
		return deliverTo(h, map[string]string{"X-Gitlab-Event": "Merge Request Hook", "X-Gitlab-Token": testSecret}, body)
	}
	want := models.Job{
		Repo: "gitlab:group/project", CloneURL: "https://gitlab.com/group/project.git", Branch: "fix", CommitSHA: forgeSHA,
		Status: models.JobStatusPending, Event: models.EventPullRequest, PullRequest: 9, TargetBranch: "main",
	}

	queued(store, want, 1)
	assert.Equal(t, http.StatusAccepted, send(mr("open", "", 1)).Code)

	queued(store, want, 2)
	assert.Equal(t, http.StatusAccepted, send(mr("update", otherSHA, 1)).Code, "an update with new commits builds")

	rec := send(mr("update", "", 1))
	assert.JSONEq(t, `{"status":"ignored","reason":"merge request updated without new commits"}`, rec.Body.String())

	fork := want
	fork.Fork = true
	queued(store, fork, 3)
	assert.Equal(t, http.StatusAccepted, send(mr("reopen", "", 2)).Code, "a fork's merge request builds when forks are on")

	store.On("CancelPullRequest", mock.Anything, "gitlab:group/project", 9).Return([]int64(nil), nil).Twice()
	assert.Equal(t, http.StatusOK, send(mr("merge", "", 1)).Code)
	assert.Equal(t, http.StatusOK, send(mr("close", "", 1)).Code)
	store.AssertExpectations(t)
}

func TestBitbucketPushQueuesEveryBranch(t *testing.T) {
	body := `{"push":{"changes":[` +
		`{"new":{"type":"branch","name":"main","target":{"hash":"` + forgeSHA + `"}}},` +
		`{"new":{"type":"tag","name":"v1","target":{"hash":"` + forgeSHA + `"}}},` +
		`{"new":null},` +
		`{"new":{"type":"branch","name":"dev","target":{"hash":"` + otherSHA + `"}}}` +
		`]},"repository":{"full_name":"team/app"}}`
	sum := sha256.Sum256([]byte(body))
	key := hex.EncodeToString(sum[:])

	store := &mockJobCreator{}
	store.On("QueueJob", mock.Anything, mock.MatchedBy(func(j models.Job) bool { return j.Branch == "main" }), key, "d-1").
		Return(int64(1), nil).Once()
	store.On("QueueJob", mock.Anything, mock.MatchedBy(func(j models.Job) bool {
		return j.Branch == "dev" && j.CommitSHA == otherSHA && j.Repo == "bitbucket:team/app" &&
			j.CloneURL == "https://bitbucket.org/team/app.git"
	}), key+"/1", "d-1").Return(int64(2), nil).Once()
	h := forgeHandler(t, forge.Bitbucket, store)

	rec := deliverTo(h, map[string]string{"X-Event-Key": "repo:push", "X-Hub-Signature": "sha256=" + hexHMAC(body), "X-Request-UUID": "d-1"}, body)

	assert.Equal(t, http.StatusAccepted, rec.Code)
	assert.JSONEq(t, `{"status":"queued","job_id":1,"job_ids":[1,2]}`, rec.Body.String())
	store.AssertExpectations(t)
}

func TestBitbucketPullRequest(t *testing.T) {
	pr := func(hash, source string) string {
		return `{"pullrequest":{"id":4,"source":{"branch":{"name":"fix"},"commit":{"hash":"` + hash + `"},"repository":{"full_name":"` + source + `"}},` +
			`"destination":{"branch":{"name":"main"}}},"repository":{"full_name":"team/app"}}`
	}
	store := &mockJobCreator{}
	h := forgeHandler(t, forge.Bitbucket, store, WithForkBuilds(true))
	send := func(event, body string) *httptest.ResponseRecorder {
		return deliverTo(h, map[string]string{"X-Event-Key": event, "X-Hub-Signature": "sha256=" + hexHMAC(body)}, body)
	}

	queued(store, models.Job{
		Repo: "bitbucket:team/app", CloneURL: "https://bitbucket.org/team/app.git", Branch: "fix", CommitSHA: forgeSHA,
		Status: models.JobStatusPending, Event: models.EventPullRequest, PullRequest: 4, TargetBranch: "main",
	}, 1)
	rec := send("pullrequest:created", pr("6113728f27ae", "team/app"))
	assert.Equal(t, http.StatusAccepted, rec.Code, "the abbreviated hash is expanded")

	rec = send("pullrequest:updated", pr("0d1a26e67d8f", "team/app"))
	assert.Equal(t, http.StatusServiceUnavailable, rec.Code, "Bitbucket being down asks for a redelivery")

	rec = send("pullrequest:created", pr("6113728f27ae", "someone/app"))
	assert.JSONEq(t, `{"status":"ignored","reason":"pull requests from forks are not built on Bitbucket"}`, rec.Body.String())

	store.On("CancelPullRequest", mock.Anything, "bitbucket:team/app", 4).Return([]int64{1}, nil).Once()
	rec = send("pullrequest:rejected", pr("6113728f27ae", "team/app"))
	assert.Equal(t, http.StatusOK, rec.Code)

	rec = send("diagnostics:ping", "{}")
	assert.JSONEq(t, `{"status":"pong"}`, rec.Body.String())

	rec = deliverTo(h, map[string]string{"X-Event-Key": "repo:push"}, pr("x", "team/app"))
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
	store.AssertExpectations(t)
}

func TestWebhooksRejectBadPayloads(t *testing.T) {
	tests := []struct {
		forge  string
		header map[string]string
		body   string
	}{
		{forge.Gitea, map[string]string{"X-Gitea-Event": "push"}, `{"ref":"refs/heads/main","after":"xyz","repository":{"full_name":"office/app","clone_url":"u"}}`},
		{forge.Gitea, map[string]string{"X-Gitea-Event": "push"}, `{"ref":"refs/heads/main","after":"` + forgeSHA + `","repository":{"full_name":"a/b/c","clone_url":"u"}}`},
		{forge.GitLab, map[string]string{"X-Gitlab-Event": "Push Hook"}, `{"ref":"refs/heads/main","after":"` + forgeSHA + `","project":{"path_with_namespace":"project","git_http_url":"u"}}`},
		{forge.Bitbucket, map[string]string{"X-Event-Key": "repo:push"}, `{"push":{"changes":[]},"repository":{"full_name":"../x"}}`},
		{forge.Gitea, map[string]string{"X-Gitea-Event": "push"}, `not json`},
	}
	for _, tt := range tests {
		store := &mockJobCreator{}
		h := forgeHandler(t, tt.forge, store)
		tt.header["X-Gitea-Signature"] = hexHMAC(tt.body)
		tt.header["X-Gitlab-Token"] = testSecret
		tt.header["X-Hub-Signature"] = "sha256=" + hexHMAC(tt.body)
		rec := deliverTo(h, tt.header, tt.body)
		assert.Equal(t, http.StatusBadRequest, rec.Code, "%s: %s", tt.forge, tt.body)
		store.AssertNotCalled(t, "QueueJob", mock.Anything, mock.Anything, mock.Anything, mock.Anything)
	}
}
