package api

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"
)

// tokenCookie keeps a browser signed in for reading. It holds a digest of
// the token, never the token, and only ever grants reading.
const tokenCookie = "cisid_token"

// ReadAccess guards what can be read: jobs, logs and metrics. Without a
// read token anyone may read. With one, a request needs the read token or
// the API token, as "Authorization: Bearer <token>" or through the cookie
// that POST /api/login sets, which the UI's log streams rely on: an
// EventSource cannot send headers.
type ReadAccess struct {
	tokens  []string
	digests []string
}

// NewReadAccess returns the access rules for readToken; the API token,
// which may change things, may read too.
func NewReadAccess(readToken, apiToken string) *ReadAccess {
	if readToken == "" {
		return &ReadAccess{}
	}
	a := &ReadAccess{}
	for _, t := range []string{readToken, apiToken} {
		if t != "" {
			a.tokens = append(a.tokens, t)
			a.digests = append(a.digests, cookieDigest(t))
		}
	}
	return a
}

// Open reports whether anyone may read.
func (a *ReadAccess) Open() bool {
	return a == nil || len(a.tokens) == 0
}

func cookieDigest(token string) string {
	sum := sha256.Sum256([]byte("cisid read access\x00" + token))
	return hex.EncodeToString(sum[:])
}

// matchToken compares got with every token in constant time.
func matchToken(got string, tokens []string) bool {
	ok := 0
	for _, t := range tokens {
		ok |= subtle.ConstantTimeCompare([]byte(got), []byte(t))
	}
	return ok == 1
}

func (a *ReadAccess) allows(r *http.Request) bool {
	if a.Open() {
		return true
	}
	if got, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer "); ok && matchToken(got, a.tokens) {
		return true
	}
	c, err := r.Cookie(tokenCookie)
	return err == nil && matchToken(c.Value, a.digests)
}

// Wrap lets only requests that may read reach next.
func (a *ReadAccess) Wrap(next http.Handler) http.Handler {
	if a.Open() {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !a.allows(r) {
			w.Header().Set("WWW-Authenticate", `Bearer realm="cisid"`)
			writeError(w, http.StatusUnauthorized, "missing or wrong read token")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (a *ReadAccess) wrapFunc(next http.HandlerFunc) http.HandlerFunc {
	return a.Wrap(next).ServeHTTP
}

func (a *ReadAccess) register(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/login", a.login)
	mux.HandleFunc("POST /api/logout", a.logout)
}

// login checks a token and signs the browser in for reading (204), or
// answers 401. With reading open there is nothing to sign in to.
func (a *ReadAccess) login(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&body); err != nil || body.Token == "" {
		writeError(w, http.StatusBadRequest, `body must be {"token": "..."}`)
		return
	}
	if a.Open() {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if !matchToken(body.Token, a.tokens) {
		writeError(w, http.StatusUnauthorized, "wrong token")
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     tokenCookie,
		Value:    cookieDigest(body.Token),
		Path:     "/",
		HttpOnly: true,
		Secure:   secureRequest(r),
		SameSite: http.SameSiteStrictMode,
	})
	w.WriteHeader(http.StatusNoContent)
}

func (a *ReadAccess) logout(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name: tokenCookie, Path: "/", MaxAge: -1,
		HttpOnly: true, Secure: secureRequest(r), SameSite: http.SameSiteStrictMode,
	})
	w.WriteHeader(http.StatusNoContent)
}

// secureRequest reports whether the browser reached the server over HTTPS,
// directly or through a proxy that says so.
func secureRequest(r *http.Request) bool {
	return r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
}
