// Package auth holds cisid's accounts logic that needs no HTTP: roles,
// password hashing, and the random secrets behind sessions and API tokens.
package auth

import (
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// Role is what a user may do. Each role may do what the ones before it may.
type Role string

const (
	// RoleViewer reads jobs, logs and metrics.
	RoleViewer Role = "viewer"
	// RoleMaintainer also runs, cancels and reruns builds and manages
	// schedules, polled branches and secrets.
	RoleMaintainer Role = "maintainer"
	// RoleAdmin also manages users.
	RoleAdmin Role = "admin"
)

var ranks = map[Role]int{RoleViewer: 1, RoleMaintainer: 2, RoleAdmin: 3}

// ErrInvalidRole is returned for a role that is not one of the three.
var ErrInvalidRole = errors.New("role must be viewer, maintainer or admin")

// ParseRole checks s is a role.
func ParseRole(s string) (Role, error) {
	r := Role(s)
	if ranks[r] == 0 {
		return "", fmt.Errorf("%w: %q", ErrInvalidRole, s)
	}
	return r, nil
}

// Allows reports whether r may do what need may.
func (r Role) Allows(need Role) bool {
	return ranks[r] > 0 && ranks[r] >= ranks[need]
}

var usernameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,38}$`)

// ValidUsername accepts lowercase letters, digits, ".", "_" and "-", up to
// 39 characters, starting with a letter or digit.
func ValidUsername(s string) bool {
	return usernameRe.MatchString(s)
}

// MinPasswordLen keeps passwords out of guessing range.
const MinPasswordLen = 10

// ErrWeakPassword is returned for a password shorter than MinPasswordLen.
var ErrWeakPassword = fmt.Errorf("password must be at least %d characters", MinPasswordLen)

// Password hashing parameters: PBKDF2-HMAC-SHA256 with OWASP's iteration
// count for it, from the standard library.
const (
	hashScheme     = "pbkdf2-sha256"
	hashIterations = 600_000
	saltLen        = 16
	keyLen         = 32
)

// HashPassword returns a salted hash of password to store.
func HashPassword(password string) (string, error) {
	return HashPasswordWith(password, hashIterations)
}

// HashPasswordWith hashes with the given iteration count, which the hash
// records; CheckPassword reads it back. Tests use a low one to stay fast.
func HashPasswordWith(password string, iterations int) (string, error) {
	if len(password) < MinPasswordLen {
		return "", ErrWeakPassword
	}
	salt := make([]byte, saltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("read salt: %w", err)
	}
	key, err := pbkdf2.Key(sha256.New, password, salt, iterations, keyLen)
	if err != nil {
		return "", fmt.Errorf("hash password: %w", err)
	}
	enc := base64.RawStdEncoding
	return fmt.Sprintf("%s$%d$%s$%s", hashScheme, iterations, enc.EncodeToString(salt), enc.EncodeToString(key)), nil
}

// CheckPassword reports whether password matches hash. An empty or
// malformed hash matches nothing.
func CheckPassword(hash, password string) bool {
	parts := strings.Split(hash, "$")
	if len(parts) != 4 || parts[0] != hashScheme {
		return false
	}
	iter, err := strconv.Atoi(parts[1])
	if err != nil || iter < 1 {
		return false
	}
	enc := base64.RawStdEncoding
	salt, err1 := enc.DecodeString(parts[2])
	want, err2 := enc.DecodeString(parts[3])
	if err1 != nil || err2 != nil || len(want) == 0 {
		return false
	}
	got, err := pbkdf2.Key(sha256.New, password, salt, iter, len(want))
	return err == nil && subtle.ConstantTimeCompare(got, want) == 1
}

// TokenPrefix starts every personal API token, so leaked ones are easy to
// recognise and scan for.
const TokenPrefix = "cisid_"

// NewSecret returns a random secret with prefix and the digest to store in
// its place: sessions and API tokens are kept only as digests.
func NewSecret(prefix string) (secret, digest string, err error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", "", fmt.Errorf("read random bytes: %w", err)
	}
	secret = prefix + base64.RawURLEncoding.EncodeToString(b)
	return secret, Digest(secret), nil
}

// Digest is what a session or token is stored and looked up as.
func Digest(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(sum[:])
}
