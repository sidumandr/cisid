package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/sidumandr/cisid/internal/forge"
	"github.com/sidumandr/cisid/pkg/models"
)

// giteaSource reads Gitea and Forgejo deliveries, which carry the
// HMAC-SHA256 of the body in hex. Forgejo sends its own headers and
// Gitea's.
type giteaSource struct {
	name string // forge.Gitea or forge.Forgejo
}

// giteaPullRequestActions build; Gitea says "synchronized" for new commits.
var giteaPullRequestActions = map[string]bool{"opened": true, "reopened": true, "synchronized": true, "synchronize": true}

type giteaRepository struct {
	FullName string `json:"full_name"`
	CloneURL string `json:"clone_url"`
}

type giteaPush struct {
	Ref        string          `json:"ref"`
	After      string          `json:"after"`
	Repository giteaRepository `json:"repository"`
}

type giteaPullRequest struct {
	Action      string `json:"action"`
	Number      int    `json:"number"`
	PullRequest struct {
		UpdatedAt time.Time `json:"updated_at"`
		Head      struct {
			Ref  string           `json:"ref"`
			SHA  string           `json:"sha"`
			Repo *giteaRepository `json:"repo"`
		} `json:"head"`
		Base struct {
			Ref string `json:"ref"`
		} `json:"base"`
	} `json:"pull_request"`
	Repository giteaRepository `json:"repository"`
}

// header returns Forgejo's header, or Gitea's, which Forgejo sends too.
func giteaHeader(h http.Header, suffix string) string {
	if v := h.Get("X-Forgejo-" + suffix); v != "" {
		return v
	}
	return h.Get("X-Gitea-" + suffix)
}

func (giteaSource) verify(secret []byte, header http.Header, body []byte) bool {
	sig := giteaHeader(header, "Signature")
	return sig != "" && validHMAC(secret, body, sig)
}

func (giteaSource) deliveryID(header http.Header) string {
	return giteaHeader(header, "Delivery")
}

func (s giteaSource) parse(_ context.Context, header http.Header, body []byte, now time.Time) (webhookAction, error) {
	switch event := giteaHeader(header, "Event"); event {
	case "push":
		return s.push(body)
	case "pull_request":
		return s.pullRequest(body, now)
	default:
		return ignore(fmt.Sprintf("unsupported event %q", event))
	}
}

func (s giteaSource) push(body []byte) (webhookAction, error) {
	act := webhookAction{kind: "push"}
	var p giteaPush
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
	if err := s.checkRepo(p.Repository, p.After); err != nil {
		return act, err
	}
	act.jobs = []models.Job{{
		Repo:      forge.Key(s.name, p.Repository.FullName),
		CloneURL:  p.Repository.CloneURL,
		Branch:    branch,
		CommitSHA: p.After,
		Status:    models.JobStatusPending,
		Event:     models.EventPush,
	}}
	return act, nil
}

func (s giteaSource) pullRequest(body []byte, now time.Time) (webhookAction, error) {
	act := webhookAction{kind: "pull request"}
	var p giteaPullRequest
	if err := json.Unmarshal(body, &p); err != nil {
		return act, fmt.Errorf("decode pull request payload: %w", err)
	}
	repo := forge.Key(s.name, p.Repository.FullName)
	if p.Action == "closed" {
		act.closed = &closedPullRequest{repo: repo, number: p.Number}
		return act, nil
	}
	if !giteaPullRequestActions[p.Action] {
		return ignore(fmt.Sprintf("pull request action %q does not build", p.Action))
	}
	if stale(p.PullRequest.UpdatedAt, now) {
		return ignore("stale delivery")
	}

	head, base := p.PullRequest.Head, p.PullRequest.Base
	switch {
	case p.Number <= 0:
		return act, fmt.Errorf("%w: pull request number %d", errInvalidPayload, p.Number)
	case head.Ref == "" || base.Ref == "":
		return act, fmt.Errorf("%w: head or base branch is empty", errInvalidPayload)
	}
	if err := s.checkRepo(p.Repository, head.SHA); err != nil {
		return act, err
	}
	act.jobs = []models.Job{{
		Repo:         repo,
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

func (s giteaSource) checkRepo(r giteaRepository, sha string) error {
	switch {
	case !forge.ValidPath(s.name, r.FullName):
		return fmt.Errorf("%w: repository.full_name %q", errInvalidPayload, r.FullName)
	case r.CloneURL == "":
		return fmt.Errorf("%w: repository.clone_url is empty", errInvalidPayload)
	case !validCommitSHA(sha):
		return fmt.Errorf("%w: commit SHA %q", errInvalidPayload, sha)
	}
	return nil
}
