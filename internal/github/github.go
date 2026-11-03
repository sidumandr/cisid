// Package github reads repositories and sets commit statuses through the
// GitHub REST API.
package github

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"github.com/sidumandr/cisid/internal/forge"
)

const (
	// DefaultAPIURL is the public GitHub REST API.
	DefaultAPIURL = "https://api.github.com"

	apiVersion = "2022-11-28"
)

// The errors of forge, under the names this package always had.
var (
	ErrNotFound    = forge.ErrNotFound
	ErrTemporary   = forge.ErrTemporary
	ErrTooLarge    = forge.ErrTooLarge
	ErrInvalidRepo = forge.ErrInvalidRepo
)

const maxFileBytes = forge.MaxFileBytes

// Client talks to the GitHub REST API. The zero value uses the public API
// without authentication (60 requests per hour per IP).
type Client struct {
	// APIURL is the API base URL; DefaultAPIURL when empty (set it for
	// GitHub Enterprise Server).
	APIURL string
	// Token is an optional personal access or installation token, needed
	// for private repositories, higher rate limits and commit statuses.
	Token string
	// HTTP is the client used for requests; a client with a timeout when nil.
	HTTP *http.Client
}

var _ forge.Client = (*Client)(nil)

// commitStatus is GitHub's form of a commit status.
type commitStatus struct {
	State       string `json:"state"` // error, failure, pending or success
	TargetURL   string `json:"target_url,omitempty"`
	Description string `json:"description,omitempty"`
	Context     string `json:"context"`
}

// states maps cisid's states to GitHub's, which has no running or canceled.
var states = map[forge.State]string{
	forge.StatePending:  "pending",
	forge.StateRunning:  "pending",
	forge.StateSuccess:  "success",
	forge.StateFailure:  "failure",
	forge.StateCanceled: "error",
	forge.StateError:    "error",
}

// CreateStatus sets a commit status on sha in repo. It needs a token with
// write access to commit statuses.
func (c *Client) CreateStatus(ctx context.Context, repo, sha string, st forge.CommitStatus) error {
	base, err := c.repoPath(repo)
	if err != nil {
		return err
	}
	if c.Token == "" {
		return forge.ErrNoToken
	}
	body := commitStatus{State: states[st.State], TargetURL: st.TargetURL, Description: st.Description, Context: st.Context}
	if body.State == "" {
		body.State = "error"
	}
	if err := c.api().Send(ctx, http.MethodPost, base+"/statuses/"+url.PathEscape(sha), body); err != nil {
		return fmt.Errorf("set status of %s@%s: %w", repo, sha, err)
	}
	return nil
}

// FileContent returns the raw content of path in repo ("owner/name") at ref.
func (c *Client) FileContent(ctx context.Context, repo, ref, path string) ([]byte, error) {
	base, err := c.repoPath(repo)
	if err != nil {
		return nil, err
	}
	u := base + "/contents/" + forge.EscapePath(path) + "?" + url.Values{"ref": {ref}}.Encode()
	data, err := c.api().File(ctx, u, "application/vnd.github.raw+json")
	if err != nil {
		return nil, fmt.Errorf("get %s from %s@%s: %w", path, repo, ref, err)
	}
	return data, nil
}

// Repository describes repo ("owner/name").
func (c *Client) Repository(ctx context.Context, repo string) (forge.Repository, error) {
	base, err := c.repoPath(repo)
	if err != nil {
		return forge.Repository{}, err
	}
	var r struct {
		CloneURL      string `json:"clone_url"`
		DefaultBranch string `json:"default_branch"`
	}
	if err := c.api().GetJSON(ctx, base, &r); err != nil {
		return forge.Repository{}, fmt.Errorf("get repository %s: %w", repo, err)
	}
	if r.CloneURL == "" || r.DefaultBranch == "" {
		return forge.Repository{}, fmt.Errorf("repository %s has no clone URL or default branch", repo)
	}
	return forge.Repository{CloneURL: r.CloneURL, DefaultBranch: r.DefaultBranch}, nil
}

// BranchHead returns the SHA of the commit branch of repo points at.
func (c *Client) BranchHead(ctx context.Context, repo, branch string) (string, error) {
	base, err := c.repoPath(repo)
	if err != nil {
		return "", err
	}
	resp, err := c.api().Do(ctx, http.MethodGet, base+"/commits/"+forge.EscapePath(branch), "application/vnd.github.sha", nil)
	if err != nil {
		return "", fmt.Errorf("get head of %s in %s: %w", branch, repo, err)
	}
	defer resp.Body.Close()
	if err := forge.CheckStatus(resp); err != nil {
		return "", fmt.Errorf("get head of %s in %s: %w", branch, repo, err)
	}

	data, err := io.ReadAll(io.LimitReader(resp.Body, 128))
	if err != nil {
		return "", fmt.Errorf("%w: read head of %s in %s: %w", ErrTemporary, branch, repo, err)
	}
	sha := strings.TrimSpace(string(data))
	if !shaRe.MatchString(sha) {
		return "", fmt.Errorf("head of %s in %s: unexpected answer %q", branch, repo, sha)
	}
	return sha, nil
}

// CloneCredentials returns the token for git, which GitHub takes as the
// password of the x-access-token user.
func (c *Client) CloneCredentials() (user, token string) {
	if c.Token == "" {
		return "", ""
	}
	return "x-access-token", c.Token
}

// shaRe matches SHA-1 and SHA-256 object IDs.
var shaRe = regexp.MustCompile(`^([0-9a-f]{40}|[0-9a-f]{64})$`)

func (c *Client) repoPath(repo string) (string, error) {
	if !forge.ValidPath(forge.GitHub, repo) {
		return "", fmt.Errorf("%w: %q", ErrInvalidRepo, repo)
	}
	return "/repos/" + repo, nil
}

func (c *Client) api() forge.API {
	base := c.APIURL
	if base == "" {
		base = DefaultAPIURL
	}
	a := forge.API{
		Base:   strings.TrimSuffix(base, "/"),
		HTTP:   c.HTTP,
		Header: http.Header{"X-Github-Api-Version": {apiVersion}},
	}
	if c.Token != "" {
		a.Authorize = func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+c.Token) }
	}
	return a
}
