package oauth

import (
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"strings"
	"testing"
)

func challengeFor(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

func TestVerifyPKCE(t *testing.T) {
	verifier := "a-verifier-long-enough-to-be-realistic-0123456789"
	challenge := challengeFor(verifier)

	if err := VerifyPKCE(challenge, "S256", verifier); err != nil {
		t.Fatalf("matching verifier rejected: %v", err)
	}
	// An omitted method means S256, which is the only one advertised.
	if err := VerifyPKCE(challenge, "", verifier); err != nil {
		t.Fatalf("empty method rejected: %v", err)
	}
	if err := VerifyPKCE(challenge, "S256", "wrong"); !errors.Is(err, ErrPKCEFailed) {
		t.Fatalf("expected ErrPKCEFailed, got %v", err)
	}
	if err := VerifyPKCE("", "S256", verifier); !errors.Is(err, ErrPKCERequired) {
		t.Fatalf("expected ErrPKCERequired, got %v", err)
	}
	// "plain" would let anyone who saw the authorization request replay the code.
	if err := VerifyPKCE(verifier, "plain", verifier); !errors.Is(err, ErrUnsupportedMethod) {
		t.Fatalf("expected plain to be refused, got %v", err)
	}
}

func TestValidateRedirectURIRequiresExactMatch(t *testing.T) {
	registered := []string{"https://claude.ai/api/mcp/auth_callback"}

	if err := ValidateRedirectURI(registered, registered[0]); err != nil {
		t.Fatalf("exact match rejected: %v", err)
	}
	// A prefix match is what lets a code land on somebody else's path.
	if err := ValidateRedirectURI(registered, registered[0]+"/../evil"); err == nil {
		t.Fatal("expected a non-registered path to be refused")
	}
	if err := ValidateRedirectURI(registered, "https://evil.example/cb"); err == nil {
		t.Fatal("expected a different host to be refused")
	}
	// Omitting it is allowed only when there is exactly one registered URI.
	if err := ValidateRedirectURI(registered, ""); err != nil {
		t.Fatalf("omitted redirect_uri rejected for single registration: %v", err)
	}
	two := []string{registered[0], "http://localhost:8080/cb"}
	if err := ValidateRedirectURI(two, ""); err == nil {
		t.Fatal("expected omitted redirect_uri to be refused with two registrations")
	}
}

func TestCheckRedirectURI(t *testing.T) {
	ok := []string{
		"https://claude.ai/api/mcp/auth_callback",
		"http://localhost:41234/callback",
		"http://127.0.0.1:8080/cb",
		"claude://oauth/callback",
	}
	for _, uri := range ok {
		if err := CheckRedirectURI(uri); err != nil {
			t.Fatalf("%s rejected: %v", uri, err)
		}
	}

	bad := []string{
		"http://example.com/cb",   // cleartext off loopback
		"/relative/cb",            // not absolute
		"https://ok.example/cb#f", // fragment
	}
	for _, uri := range bad {
		if err := CheckRedirectURI(uri); err == nil {
			t.Fatalf("%s should have been refused", uri)
		}
	}
}

func TestNormalizeScope(t *testing.T) {
	if got := NormalizeScope(""); got != DefaultScope {
		t.Fatalf("empty scope: got %q want %q", got, DefaultScope)
	}
	if got := NormalizeScope("budgets:read"); got != "budgets:read" {
		t.Fatalf("narrowed scope: got %q", got)
	}
	// Unknown scopes are dropped, never granted.
	if got := NormalizeScope("budgets:read admin:everything"); got != "budgets:read" {
		t.Fatalf("unknown scope leaked through: got %q", got)
	}
	// A request made entirely of unknown scopes falls back rather than granting
	// an empty string, which downstream would read as "no restriction".
	if got := NormalizeScope("admin:everything"); got != DefaultScope {
		t.Fatalf("all-unknown scope: got %q want %q", got, DefaultScope)
	}
	// Output follows the advertised order, not the request's.
	if got := NormalizeScope("budgets:write budgets:read"); got != DefaultScope {
		t.Fatalf("scope order not normalized: got %q", got)
	}
}

func TestHasScope(t *testing.T) {
	if !HasScope(DefaultScope, ScopeBudgetsWrite) {
		t.Fatal("expected write scope to be present")
	}
	if HasScope(ScopeBudgetsRead, ScopeBudgetsWrite) {
		t.Fatal("read-only grant reported write scope")
	}
	// Substrings must not count.
	if HasScope("budgets:reader", ScopeBudgetsRead) {
		t.Fatal("substring matched as a scope")
	}
}

func TestGenerateSecretIsUniqueAndHashed(t *testing.T) {
	a, hashA, err := GenerateSecret("mpbat_")
	if err != nil {
		t.Fatalf("GenerateSecret error: %v", err)
	}
	b, _, err := GenerateSecret("mpbat_")
	if err != nil {
		t.Fatalf("GenerateSecret error: %v", err)
	}
	if a == b {
		t.Fatal("two secrets came back identical")
	}
	if !strings.HasPrefix(a, "mpbat_") {
		t.Fatalf("prefix missing: %q", a)
	}
	if hashA != HashSecret(a) {
		t.Fatal("returned hash does not match HashSecret")
	}
	if strings.Contains(hashA, a) {
		t.Fatal("hash contains the plaintext")
	}
}
