// Package gitlab reads projects and sets commit statuses through the
// GitLab REST API (v4), on GitLab.com or a self-managed instance.
package gitlab

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"github.com/sidumandr/cisid/internal/forge"
)

// DefaultURL is GitLab.com.
const DefaultURL = "https://gitlab.com"

// Client talks to a GitLab instance.
type Client struct {
	// URL is the instance's web address; DefaultURL when empty.
	URL string
	// Token is an optional personal, group or project access token, needed
	// for private projects and commit statuses (api scope).
	Token string
	// HTTP is the client used for requests; a client with a timeout when nil.
	HTTP *http.Client
}

var _ forge.Client = (*Client)(nil)

type commitStatus struct {
	State       string `json:"state"` // pending, running, success, failed or canceled
	Name        string `json:"name"`
	TargetURL   string `json:"target_url,omitempty"`
	Description string `json:"description,omitempty"`
}

var states = map[forge.State]string{
	forge.StatePending:  "pending",
	forge.StateRunning:  "running",
	forge.StateSuccess:  "success",
	forge.StateFailure:  "failed",
	forge.StateCanceled: "canceled",
	forge.StateError:    "failed",
}

// Repository describes the project at path ("group/subgroup/project").
func (c *Client) Repository(ctx context.Context, path string) (forge.Repository, error) {
	base, err := projectPath(path)
	if err != nil {
		return forge.Repository{}, err
	}
	var p struct {
		CloneURL      string `json:"http_url_to_repo"`
		DefaultBranch string `json:"default_branch"`
	}
	if err := c.api().GetJSON(ctx, base, &p); err != nil {
		return forge.Repository{}, fmt.Errorf("get project %s: %w", path, err)
	}
	if p.CloneURL == "" || p.DefaultBranch == "" {
		return forge.Repository{}, fmt.Errorf("project %s has no clone URL or default branch", path)
	}
	return forge.Repository{CloneURL: p.CloneURL, DefaultBranch: p.DefaultBranch}, nil
}

// BranchHead returns the SHA of the commit branch of the project points at.
func (c *Client) BranchHead(ctx context.Context, path, branch string) (string, error) {
	base, err := projectPath(path)
	if err != nil {
		return "", err
	}
	var b struct {
		Commit struct {
			ID string `json:"id"`
		} `json:"commit"`
	}
	if err := c.api().GetJSON(ctx, base+"/repository/branches/"+url.PathEscape(branch), &b); err != nil {
		return "", fmt.Errorf("get head of %s in %s: %w", branch, path, err)
	}
	if !shaRe.MatchString(b.Commit.ID) {
		return "", fmt.Errorf("head of %s in %s: unexpected commit %q", branch, path, b.Commit.ID)
	}
	return b.Commit.ID, nil
}

// FileContent returns the raw content of file in the project at ref.
func (c *Client) FileContent(ctx context.Context, path, ref, file string) ([]byte, error) {
	base, err := projectPath(path)
	if err != nil {
		return nil, err
	}
	// The file's path is one segment, slashes escaped.
	u := base + "/repository/files/" + url.PathEscape(file) + "/raw?" + url.Values{"ref": {ref}}.Encode()
	data, err := c.api().File(ctx, u, "")
	if err != nil {
		return nil, fmt.Errorf("get %s from %s@%s: %w", file, path, ref, err)
	}
	return data, nil
}

// CreateStatus sets a commit status on sha in the project.
func (c *Client) CreateStatus(ctx context.Context, path, sha string, st forge.CommitStatus) error {
	base, err := projectPath(path)
	if err != nil {
		return err
	}
	if c.Token == "" {
		return forge.ErrNoToken
	}
	body := commitStatus{State: states[st.State], Name: st.Context, TargetURL: st.TargetURL, Description: st.Description}
	if body.State == "" {
		body.State = "failed"
	}
	if err := c.api().Send(ctx, http.MethodPost, base+"/statuses/"+url.PathEscape(sha), body); err != nil {
		return fmt.Errorf("set status of %s@%s: %w", path, sha, err)
	}
	return nil
}

// CloneCredentials returns the token for git, which GitLab takes as the
// password of the oauth2 user.
func (c *Client) CloneCredentials() (user, token string) {
	if c.Token == "" {
		return "", ""
	}
	return "oauth2", c.Token
}

var shaRe = regexp.MustCompile(`^([0-9a-f]{40}|[0-9a-f]{64})$`)

// projectPath returns the API path of a project, which GitLab takes as
// one segment with its slashes escaped.
func projectPath(path string) (string, error) {
	if !forge.ValidPath(forge.GitLab, path) {
		return "", fmt.Errorf("%w: %q", forge.ErrInvalidRepo, path)
	}
	return "/projects/" + url.PathEscape(path), nil
}

func (c *Client) api() forge.API {
	base := c.URL
	if base == "" {
		base = DefaultURL
	}
	a := forge.API{Base: strings.TrimSuffix(base, "/") + "/api/v4", HTTP: c.HTTP}
	if c.Token != "" {
		a.Authorize = func(r *http.Request) { r.Header.Set("PRIVATE-TOKEN", c.Token) }
	}
	return a
}

var _ forge.Lister = (*Client)(nil)

// Repositories lists the hundred projects the token is a member of that
// were active last.
func (c *Client) Repositories(ctx context.Context) ([]forge.ListedPath, error) {
	if c.Token == "" {
		return nil, forge.ErrNoListing
	}
	var projects []struct {
		Path       string `json:"path_with_namespace"`
		Visibility string `json:"visibility"`
	}
	if err := c.api().GetJSON(ctx, "/projects?membership=true&simple=true&per_page=100&order_by=last_activity_at", &projects); err != nil {
		return nil, fmt.Errorf("list projects: %w", err)
	}
	out := make([]forge.ListedPath, 0, len(projects))
	for _, p := range projects {
		out = append(out, forge.ListedPath{Path: p.Path, Private: p.Visibility != "public"})
	}
	return out, nil
}
