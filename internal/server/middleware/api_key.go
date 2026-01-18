package middleware

import (
	"context"
	"net/http"
	"strings"

	"my-personal-budget/internal/auth"
	"my-personal-budget/internal/store"
)

type APIKeyStore interface {
	GetAPIKeyByToken(ctx context.Context, token string) (store.APIKey, error)
}

// APIKeyAuth enforces API key auth and injects the user id into the context.
func APIKeyAuth(store APIKeyStore, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Allow preflight without auth header
		if r.Method == http.MethodOptions {
			next.ServeHTTP(w, r)
			return
		}

		token := extractAPIKey(r)
		if token == "" {
			unauthorized(w)
			return
		}
		key, err := store.GetAPIKeyByToken(r.Context(), token)
		if err != nil {
			unauthorized(w)
			return
		}
		ctx := auth.WithUserID(r.Context(), key.UserID)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func extractAPIKey(r *http.Request) string {
	authHeader := r.Header.Get("Authorization")
	const prefix = "Bearer "
	if strings.HasPrefix(authHeader, prefix) {
		return strings.TrimSpace(strings.TrimPrefix(authHeader, prefix))
	}
	return strings.TrimSpace(r.Header.Get("X-API-Key"))
}
