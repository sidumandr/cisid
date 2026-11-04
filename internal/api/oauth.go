package api

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"

	"github.com/sidumandr/cisid/internal/auth"
	"github.com/sidumandr/cisid/internal/db"
	"github.com/sidumandr/cisid/pkg/models"
)

// oauthCookie carries a sign-in's state, the page to return to and, for
// connecting a forge account rather than signing in, modeConnect, from the
// redirect to the forge to the callback.
const oauthCookie = "cisid_oauth"

const modeConnect = "connect"

// OAuthConfig signs users in through their forges.
type OAuthConfig struct {
	Providers []*auth.Provider
	// PublicURL is where the forges send the browser back to; the
	// callback is PublicURL/auth/{forge}/callback.
	PublicURL string
	// SignupRole gives forge users without an account one with this
	// role on their first sign-in; empty lets only users an admin linked
	// to the forge in.
	SignupRole auth.Role
}

func (a *Access) provider(name string) *auth.Provider {
	for _, p := range a.cfg.OAuth.Providers {
		if p.Name == name {
			return p
		}
	}
	return nil
}

// providerNames lists the forges one can sign in through.
func (a *Access) providerNames() []string {
	var names []string
	for _, p := range a.cfg.OAuth.Providers {
		names = append(names, p.Name)
	}
	slices.Sort(names)
	return names
}

func (a *Access) callbackURL(forge string) string {
	return a.cfg.OAuth.PublicURL + "/auth/" + forge + "/callback"
}

// safeNext keeps where to return to on this site: a path, never another
// host ("//evil.example" or "https://...").
func safeNext(next string) string {
	if !strings.HasPrefix(next, "/") || strings.HasPrefix(next, "//") || strings.Contains(next, "\\") {
		return "/"
	}
	return next
}

// oauthLogin sends the browser to the forge to sign in.
func (a *Access) oauthLogin(w http.ResponseWriter, r *http.Request) {
	p := a.provider(r.PathValue("forge"))
	if p == nil {
		writeError(w, http.StatusNotFound, "signing in through this forge is not set up")
		return
	}
	state, _, err := auth.NewSecret("")
	if err != nil {
		a.internalError(w, r, "start forge sign-in", err)
		return
	}
	// Lax, so the cookie comes back with the forge's redirect.
	http.SetCookie(w, &http.Cookie{
		Name: oauthCookie, Value: state + "|" + url.QueryEscape(safeNext(r.URL.Query().Get("next"))),
		Path: "/auth/", MaxAge: 600, HttpOnly: true, Secure: secureRequest(r), SameSite: http.SameSiteLaxMode,
	})
	http.Redirect(w, r, p.AuthCodeURL(state, a.callbackURL(p.Name)), http.StatusFound)
}

// oauthCallback finishes a sign-in: it checks the state, asks the forge
// who signed in, finds their account (or creates one, with SignupRole)
// and starts a session. Failures land on the account page with a reason.
func (a *Access) oauthCallback(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	p := a.provider(r.PathValue("forge"))
	if p == nil {
		writeError(w, http.StatusNotFound, "signing in through this forge is not set up")
		return
	}
	fail := func(reason string) {
		http.Redirect(w, r, "/login?signin="+url.QueryEscape(reason), http.StatusFound)
	}

	c, err := r.Cookie(oauthCookie)
	http.SetCookie(w, &http.Cookie{Name: oauthCookie, Path: "/auth/", MaxAge: -1, HttpOnly: true, Secure: secureRequest(r)})
	if err != nil {
		fail("expired")
		return
	}
	state, rest, _ := strings.Cut(c.Value, "|")
	escapedNext, mode, _ := strings.Cut(rest, "|")
	got := r.URL.Query().Get("state")
	if state == "" || subtle.ConstantTimeCompare([]byte(got), []byte(state)) != 1 {
		a.log.WarnContext(ctx, "forge sign-in with a wrong state", "forge", p.Name, "remote_addr", r.RemoteAddr)
		fail("expired")
		return
	}
	next, err := url.QueryUnescape(escapedNext)
	if err != nil {
		next = "/"
	}
	next = safeNext(next)
	if mode == modeConnect {
		if r.URL.Query().Get("error") != "" {
			http.Redirect(w, r, withQuery(next, "connect", "denied"), http.StatusFound)
			return
		}
		a.finishConnect(w, r, p, next)
		return
	}
	if r.URL.Query().Get("error") != "" {
		fail("denied") // the user said no on the forge
		return
	}

	id, err := p.SignIn(ctx, r.URL.Query().Get("code"), a.callbackURL(p.Name))
	if err != nil {
		a.log.WarnContext(ctx, "forge sign-in failed", "forge", p.Name, "error", err)
		fail("failed")
		return
	}
	u, err := a.forgeUser(ctx, id)
	if errors.Is(err, db.ErrUserNotFound) {
		a.log.WarnContext(ctx, "forge user has no account", "forge", p.Name, "login", id.Login)
		fail("unknown")
		return
	}
	if err != nil {
		a.log.ErrorContext(ctx, "find forge user", "forge", p.Name, "login", id.Login, "error", err)
		fail("failed")
		return
	}
	if !a.startSession(w, r, u) {
		return
	}
	http.Redirect(w, r, next, http.StatusFound)
}

// forgeUser returns the account of a forge identity, creating it when
// signing up is on.
func (a *Access) forgeUser(ctx context.Context, id auth.Identity) (models.User, error) {
	u, err := a.cfg.Accounts.UserByIdentity(ctx, id.Forge, id.Subject, id.Login)
	if !errors.Is(err, db.ErrUserNotFound) || a.cfg.OAuth.SignupRole == "" {
		return u, err
	}
	for _, name := range signupNames(id) {
		_, err := a.cfg.Accounts.CreateUser(ctx, models.User{
			Username: name, Role: string(a.cfg.OAuth.SignupRole), Identities: map[string]string{id.Forge: id.Login},
		}, "")
		if errors.Is(err, db.ErrUserExists) {
			continue
		}
		if err != nil {
			return models.User{}, err
		}
		a.log.InfoContext(ctx, "user signed up through a forge", "username", name, "forge", id.Forge,
			"login", id.Login, "role", a.cfg.OAuth.SignupRole)
		return a.cfg.Accounts.UserByIdentity(ctx, id.Forge, id.Subject, id.Login)
	}
	return models.User{}, fmt.Errorf("%w: no free username for %s user %s", db.ErrUserNotFound, id.Forge, id.Login)
}

// signupNames are the usernames a forge user may get: their login made a
// valid username, then with the forge's name added.
func signupNames(id auth.Identity) []string {
	base := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '.', r == '_', r == '-':
			return r
		case r >= 'A' && r <= 'Z':
			return r + ('a' - 'A')
		}
		return '-'
	}, id.Login)
	base = strings.TrimLeft(base, ".-_")
	var names []string
	for _, n := range []string{base, base + "-" + id.Forge} {
		if len(n) > 39 {
			n = n[:39]
		}
		if auth.ValidUsername(n) && !slices.Contains(names, n) {
			names = append(names, n)
		}
	}
	return names
}
