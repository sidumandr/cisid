package forge

import (
	"context"
	"errors"
)

// ErrNoAccounts is returned for forges whose users cannot connect their
// own accounts: their clients cannot take another token or say whose it is.
var ErrNoAccounts = errors.New("this forge does not take users' own tokens")

// Tokened is a client that can work with another token than its own, as
// a user's own access to the forge.
type Tokened interface {
	WithToken(token string) Client
}

// Identifier is a client that can say whose its token is.
type Identifier interface {
	// Whoami returns the login of the token's user.
	Whoami(ctx context.Context) (string, error)
}

// Credentials gives the token to reach the repository key with, when one
// of its users' own accounts is to be used rather than the server's token.
type Credentials func(ctx context.Context, key string) (token string, ok bool)

type accessKey struct{ forge string }

// WithAccess returns a context in which the forge called name is reached
// with token, as when a user connects a repository with their own access.
func WithAccess(ctx context.Context, name, token string) context.Context {
	return context.WithValue(ctx, accessKey{name}, token)
}

// AccessFrom returns the token WithAccess put in ctx for the forge called
// name.
func AccessFrom(ctx context.Context, name string) (string, bool) {
	token, ok := ctx.Value(accessKey{name}).(string)
	return token, ok && token != ""
}

// UseCredentials makes the Set reach each repository with the token creds
// gives for it; repositories it gives none for use the server's token.
func (s *Set) UseCredentials(creds Credentials) {
	s.creds = creds
}

// client returns the client of the forge called name, reaching it with
// the token of the context, else the one creds gives for key, else its own.
func (s *Set) client(ctx context.Context, name, key string) (Client, bool) {
	c, ok := s.clients[name]
	if !ok {
		return nil, false
	}
	t, tokened := c.(Tokened)
	if !tokened {
		return c, true
	}
	if token, ok := AccessFrom(ctx, name); ok {
		return t.WithToken(token), true
	}
	if s.creds != nil && key != "" {
		if token, ok := s.creds(ctx, key); ok {
			return t.WithToken(token), true
		}
	}
	return c, true
}

// Whoami returns whose token is on the forge called name: the login of
// its user. A token the forge does not take is ErrNotFound or another
// error of the forge.
func (s *Set) Whoami(ctx context.Context, name, token string) (string, error) {
	c, ok := s.clients[name]
	if !ok {
		return "", ErrUnknownForge
	}
	t, tokened := c.(Tokened)
	if !tokened {
		return "", ErrNoAccounts
	}
	id, ok := t.WithToken(token).(Identifier)
	if !ok {
		return "", ErrNoAccounts
	}
	return id.Whoami(ctx)
}

// Accounts names the forges whose users can connect their own accounts.
func (s *Set) Accounts() []string {
	var names []string
	for _, n := range s.Names() {
		if t, ok := s.clients[n].(Tokened); ok {
			if _, ok := t.WithToken("").(Identifier); ok {
				names = append(names, n)
			}
		}
	}
	return names
}
