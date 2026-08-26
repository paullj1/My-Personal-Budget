// Package oauth holds the pieces of the authorization server that are pure
// functions of their input: secret generation, PKCE verification, and redirect
// URI matching. Storage lives in internal/store, HTTP in internal/server.
package oauth

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"net/url"
	"strings"
)

// Scopes this server understands. They are coarse on purpose: an MCP client
// either acts as the user or it does not, and a finer split would be a promise
// the tool surface does not keep.
const (
	ScopeBudgetsRead  = "budgets:read"
	ScopeBudgetsWrite = "budgets:write"
)

// DefaultScope is granted when a client asks for nothing in particular.
const DefaultScope = ScopeBudgetsRead + " " + ScopeBudgetsWrite

// SupportedScopes is the advertised set, in metadata order.
var SupportedScopes = []string{ScopeBudgetsRead, ScopeBudgetsWrite}

// Errors surfaced to callers as OAuth error codes.
var (
	ErrInvalidRedirectURI = errors.New("invalid redirect_uri")
	ErrPKCERequired       = errors.New("code_challenge is required")
	ErrPKCEFailed         = errors.New("code_verifier does not match code_challenge")
	ErrUnsupportedMethod  = errors.New("unsupported code_challenge_method")
)

// GenerateSecret returns a URL-safe random string with the given prefix, along
// with the hash to store. The plaintext is returned once and never persisted,
// matching how api_keys are handled.
func GenerateSecret(prefix string) (token, hash string, err error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", "", err
	}
	token = prefix + base64.RawURLEncoding.EncodeToString(raw)
	return token, HashSecret(token), nil
}

// HashSecret is the one-way function used for every stored credential here.
func HashSecret(token string) string {
	sum := sha256.Sum256([]byte(token))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// GenerateClientID returns a public, non-secret identifier for a registered client.
func GenerateClientID() (string, error) {
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return "mpbc_" + base64.RawURLEncoding.EncodeToString(raw), nil
}

// VerifyPKCE checks a code_verifier against the stored challenge.
//
// Only S256 is accepted. OAuth 2.1 removes "plain", and accepting it here would
// let a network attacker who saw the authorization request replay the code.
func VerifyPKCE(challenge, method, verifier string) error {
	if challenge == "" {
		return ErrPKCERequired
	}
	if method != "" && method != "S256" {
		return ErrUnsupportedMethod
	}
	sum := sha256.Sum256([]byte(verifier))
	computed := base64.RawURLEncoding.EncodeToString(sum[:])
	if subtle.ConstantTimeCompare([]byte(computed), []byte(challenge)) != 1 {
		return ErrPKCEFailed
	}
	return nil
}

// ValidateRedirectURI enforces exact string matching against the registered
// set. OAuth 2.1 requires exact matching -- prefix or wildcard matching is how
// authorization codes end up delivered to somebody else's path.
func ValidateRedirectURI(registered []string, candidate string) error {
	if candidate == "" {
		// A client that registered exactly one URI may omit it.
		if len(registered) == 1 {
			return nil
		}
		return ErrInvalidRedirectURI
	}
	for _, uri := range registered {
		if subtle.ConstantTimeCompare([]byte(uri), []byte(candidate)) == 1 {
			return nil
		}
	}
	return ErrInvalidRedirectURI
}

// CheckRedirectURI rejects redirect targets that are not safe to send a code to.
// Loopback HTTP is allowed because native clients need it; anything else must be
// HTTPS or a private-use scheme such as claude://.
func CheckRedirectURI(raw string) error {
	parsed, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("redirect_uri %q is not a URI", raw)
	}
	if parsed.Fragment != "" {
		return fmt.Errorf("redirect_uri %q must not contain a fragment", raw)
	}
	switch {
	case parsed.Scheme == "https":
		return nil
	case parsed.Scheme == "http":
		host := parsed.Hostname()
		if host == "localhost" || host == "127.0.0.1" || host == "::1" {
			return nil
		}
		return fmt.Errorf("redirect_uri %q must use https outside loopback", raw)
	case parsed.Scheme == "":
		return fmt.Errorf("redirect_uri %q must be absolute", raw)
	default:
		// Private-use scheme (native app). Must not be a bare scheme.
		if parsed.Opaque == "" && parsed.Host == "" && parsed.Path == "" {
			return fmt.Errorf("redirect_uri %q has no target", raw)
		}
		return nil
	}
}

// NormalizeScope intersects a requested scope with what this server grants,
// preserving the advertised order so the consent screen reads consistently.
// An empty or wholly unrecognized request falls back to DefaultScope.
func NormalizeScope(requested string) string {
	fields := strings.Fields(requested)
	if len(fields) == 0 {
		return DefaultScope
	}
	asked := make(map[string]bool, len(fields))
	for _, f := range fields {
		asked[f] = true
	}
	granted := make([]string, 0, len(SupportedScopes))
	for _, s := range SupportedScopes {
		if asked[s] {
			granted = append(granted, s)
		}
	}
	if len(granted) == 0 {
		return DefaultScope
	}
	return strings.Join(granted, " ")
}

// HasScope reports whether a granted scope string contains the named scope.
func HasScope(granted, want string) bool {
	for _, f := range strings.Fields(granted) {
		if f == want {
			return true
		}
	}
	return false
}
