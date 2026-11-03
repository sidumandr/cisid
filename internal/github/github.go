// Package github reads repository content through the GitHub REST API.
package github

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	// DefaultAPIURL is the public GitHub REST API.
	DefaultAPIURL = "https://api.github.com"

	apiVersion     = "2022-11-28"
	maxFileBytes   = 1 << 20
	requestTimeout = 15 * time.Second
)

var (
	// ErrNotFound is returned when the repository, ref or file does not exist
	// (or is not visible with the configured token).
	ErrNotFound = errors.New("not found on GitHub")
	// ErrTemporary marks failures worth retrying later: network errors,
	// 5xx responses and rate limits.
	ErrTemporary = errors.New("GitHub temporarily unavailable")
	// ErrTooLarge is returned for files over 1 MiB.
	ErrTooLarge = errors.New("file too large")
	// ErrInvalidRepo is returned for a repository name not in owner/name form.
	ErrInvalidRepo = errors.New("invalid repository name")
)

// Client talks to the GitHub REST API. The zero value uses the public API
// without authentication (60 requests per hour per IP).
type Client struct {
	// APIURL is the API base URL; DefaultAPIURL when empty (set it for
	// GitHub Enterprise Server).
	APIURL string
	// Token is an optional personal access or installation token, needed
	// for private repositories and higher rate limits.
	Token string
	// HTTP is the client used for requests; a client with a timeout when nil.
	HTTP *http.Client
}

// FileContent returns the raw content of path in repo ("owner/name") at ref.
func (c *Client) FileContent(ctx context.Context, repo, ref, path string) ([]byte, error) {
	owner, name, ok := strings.Cut(repo, "/")
	if !ok || !validRepoPart(owner) || !validRepoPart(name) {
		return nil, fmt.Errorf("%w: %q", ErrInvalidRepo, repo)
	}

	base := c.APIURL
	if base == "" {
		base = DefaultAPIURL
	}
	var escaped []string
	for _, seg := range strings.Split(path, "/") {
		escaped = append(escaped, url.PathEscape(seg))
	}
	u := strings.TrimSuffix(base, "/") + "/repos/" + owner + "/" + name + "/contents/" +
		strings.Join(escaped, "/") + "?" + url.Values{"ref": {ref}}.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, fmt.Errorf("build request for %s: %w", path, err)
	}
	req.Header.Set("Accept", "application/vnd.github.raw+json")
	req.Header.Set("X-GitHub-Api-Version", apiVersion)
	req.Header.Set("User-Agent", "cisid")
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}

	resp, err := c.httpClient().Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: get %s from %s: %w", ErrTemporary, path, repo, err)
	}
	defer resp.Body.Close()

	if err := checkStatus(resp); err != nil {
		return nil, fmt.Errorf("get %s from %s@%s: %w", path, repo, ref, err)
	}

	data, err := io.ReadAll(io.LimitReader(resp.Body, maxFileBytes+1))
	if err != nil {
		return nil, fmt.Errorf("%w: read %s from %s: %w", ErrTemporary, path, repo, err)
	}
	if len(data) > maxFileBytes {
		return nil, fmt.Errorf("%w: %s in %s is over %d bytes", ErrTooLarge, path, repo, maxFileBytes)
	}
	return data, nil
}

func (c *Client) httpClient() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return &http.Client{Timeout: requestTimeout}
}

// checkStatus maps a non-2xx response to ErrNotFound, ErrTemporary or a
// permanent error carrying GitHub's message.
func checkStatus(resp *http.Response) error {
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
	msg := strings.TrimSpace(string(body))

	switch {
	case resp.StatusCode == http.StatusNotFound:
		return ErrNotFound
	case resp.StatusCode == http.StatusTooManyRequests,
		resp.StatusCode == http.StatusForbidden && resp.Header.Get("X-RateLimit-Remaining") == "0",
		resp.StatusCode >= 500:
		return fmt.Errorf("%w: %s: %s", ErrTemporary, resp.Status, msg)
	default:
		return fmt.Errorf("unexpected response %s: %s", resp.Status, msg)
	}
}

// validRepoPart accepts GitHub owner and repository names.
func validRepoPart(s string) bool {
	if s == "" || s == "." || s == ".." || len(s) > 100 {
		return false
	}
	for _, r := range s {
		ok := r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' || r == '.'
		if !ok {
			return false
		}
	}
	return true
}
