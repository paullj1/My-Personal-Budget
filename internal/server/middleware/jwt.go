package middleware

import (
	"net/http"
	"strings"

	"my-personal-budget/internal/auth"
)

// JWTAuth enforces JWT-based auth and injects user id into the context.
func JWTAuth(secret string, next http.Handler) http.Handler {
	trimmed := strings.TrimSpace(secret)

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Allow preflight without auth header
		if r.Method == http.MethodOptions {
			next.ServeHTTP(w, r)
			return
		}

		authHeader := r.Header.Get("Authorization")
		const prefix = "Bearer "
		if !strings.HasPrefix(authHeader, prefix) {
			unauthorized(w)
			return
		}
		token := strings.TrimSpace(strings.TrimPrefix(authHeader, prefix))
		userID, err := auth.ParseToken(token, trimmed)
		if err != nil {
			unauthorized(w)
			return
		}
		ctx := auth.WithUserID(r.Context(), userID)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func unauthorized(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
}
