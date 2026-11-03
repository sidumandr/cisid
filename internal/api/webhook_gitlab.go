package api

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/sidumandr/cisid/internal/forge"
	"github.com/sidumandr/cisid/pkg/models"
)

// gitlabSource reads GitLab deliveries. GitLab does not sign them; it
// sends the secret token itself in X-Gitlab-Token, so the webhook must be
// reached over HTTPS.
type gitlabSource struct{}

type gitlabProject struct {
	Path     string `json:"path_with_namespace"`
	CloneURL string `json:"git_http_url"`
}

type gitlabPush struct {
	Ref     string        `json:"ref"`
	After   string        `json:"after"`
	Project gitlabProject `json:"project"`
}

type gitlabMergeRequest struct {
	Project gitlabProject `json:"project"`
	Attrs   struct {
		IID             int    `json:"iid"`
		Action          string `json:"action"`
		SourceBranch    string `json:"source_branch"`
		TargetBranch    string `json:"target_branch"`
		SourceProjectID int64  `json:"source_project_id"`
		TargetProjectID int64  `json:"target_project_id"`
		// OldRev is set on an update that pushed new commits.
		OldRev     string `json:"oldrev"`
		LastCommit struct {
			ID string `json:"id"`
		} `json:"last_commit"`
	} `json:"object_attributes"`
}

func (gitlabSource) verify(secret []byte, header http.Header, _ []byte) bool {
	token := header.Get("X-Gitlab-Token")
	return token != "" && subtle.ConstantTimeCompare([]byte(token), secret) == 1
}

func (gitlabSource) deliveryID(header http.Header) string {
	return header.Get("X-Gitlab-Event-UUID")
}

func (s gitlabSource) parse(_ context.Context, header http.Header, body []byte, _ time.Time) (webhookAction, error) {
	switch event := header.Get("X-Gitlab-Event"); event {
	case "Push Hook":
		return s.push(body)
	case "Merge Request Hook":
		return s.mergeRequest(body)
	default:
		return ignore(fmt.Sprintf("unsupported event %q", event))
	}
}

func (s gitlabSource) push(body []byte) (webhookAction, error) {
	act := webhookAction{kind: "push"}
	var p gitlabPush
	if err := json.Unmarshal(body, &p); err != nil {
		return act, fmt.Errorf("decode push payload: %w", err)
	}
	branch, ok := strings.CutPrefix(p.Ref, "refs/heads/")
	if !ok || branch == "" {
		return ignore("not a branch push")
	}
	if zeroSHA(p.After) {
		return ignore("branch deleted")
	}
	if err := checkGitLabProject(p.Project, p.After); err != nil {
		return act, err
	}
	act.jobs = []models.Job{{
		Repo:      forge.Key(forge.GitLab, p.Project.Path),
		CloneURL:  p.Project.CloneURL,
		Branch:    branch,
		CommitSHA: p.After,
		Status:    models.JobStatusPending,
		Event:     models.EventPush,
	}}
	return act, nil
}

// mergeRequest builds a merge request when it is opened or reopened and
// when new commits are pushed to it, and cancels its jobs when it closes.
// The commit is fetched from the target project, which keeps every merge
// request's commits under refs/merge-requests.
func (s gitlabSource) mergeRequest(body []byte) (webhookAction, error) {
	act := webhookAction{kind: "pull request"}
	var p gitlabMergeRequest
	if err := json.Unmarshal(body, &p); err != nil {
		return act, fmt.Errorf("decode merge request payload: %w", err)
	}
	a := p.Attrs
	repo := forge.Key(forge.GitLab, p.Project.Path)
	switch {
	case a.Action == "close" || a.Action == "merge":
		act.closed = &closedPullRequest{repo: repo, number: a.IID}
		return act, nil
	case a.Action == "update" && a.OldRev == "":
		return ignore("merge request updated without new commits")
	case a.Action != "open" && a.Action != "reopen" && a.Action != "update":
		return ignore(fmt.Sprintf("merge request action %q does not build", a.Action))
	case a.IID <= 0:
		return act, fmt.Errorf("%w: merge request number %d", errInvalidPayload, a.IID)
	case a.SourceBranch == "" || a.TargetBranch == "":
		return act, fmt.Errorf("%w: source or target branch is empty", errInvalidPayload)
	}
	if err := checkGitLabProject(p.Project, a.LastCommit.ID); err != nil {
		return act, err
	}
	act.jobs = []models.Job{{
		Repo:         repo,
		CloneURL:     p.Project.CloneURL,
		Branch:       a.SourceBranch,
		CommitSHA:    a.LastCommit.ID,
		Status:       models.JobStatusPending,
		Event:        models.EventPullRequest,
		PullRequest:  a.IID,
		TargetBranch: a.TargetBranch,
		Fork:         a.SourceProjectID != a.TargetProjectID,
	}}
	return act, nil
}

func checkGitLabProject(p gitlabProject, sha string) error {
	switch {
	case !forge.ValidPath(forge.GitLab, p.Path):
		return fmt.Errorf("%w: project.path_with_namespace %q", errInvalidPayload, p.Path)
	case p.CloneURL == "":
		return fmt.Errorf("%w: project.git_http_url is empty", errInvalidPayload)
	case !validCommitSHA(sha):
		return fmt.Errorf("%w: commit SHA %q", errInvalidPayload, sha)
	}
	return nil
}
