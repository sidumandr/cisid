package api

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sidumandr/cisid/internal/auth"
	"github.com/sidumandr/cisid/internal/forge"
	"github.com/sidumandr/cisid/internal/forgeauth"
	"github.com/sidumandr/cisid/pkg/models"
)

// fakeForgeAccounts takes tokens that start with "users", and remembers
// the OAuth tokens it was given.
type fakeForgeAccounts struct {
	mu     sync.Mutex
	tokens map[string]string // "userID forge"
	oauth  []auth.Token
}

func fkey(userID int64, f string) string {
	return strings.Join([]string{string(rune('0' + userID)), f}, " ")
}

func (f *fakeForgeAccounts) Forges() []string { return []string{"gitea", "github"} }

func (f *fakeForgeAccounts) Accounts(_ context.Context, userID int64) ([]models.ForgeAccount, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []models.ForgeAccount
	for _, name := range f.Forges() {
		if _, ok := f.tokens[fkey(userID, name)]; ok {
			out = append(out, models.ForgeAccount{Forge: name, Login: "login", Kind: forgeauth.KindToken})
		}
	}
	return out, nil
}

func (f *fakeForgeAccounts) put(userID int64, name, token string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.tokens == nil {
		f.tokens = map[string]string{}
	}
	f.tokens[fkey(userID, name)] = token
}

func (f *fakeForgeAccounts) ConnectToken(_ context.Context, userID int64, name, token string) (models.ForgeAccount, error) {
	if !strings.HasPrefix(token, "users") {
		return models.ForgeAccount{}, forgeauth.ErrTokenRejected
	}
	f.put(userID, name, token)
	return models.ForgeAccount{Forge: name, Login: "sidumandr", Kind: forgeauth.KindToken}, nil
}

func (f *fakeForgeAccounts) ConnectOAuth(_ context.Context, userID int64, name string, tok auth.Token) (models.ForgeAccount, error) {
	f.mu.Lock()
	f.oauth = append(f.oauth, tok)
	f.mu.Unlock()
	f.put(userID, name, tok.AccessToken)
	return models.ForgeAccount{Forge: name, Login: "sidumandr", Kind: forgeauth.KindOAuth}, nil
}

func (f *fakeForgeAccounts) Disconnect(_ context.Context, userID int64, name string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	_, ok := f.tokens[fkey(userID, name)]
	delete(f.tokens, fkey(userID, name))
	return ok, nil
}

func (f *fakeForgeAccounts) Token(_ context.Context, userID int64, name string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	token, ok := f.tokens[fkey(userID, name)]
	if !ok {
		return "", forgeauth.ErrNotConnected
	}
	return token, nil
}

// privateForges sees sidumandr/private only with a user's own access.
type privateForges struct{ stubForges }

func (privateForges) Repository(ctx context.Context, key string) (forge.Repository, error) {
	if key == "sidumandr/private" {
		if token, ok := forge.AccessFrom(ctx, forge.GitHub); !ok || token != "users-token" {
			return forge.Repository{}, forge.ErrNotFound
		}
		return forge.Repository{CloneURL: "https://github.com/sidumandr/private.git", DefaultBranch: "main"}, nil
	}
	return stubForges{}.Repository(ctx, key)
}

func (privateForges) Repositories(ctx context.Context) forge.Listing {
	if _, ok := forge.AccessFrom(ctx, forge.GitHub); ok {
		return forge.Listing{Repos: []forge.Listed{{Repo: "sidumandr/private", Forge: forge.GitHub, Private: true}}, Unlisted: []string{}}
	}
	return forge.Listing{Repos: []forge.Listed{}, Unlisted: []string{forge.GitHub}}
}

func forgeAccountsRouter(t *testing.T, m *memAccounts, fa ForgeAccounts, repos RepoStore) http.Handler {
	t.Helper()
	p := fakeGitea(t, "7", "sidumandr")
	p.ConnectScopes = []string{"repo"}
	cfg := AccessConfig{
		PublicRead: true, Accounts: m, Log: slog.New(slog.DiscardHandler),
		OAuth: OAuthConfig{Providers: []*auth.Provider{p}, PublicURL: "https://ci.example.com"},
	}
	if fa != nil {
		cfg.ForgeAccounts = fa
	}
	access := NewAccess(cfg)
	return NewRouter(Handlers{
		Agents: http.NotFoundHandler(), Access: access, DB: &mockPinger{},
		Repos: NewReposAPI(repos, privateForges{}, access, slog.New(slog.DiscardHandler), true),
	}, slog.New(slog.DiscardHandler))
}

func TestForgeAccountsAPI(t *testing.T) {
	m := newMemAccounts()
	addUser(t, m, "ilhan", "maintainer")
	fa := &fakeForgeAccounts{}
	h := forgeAccountsRouter(t, m, fa, &memRepos{})
	c := signIn(t, h, "ilhan")

	assert.Equal(t, http.StatusForbidden, as(h, nil, http.MethodGet, "/api/me/forges", "", true).Code, "a reader who is not a user has no accounts")
	rec := as(h, c, http.MethodGet, "/api/me/forges", "", true)
	require.Equal(t, http.StatusOK, rec.Code)
	assert.JSONEq(t, `{"enabled":true,"forges":[{"forge":"gitea","oauth":true},{"forge":"github","oauth":false}],"accounts":[]}`, rec.Body.String(),
		"gitea has an OAuth app that can connect; github takes tokens")

	rec = as(h, c, http.MethodPut, "/api/me/forges/github", `{"token":" users-token "}`, true)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), `"login":"sidumandr"`)
	assert.Equal(t, "users-token", fa.tokens[fkey(m.users["ilhan"].ID, "github")], "trimmed")
	assert.Equal(t, http.StatusBadRequest, as(h, c, http.MethodPut, "/api/me/forges/github", `{"token":"wrong"}`, true).Code)
	assert.Equal(t, http.StatusBadRequest, as(h, c, http.MethodPut, "/api/me/forges/github", `{}`, true).Code)
	assert.Equal(t, http.StatusNotFound, as(h, c, http.MethodPut, "/api/me/forges/bitbucket", `{"token":"users-x"}`, true).Code)

	assert.Equal(t, http.StatusNoContent, as(h, c, http.MethodDelete, "/api/me/forges/github", "", true).Code)
	assert.Equal(t, http.StatusNotFound, as(h, c, http.MethodDelete, "/api/me/forges/github", "", true).Code)

	off := forgeAccountsRouter(t, m, nil, &memRepos{})
	c = signIn(t, off, "ilhan")
	rec = as(off, c, http.MethodGet, "/api/me/forges", "", true)
	require.Equal(t, http.StatusOK, rec.Code)
	assert.JSONEq(t, `{"enabled":false,"forges":[],"accounts":[]}`, rec.Body.String())
	assert.Equal(t, http.StatusConflict, as(off, c, http.MethodPut, "/api/me/forges/github", `{"token":"users-token"}`, true).Code,
		"no secret key to seal tokens with")
}

func TestConnectAForgeByOAuth(t *testing.T) {
	m := newMemAccounts()
	addUser(t, m, "ilhan", "maintainer")
	fa := &fakeForgeAccounts{}
	h := forgeAccountsRouter(t, m, fa, &memRepos{})

	rec := send(h, httptest.NewRequest(http.MethodGet, "/auth/gitea/connect?next=/account", nil))
	require.Equal(t, http.StatusFound, rec.Code)
	assert.Equal(t, "/login?next=%2Faccount", rec.Header().Get("Location"), "connecting needs a signed-in user")

	session := signIn(t, h, "ilhan")
	req := httptest.NewRequest(http.MethodGet, "/auth/gitea/connect?next=/account", nil)
	req.AddCookie(session)
	rec = send(h, req)
	require.Equal(t, http.StatusFound, rec.Code)
	to, err := url.Parse(rec.Header().Get("Location"))
	require.NoError(t, err)
	assert.Equal(t, "repo", to.Query().Get("scope"), "connecting asks for the repositories")
	state := to.Query().Get("state")
	oauthC := rec.Result().Cookies()[0]

	cb := func(query string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/auth/gitea/callback?"+query+"&state="+url.QueryEscape(state), nil)
		req.AddCookie(oauthC)
		req.AddCookie(session)
		return send(h, req)
	}
	rec = cb("error=access_denied")
	assert.Equal(t, "/account?connect=denied", rec.Header().Get("Location"))

	rec = cb("code=good")
	require.Equal(t, http.StatusFound, rec.Code)
	assert.Equal(t, "/account?connected=gitea", rec.Header().Get("Location"))
	require.Len(t, fa.oauth, 1)
	assert.Equal(t, "t", fa.oauth[0].AccessToken)
	assert.Nil(t, sessionOf(rec), "connecting does not start another session")

	rec = cb("code=stale")
	assert.Equal(t, "/account?connect=failed", rec.Header().Get("Location"))
}

func TestConnectARepositoryWithOnesOwnAccess(t *testing.T) {
	m := newMemAccounts()
	addUser(t, m, "ilhan", "maintainer")
	addUser(t, m, "ayse", "maintainer")
	fa := &fakeForgeAccounts{}
	fa.put(m.users["ilhan"].ID, "github", "users-token")
	repos := &memRepos{}
	h := forgeAccountsRouter(t, m, fa, repos)

	ayse := signIn(t, h, "ayse")
	assert.Equal(t, http.StatusNotFound, as(h, ayse, http.MethodPost, "/api/repos", `{"repo":"sidumandr/private","poll":true}`, true).Code,
		"the server's token does not see it")
	rec := as(h, ayse, http.MethodGet, "/api/forges/repositories", "", true)
	assert.JSONEq(t, `{"repositories":[],"unlisted":["github"]}`, rec.Body.String())

	ilhan := signIn(t, h, "ilhan")
	rec = as(h, ilhan, http.MethodGet, "/api/forges/repositories", "", true)
	assert.Contains(t, rec.Body.String(), `"sidumandr/private"`, "one's own access lists one's repositories")

	rec = as(h, ilhan, http.MethodPost, "/api/repos", `{"repo":"sidumandr/private","poll":true}`, true)
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	var got models.Repository
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	assert.Equal(t, "ilhan", got.Via)
	require.NotNil(t, repos.repos["sidumandr/private"].ViaUserID)
	assert.Equal(t, m.users["ilhan"].ID, *repos.repos["sidumandr/private"].ViaUserID, "built with ilhan's access from now on")

	rec = as(h, ilhan, http.MethodPost, "/api/repos", `{"repo":"sidumandr/biopotan","poll":true}`, true)
	require.Equal(t, http.StatusCreated, rec.Code)
	assert.NotNil(t, repos.repos["sidumandr/biopotan"].ViaUserID, "his access is used for any repository of the forge he connected")
}
