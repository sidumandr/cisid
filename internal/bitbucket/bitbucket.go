// Package bitbucket reads repositories and sets commit statuses through the
// Bitbucket Cloud REST API (2.0).
package bitbucket

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"github.com/sidumandr/cisid/internal/forge"
)

const (
	// DefaultAPIURL is the Bitbucket Cloud API.
	DefaultAPIURL = "https://api.bitbucket.org/2.0"
	// WebURL is where Bitbucket Cloud serves repositories.
	WebURL = "https://bitbucket.org"

	// maxKeyLen is Bitbucket's limit on a build status key.
	maxKeyLen = 40
)

// Client talks to Bitbucket Cloud.
type Client struct {
	// APIURL is the API base URL; DefaultAPIURL when empty.
	APIURL string
	// Token is an optional repository, project or workspace access token,
	// needed for private repositories and build statuses.
	Token string
	// HTTP is the client used for requests; a client with a timeout when nil.
	HTTP *http.Client
}

var _ forge.Client = (*Client)(nil)

type buildStatus struct {
	Key         string `json:"key"`
	State       string `json:"state"` // INPROGRESS, SUCCESSFUL, FAILED or STOPPED
	Name        string `json:"name"`
	URL         string `json:"url"`
	Description string `json:"description,omitempty"`
}

var states = map[forge.State]string{
	forge.StatePending:  "INPROGRESS",
	forge.StateRunning:  "INPROGRESS",
	forge.StateSuccess:  "SUCCESSFUL",
	forge.StateFailure:  "FAILED",
	forge.StateCanceled: "STOPPED",
	forge.StateError:    "FAILED",
}

// Repository describes repo ("workspace/slug").
func (c *Client) Repository(ctx context.Context, repo string) (forge.Repository, error) {
	base, err := repoPath(repo)
	if err != nil {
		return forge.Repository{}, err
	}
	var r struct {
		MainBranch struct {
			Name string `json:"name"`
		} `json:"mainbranch"`
	}
	if err := c.api().GetJSON(ctx, base, &r); err != nil {
		return forge.Repository{}, fmt.Errorf("get repository %s: %w", repo, err)
	}
	if r.MainBranch.Name == "" {
		return forge.Repository{}, fmt.Errorf("repository %s has no main branch", repo)
	}
	return forge.Repository{CloneURL: CloneURL(repo), DefaultBranch: r.MainBranch.Name}, nil
}

// CloneURL returns the HTTPS clone address of repo.
func CloneURL(repo string) string {
	return WebURL + "/" + repo + ".git"
}

// BranchHead returns the SHA of the commit branch of repo points at.
func (c *Client) BranchHead(ctx context.Context, repo, branch string) (string, error) {
	base, err := repoPath(repo)
	if err != nil {
		return "", err
	}
	var b struct {
		Target struct {
			Hash string `json:"hash"`
		} `json:"target"`
	}
	if err := c.api().GetJSON(ctx, base+"/refs/branches/"+url.PathEscape(branch), &b); err != nil {
		return "", fmt.Errorf("get head of %s in %s: %w", branch, repo, err)
	}
	if !shaRe.MatchString(b.Target.Hash) {
		return "", fmt.Errorf("head of %s in %s: unexpected commit %q", branch, repo, b.Target.Hash)
	}
	return b.Target.Hash, nil
}

// Commit returns the full SHA of rev in repo. Bitbucket's pull request
// events name commits by an abbreviated hash.
func (c *Client) Commit(ctx context.Context, repo, rev string) (string, error) {
	base, err := repoPath(repo)
	if err != nil {
		return "", err
	}
	var cm struct {
		Hash string `json:"hash"`
	}
	if err := c.api().GetJSON(ctx, base+"/commit/"+url.PathEscape(rev), &cm); err != nil {
		return "", fmt.Errorf("get commit %s in %s: %w", rev, repo, err)
	}
	if !shaRe.MatchString(cm.Hash) {
		return "", fmt.Errorf("commit %s in %s: unexpected hash %q", rev, repo, cm.Hash)
	}
	return cm.Hash, nil
}

// FileContent returns the raw content of path in repo at ref.
func (c *Client) FileContent(ctx context.Context, repo, ref, path string) ([]byte, error) {
	base, err := repoPath(repo)
	if err != nil {
		return nil, err
	}
	data, err := c.api().File(ctx, base+"/src/"+url.PathEscape(ref)+"/"+forge.EscapePath(path), "")
	if err != nil {
		return nil, fmt.Errorf("get %s from %s@%s: %w", path, repo, ref, err)
	}
	return data, nil
}

// CreateStatus sets a build status on sha in repo. Bitbucket requires a
// link; without one, the status links to the repository.
func (c *Client) CreateStatus(ctx context.Context, repo, sha string, st forge.CommitStatus) error {
	base, err := repoPath(repo)
	if err != nil {
		return err
	}
	if c.Token == "" {
		return forge.ErrNoToken
	}
	body := buildStatus{
		Key: statusKey(st.Context), State: states[st.State], Name: st.Context,
		URL: st.TargetURL, Description: st.Description,
	}
	if body.State == "" {
		body.State = "FAILED"
	}
	if body.URL == "" {
		body.URL = WebURL + "/" + repo
	}
	if err := c.api().Send(ctx, http.MethodPost, base+"/commit/"+url.PathEscape(sha)+"/statuses/build", body); err != nil {
		return fmt.Errorf("set status of %s@%s: %w", repo, sha, err)
	}
	return nil
}

// statusKey fits a status context into Bitbucket's 40-character key, which
// identifies the status like a context does elsewhere.
func statusKey(context string) string {
	if len(context) <= maxKeyLen {
		return context
	}
	sum := sha256.Sum256([]byte(context))
	h := hex.EncodeToString(sum[:4])
	// A rune cut in half at the end is dropped.
	return strings.ToValidUTF8(context[:maxKeyLen-len(h)-1], "") + "-" + h
}

// CloneCredentials returns the access token for git, which Bitbucket takes
// as the password of the x-token-auth user.
func (c *Client) CloneCredentials() (user, token string) {
	if c.Token == "" {
		return "", ""
	}
	return "x-token-auth", c.Token
}

var shaRe = regexp.MustCompile(`^([0-9a-f]{40}|[0-9a-f]{64})$`)

func repoPath(repo string) (string, error) {
	if !forge.ValidPath(forge.Bitbucket, repo) {
		return "", fmt.Errorf("%w: %q", forge.ErrInvalidRepo, repo)
	}
	return "/repositories/" + repo, nil
}

func (c *Client) api() forge.API {
	base := c.APIURL
	if base == "" {
		base = DefaultAPIURL
	}
	a := forge.API{Base: strings.TrimSuffix(base, "/"), HTTP: c.HTTP}
	if c.Token != "" {
		a.Authorize = func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+c.Token) }
	}
	return a
}
