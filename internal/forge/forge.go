// Package forge is what cisid needs from the git hosts it builds from:
// reading a repository, its branches and files, and showing commit
// statuses. Each host has a client package (github, gitea, gitlab,
// bitbucket); a Set routes a repository key to its client.
package forge

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
)

// Forge names. A repository key starts with the name, except on GitHub.
const (
	GitHub    = "github"
	Gitea     = "gitea"
	Forgejo   = "forgejo"
	GitLab    = "gitlab"
	Bitbucket = "bitbucket"
)

// MaxFileBytes is the largest file FileContent returns.
const MaxFileBytes = 1 << 20

var (
	// ErrNotFound is returned when the repository, ref or file does not
	// exist (or is not visible with the configured token).
	ErrNotFound = errors.New("not found on the forge")
	// ErrTemporary marks failures worth retrying later: network errors,
	// 5xx responses and rate limits.
	ErrTemporary = errors.New("forge temporarily unavailable")
	// ErrTooLarge is returned for files over MaxFileBytes.
	ErrTooLarge = errors.New("file too large")
	// ErrInvalidRepo is returned for a repository key or path that is not
	// well formed.
	ErrInvalidRepo = errors.New("invalid repository name")
	// ErrUnknownForge is returned for a repository on a forge the server
	// is not configured for.
	ErrUnknownForge = errors.New("forge not configured")
	// ErrNoToken is returned by CreateStatus when the forge's client has
	// no token to post statuses with.
	ErrNoToken = errors.New("no token configured for the forge")
)

// Repository is what building a repository without a webhook needs.
type Repository struct {
	CloneURL      string
	DefaultBranch string
}

// State is a commit status state; each client maps it to its forge's own.
type State string

// Commit status states.
const (
	StatePending  State = "pending"
	StateRunning  State = "running"
	StateSuccess  State = "success"
	StateFailure  State = "failure"
	StateCanceled State = "canceled"
	StateError    State = "error"
)

// CommitStatus is a status shown next to a commit.
type CommitStatus struct {
	State       State
	TargetURL   string
	Description string
	// Context tells cisid's statuses apart, e.g. "cisid/pr".
	Context string
}

// Client is one forge's API. Paths are the forge's own repository paths,
// such as "owner/name" or "group/subgroup/project".
type Client interface {
	Repository(ctx context.Context, path string) (Repository, error)
	// BranchHead returns the SHA of the commit branch points at.
	BranchHead(ctx context.Context, path, branch string) (string, error)
	// FileContent returns the raw content of file at ref.
	FileContent(ctx context.Context, path, ref, file string) ([]byte, error)
	// CreateStatus sets a commit status on sha. It needs a token.
	CreateStatus(ctx context.Context, path, sha string, st CommitStatus) error
	// CloneCredentials returns the user and token git clones with; an
	// empty token clones anonymously.
	CloneCredentials() (user, token string)
}

// Key returns the repository key of path on forge: path itself on GitHub,
// so keys from before cisid knew other forges stay valid, and
// "forge:path" elsewhere, e.g. "gitlab:group/subgroup/project".
func Key(forge, path string) string {
	if forge == GitHub {
		return path
	}
	return forge + ":" + path
}

// SplitKey returns the forge and the path of a repository key, and fails
// for a key whose path is not well formed for its forge.
func SplitKey(key string) (forge, path string, err error) {
	forge, path = GitHub, key
	if f, p, ok := strings.Cut(key, ":"); ok {
		forge, path = f, p
	}
	if !ValidPath(forge, path) {
		return "", "", fmt.Errorf("%w: %q", ErrInvalidRepo, key)
	}
	return forge, path, nil
}

// maxDepth is how many segments a path may have; GitLab nests groups.
func maxDepth(forge string) int {
	if forge == GitLab {
		return 20
	}
	return 2
}

// ValidPath reports whether path is a repository path on forge: two
// segments ("owner/name"), or up to 20 on GitLab, each made of letters,
// digits, "-", "_" and ".".
func ValidPath(forge, path string) bool {
	if !slices.Contains([]string{GitHub, Gitea, Forgejo, GitLab, Bitbucket}, forge) {
		return false
	}
	segs := strings.Split(path, "/")
	if len(segs) < 2 || len(segs) > maxDepth(forge) {
		return false
	}
	for _, s := range segs {
		if !validSegment(s) {
			return false
		}
	}
	return true
}

func validSegment(s string) bool {
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

// PullRequestRef is the ref that holds the head of pull request number on
// forge, for git servers that do not serve a commit by its SHA alone.
// Bitbucket has none; its pull requests are fetched by branch.
func PullRequestRef(forge string, number int, branch string) string {
	switch forge {
	case GitLab:
		return fmt.Sprintf("refs/merge-requests/%d/head", number)
	case Bitbucket:
		return "refs/heads/" + branch
	default:
		return fmt.Sprintf("refs/pull/%d/head", number)
	}
}

// Set routes repository keys to the clients of the configured forges.
// Its methods take keys where Client's take paths.
type Set struct {
	clients map[string]Client
}

// NewSet returns an empty Set.
func NewSet() *Set {
	return &Set{clients: map[string]Client{}}
}

// Add registers c for the forge called name.
func (s *Set) Add(name string, c Client) {
	s.clients[name] = c
}

// Client returns the client of forge name.
func (s *Set) Client(name string) (Client, bool) {
	c, ok := s.clients[name]
	return c, ok
}

// Names lists the configured forges, sorted.
func (s *Set) Names() []string {
	names := make([]string, 0, len(s.clients))
	for n := range s.clients {
		names = append(names, n)
	}
	slices.Sort(names)
	return names
}

func (s *Set) lookup(key string) (Client, string, error) {
	name, path, err := SplitKey(key)
	if err != nil {
		return nil, "", err
	}
	c, ok := s.clients[name]
	if !ok {
		return nil, "", fmt.Errorf("%w: %s (repository %s)", ErrUnknownForge, name, key)
	}
	return c, path, nil
}

// Repository describes the repository key names.
func (s *Set) Repository(ctx context.Context, key string) (Repository, error) {
	c, path, err := s.lookup(key)
	if err != nil {
		return Repository{}, err
	}
	return c.Repository(ctx, path)
}

// BranchHead returns the SHA of the commit branch of key points at.
func (s *Set) BranchHead(ctx context.Context, key, branch string) (string, error) {
	c, path, err := s.lookup(key)
	if err != nil {
		return "", err
	}
	return c.BranchHead(ctx, path, branch)
}

// FileContent returns the raw content of file in key at ref.
func (s *Set) FileContent(ctx context.Context, key, ref, file string) ([]byte, error) {
	c, path, err := s.lookup(key)
	if err != nil {
		return nil, err
	}
	return c.FileContent(ctx, path, ref, file)
}

// CreateStatus sets a commit status on sha in key.
func (s *Set) CreateStatus(ctx context.Context, key, sha string, st CommitStatus) error {
	c, path, err := s.lookup(key)
	if err != nil {
		return err
	}
	return c.CreateStatus(ctx, path, sha, st)
}

// CloneCredentials returns what git clones key with; an empty token
// clones anonymously.
func (s *Set) CloneCredentials(key string) (user, token string) {
	c, _, err := s.lookup(key)
	if err != nil {
		return "", ""
	}
	return c.CloneCredentials()
}
