package api

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/sidumandr/cisid/internal/db"
	"github.com/sidumandr/cisid/internal/forge"
)

const (
	// defaultBranchTTL is how long a repository's default branch is
	// remembered; badges are fetched often, the default branch rarely
	// changes.
	defaultBranchTTL = 10 * time.Minute
	// maxDefaultBranches bounds the remembered default branches.
	maxDefaultBranches = 1000
	// resolveTimeout bounds asking the forge for the default branch.
	resolveTimeout = 5 * time.Second
)

// BadgeStore tells how the builds of a branch went. *db.Store satisfies it.
type BadgeStore interface {
	BranchResult(ctx context.Context, repo, branch string) (failed, ok bool, err error)
	RepoHasJobs(ctx context.Context, repo string) (bool, error)
}

var _ BadgeStore = (*db.Store)(nil)

// RepoResolver finds a repository's default branch. *forge.Set satisfies it.
type RepoResolver interface {
	Repository(ctx context.Context, repo string) (forge.Repository, error)
}

// BadgeAPI draws a branch's build status as an SVG badge for READMEs:
//
//	GET /api/repos/{owner}/{repo}/badge.svg?branch=main
//
// Without a branch it shows the repository's default branch.
type BadgeAPI struct {
	store  BadgeStore
	repos  RepoResolver
	access *Access
	public bool
	log    *slog.Logger
	now    func() time.Time

	mu       sync.Mutex
	defaults map[string]defaultBranch // by repository
}

type defaultBranch struct {
	name  string // empty when the forge could not tell
	until time.Time
}

// NewBadgeAPI returns a BadgeAPI. Badges are read like jobs, as access
// allows, unless public, which lets anyone see them even when reading
// needs a sign-in.
func NewBadgeAPI(store BadgeStore, repos RepoResolver, access *Access, public bool, log *slog.Logger) *BadgeAPI {
	if log == nil {
		log = slog.Default()
	}
	if access == nil {
		access = openAccess()
	}
	return &BadgeAPI{store: store, repos: repos, access: access, public: public, log: log, now: time.Now,
		defaults: map[string]defaultBranch{}}
}

func (b *BadgeAPI) register(mux *http.ServeMux) {
	h := b.badge
	if !b.public {
		h = b.access.wrapFunc(h)
	}
	mux.HandleFunc("GET /api/repos/{owner}/{repo}/badge.svg", h)
}

func (b *BadgeAPI) badge(w http.ResponseWriter, r *http.Request) {
	repo, ok := repoParam(w, r)
	if !ok {
		return
	}
	branch := r.URL.Query().Get("branch")
	if branch != "" && !validBranch(branch) {
		writeError(w, http.StatusBadRequest, "invalid branch")
		return
	}
	state, err := b.state(r.Context(), repo, branch)
	if err != nil {
		b.log.ErrorContext(r.Context(), "draw badge", "repo", repo, "branch", branch, "error", err)
		writeError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	writeBadge(w, r, state)
}

// state returns the badge's word: passing, failing or unknown.
func (b *BadgeAPI) state(ctx context.Context, repo, branch string) (string, error) {
	if branch == "" {
		// Only repositories cisid builds are looked up on their forge, so
		// badge requests cannot be turned into requests to the forge.
		known, err := b.store.RepoHasJobs(ctx, repo)
		if err != nil {
			return "", err
		}
		if !known {
			return badgeUnknown, nil
		}
		if branch = b.defaultBranch(ctx, repo); branch == "" {
			return badgeUnknown, nil
		}
	}
	failed, ok, err := b.store.BranchResult(ctx, repo, branch)
	switch {
	case err != nil:
		return "", err
	case !ok:
		return badgeUnknown, nil
	case failed:
		return badgeFailing, nil
	}
	return badgePassing, nil
}

// defaultBranch returns repo's default branch, or "" when the forge cannot
// tell, remembering either for a while.
func (b *BadgeAPI) defaultBranch(ctx context.Context, repo string) string {
	now := b.now()
	b.mu.Lock()
	d, ok := b.defaults[repo]
	b.mu.Unlock()
	if ok && now.Before(d.until) {
		return d.name
	}

	ctx, cancel := context.WithTimeout(ctx, resolveTimeout)
	defer cancel()
	info, err := b.repos.Repository(ctx, repo)
	if err != nil {
		b.log.DebugContext(ctx, "badge: default branch unknown", "repo", repo, "error", err)
	}
	d = defaultBranch{name: info.DefaultBranch, until: now.Add(defaultBranchTTL)}

	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.defaults) >= maxDefaultBranches {
		clear(b.defaults)
	}
	b.defaults[repo] = d
	return d.name
}

// Badge words.
const (
	badgePassing = "passing"
	badgeFailing = "failing"
	badgeUnknown = "unknown"
)

// badgeStyle is how a word is drawn. Widths are for Verdana at 11px, the
// usual badge font; textLength makes other fonts fit the same box.
type badgeStyle struct {
	width, textWidth int
	color            string
}

var badgeStyles = map[string]badgeStyle{
	badgePassing: {52, 42, "#1f8a4c"},
	badgeFailing: {44, 34, "#c9304a"},
	badgeUnknown: {60, 50, "#5c5c5c"},
}

const (
	badgeLabelWidth = 35
	badgeLabelText  = 25
)

// badgeSVG draws "cisid" on black next to the word on its color. Every
// value in it is one of the constants above.
func badgeSVG(state string) string {
	s := badgeStyles[state]
	w := badgeLabelWidth + s.width
	return fmt.Sprintf(`<svg xmlns="http://www.w3.org/2000/svg" width="%d" height="20" role="img" aria-label="cisid: %s">`+
		`<title>cisid: %s</title>`+
		`<clipPath id="r"><rect width="%d" height="20" rx="3"/></clipPath>`+
		`<g clip-path="url(#r)"><rect width="%d" height="20" fill="#0d0d0d"/><rect x="%d" width="%d" height="20" fill="%s"/></g>`+
		`<g font-family="Verdana,Geneva,DejaVu Sans,sans-serif" font-size="11" text-anchor="middle">`+
		`<text x="%.1f" y="14" fill="#ff8a47" textLength="%d">cisid</text>`+
		`<text x="%.1f" y="14" fill="#ffffff" textLength="%d">%s</text>`+
		`</g></svg>`,
		w, state, state, w, badgeLabelWidth, badgeLabelWidth, s.width, s.color,
		float64(badgeLabelWidth)/2, badgeLabelText,
		float64(badgeLabelWidth)+float64(s.width)/2, s.textWidth, state)
}

// writeBadge sends the badge for state. Image proxies such as GitHub's
// are told not to keep it, so a README shows the latest result.
func writeBadge(w http.ResponseWriter, r *http.Request, state string) {
	etag := `"` + state + `"`
	h := w.Header()
	h.Set("Cache-Control", "no-cache, max-age=0")
	h.Set("ETag", etag)
	if r.Header.Get("If-None-Match") == etag {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	h.Set("Content-Type", "image/svg+xml")
	h.Set("Content-Security-Policy", "default-src 'none'")
	h.Set("X-Content-Type-Options", "nosniff")
	_, _ = fmt.Fprint(w, badgeSVG(state))
}
