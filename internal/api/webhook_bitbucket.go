package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/sidumandr/cisid/internal/bitbucket"
	"github.com/sidumandr/cisid/internal/forge"
	"github.com/sidumandr/cisid/pkg/models"
)

// bitbucketSource reads Bitbucket Cloud deliveries, signed like GitHub's
// but in X-Hub-Signature. Pull request events name the head commit by an
// abbreviated hash, which commits expands.
type bitbucketSource struct {
	commits CommitResolver
}

type bitbucketPush struct {
	Push struct {
		Changes []struct {
			// New is null when the branch was deleted.
			New *struct {
				Type   string `json:"type"` // branch, tag, ...
				Name   string `json:"name"`
				Target struct {
					Hash string `json:"hash"`
				} `json:"target"`
			} `json:"new"`
		} `json:"changes"`
	} `json:"push"`
	Repository struct {
		FullName string `json:"full_name"`
	} `json:"repository"`
}

type bitbucketPullRequest struct {
	PullRequest struct {
		ID        int       `json:"id"`
		UpdatedOn time.Time `json:"updated_on"`
		Source    struct {
			Branch struct {
				Name string `json:"name"`
			} `json:"branch"`
			Commit struct {
				Hash string `json:"hash"`
			} `json:"commit"`
			Repository struct {
				FullName string `json:"full_name"`
			} `json:"repository"`
		} `json:"source"`
		Destination struct {
			Branch struct {
				Name string `json:"name"`
			} `json:"branch"`
		} `json:"destination"`
	} `json:"pullrequest"`
	Repository struct {
		FullName string `json:"full_name"`
	} `json:"repository"`
}

func (bitbucketSource) verify(secret []byte, header http.Header, body []byte) bool {
	return validSignature(secret, body, header.Get("X-Hub-Signature"))
}

func (bitbucketSource) deliveryID(header http.Header) string {
	return header.Get("X-Request-UUID")
}

func (s bitbucketSource) parse(ctx context.Context, header http.Header, body []byte, now time.Time) (webhookAction, error) {
	switch event := header.Get("X-Event-Key"); event {
	case "diagnostics:ping":
		return webhookAction{pong: true}, nil
	case "repo:push":
		return s.push(body)
	case "pullrequest:created", "pullrequest:updated":
		return s.pullRequest(ctx, body, now)
	case "pullrequest:fulfilled", "pullrequest:rejected":
		act := webhookAction{kind: "pull request"}
		var p bitbucketPullRequest
		if err := json.Unmarshal(body, &p); err != nil {
			return act, fmt.Errorf("decode pull request payload: %w", err)
		}
		act.closed = &closedPullRequest{repo: forge.Key(forge.Bitbucket, p.Repository.FullName), number: p.PullRequest.ID}
		return act, nil
	default:
		return ignore(fmt.Sprintf("unsupported event %q", event))
	}
}

// push queues a job for every branch the push updated.
func (s bitbucketSource) push(body []byte) (webhookAction, error) {
	act := webhookAction{kind: "push"}
	var p bitbucketPush
	if err := json.Unmarshal(body, &p); err != nil {
		return act, fmt.Errorf("decode push payload: %w", err)
	}
	repo := p.Repository.FullName
	if !forge.ValidPath(forge.Bitbucket, repo) {
		return act, fmt.Errorf("%w: repository.full_name %q", errInvalidPayload, repo)
	}
	for _, c := range p.Push.Changes {
		if c.New == nil || c.New.Type != "branch" || c.New.Name == "" {
			continue
		}
		if !validCommitSHA(c.New.Target.Hash) {
			return act, fmt.Errorf("%w: commit SHA %q", errInvalidPayload, c.New.Target.Hash)
		}
		act.jobs = append(act.jobs, models.Job{
			Repo:      forge.Key(forge.Bitbucket, repo),
			CloneURL:  bitbucket.CloneURL(repo),
			Branch:    c.New.Name,
			CommitSHA: c.New.Target.Hash,
			Status:    models.JobStatusPending,
			Event:     models.EventPush,
		})
	}
	if len(act.jobs) == 0 {
		return ignore("no branch updated")
	}
	return act, nil
}

// pullRequest builds a pull request's head commit. Bitbucket keeps the
// commits of a fork's pull request in the fork only, so those are not
// built.
func (s bitbucketSource) pullRequest(ctx context.Context, body []byte, now time.Time) (webhookAction, error) {
	act := webhookAction{kind: "pull request"}
	var p bitbucketPullRequest
	if err := json.Unmarshal(body, &p); err != nil {
		return act, fmt.Errorf("decode pull request payload: %w", err)
	}
	pr, repo := p.PullRequest, p.Repository.FullName
	switch {
	case stale(pr.UpdatedOn, now):
		return ignore("stale delivery")
	case !forge.ValidPath(forge.Bitbucket, repo):
		return act, fmt.Errorf("%w: repository.full_name %q", errInvalidPayload, repo)
	case pr.ID <= 0:
		return act, fmt.Errorf("%w: pull request number %d", errInvalidPayload, pr.ID)
	case pr.Source.Branch.Name == "" || pr.Destination.Branch.Name == "":
		return act, fmt.Errorf("%w: source or destination branch is empty", errInvalidPayload)
	case pr.Source.Commit.Hash == "":
		return act, fmt.Errorf("%w: source commit is empty", errInvalidPayload)
	case pr.Source.Repository.FullName != repo:
		return ignore("pull requests from forks are not built on Bitbucket")
	}

	sha, err := s.commits.Commit(ctx, repo, pr.Source.Commit.Hash)
	if err != nil {
		return act, fmt.Errorf("expand commit %s: %w", pr.Source.Commit.Hash, err)
	}
	act.jobs = []models.Job{{
		Repo:         forge.Key(forge.Bitbucket, repo),
		CloneURL:     bitbucket.CloneURL(repo),
		Branch:       pr.Source.Branch.Name,
		CommitSHA:    sha,
		Status:       models.JobStatusPending,
		Event:        models.EventPullRequest,
		PullRequest:  pr.ID,
		TargetBranch: pr.Destination.Branch.Name,
	}}
	return act, nil
}
