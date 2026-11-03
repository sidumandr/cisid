package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/sidumandr/cisid/pkg/models"
)

// githubSource reads GitHub deliveries, signed in X-Hub-Signature-256.
type githubSource struct{}

// pullRequestActions are the pull request events that queue a job: a pull
// request opened or reopened, and new commits pushed to it.
var pullRequestActions = map[string]bool{"opened": true, "reopened": true, "synchronize": true}

// pushPayload holds the fields of a GitHub push event that cisid uses.
type pushPayload struct {
	Ref        string `json:"ref"`
	After      string `json:"after"`
	Deleted    bool   `json:"deleted"`
	Repository struct {
		FullName string `json:"full_name"`
		CloneURL string `json:"clone_url"`
		// PushedAt is a Unix time in push events (a string elsewhere).
		PushedAt json.RawMessage `json:"pushed_at"`
	} `json:"repository"`
}

// pullRequestPayload holds the fields of a GitHub pull_request event that
// cisid uses.
type pullRequestPayload struct {
	Action      string `json:"action"`
	Number      int    `json:"number"`
	PullRequest struct {
		UpdatedAt time.Time `json:"updated_at"`
		Head      struct {
			Ref string `json:"ref"`
			SHA string `json:"sha"`
			// Repo is null when the fork has been deleted.
			Repo *struct {
				FullName string `json:"full_name"`
			} `json:"repo"`
		} `json:"head"`
		Base struct {
			Ref string `json:"ref"`
		} `json:"base"`
	} `json:"pull_request"`
	Repository struct {
		FullName string `json:"full_name"`
		CloneURL string `json:"clone_url"`
	} `json:"repository"`
}

func (githubSource) verify(secret []byte, header http.Header, body []byte) bool {
	return validSignature(secret, body, header.Get("X-Hub-Signature-256"))
}

func (githubSource) deliveryID(header http.Header) string {
	return header.Get("X-GitHub-Delivery")
}

func (githubSource) parse(_ context.Context, header http.Header, body []byte, now time.Time) (webhookAction, error) {
	switch event := header.Get("X-GitHub-Event"); event {
	case "ping":
		return webhookAction{pong: true}, nil
	case "push":
		return jobFromPush(body, now)
	case "pull_request":
		return fromPullRequest(body, now)
	default:
		return ignore(fmt.Sprintf("unsupported event %q", event))
	}
}

// jobFromPush builds a pending job from a push payload, unless the push
// is valid but triggers nothing.
func jobFromPush(body []byte, now time.Time) (webhookAction, error) {
	act := webhookAction{kind: "push"}
	var p pushPayload
	if err := json.Unmarshal(body, &p); err != nil {
		return act, fmt.Errorf("decode push payload: %w", err)
	}

	branch, ok := strings.CutPrefix(p.Ref, "refs/heads/")
	if !ok || branch == "" {
		return ignore("not a branch push")
	}
	if p.Deleted {
		return ignore("branch deleted")
	}
	if stale(unixTime(p.Repository.PushedAt), now) {
		return ignore("stale delivery")
	}

	switch {
	case p.Repository.FullName == "":
		return act, fmt.Errorf("%w: repository.full_name is empty", errInvalidPayload)
	case p.Repository.CloneURL == "":
		return act, fmt.Errorf("%w: repository.clone_url is empty", errInvalidPayload)
	case !validCommitSHA(p.After):
		return act, fmt.Errorf("%w: commit SHA %q", errInvalidPayload, p.After)
	}

	act.jobs = []models.Job{{
		Repo:      p.Repository.FullName,
		CloneURL:  p.Repository.CloneURL,
		Branch:    branch,
		CommitSHA: p.After,
		Status:    models.JobStatusPending,
		Event:     models.EventPush,
	}}
	return act, nil
}

// fromPullRequest builds a pending job for the head commit of a pull
// request, or cancels the jobs of a closed one. The commit is fetched from
// the base repository, which serves every pull request's commits, so a
// fork's own URL is never used.
func fromPullRequest(body []byte, now time.Time) (webhookAction, error) {
	act := webhookAction{kind: "pull request"}
	var p pullRequestPayload
	if err := json.Unmarshal(body, &p); err != nil {
		return act, fmt.Errorf("decode pull request payload: %w", err)
	}
	if p.Action == "closed" {
		act.closed = &closedPullRequest{repo: p.Repository.FullName, number: p.Number}
		return act, nil
	}
	if !pullRequestActions[p.Action] {
		return ignore(fmt.Sprintf("pull request action %q does not build", p.Action))
	}
	if stale(p.PullRequest.UpdatedAt, now) {
		return ignore("stale delivery")
	}

	head, base := p.PullRequest.Head, p.PullRequest.Base
	switch {
	case p.Number <= 0:
		return act, fmt.Errorf("%w: pull request number %d", errInvalidPayload, p.Number)
	case p.Repository.FullName == "":
		return act, fmt.Errorf("%w: repository.full_name is empty", errInvalidPayload)
	case p.Repository.CloneURL == "":
		return act, fmt.Errorf("%w: repository.clone_url is empty", errInvalidPayload)
	case head.Ref == "" || base.Ref == "":
		return act, fmt.Errorf("%w: head or base branch is empty", errInvalidPayload)
	case !validCommitSHA(head.SHA):
		return act, fmt.Errorf("%w: commit SHA %q", errInvalidPayload, head.SHA)
	}

	act.jobs = []models.Job{{
		Repo:         p.Repository.FullName,
		CloneURL:     p.Repository.CloneURL,
		Branch:       head.Ref,
		CommitSHA:    head.SHA,
		Status:       models.JobStatusPending,
		Event:        models.EventPullRequest,
		PullRequest:  p.Number,
		TargetBranch: base.Ref,
		Fork:         head.Repo == nil || head.Repo.FullName != p.Repository.FullName,
	}}
	return act, nil
}
