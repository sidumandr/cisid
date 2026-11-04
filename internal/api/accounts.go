package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"

	"github.com/sidumandr/cisid/internal/auth"
	"github.com/sidumandr/cisid/internal/db"
	"github.com/sidumandr/cisid/pkg/models"
)

// dummyHash is checked against when a username is unknown, so a failed
// sign-in takes as long whether or not the user exists.
var dummyHash = sync.OnceValue(func() string {
	h, _ := auth.HashPassword("cisid timing equaliser")
	return h
})

func (a *Access) register(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/login", a.login)
	mux.HandleFunc("POST /api/logout", a.logout)
	mux.HandleFunc("GET /api/me", a.me)
	if a.cfg.Accounts == nil {
		return
	}
	mux.HandleFunc("POST /api/register", a.registerUser)
	mux.HandleFunc("GET /auth/{forge}/login", a.oauthLogin)
	mux.HandleFunc("GET /auth/{forge}/callback", a.oauthCallback)
	mux.HandleFunc("PUT /api/me/password", a.require(auth.RoleViewer, a.changePassword))
	mux.HandleFunc("GET /api/me/tokens", a.require(auth.RoleViewer, a.listTokens))
	mux.HandleFunc("POST /api/me/tokens", a.require(auth.RoleViewer, a.createToken))
	mux.HandleFunc("DELETE /api/me/tokens/{id}", a.require(auth.RoleViewer, a.deleteToken))
	mux.HandleFunc("GET /api/users", a.require(auth.RoleAdmin, a.listUsers))
	mux.HandleFunc("POST /api/users", a.require(auth.RoleAdmin, a.createUser))
	mux.HandleFunc("PATCH /api/users/{username}", a.require(auth.RoleAdmin, a.updateUser))
	mux.HandleFunc("DELETE /api/users/{username}", a.require(auth.RoleAdmin, a.deleteUser))
}

// decode reads a small JSON body into v, rejecting unknown fields.
func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192))
	dec.DisallowUnknownFields()
	return dec.Decode(v) == nil
}

// login signs a browser in with a username and password, or with a token.
// A user (by password or personal token) gets a session; the read and API
// tokens get the read cookie, as before accounts existed.
func (a *Access) login(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Username string `json:"username"`
		Password string `json:"password"`
		Token    string `json:"token"`
	}
	if !decode(w, r, &body) || (body.Token == "") == (body.Username == "") {
		writeError(w, http.StatusBadRequest, `body must be {"username": "...", "password": "..."} or {"token": "..."}`)
		return
	}
	ctx := r.Context()

	if body.Token != "" {
		if a.cfg.Accounts != nil && strings.HasPrefix(body.Token, auth.TokenPrefix) {
			u, err := a.cfg.Accounts.TokenUser(ctx, auth.Digest(body.Token))
			if err != nil {
				a.lookupFailed(ctx, "token", err)
				writeError(w, http.StatusUnauthorized, "wrong token")
				return
			}
			if a.startSession(w, r, u) {
				w.WriteHeader(http.StatusNoContent)
			}
			return
		}
		if !equal(body.Token, a.cfg.ReadToken) && !equal(body.Token, a.cfg.APIToken) {
			if a.cfg.PublicRead {
				w.WriteHeader(http.StatusNoContent) // nothing to sign in to
				return
			}
			writeError(w, http.StatusUnauthorized, "wrong token")
			return
		}
		http.SetCookie(w, &http.Cookie{
			Name: tokenCookie, Value: cookieDigest(body.Token), Path: "/",
			HttpOnly: true, Secure: secureRequest(r), SameSite: http.SameSiteStrictMode,
		})
		w.WriteHeader(http.StatusNoContent)
		return
	}

	if a.cfg.Accounts == nil {
		writeError(w, http.StatusUnauthorized, "wrong username or password")
		return
	}
	u, hash, err := a.cfg.Accounts.User(ctx, strings.ToLower(strings.TrimSpace(body.Username)))
	if err != nil {
		a.lookupFailed(ctx, "user", err)
		hash = dummyHash()
	}
	if !auth.CheckPassword(hash, body.Password) || err != nil || u.Disabled {
		a.log.WarnContext(ctx, "sign-in refused", "username", body.Username, "remote_addr", r.RemoteAddr)
		writeError(w, http.StatusUnauthorized, "wrong username or password")
		return
	}
	if a.startSession(w, r, u) {
		w.WriteHeader(http.StatusNoContent)
	}
}

// startSession signs u in on this browser, or answers 500 and returns
// false.
func (a *Access) startSession(w http.ResponseWriter, r *http.Request, u models.User) bool {
	ctx := r.Context()
	secret, digest, err := auth.NewSecret("")
	if err != nil {
		a.internalError(w, r, "start session", err)
		return false
	}
	if err := a.cfg.Accounts.CreateSession(ctx, digest, u.ID, a.now().Add(a.cfg.SessionTTL)); err != nil {
		a.internalError(w, r, "start session", err)
		return false
	}
	// Lax, not Strict: signing in through a forge comes back from another
	// site, and the first page after it must see the session.
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Value: secret, Path: "/", MaxAge: int(a.cfg.SessionTTL.Seconds()),
		HttpOnly: true, Secure: secureRequest(r), SameSite: http.SameSiteLaxMode,
	})
	a.log.InfoContext(ctx, "signed in", "username", u.Username, "remote_addr", r.RemoteAddr)
	return true
}

func (a *Access) logout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(sessionCookie); err == nil && a.cfg.Accounts != nil {
		if err := a.cfg.Accounts.DeleteSession(r.Context(), auth.Digest(c.Value)); err != nil {
			a.internalError(w, r, "end session", err)
			return
		}
	}
	for _, name := range []string{sessionCookie, tokenCookie} {
		http.SetCookie(w, &http.Cookie{Name: name, Path: "/", MaxAge: -1, HttpOnly: true, Secure: secureRequest(r)})
	}
	w.WriteHeader(http.StatusNoContent)
}

// meResponse tells the UI who is signed in and how one signs in.
type meResponse struct {
	User *models.User `json:"user,omitempty"`
	// Role is what the caller may do; empty when not signed in.
	Role string `json:"role,omitempty"`
	// Via is how the caller signed in: "session", "token" or "read cookie".
	Via        string `json:"via,omitempty"`
	PublicRead bool   `json:"public_read"`
	// Accounts says users can sign in with a password.
	Accounts bool `json:"accounts"`
	// Providers are the forges one can sign in through.
	Providers []string `json:"providers,omitempty"`
	// Signup says who can open an account: "first" while the server has no
	// users (the first becomes an admin), "open" when anyone can, empty
	// when only admins create accounts.
	Signup string `json:"signup,omitempty"`
	// ProviderSignup says forge users without an account get one when they
	// first sign in through their forge.
	ProviderSignup bool `json:"provider_signup,omitempty"`
}

func (a *Access) me(w http.ResponseWriter, r *http.Request) {
	resp := meResponse{PublicRead: a.cfg.PublicRead, Accounts: a.cfg.Accounts != nil, Providers: a.providerNames()}
	if a.cfg.Accounts != nil {
		switch {
		case !a.usersExist(r.Context()):
			resp.Signup = "first"
		case a.cfg.SignupRole != "":
			resp.Signup = "open"
		}
		resp.ProviderSignup = a.cfg.OAuth.SignupRole != "" && len(resp.Providers) > 0
	}
	if p, ok := a.who(r); ok {
		resp.User, resp.Role = p.user, string(p.role)
		switch {
		case p.user != nil && p.cookie:
			resp.Via = "session"
		case p.cookie:
			resp.Via = "read cookie"
		default:
			resp.Via = "token"
		}
	}
	writeJSON(w, http.StatusOK, resp)
}

// usersExist reports whether the server has any user. Once it has, it is
// remembered until the server restarts, sparing a query per page.
func (a *Access) usersExist(ctx context.Context) bool {
	if a.someUsers.Load() {
		return true
	}
	if a.hasUsers(ctx) {
		a.someUsers.Store(true)
		return true
	}
	return false
}

// registerUser opens an account for whoever asks, when the server takes
// sign-ups or has no users yet, and signs them in (201, the user).
func (a *Access) registerUser(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if !decode(w, r, &body) {
		writeError(w, http.StatusBadRequest, `body must be {"username": "...", "password": "..."}`)
		return
	}
	username := strings.ToLower(strings.TrimSpace(body.Username))
	if !auth.ValidUsername(username) {
		writeError(w, http.StatusBadRequest, "username may only contain a-z, 0-9, '.', '_' and '-', up to 39 characters")
		return
	}
	if len(body.Password) < auth.MinPasswordLen {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("password must be at least %d characters", auth.MinPasswordLen))
		return
	}
	hash, err := auth.HashPassword(body.Password)
	if err != nil {
		a.internalError(w, r, "hash password", err)
		return
	}
	ctx := r.Context()
	u, first, err := a.cfg.Accounts.RegisterUser(ctx, username, hash, string(a.cfg.SignupRole))
	switch {
	case errors.Is(err, db.ErrSignupClosed):
		writeError(w, http.StatusForbidden, "sign-up is closed on this server: ask an admin for an account")
		return
	case errors.Is(err, db.ErrUserExists):
		writeError(w, http.StatusConflict, "that username is taken")
		return
	case err != nil:
		a.internalError(w, r, "register user", err)
		return
	}
	a.someUsers.Store(true)
	a.log.InfoContext(ctx, "user signed up", "username", u.Username, "role", u.Role, "first", first, "remote_addr", r.RemoteAddr)
	if a.startSession(w, r, u) {
		writeJSON(w, http.StatusCreated, u)
	}
}

// signedInUser is the user behind a request that require let through, or
// writes a 403 for the server's own tokens, which belong to no user.
func signedInUser(w http.ResponseWriter, r *http.Request) (*models.User, bool) {
	p, ok := caller(r)
	if !ok || p.user == nil {
		writeError(w, http.StatusForbidden, "this needs a signed-in user, not the server's token")
		return nil, false
	}
	return p.user, true
}

func (a *Access) changePassword(w http.ResponseWriter, r *http.Request) {
	u, ok := signedInUser(w, r)
	if !ok {
		return
	}
	var body struct {
		Current string `json:"current"`
		New     string `json:"new"`
	}
	if !decode(w, r, &body) {
		writeError(w, http.StatusBadRequest, `body must be {"current": "...", "new": "..."}`)
		return
	}
	ctx := r.Context()
	_, hash, err := a.cfg.Accounts.User(ctx, u.Username)
	if err != nil {
		a.internalError(w, r, "read user", err)
		return
	}
	if hash != "" && !auth.CheckPassword(hash, body.Current) {
		writeError(w, http.StatusForbidden, "the current password is wrong")
		return
	}
	newHash, err := auth.HashPassword(body.New)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if _, err := a.cfg.Accounts.UpdateUser(ctx, u.Username, db.UserUpdate{PasswordHash: &newHash}); err != nil {
		a.internalError(w, r, "change password", err)
		return
	}
	a.log.InfoContext(ctx, "password changed", "username", u.Username)
	w.WriteHeader(http.StatusNoContent)
}

func (a *Access) listTokens(w http.ResponseWriter, r *http.Request) {
	u, ok := signedInUser(w, r)
	if !ok {
		return
	}
	tokens, err := a.cfg.Accounts.APITokens(r.Context(), u.ID)
	if err != nil {
		a.internalError(w, r, "list tokens", err)
		return
	}
	if tokens == nil {
		tokens = []models.APIToken{}
	}
	writeJSON(w, http.StatusOK, tokens)
}

// createToken makes a personal API token with the user's role. The token
// is in the answer only; the server keeps its digest.
func (a *Access) createToken(w http.ResponseWriter, r *http.Request) {
	u, ok := signedInUser(w, r)
	if !ok {
		return
	}
	var body struct {
		Name string `json:"name"`
	}
	if !decode(w, r, &body) || strings.TrimSpace(body.Name) == "" || len(body.Name) > 100 {
		writeError(w, http.StatusBadRequest, `body must be {"name": "..."}, up to 100 characters`)
		return
	}
	secret, digest, err := auth.NewSecret(auth.TokenPrefix)
	if err != nil {
		a.internalError(w, r, "create token", err)
		return
	}
	t, err := a.cfg.Accounts.CreateAPIToken(r.Context(), u.ID, strings.TrimSpace(body.Name), digest)
	if err != nil {
		a.internalError(w, r, "create token", err)
		return
	}
	a.log.InfoContext(r.Context(), "API token created", "username", u.Username, "token", t.Name)
	writeJSON(w, http.StatusCreated, struct {
		models.APIToken
		Token string `json:"token"`
	}{t, secret})
}

func (a *Access) deleteToken(w http.ResponseWriter, r *http.Request) {
	u, ok := signedInUser(w, r)
	if !ok {
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid token id")
		return
	}
	found, err := a.cfg.Accounts.DeleteAPIToken(r.Context(), u.ID, id)
	switch {
	case err != nil:
		a.internalError(w, r, "delete token", err)
	case !found:
		writeError(w, http.StatusNotFound, "token not found")
	default:
		w.WriteHeader(http.StatusNoContent)
	}
}

func (a *Access) listUsers(w http.ResponseWriter, r *http.Request) {
	users, err := a.cfg.Accounts.Users(r.Context())
	if err != nil {
		a.internalError(w, r, "list users", err)
		return
	}
	if users == nil {
		users = []models.User{}
	}
	writeJSON(w, http.StatusOK, users)
}

// userBody is what an admin sends to create or change a user.
type userBody struct {
	Username   string            `json:"username"`
	Role       *string           `json:"role"`
	Password   *string           `json:"password"`
	Disabled   *bool             `json:"disabled"`
	Identities map[string]string `json:"identities"`
}

// check validates the body's fields, returning the hash of a new password.
func (b userBody) check() (hash *string, err error) {
	if b.Role != nil {
		if _, err := auth.ParseRole(*b.Role); err != nil {
			return nil, err
		}
	}
	for forge, login := range b.Identities {
		if !knownForge(forge) || login == "" || len(login) > 100 {
			return nil, fmt.Errorf("identities: %q: %q is not a forge login", forge, login)
		}
	}
	if b.Password != nil {
		h := ""
		if *b.Password != "" {
			if h, err = auth.HashPassword(*b.Password); err != nil {
				return nil, err
			}
		}
		hash = &h
	}
	return hash, nil
}

func knownForge(name string) bool {
	switch name {
	case "github", "gitea", "forgejo", "gitlab", "bitbucket":
		return true
	}
	return false
}

func (a *Access) createUser(w http.ResponseWriter, r *http.Request) {
	var body userBody
	if !decode(w, r, &body) || body.Role == nil {
		writeError(w, http.StatusBadRequest, `body must be {"username": "...", "role": "...", "password": "...", "identities": {"<forge>": "<login>"}} with password and identities optional`)
		return
	}
	if !auth.ValidUsername(body.Username) {
		writeError(w, http.StatusBadRequest, "username may only contain a-z, 0-9, '.', '_' and '-', up to 39 characters")
		return
	}
	hash, err := body.check()
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if hash == nil {
		hash = new(string)
	}
	u, err := a.cfg.Accounts.CreateUser(r.Context(), models.User{Username: body.Username, Role: *body.Role, Identities: body.Identities}, *hash)
	if errors.Is(err, db.ErrUserExists) {
		writeError(w, http.StatusConflict, "the username or a forge login is taken")
		return
	}
	if err != nil {
		a.internalError(w, r, "create user", err)
		return
	}
	p, _ := caller(r)
	a.log.InfoContext(r.Context(), "user created", "username", u.Username, "role", u.Role, "by", p.name)
	writeJSON(w, http.StatusCreated, u)
}

// notYourself refuses changes that would lock the admin making them out.
func notYourself(w http.ResponseWriter, r *http.Request, username string) bool {
	if p, _ := caller(r); p.user != nil && p.user.Username == username {
		writeError(w, http.StatusForbidden, "you cannot demote, disable or delete yourself; another admin can")
		return false
	}
	return true
}

func (a *Access) updateUser(w http.ResponseWriter, r *http.Request) {
	username := r.PathValue("username")
	var body userBody
	if !decode(w, r, &body) || body.Username != "" {
		writeError(w, http.StatusBadRequest, `body must be {"role": "...", "password": "...", "disabled": true, "identities": {...}}, each optional`)
		return
	}
	hash, err := body.check()
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if (body.Role != nil && *body.Role != string(auth.RoleAdmin)) || (body.Disabled != nil && *body.Disabled) {
		if !notYourself(w, r, username) {
			return
		}
	}
	u, err := a.cfg.Accounts.UpdateUser(r.Context(), username, db.UserUpdate{
		Role: body.Role, PasswordHash: hash, Disabled: body.Disabled, Identities: body.Identities,
	})
	switch {
	case errors.Is(err, db.ErrUserNotFound):
		writeError(w, http.StatusNotFound, "user not found")
	case errors.Is(err, db.ErrUserExists):
		writeError(w, http.StatusConflict, "a forge login belongs to another user")
	case err != nil:
		a.internalError(w, r, "update user", err)
	default:
		p, _ := caller(r)
		a.log.InfoContext(r.Context(), "user changed", "username", u.Username, "role", u.Role, "disabled", u.Disabled, "by", p.name)
		writeJSON(w, http.StatusOK, u)
	}
}

func (a *Access) deleteUser(w http.ResponseWriter, r *http.Request) {
	username := r.PathValue("username")
	if !notYourself(w, r, username) {
		return
	}
	found, err := a.cfg.Accounts.DeleteUser(r.Context(), username)
	switch {
	case err != nil:
		a.internalError(w, r, "delete user", err)
	case !found:
		writeError(w, http.StatusNotFound, "user not found")
	default:
		p, _ := caller(r)
		a.log.InfoContext(r.Context(), "user deleted", "username", username, "by", p.name)
		w.WriteHeader(http.StatusNoContent)
	}
}

func (a *Access) internalError(w http.ResponseWriter, r *http.Request, what string, err error) {
	a.log.ErrorContext(r.Context(), what, "path", r.URL.Path, "error", err)
	writeError(w, http.StatusInternalServerError, "internal server error")
}

// BootstrapAdmin creates the admin username with password unless a user
// of that name exists, and reports whether it did. It gives a new server
// its first account (CISID_ADMIN_USER, CISID_ADMIN_PASSWORD).
func BootstrapAdmin(ctx context.Context, accounts AccountStore, username, password string) (bool, error) {
	if _, _, err := accounts.User(ctx, username); err == nil {
		return false, nil
	} else if !errors.Is(err, db.ErrUserNotFound) {
		return false, err
	}
	hash, err := auth.HashPassword(password)
	if err != nil {
		return false, fmt.Errorf("admin password: %w", err)
	}
	_, err = accounts.CreateUser(ctx, models.User{Username: username, Role: string(auth.RoleAdmin)}, hash)
	if errors.Is(err, db.ErrUserExists) {
		return false, nil // another server created it first
	}
	if err != nil {
		return false, err
	}
	return true, nil
}
