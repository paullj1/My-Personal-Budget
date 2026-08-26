package middleware

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"my-personal-budget/internal/auth"
	"my-personal-budget/internal/store"
)

// OAuthTokenStore resolves an opaque OAuth access token to its owner.
type OAuthTokenStore interface {
	LookupOAuthAccessToken(ctx context.Context, token string) (store.OAuthTokenInfo, error)
}

// MCPAuthStore is everything /mcp accepts as a credential.
type MCPAuthStore interface {
	APIKeyStore
	OAuthTokenStore
}

// MCPAuth authenticates the MCP endpoint with either a personal API key or an
// OAuth access token, and injects the user id into the context.
//
// Both are bearer tokens on the same header, so they are told apart by trying
// the OAuth table first -- OAuth tokens are the ones that expire, and an expired
// one has to produce a 401 carrying the resource metadata pointer so the client
// knows to refresh rather than treating the key as simply wrong.
//
// resourceMetadataURL is empty when the authorization server is not configured;
// the challenge then omits the pointer and only API keys work.
func MCPAuth(creds MCPAuthStore, resourceMetadataURL string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Allow preflight without auth header
		if r.Method == http.MethodOptions {
			next.ServeHTTP(w, r)
			return
		}

		token := extractAPIKey(r)
		if token == "" {
			challenge(w, resourceMetadataURL, "invalid_request", "a bearer token is required")
			return
		}

		if info, err := creds.LookupOAuthAccessToken(r.Context(), token); err == nil {
			ctx := auth.WithUserID(r.Context(), info.UserID)
			next.ServeHTTP(w, r.WithContext(ctx))
			return
		} else if errors.Is(err, store.ErrOAuthExpired) {
			challenge(w, resourceMetadataURL, "invalid_token", "the access token expired")
			return
		}

		key, err := creds.GetAPIKeyByToken(r.Context(), token)
		if err != nil {
			challenge(w, resourceMetadataURL, "invalid_token", "the bearer token is not valid")
			return
		}
		ctx := auth.WithUserID(r.Context(), key.UserID)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// challenge writes the 401 that starts the OAuth discovery chain. Without the
// resource_metadata pointer a web client has no way to find the authorization
// server, so this header is the entire bootstrap.
func challenge(w http.ResponseWriter, resourceMetadataURL, code, description string) {
	value := fmt.Sprintf(`Bearer error=%q, error_description=%q`, code, description)
	if resourceMetadataURL != "" {
		value += fmt.Sprintf(`, resource_metadata=%q`, resourceMetadataURL)
	}
	w.Header().Set("WWW-Authenticate", value)
	w.Header().Set("Content-Type", "application/json")
	http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
}
