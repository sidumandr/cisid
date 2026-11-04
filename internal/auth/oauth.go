package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// ErrOAuth is returned when a forge refuses a sign-in or answers oddly.
var ErrOAuth = errors.New("forge sign-in failed")

const oauthTimeout = 15 * time.Second

// Identity is who a forge says signed in.
type Identity struct {
	Forge string
	// Subject is the forge's stable user id, which survives renames.
	Subject string
	Login   string
}

// Provider signs users in through a forge with OAuth 2.0's authorization
// code flow.
type Provider struct {
	Name         string // the forge, e.g. "gitea"
	ClientID     string
	ClientSecret string
	AuthURL      string
	TokenURL     string
	UserURL      string
	Scopes       []string
	// BasicAuth sends the client's credentials to TokenURL as HTTP Basic
	// auth, as Bitbucket wants, instead of in the form.
	BasicAuth bool
	// HTTP sends the requests; a client with a timeout when nil.
	HTTP *http.Client

	user func(data []byte) (subject, login string, err error)
}

// AuthCodeURL is where the browser goes to sign in; the forge sends it
// back to redirectURI with a code and state.
func (p *Provider) AuthCodeURL(state, redirectURI string) string {
	q := url.Values{
		"client_id":     {p.ClientID},
		"redirect_uri":  {redirectURI},
		"response_type": {"code"},
		"state":         {state},
	}
	if len(p.Scopes) > 0 {
		q.Set("scope", strings.Join(p.Scopes, " "))
	}
	sep := "?"
	if strings.Contains(p.AuthURL, "?") {
		sep = "&"
	}
	return p.AuthURL + sep + q.Encode()
}

// SignIn exchanges code for an access token and asks the forge who it
// belongs to. The token is used for that one question and forgotten.
func (p *Provider) SignIn(ctx context.Context, code, redirectURI string) (Identity, error) {
	token, err := p.exchange(ctx, code, redirectURI)
	if err != nil {
		return Identity{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.UserURL, nil)
	if err != nil {
		return Identity{}, fmt.Errorf("build user request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "cisid")
	data, err := p.do(req)
	if err != nil {
		return Identity{}, fmt.Errorf("ask %s who signed in: %w", p.Name, err)
	}
	subject, login, err := p.user(data)
	if err != nil {
		return Identity{}, fmt.Errorf("%w: %s user: %w", ErrOAuth, p.Name, err)
	}
	if subject == "" || login == "" {
		return Identity{}, fmt.Errorf("%w: %s did not say who signed in", ErrOAuth, p.Name)
	}
	return Identity{Forge: p.Name, Subject: subject, Login: login}, nil
}

func (p *Provider) exchange(ctx context.Context, code, redirectURI string) (string, error) {
	form := url.Values{
		"grant_type":   {"authorization_code"},
		"code":         {code},
		"redirect_uri": {redirectURI},
	}
	if !p.BasicAuth {
		form.Set("client_id", p.ClientID)
		form.Set("client_secret", p.ClientSecret)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.TokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return "", fmt.Errorf("build token request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "cisid")
	if p.BasicAuth {
		req.SetBasicAuth(p.ClientID, p.ClientSecret)
	}
	data, err := p.do(req)
	if err != nil {
		return "", fmt.Errorf("exchange %s code: %w", p.Name, err)
	}
	var resp struct {
		AccessToken string `json:"access_token"`
		Error       string `json:"error"` // GitHub answers 200 with an error
	}
	if err := json.Unmarshal(data, &resp); err != nil {
		return "", fmt.Errorf("%w: %s token answer: %w", ErrOAuth, p.Name, err)
	}
	if resp.AccessToken == "" {
		return "", fmt.Errorf("%w: %s gave no token: %s", ErrOAuth, p.Name, resp.Error)
	}
	return resp.AccessToken, nil
}

// do sends req and returns the body of a 2xx answer, up to 1 MiB.
func (p *Provider) do(req *http.Request) ([]byte, error) {
	client := p.HTTP
	if client == nil {
		client = &http.Client{Timeout: oauthTimeout}
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrOAuth, err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("%w: read answer: %w", ErrOAuth, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, fmt.Errorf("%w: %s: %.200s", ErrOAuth, resp.Status, data)
	}
	return data, nil
}

// numericUser reads {"id": 123, "<loginField>": "..."}.
func numericUser(loginField string) func([]byte) (string, string, error) {
	return func(data []byte) (string, string, error) {
		var u map[string]json.RawMessage
		if err := json.Unmarshal(data, &u); err != nil {
			return "", "", err
		}
		var id int64
		var login string
		if err := json.Unmarshal(u["id"], &id); err != nil || id <= 0 {
			return "", "", fmt.Errorf("no user id")
		}
		if err := json.Unmarshal(u[loginField], &login); err != nil {
			return "", "", fmt.Errorf("no %s", loginField)
		}
		return strconv.FormatInt(id, 10), login, nil
	}
}

func bitbucketUser(data []byte) (string, string, error) {
	var u struct {
		UUID     string `json:"uuid"`
		Username string `json:"username"`
	}
	if err := json.Unmarshal(data, &u); err != nil {
		return "", "", err
	}
	return u.UUID, u.Username, nil
}

// GitHubProvider signs in through GitHub; webURL is https://github.com or
// a GitHub Enterprise Server, apiURL its REST API.
func GitHubProvider(webURL, apiURL, clientID, secret string) *Provider {
	return &Provider{
		Name: "github", ClientID: clientID, ClientSecret: secret,
		AuthURL: webURL + "/login/oauth/authorize", TokenURL: webURL + "/login/oauth/access_token",
		UserURL: apiURL + "/user", user: numericUser("login"),
	}
}

// GiteaProvider signs in through a Gitea or Forgejo server (name is
// "gitea" or "forgejo").
func GiteaProvider(name, webURL, clientID, secret string) *Provider {
	return &Provider{
		Name: name, ClientID: clientID, ClientSecret: secret,
		AuthURL: webURL + "/login/oauth/authorize", TokenURL: webURL + "/login/oauth/access_token",
		UserURL: webURL + "/api/v1/user", Scopes: []string{"read:user"}, user: numericUser("login"),
	}
}

// GitLabProvider signs in through GitLab.com or a self-managed instance.
func GitLabProvider(webURL, clientID, secret string) *Provider {
	return &Provider{
		Name: "gitlab", ClientID: clientID, ClientSecret: secret,
		AuthURL: webURL + "/oauth/authorize", TokenURL: webURL + "/oauth/token",
		UserURL: webURL + "/api/v4/user", Scopes: []string{"read_user"}, user: numericUser("username"),
	}
}

// BitbucketProvider signs in through Bitbucket Cloud; apiURL is its REST
// API (https://api.bitbucket.org/2.0).
func BitbucketProvider(apiURL, clientID, secret string) *Provider {
	return &Provider{
		Name: "bitbucket", ClientID: clientID, ClientSecret: secret,
		AuthURL: "https://bitbucket.org/site/oauth2/authorize", TokenURL: "https://bitbucket.org/site/oauth2/access_token",
		UserURL: apiURL + "/user", Scopes: []string{"account"}, BasicAuth: true, user: bitbucketUser,
	}
}
