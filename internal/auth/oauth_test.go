package auth

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeForge answers token and user requests like a forge's OAuth server.
func fakeForge(t *testing.T, user string, basic bool) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/token":
			require.NoError(t, r.ParseForm())
			assert.Equal(t, "authorization_code", r.PostForm.Get("grant_type"))
			assert.Equal(t, "https://ci.example.com/auth/x/callback", r.PostForm.Get("redirect_uri"))
			id, secret, ok := r.BasicAuth()
			if basic {
				assert.True(t, ok)
				assert.Empty(t, r.PostForm.Get("client_secret"), "the secret goes in the header only")
			} else {
				id, secret = r.PostForm.Get("client_id"), r.PostForm.Get("client_secret")
			}
			assert.Equal(t, "client-id", id)
			assert.Equal(t, "client-secret", secret)
			w.Header().Set("Content-Type", "application/json")
			if r.PostForm.Get("code") != "good" {
				_, _ = w.Write([]byte(`{"error":"bad_verification_code"}`)) // GitHub's way: 200 with an error
				return
			}
			_, _ = w.Write([]byte(`{"access_token":"forge-token","token_type":"bearer"}`))
		case "/user":
			assert.Equal(t, "Bearer forge-token", r.Header.Get("Authorization"))
			_, _ = w.Write([]byte(user))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func point(p *Provider, base string) *Provider {
	p.TokenURL, p.UserURL = base+"/token", base+"/user"
	return p
}

const redirect = "https://ci.example.com/auth/x/callback"

func TestProvidersSignIn(t *testing.T) {
	tests := []struct {
		name     string
		provider func(base string) *Provider
		user     string
		basic    bool
		want     Identity
	}{
		{"github", func(b string) *Provider { return GitHubProvider("https://github.com", b, "client-id", "client-secret") },
			`{"id":1001,"login":"ahmet"}`, false, Identity{"github", "1001", "ahmet"}},
		{"gitea", func(b string) *Provider { return GiteaProvider("gitea", b, "client-id", "client-secret") },
			`{"id":7,"login":"ahmet.y","full_name":"Ahmet"}`, false, Identity{"gitea", "7", "ahmet.y"}},
		{"gitlab", func(b string) *Provider { return GitLabProvider(b, "client-id", "client-secret") },
			`{"id":42,"username":"ahmet"}`, false, Identity{"gitlab", "42", "ahmet"}},
		{"bitbucket", func(b string) *Provider { return BitbucketProvider(b, "client-id", "client-secret") },
			`{"uuid":"{a1b2}","username":"ahmet"}`, true, Identity{"bitbucket", "{a1b2}", "ahmet"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := fakeForge(t, tt.user, tt.basic)
			p := point(tt.provider(srv.URL), srv.URL)

			got, err := p.SignIn(t.Context(), "good", redirect)
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)

			_, err = p.SignIn(t.Context(), "stale", redirect)
			assert.ErrorIs(t, err, ErrOAuth, "a refused code is no sign-in")
		})
	}
}

func TestSignInNeedsAUser(t *testing.T) {
	srv := fakeForge(t, `{"login":"no-id"}`, false)
	p := point(GiteaProvider("gitea", srv.URL, "client-id", "client-secret"), srv.URL)
	_, err := p.SignIn(t.Context(), "good", redirect)
	assert.ErrorIs(t, err, ErrOAuth)
}

func TestAuthCodeURL(t *testing.T) {
	p := GitLabProvider("https://gitlab.example.com", "client-id", "client-secret")
	u, err := url.Parse(p.AuthCodeURL("state-123", redirect))
	require.NoError(t, err)
	assert.Equal(t, "gitlab.example.com", u.Host)
	assert.Equal(t, "/oauth/authorize", u.Path)
	q := u.Query()
	assert.Equal(t, "client-id", q.Get("client_id"))
	assert.Equal(t, "state-123", q.Get("state"))
	assert.Equal(t, redirect, q.Get("redirect_uri"))
	assert.Equal(t, "code", q.Get("response_type"))
	assert.Equal(t, "read_user", q.Get("scope"))
	assert.NotContains(t, u.String(), "client-secret")
}

func TestConnectURLAsksForRepositoryScopes(t *testing.T) {
	u, err := url.Parse(GitHubProvider("https://github.com", "https://api.github.com", "client-id", "client-secret").ConnectURL("s", redirect))
	require.NoError(t, err)
	assert.Equal(t, "repo", u.Query().Get("scope"))
	u, err = url.Parse(GitLabProvider("https://gitlab.com", "client-id", "client-secret").ConnectURL("s", redirect))
	require.NoError(t, err)
	assert.Equal(t, "api", u.Query().Get("scope"), "GitLab's api scope sets commit statuses")
	u, err = url.Parse(GitLabProvider("https://gitlab.com", "client-id", "client-secret").AuthCodeURL("s", redirect))
	require.NoError(t, err)
	assert.Equal(t, "read_user", u.Query().Get("scope"), "signing in still asks for no more than the user")
}

func TestExchangeAndRefreshKeepTheWholeToken(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.NoError(t, r.ParseForm())
		w.Header().Set("Content-Type", "application/json")
		switch r.PostForm.Get("grant_type") {
		case "authorization_code":
			_, _ = w.Write([]byte(`{"access_token":"a1","refresh_token":"r1","expires_in":7200}`))
		case "refresh_token":
			assert.Equal(t, "r1", r.PostForm.Get("refresh_token"))
			assert.Equal(t, "client-secret", r.PostForm.Get("client_secret"))
			_, _ = w.Write([]byte(`{"access_token":"a2","refresh_token":"r2","expires_in":7200}`))
		}
	}))
	t.Cleanup(srv.Close)
	p := GitLabProvider(srv.URL, "client-id", "client-secret")
	p.TokenURL = srv.URL + "/token"

	tok, err := p.Exchange(t.Context(), "good", redirect)
	require.NoError(t, err)
	assert.Equal(t, "a1", tok.AccessToken)
	assert.Equal(t, "r1", tok.RefreshToken)
	assert.WithinDuration(t, time.Now().Add(2*time.Hour), tok.Expiry, time.Minute)

	tok, err = p.Refresh(t.Context(), "r1", redirect)
	require.NoError(t, err)
	assert.Equal(t, "a2", tok.AccessToken)
	assert.Equal(t, "r2", tok.RefreshToken)
}
