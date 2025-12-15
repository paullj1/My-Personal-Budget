package middleware

import (
	"net/http"
	"strings"
)

// RequireAPIToken enforces a static bearer token on the wrapped handler.
func RequireAPIToken(token string, next http.Handler) http.Handler {
	trimmed := strings.TrimSpace(token)

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Allow preflight without auth header
		if r.Method == http.MethodOptions {
			next.ServeHTTP(w, r)
			return
		}

		auth := r.Header.Get("Authorization")
		const prefix = "Bearer "
		if !strings.HasPrefix(auth, prefix) {
			w.Header().Set("Content-Type", "application/json")
			http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
			return
		}
		supplied := strings.TrimSpace(strings.TrimPrefix(auth, prefix))
		if supplied != trimmed {
			w.Header().Set("Content-Type", "application/json")
			http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}
