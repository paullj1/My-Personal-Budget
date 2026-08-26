package middleware

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"my-personal-budget/internal/auth"
	"my-personal-budget/internal/store"
)

type fakeMCPAuthStore struct {
	apiKeys      map[string]store.APIKey
	oauthTokens  map[string]store.OAuthTokenInfo
	expiredToken string
}

func (f *fakeMCPAuthStore) GetAPIKeyByToken(_ context.Context, token string) (store.APIKey, error) {
	key, ok := f.apiKeys[token]
	if !ok {
		return store.APIKey{}, store.ErrNotFound
	}
	return key, nil
}

func (f *fakeMCPAuthStore) LookupOAuthAccessToken(_ context.Context, token string) (store.OAuthTokenInfo, error) {
	if token == f.expiredToken {
		return store.OAuthTokenInfo{}, store.ErrOAuthExpired
	}
	info, ok := f.oauthTokens[token]
	if !ok {
		return store.OAuthTokenInfo{}, store.ErrNotFound
	}
	return info, nil
}

const testResourceMetadata = "https://budget.example/.well-known/oauth-protected-resource"

func TestMCPAuthAcceptsBothCredentialKinds(t *testing.T) {
	fs := &fakeMCPAuthStore{
		apiKeys:     map[string]store.APIKey{"mpb_key": {UserID: 11}},
		oauthTokens: map[string]store.OAuthTokenInfo{"mpbat_tok": {UserID: 22}},
	}

	for _, tc := range []struct {
		name   string
		header string
		want   int64
	}{
		{"api key", "Bearer mpb_key", 11},
		{"oauth token", "Bearer mpbat_tok", 22},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var seen *int64
			handler := MCPAuth(fs, testResourceMetadata, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				seen = auth.UserIDFromContext(r.Context())
				w.WriteHeader(http.StatusOK)
			}))
			rr := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
			req.Header.Set("Authorization", tc.header)
			handler.ServeHTTP(rr, req)

			if rr.Code != http.StatusOK {
				t.Fatalf("expected 200, got %d", rr.Code)
			}
			if seen == nil || *seen != tc.want {
				t.Fatalf("user id: got %v want %d", seen, tc.want)
			}
		})
	}
}

// The WWW-Authenticate pointer is the entire bootstrap for a web client: without
// it there is no way to find the authorization server from a 401.
func TestMCPAuthChallengeCarriesResourceMetadata(t *testing.T) {
	fs := &fakeMCPAuthStore{apiKeys: map[string]store.APIKey{}}
	handler := MCPAuth(fs, testResourceMetadata, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("handler ran without a credential")
	}))

	for _, header := range []string{"", "Bearer nope"} {
		rr := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
		if header != "" {
			req.Header.Set("Authorization", header)
		}
		handler.ServeHTTP(rr, req)

		if rr.Code != http.StatusUnauthorized {
			t.Fatalf("expected 401 for %q, got %d", header, rr.Code)
		}
		challenge := rr.Header().Get("WWW-Authenticate")
		if !strings.HasPrefix(challenge, "Bearer ") {
			t.Fatalf("expected a Bearer challenge, got %q", challenge)
		}
		if !strings.Contains(challenge, `resource_metadata="`+testResourceMetadata+`"`) {
			t.Fatalf("challenge is missing the resource_metadata pointer: %q", challenge)
		}
	}
}

// An expired token is a different situation from a wrong one: the client should
// refresh rather than conclude its credential is bad.
func TestMCPAuthReportsExpiredTokenAsInvalidToken(t *testing.T) {
	fs := &fakeMCPAuthStore{expiredToken: "mpbat_old", apiKeys: map[string]store.APIKey{}}
	handler := MCPAuth(fs, testResourceMetadata, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("handler ran with an expired token")
	}))

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	req.Header.Set("Authorization", "Bearer mpbat_old")
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rr.Code)
	}
	challenge := rr.Header().Get("WWW-Authenticate")
	if !strings.Contains(challenge, `error="invalid_token"`) {
		t.Fatalf("expected invalid_token, got %q", challenge)
	}
}

// Without a configured public origin there is no metadata to point at, but the
// challenge still has to be a well-formed one.
func TestMCPAuthChallengeWithoutOAuthConfigured(t *testing.T) {
	fs := &fakeMCPAuthStore{apiKeys: map[string]store.APIKey{}}
	handler := MCPAuth(fs, "", http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))

	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/mcp", nil))

	challenge := rr.Header().Get("WWW-Authenticate")
	if !strings.HasPrefix(challenge, "Bearer ") {
		t.Fatalf("expected a Bearer challenge, got %q", challenge)
	}
	if strings.Contains(challenge, "resource_metadata") {
		t.Fatalf("expected no resource_metadata pointer, got %q", challenge)
	}
}

// Preflight carries no Authorization header by design.
func TestMCPAuthAllowsPreflight(t *testing.T) {
	fs := &fakeMCPAuthStore{}
	reached := false
	handler := MCPAuth(fs, testResourceMetadata, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached = true
		w.WriteHeader(http.StatusNoContent)
	}))

	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, httptest.NewRequest(http.MethodOptions, "/mcp", nil))
	if !reached {
		t.Fatal("preflight was rejected")
	}
}

// X-API-Key stays supported so existing key holders are unaffected.
func TestMCPAuthAcceptsXAPIKeyHeader(t *testing.T) {
	fs := &fakeMCPAuthStore{apiKeys: map[string]store.APIKey{"mpb_key": {UserID: 3}}}
	var seen *int64
	handler := MCPAuth(fs, testResourceMetadata, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = auth.UserIDFromContext(r.Context())
	}))

	req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	req.Header.Set("X-API-Key", "mpb_key")
	handler.ServeHTTP(httptest.NewRecorder(), req)

	if seen == nil || *seen != 3 {
		t.Fatalf("user id: got %v want 3", seen)
	}
}
