package forge

import (
	"context"
	"errors"
	"slices"
)

// ErrNoListing is returned by a Lister without a token: listing the
// repositories one can reach needs to know who one is.
var ErrNoListing = errors.New("no token to list repositories with")

// Listed is a repository a forge's token reaches, offered for connecting.
type Listed struct {
	// Repo is the repository key, as cisid names it.
	Repo    string `json:"repo"`
	Forge   string `json:"forge"`
	Private bool   `json:"private"`
}

// Lister is a forge client that lists the repositories its token reaches,
// most recently active first. Paths are the forge's own.
type Lister interface {
	Repositories(ctx context.Context) ([]ListedPath, error)
}

// ListedPath is one repository a Lister found, by its path on the forge.
type ListedPath struct {
	Path    string
	Private bool
}

// Listing is every repository the configured forges' tokens reach.
type Listing struct {
	Repos []Listed `json:"repositories"`
	// Unlisted names the forges that listed nothing: they have no token,
	// cannot list, or failed to.
	Unlisted []string `json:"unlisted"`
}

// Repositories lists what each forge's token reaches. A forge that cannot
// list, has no token or fails is named in Unlisted, and the rest are still
// listed.
func (s *Set) Repositories(ctx context.Context) Listing {
	l := Listing{Repos: []Listed{}, Unlisted: []string{}}
	for _, name := range s.Names() {
		lister, ok := s.clients[name].(Lister)
		if !ok {
			l.Unlisted = append(l.Unlisted, name)
			continue
		}
		paths, err := lister.Repositories(ctx)
		if err != nil {
			l.Unlisted = append(l.Unlisted, name)
			continue
		}
		for _, p := range paths {
			if ValidPath(name, p.Path) {
				l.Repos = append(l.Repos, Listed{Repo: Key(name, p.Path), Forge: name, Private: p.Private})
			}
		}
	}
	slices.Sort(l.Unlisted)
	return l
}
