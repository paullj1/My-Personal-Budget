package server

import (
	"context"
	"log"
	"net/http"
	"time"
)

// withRouteTimeouts bounds how long a handler may run, per route.
//
// The server-wide Read/WriteTimeout has to clear the slowest legitimate handler,
// and a synchronous receipt scan runs for tens of seconds. Raising it alone would
// hand every other route the same generous window. This restores a tight deadline
// everywhere except the scan endpoint, enforced through the request context that
// the inference client and database calls already honour.
func withRouteTimeouts(next http.Handler, scanPath string, scanTimeout, defaultTimeout time.Duration) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		budget := defaultTimeout
		if scanTimeout > 0 && r.URL.Path == scanPath {
			budget = scanTimeout
		}
		if budget <= 0 {
			next.ServeHTTP(w, r)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), budget)
		defer cancel()
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func requestLogger(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		next.ServeHTTP(w, r)
		log.Printf("%s %s %s", r.Method, r.URL.Path, time.Since(start).String())
	})
}

// publicCORSPaths are reachable from any origin.
//
// The OAuth discovery documents and the endpoints a remote client drives are
// public by design: metadata carries no secrets, and every other request here
// authenticates with an explicit Authorization header rather than a cookie. With
// no ambient credential to borrow, an allow-list buys nothing and would instead
// mean naming every client origin -- claude.ai today, something else tomorrow --
// in CORS_ALLOWED_ORIGINS before the connector could even be discovered.
func publicCORSPath(path string) bool {
	switch path {
	case "/mcp",
		"/.well-known/oauth-protected-resource",
		"/.well-known/oauth-protected-resource/mcp",
		"/.well-known/oauth-authorization-server",
		"/oauth/register",
		"/oauth/token",
		"/oauth/revoke":
		return true
	}
	return false
}

func withCORS(next http.Handler, allowedOrigins []string) http.Handler {
	allowed := make(map[string]struct{}, len(allowedOrigins))
	for _, origin := range allowedOrigins {
		allowed[origin] = struct{}{}
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		_, wildcard := allowed["*"]
		_, listed := allowed[origin]
		if origin != "" && (wildcard || listed || publicCORSPath(r.URL.Path)) {
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Vary", "Origin")
			// WWW-Authenticate carries the resource_metadata pointer that starts
			// OAuth discovery. A browser client that cannot read it off the 401 has
			// no way to find the authorization server.
			w.Header().Set("Access-Control-Expose-Headers", "WWW-Authenticate, Mcp-Session-Id, MCP-Protocol-Version")
		}

		if r.Method == http.MethodOptions {
			w.Header().Set("Access-Control-Allow-Methods", "GET,POST,PUT,PATCH,DELETE,OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, Mcp-Session-Id, MCP-Protocol-Version, Last-Event-ID")
			w.Header().Set("Access-Control-Max-Age", "600")
			w.WriteHeader(http.StatusNoContent)
			return
		}

		next.ServeHTTP(w, r)
	})
}
