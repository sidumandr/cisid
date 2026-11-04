// Package gitea reads repositories and sets commit statuses through the
// API of Gitea and Forgejo, which share it.
package gitea

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"github.com/sidumandr/cisid/internal/forge"
)

// Client talks to a Gitea or Forgejo server.
type Client struct {
	// URL is the server's web address, e.g. https://git.example.com.
	URL string
	// Token is an optional access token, needed for private repositories
	// and commit statuses.
	Token string
	// HTTP is the client used for requests; a client with a timeout when nil.
	HTTP *http.Client
	// Name is the forge's name in repository keys, forge.Gitea or
	// forge.Forgejo; forge.Gitea when empty.
	Name string
}

var _ forge.Client = (*Client)(nil)

type commitStatus struct {
	State       string `json:"state"` // pending, success, error, failure or warning
	TargetURL   string `json:"target_url,omitempty"`
	Description string `json:"description,omitempty"`
	Context     string `json:"context"`
}

var states = map[forge.State]string{
	forge.StatePending:  "pending",
	forge.StateRunning:  "pending",
	forge.StateSuccess:  "success",
	forge.StateFailure:  "failure",
	forge.StateCanceled: "error",
	forge.StateError:    "error",
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
	var b struct {
		Commit struct {
			ID string `json:"id"`
		} `json:"commit"`
	}
	if err := c.api().GetJSON(ctx, base+"/branches/"+url.PathEscape(branch), &b); err != nil {
		return "", fmt.Errorf("get head of %s in %s: %w", branch, repo, err)
	}
	if !shaRe.MatchString(b.Commit.ID) {
		return "", fmt.Errorf("head of %s in %s: unexpected commit %q", branch, repo, b.Commit.ID)
	}
	return b.Commit.ID, nil
}

// FileContent returns the raw content of path in repo at ref.
func (c *Client) FileContent(ctx context.Context, repo, ref, path string) ([]byte, error) {
	base, err := c.repoPath(repo)
	if err != nil {
		return nil, err
	}
	u := base + "/raw/" + forge.EscapePath(path) + "?" + url.Values{"ref": {ref}}.Encode()
	data, err := c.api().File(ctx, u, "")
	if err != nil {
		return nil, fmt.Errorf("get %s from %s@%s: %w", path, repo, ref, err)
	}
	return data, nil
}

// CreateStatus sets a commit status on sha in repo. The token needs write
// access to the repository.
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

// CloneCredentials returns the token for git; Gitea takes a token as the
// password of any user.
func (c *Client) CloneCredentials() (user, token string) {
	if c.Token == "" {
		return "", ""
	}
	return "oauth2", c.Token
}

var shaRe = regexp.MustCompile(`^([0-9a-f]{40}|[0-9a-f]{64})$`)

func (c *Client) repoPath(repo string) (string, error) {
	name := c.Name
	if name == "" {
		name = forge.Gitea
	}
	if !forge.ValidPath(name, repo) {
		return "", fmt.Errorf("%w: %q", forge.ErrInvalidRepo, repo)
	}
	return "/repos/" + repo, nil
}

func (c *Client) api() forge.API {
	a := forge.API{Base: strings.TrimSuffix(c.URL, "/") + "/api/v1", HTTP: c.HTTP}
	if c.Token != "" {
		a.Authorize = func(r *http.Request) { r.Header.Set("Authorization", "token "+c.Token) }
	}
	return a
}

var _ forge.Lister = (*Client)(nil)

// Repositories lists the repositories the token's user can reach, up to
// fifty, the most a Gitea page holds by default.
func (c *Client) Repositories(ctx context.Context) ([]forge.ListedPath, error) {
	if c.Token == "" {
		return nil, forge.ErrNoListing
	}
	var repos []struct {
		FullName string `json:"full_name"`
		Private  bool   `json:"private"`
	}
	if err := c.api().GetJSON(ctx, "/user/repos?limit=50", &repos); err != nil {
		return nil, fmt.Errorf("list repositories: %w", err)
	}
	out := make([]forge.ListedPath, 0, len(repos))
	for _, r := range repos {
		out = append(out, forge.ListedPath{Path: r.FullName, Private: r.Private})
	}
	return out, nil
}

var (
	_ forge.Tokened    = (*Client)(nil)
	_ forge.Identifier = (*Client)(nil)
)

// WithToken returns a copy of the client that uses token.
func (c *Client) WithToken(token string) forge.Client {
	cp := *c
	cp.Token = token
	return &cp
}

// Whoami returns the login of the token's user.
func (c *Client) Whoami(ctx context.Context) (string, error) {
	var u struct {
		Login string `json:"login"`
	}
	if err := c.api().GetJSON(ctx, "/user", &u); err != nil {
		return "", fmt.Errorf("ask whose the token is: %w", err)
	}
	return u.Login, nil
}
