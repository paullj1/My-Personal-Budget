package server

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
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
