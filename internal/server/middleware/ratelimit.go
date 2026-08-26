package middleware

import (
	"net/http"
	"strconv"
	"sync"
	"time"
)

// RateLimit caps how often a handler may run, counted across all callers rather
// than per client address.
//
// Global is the honest choice here. The endpoint this guards sits behind a
// reverse proxy, so RemoteAddr is the proxy's address and a per-IP bucket would
// collapse to this one anyway; trusting X-Forwarded-For instead would let a
// caller mint a fresh identity per request by setting a header. What actually
// needs bounding is total rows written by unauthenticated callers, and a global
// cap bounds that no matter who is asking.
//
// The tradeoff is real and worth naming: a caller who exhausts the window locks
// everyone out until it rolls over. For registering an MCP client -- something
// done a handful of times ever, against an hour-long window -- that is a better
// failure than an unbounded table.
//
// A fixed window can pass up to 2*limit across a window boundary. That is fine
// for a growth bound and avoids pretending to a precision this does not need.
func RateLimit(limit int, window time.Duration, next http.Handler) http.Handler {
	if limit <= 0 || window <= 0 {
		return next
	}
	var (
		mu          sync.Mutex
		count       int
		windowStart = time.Now()
	)

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Preflight carries no body and writes nothing.
		if r.Method == http.MethodOptions {
			next.ServeHTTP(w, r)
			return
		}

		mu.Lock()
		now := time.Now()
		if now.Sub(windowStart) >= window {
			windowStart = now
			count = 0
		}
		if count >= limit {
			retryAfter := int((window - now.Sub(windowStart)).Seconds()) + 1
			mu.Unlock()
			w.Header().Set("Retry-After", strconv.Itoa(retryAfter))
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusTooManyRequests)
			// temporarily_unavailable is the OAuth error code for "come back
			// later", so a client that parses the body gets something it knows.
			_, _ = w.Write([]byte(`{"error":"temporarily_unavailable","error_description":"too many registration requests; try again later"}` + "\n"))
			return
		}
		count++
		mu.Unlock()

		next.ServeHTTP(w, r)
	})
}
