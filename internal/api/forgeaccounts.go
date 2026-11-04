package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"slices"
	"strings"

	"github.com/sidumandr/cisid/internal/auth"
	"github.com/sidumandr/cisid/internal/forge"
	"github.com/sidumandr/cisid/internal/forgeauth"
	"github.com/sidumandr/cisid/pkg/models"
)

// ForgeAccounts keeps users' own access to their forges.
// *forgeauth.Service satisfies it.
type ForgeAccounts interface {
	Forges() []string
	Accounts(ctx context.Context, userID int64) ([]models.ForgeAccount, error)
	ConnectToken(ctx context.Context, userID int64, forge, token string) (models.ForgeAccount, error)
	ConnectOAuth(ctx context.Context, userID int64, forge string, tok auth.Token) (models.ForgeAccount, error)
	Disconnect(ctx context.Context, userID int64, forge string) (bool, error)
	Token(ctx context.Context, userID int64, forge string) (string, error)
}

var _ ForgeAccounts = (*forgeauth.Service)(nil)

// forgeOption is a forge the user can connect, and whether by OAuth.
type forgeOption struct {
	Forge string `json:"forge"`
	OAuth bool   `json:"oauth"`
}

type forgesResponse struct {
	// Enabled is false when the server has no CISID_SECRET_KEY to keep
	// users' tokens sealed with.
	Enabled  bool                  `json:"enabled"`
	Forges   []forgeOption         `json:"forges"`
	Accounts []models.ForgeAccount `json:"accounts"`
}

// registerForgeAccounts serves users' own forge access:
//
//	GET    /api/me/forges            the forges one can connect, and those one did
//	PUT    /api/me/forges/{forge}    connect one with a token of one's own
//	DELETE /api/me/forges/{forge}    disconnect it
//	GET    /auth/{forge}/connect     connect one by OAuth, coming back to ?next=
func (a *Access) registerForgeAccounts(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/me/forges", a.require(auth.RoleViewer, a.listForgeAccounts))
	mux.HandleFunc("PUT /api/me/forges/{forge}", a.require(auth.RoleViewer, a.connectForgeToken))
	mux.HandleFunc("DELETE /api/me/forges/{forge}", a.require(auth.RoleViewer, a.disconnectForge))
	mux.HandleFunc("GET /auth/{forge}/connect", a.oauthConnect)
}

func (a *Access) listForgeAccounts(w http.ResponseWriter, r *http.Request) {
	u, ok := signedInUser(w, r)
	if !ok {
		return
	}
	resp := forgesResponse{Forges: []forgeOption{}, Accounts: []models.ForgeAccount{}}
	if fa := a.cfg.ForgeAccounts; fa != nil {
		resp.Enabled = true
		for _, f := range fa.Forges() {
			p := a.provider(f)
			resp.Forges = append(resp.Forges, forgeOption{Forge: f, OAuth: p != nil && len(p.ConnectScopes) > 0})
		}
		accounts, err := fa.Accounts(r.Context(), u.ID)
		if err != nil {
			a.internalError(w, r, "list forge accounts", err)
			return
		}
		if accounts != nil {
			resp.Accounts = accounts
		}
	}
	writeJSON(w, http.StatusOK, resp)
}

// forgeParam reads the forge of the path, one the user can connect.
func (a *Access) forgeParam(w http.ResponseWriter, r *http.Request) (string, bool) {
	if a.cfg.ForgeAccounts == nil {
		writeError(w, http.StatusConflict, "connecting forge accounts needs CISID_SECRET_KEY on the server, to keep the tokens sealed")
		return "", false
	}
	name := r.PathValue("forge")
	if !slices.Contains(a.cfg.ForgeAccounts.Forges(), name) {
		writeError(w, http.StatusNotFound, "this forge cannot be connected here")
		return "", false
	}
	return name, true
}

func (a *Access) connectForgeToken(w http.ResponseWriter, r *http.Request) {
	u, ok := signedInUser(w, r)
	if !ok {
		return
	}
	name, ok := a.forgeParam(w, r)
	if !ok {
		return
	}
	var body struct {
		Token string `json:"token"`
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&body); err != nil || strings.TrimSpace(body.Token) == "" {
		writeError(w, http.StatusBadRequest, `body must be {"token": "..."}`)
		return
	}
	acct, err := a.cfg.ForgeAccounts.ConnectToken(r.Context(), u.ID, name, strings.TrimSpace(body.Token))
	switch {
	case errors.Is(err, forgeauth.ErrTokenRejected):
		writeError(w, http.StatusBadRequest, "the forge does not take this token")
	case errors.Is(err, forge.ErrTemporary):
		writeError(w, http.StatusServiceUnavailable, "the forge is unavailable, try again")
	case err != nil:
		a.internalError(w, r, "connect forge account", err)
	default:
		writeJSON(w, http.StatusOK, acct)
	}
}

func (a *Access) disconnectForge(w http.ResponseWriter, r *http.Request) {
	u, ok := signedInUser(w, r)
	if !ok {
		return
	}
	name, ok := a.forgeParam(w, r)
	if !ok {
		return
	}
	found, err := a.cfg.ForgeAccounts.Disconnect(r.Context(), u.ID, name)
	switch {
	case err != nil:
		a.internalError(w, r, "disconnect forge account", err)
	case !found:
		writeError(w, http.StatusNotFound, "this forge is not connected")
	default:
		w.WriteHeader(http.StatusNoContent)
	}
}

// oauthConnect sends a signed-in user to the forge to let cisid reach
// their repositories; the callback keeps the token. Failures come back to
// next with ?connect=.
func (a *Access) oauthConnect(w http.ResponseWriter, r *http.Request) {
	next := safeNext(r.URL.Query().Get("next"))
	p := a.provider(r.PathValue("forge"))
	if p == nil || len(p.ConnectScopes) == 0 || a.cfg.ForgeAccounts == nil {
		http.Redirect(w, r, withQuery(next, "connect", "unavailable"), http.StatusFound)
		return
	}
	if who, ok := a.who(r); !ok || who.user == nil || !who.cookie {
		http.Redirect(w, r, "/login?next="+url.QueryEscape(next), http.StatusFound)
		return
	}
	state, _, err := auth.NewSecret("")
	if err != nil {
		a.internalError(w, r, "start connecting a forge", err)
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name: oauthCookie, Value: state + "|" + url.QueryEscape(next) + "|" + modeConnect,
		Path: "/auth/", MaxAge: 600, HttpOnly: true, Secure: secureRequest(r), SameSite: http.SameSiteLaxMode,
	})
	http.Redirect(w, r, p.ConnectURL(state, a.callbackURL(p.Name)), http.StatusFound)
}

// finishConnect keeps the token of a connect the forge sent back, for the
// signed-in user, and returns to next with ?connected= or ?connect=.
func (a *Access) finishConnect(w http.ResponseWriter, r *http.Request, p *auth.Provider, next string) {
	ctx := r.Context()
	who, ok := a.who(r)
	if !ok || who.user == nil || a.cfg.ForgeAccounts == nil {
		http.Redirect(w, r, withQuery(next, "connect", "expired"), http.StatusFound)
		return
	}
	tok, err := p.Exchange(ctx, r.URL.Query().Get("code"), a.callbackURL(p.Name))
	if err == nil {
		_, err = a.cfg.ForgeAccounts.ConnectOAuth(ctx, who.user.ID, p.Name, tok)
	}
	if err != nil {
		a.log.WarnContext(ctx, "connecting a forge failed", "forge", p.Name, "user", who.user.Username, "error", err)
		http.Redirect(w, r, withQuery(next, "connect", "failed"), http.StatusFound)
		return
	}
	http.Redirect(w, r, withQuery(next, "connected", p.Name), http.StatusFound)
}

// withQuery adds key=value to a path on this site.
func withQuery(path, key, value string) string {
	sep := "?"
	if strings.Contains(path, "?") {
		sep = "&"
	}
	return path + sep + url.QueryEscape(key) + "=" + url.QueryEscape(value)
}
