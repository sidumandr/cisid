package api

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sidumandr/cisid/internal/auth"
	"github.com/sidumandr/cisid/pkg/models"
)

// fakeGitea is a Gitea OAuth server that signs in whoever login names.
func fakeGitea(t *testing.T, id, login string) *auth.Provider {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/login/oauth/access_token":
			_ = r.ParseForm()
			if r.PostForm.Get("code") != "good" || r.PostForm.Get("redirect_uri") != "https://ci.example.com/auth/gitea/callback" {
				http.Error(w, `{"error":"invalid_grant"}`, http.StatusBadRequest)
				return
			}
			_, _ = w.Write([]byte(`{"access_token":"t"}`))
		case "/api/v1/user":
			_, _ = w.Write([]byte(`{"id":` + id + `,"login":"` + login + `"}`))
		}
	}))
	t.Cleanup(srv.Close)
	return auth.GiteaProvider("gitea", srv.URL, "client-id", "client-secret")
}

func oauthRouter(m *memAccounts, p *auth.Provider, signup auth.Role) http.Handler {
	access := NewAccess(AccessConfig{
		PublicRead: true, Accounts: m, Log: slog.New(slog.DiscardHandler),
		OAuth: OAuthConfig{Providers: []*auth.Provider{p}, PublicURL: "https://ci.example.com", SignupRole: signup},
	})
	return NewRouter(Handlers{Agents: http.NotFoundHandler(), Access: access, DB: &mockPinger{}}, slog.New(slog.DiscardHandler))
}

// startForgeSignIn follows /auth/gitea/login and returns the state the
// forge would echo and the cookie that carries it.
func startForgeSignIn(t *testing.T, h http.Handler, next string) (string, *http.Cookie) {
	t.Helper()
	rec := send(h, httptest.NewRequest(http.MethodGet, "/auth/gitea/login?next="+url.QueryEscape(next), nil))
	require.Equal(t, http.StatusFound, rec.Code)
	to, err := url.Parse(rec.Header().Get("Location"))
	require.NoError(t, err)
	assert.Equal(t, "/login/oauth/authorize", to.Path)
	assert.Equal(t, "https://ci.example.com/auth/gitea/callback", to.Query().Get("redirect_uri"))
	cookies := rec.Result().Cookies()
	require.Len(t, cookies, 1)
	assert.Equal(t, http.SameSiteLaxMode, cookies[0].SameSite, "the cookie must come back with the forge's redirect")
	return to.Query().Get("state"), cookies[0]
}

func callback(h http.Handler, c *http.Cookie, state, code string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, "/auth/gitea/callback?code="+code+"&state="+url.QueryEscape(state), nil)
	if c != nil {
		req.AddCookie(c)
	}
	return send(h, req)
}

func sessionOf(rec *httptest.ResponseRecorder) *http.Cookie {
	for _, c := range rec.Result().Cookies() {
		if c.Name == sessionCookie && c.MaxAge > 0 {
			return c
		}
	}
	return nil
}

func TestForgeSignInOfALinkedUser(t *testing.T) {
	m := newMemAccounts()
	_, err := m.CreateUser(t.Context(), models.User{Username: "ahmet", Role: "maintainer", Identities: map[string]string{"gitea": "ahmet.y"}}, "")
	require.NoError(t, err)
	h := oauthRouter(m, fakeGitea(t, "7", "ahmet.y"), "")

	state, c := startForgeSignIn(t, h, "/repos")
	rec := callback(h, c, state, "good")

	require.Equal(t, http.StatusFound, rec.Code)
	assert.Equal(t, "/repos", rec.Header().Get("Location"), "back to where the sign-in started")
	session := sessionOf(rec)
	require.NotNil(t, session)
	me := as(h, session, http.MethodGet, "/api/me", "", false)
	assert.Contains(t, me.Body.String(), `"username":"ahmet"`)
	assert.Contains(t, me.Body.String(), `"providers":["gitea"]`)
}

func TestForgeSignInRefusals(t *testing.T) {
	m := newMemAccounts()
	h := oauthRouter(m, fakeGitea(t, "7", "stranger"), "")

	state, c := startForgeSignIn(t, h, "/")
	for name, tc := range map[string]struct {
		cookie *http.Cookie
		state  string
		code   string
		want   string
	}{
		"no cookie":   {nil, state, "good", "expired"},
		"wrong state": {c, state + "x", "good", "expired"},
		"bad code":    {c, state, "stale", "failed"},
		"no account":  {c, state, "good", "unknown"},
	} {
		rec := callback(h, tc.cookie, tc.state, tc.code)
		assert.Equal(t, http.StatusFound, rec.Code, name)
		assert.Equal(t, "/login?signin="+tc.want, rec.Header().Get("Location"), name)
		assert.Nil(t, sessionOf(rec), name)
	}
	n, _ := m.CountUsers(t.Context())
	assert.Zero(t, n, "without signup, no account is made")

	rec := send(h, httptest.NewRequest(http.MethodGet, "/auth/gitlab/login", nil))
	assert.Equal(t, http.StatusNotFound, rec.Code, "a forge that is not set up")
}

func TestForgeSignUp(t *testing.T) {
	m := newMemAccounts()
	_, err := m.CreateUser(t.Context(), models.User{Username: "ahmet.y", Role: "admin"}, "")
	require.NoError(t, err)
	h := oauthRouter(m, fakeGitea(t, "7", "Ahmet.Y"), auth.RoleViewer)

	state, c := startForgeSignIn(t, h, "/")
	rec := callback(h, c, state, "good")
	require.NotNil(t, sessionOf(rec), rec.Header().Get("Location"))

	u, _, err := m.User(t.Context(), "ahmet.y-gitea")
	require.NoError(t, err, "the login was taken, so the forge's name is added")
	assert.Equal(t, "viewer", u.Role, "new users get the signup role, never the taken account's")
	assert.Equal(t, map[string]string{"gitea": "Ahmet.Y"}, u.Identities)
}

func TestForgeSignInStaysOnSite(t *testing.T) {
	m := newMemAccounts()
	_, err := m.CreateUser(t.Context(), models.User{Username: "ahmet", Role: "viewer", Identities: map[string]string{"gitea": "ahmet"}}, "")
	require.NoError(t, err)
	h := oauthRouter(m, fakeGitea(t, "7", "ahmet"), "")

	for _, next := range []string{"https://evil.example/", "//evil.example/x", "/\\evil.example"} {
		state, c := startForgeSignIn(t, h, next)
		rec := callback(h, c, state, "good")
		assert.Equal(t, "/", rec.Header().Get("Location"), "%s: no open redirect", next)
	}
}

func TestSignupNames(t *testing.T) {
	assert.Equal(t, []string{"ahmet.y", "ahmet.y-gitea"}, signupNames(auth.Identity{Forge: "gitea", Login: "Ahmet.Y"}))
	assert.Equal(t, []string{"ahmet-y-z", "ahmet-y-z-github"}, signupNames(auth.Identity{Forge: "github", Login: "ahmet y@z"}))
	assert.Equal(t, []string{"x-bitbucket"}, signupNames(auth.Identity{Forge: "bitbucket", Login: "_x"})[1:])
}
