package middleware

import (
	"context"
	"net/http"
	"strings"

	"my-personal-budget/internal/store"
)

// APIKeyStore resolves a personal API key to its owner.
type APIKeyStore interface {
	GetAPIKeyByToken(ctx context.Context, token string) (store.APIKey, error)
}

func extractAPIKey(r *http.Request) string {
	authHeader := r.Header.Get("Authorization")
	const prefix = "Bearer "
	if strings.HasPrefix(authHeader, prefix) {
		return strings.TrimSpace(strings.TrimPrefix(authHeader, prefix))
	}
	return strings.TrimSpace(r.Header.Get("X-API-Key"))
}
