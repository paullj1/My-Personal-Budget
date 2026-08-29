package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"my-personal-budget/internal/server/handlers"
)

// The server-wide write timeout has to clear the slowest legitimate handler, and a
// synchronous receipt scan runs for tens of seconds. Raising it alone would hand
// every other route the same generous window, so deadlines are applied per route.
func TestWithRouteTimeouts(t *testing.T) {
	const scanPath = "/api/v1/receipts/scan"

	var gotDeadline time.Duration
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if dl, ok := r.Context().Deadline(); ok {
			gotDeadline = time.Until(dl)
		} else {
			gotDeadline = 0
		}
	})

	h := withRouteTimeouts(next, scanPath, 90*time.Second, 20*time.Second)

	t.Run("scan route gets the long budget", func(t *testing.T) {
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, scanPath, nil))
		if gotDeadline < 80*time.Second || gotDeadline > 90*time.Second {
			t.Errorf("scan deadline = %s, want ~90s", gotDeadline.Round(time.Second))
		}
	})

	t.Run("every other route keeps a tight budget", func(t *testing.T) {
		for _, path := range []string{"/api/v1/budgets", "/api/v1/receipts", "/healthz", "/"} {
			h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, path, nil))
			if gotDeadline > 20*time.Second {
				t.Errorf("%s deadline = %s, want <= 20s", path, gotDeadline.Round(time.Second))
			}
			if gotDeadline == 0 {
				t.Errorf("%s got no deadline at all", path)
			}
		}
	})

	t.Run("a cancelled deadline is observable by the handler", func(t *testing.T) {
		var sawExpiry bool
		slow := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			select {
			case <-r.Context().Done():
				sawExpiry = true
			case <-time.After(time.Second):
			}
		})
		withRouteTimeouts(slow, scanPath, time.Second, 10*time.Millisecond).
			ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/api/v1/budgets", nil))
		if !sawExpiry {
			t.Error("handler never observed the deadline expiring")
		}
	})

	t.Run("no scan route configured still bounds everything", func(t *testing.T) {
		withRouteTimeouts(next, "", 0, 5*time.Second).
			ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/api/v1/budgets", nil))
		if gotDeadline == 0 || gotDeadline > 5*time.Second {
			t.Errorf("deadline = %s, want ~5s", gotDeadline.Round(time.Second))
		}
	})
}

// scanFullPath is derived from the handler's constant so a route rename cannot
// silently drop scans to the default deadline. Assert the derivation, and that it
// is the path the timeout middleware actually recognises.
func TestScanFullPathMatchesTheHandler(t *testing.T) {
	if want := apiPrefix + handlers.ScanPath; scanFullPath != want {
		t.Fatalf("scanFullPath = %q, want %q", scanFullPath, want)
	}
	if scanFullPath != "/api/v1/receipts/scan" {
		t.Errorf("scanFullPath = %q; the API moved, so check the deadline wiring", scanFullPath)
	}

	var long bool
	h := withRouteTimeouts(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		dl, ok := r.Context().Deadline()
		long = ok && time.Until(dl) > time.Minute
	}), scanFullPath, 5*time.Minute, 20*time.Second)

	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, scanFullPath, nil))
	if !long {
		t.Error("the scan path did not receive the long deadline")
	}
}

func TestPublicCORSPathCoversTheOAuthBootstrap(t *testing.T) {
	// A remote client discovers this server from an origin nobody configured in
	// CORS_ALLOWED_ORIGINS, so these paths have to answer any origin.
	for _, path := range []string{
		"/mcp",
		"/.well-known/oauth-protected-resource",
		"/.well-known/oauth-protected-resource/mcp",
		"/.well-known/oauth-authorization-server",
		"/oauth/register",
		"/oauth/token",
		"/oauth/revoke",
	} {
		if !publicCORSPath(path) {
			t.Fatalf("%s should be reachable cross-origin", path)
		}
	}
	// The first-party API is not in that set.
	for _, path := range []string{"/api/v1/budgets", "/api/v1/connections", "/oauth/consent"} {
		if publicCORSPath(path) {
			t.Fatalf("%s should not be open to every origin", path)
		}
	}
}

func TestWithCORSExposesTheAuthenticateChallenge(t *testing.T) {
	handler := withCORS(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}), nil)

	req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	req.Header.Set("Origin", "https://claude.ai")
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if got := rr.Header().Get("Access-Control-Allow-Origin"); got != "https://claude.ai" {
		t.Fatalf("Access-Control-Allow-Origin: got %q", got)
	}
	// Without this the browser hides the header that names the authorization server.
	if got := rr.Header().Get("Access-Control-Expose-Headers"); !strings.Contains(got, "WWW-Authenticate") {
		t.Fatalf("Access-Control-Expose-Headers: got %q", got)
	}
}

func TestWithCORSStillRestrictsTheFirstPartyAPI(t *testing.T) {
	handler := withCORS(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}), []string{"https://budget.example"})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/budgets", nil)
	req.Header.Set("Origin", "https://evil.example")
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if got := rr.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Fatalf("an unlisted origin was allowed on the API: %q", got)
	}
}

func TestWithCORSPreflightAllowsMCPHeaders(t *testing.T) {
	handler := withCORS(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("preflight reached the inner handler")
	}), nil)

	req := httptest.NewRequest(http.MethodOptions, "/mcp", nil)
	req.Header.Set("Origin", "https://claude.ai")
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	allowed := rr.Header().Get("Access-Control-Allow-Headers")
	for _, header := range []string{"Authorization", "MCP-Protocol-Version", "Mcp-Session-Id"} {
		if !strings.Contains(allowed, header) {
			t.Fatalf("preflight does not allow %s: %q", header, allowed)
		}
	}
}

// A discovery probe answered with the SPA is worse than a 404: the client sees
// 200 and tries to parse HTML as JSON. openid-configuration is the one clients
// reach for before falling back to RFC 8414, and this server is an OAuth 2.0
// authorization server, not an OpenID provider.
func TestWellKnownDoesNotFallThroughToTheSPA(t *testing.T) {
	spa := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte("<html>SPA</html>"))
	})
	h := wellKnownNotFound(spa)

	for _, path := range []string{
		"/.well-known/openid-configuration",
		"/.well-known/oauth-authorization-server/extra",
		"/.well-known/anything-else",
	} {
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, path, nil))
		if rr.Code != http.StatusNotFound {
			t.Errorf("%s: got %d, want 404", path, rr.Code)
		}
		if ct := rr.Header().Get("Content-Type"); ct != "application/json" {
			t.Errorf("%s: Content-Type %q, want application/json", path, ct)
		}
		if strings.Contains(rr.Body.String(), "SPA") {
			t.Errorf("%s: fell through to the SPA", path)
		}
	}
}

// certbot's webroot mode writes challenges under the static directory, so that
// one prefix has to keep reaching the file server.
func TestWellKnownStillServesAcmeChallenges(t *testing.T) {
	spa := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("challenge-token"))
	})
	rr := httptest.NewRecorder()
	wellKnownNotFound(spa).ServeHTTP(rr,
		httptest.NewRequest(http.MethodGet, "/.well-known/acme-challenge/abc123", nil))

	if rr.Code != http.StatusOK {
		t.Fatalf("acme challenge got %d, want 200", rr.Code)
	}
	if rr.Body.String() != "challenge-token" {
		t.Fatalf("acme challenge body %q", rr.Body.String())
	}
}

// The real metadata routes must still win over the catch-all: Go's ServeMux
// prefers the more specific pattern, and this pins that so a future reshuffle
// cannot quietly 404 the discovery documents.
func TestWellKnownCatchAllDoesNotShadowMetadataRoutes(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/oauth-authorization-server", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"issuer":"x"}`))
	})
	mux.HandleFunc("/.well-known/oauth-protected-resource", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"resource":"x"}`))
	})
	mux.Handle("/.well-known/", wellKnownNotFound(http.NotFoundHandler()))

	for _, tc := range []struct {
		path string
		code int
	}{
		{"/.well-known/oauth-authorization-server", http.StatusOK},
		{"/.well-known/oauth-protected-resource", http.StatusOK},
		{"/.well-known/openid-configuration", http.StatusNotFound},
	} {
		rr := httptest.NewRecorder()
		mux.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, tc.path, nil))
		if rr.Code != tc.code {
			t.Errorf("%s: got %d, want %d", tc.path, rr.Code, tc.code)
		}
	}
}
