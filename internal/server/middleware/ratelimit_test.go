package middleware

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"
	"time"
)

func countingHandler(served *int) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*served++
		w.WriteHeader(http.StatusCreated)
	})
}

func TestRateLimitAllowsUpToTheLimit(t *testing.T) {
	served := 0
	h := RateLimit(3, time.Hour, countingHandler(&served))

	for i := 0; i < 3; i++ {
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/oauth/register", nil))
		if rr.Code != http.StatusCreated {
			t.Fatalf("request %d: got %d want 201", i+1, rr.Code)
		}
	}
	if served != 3 {
		t.Fatalf("handler ran %d times, want 3", served)
	}
}

func TestRateLimitRefusesBeyondTheLimit(t *testing.T) {
	served := 0
	h := RateLimit(2, time.Hour, countingHandler(&served))

	for i := 0; i < 2; i++ {
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/oauth/register", nil))
	}

	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/oauth/register", nil))

	if rr.Code != http.StatusTooManyRequests {
		t.Fatalf("got %d want 429", rr.Code)
	}
	if served != 2 {
		t.Fatalf("a refused request still reached the handler: %d", served)
	}

	// Retry-After tells the client when to come back rather than making it guess.
	retry := rr.Header().Get("Retry-After")
	seconds, err := strconv.Atoi(retry)
	if err != nil || seconds <= 0 {
		t.Fatalf("Retry-After: got %q", retry)
	}

	var body map[string]string
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("body is not JSON: %v (%s)", err, rr.Body.String())
	}
	// An OAuth client that parses the body should recognise the code.
	if body["error"] != "temporarily_unavailable" {
		t.Fatalf("error: got %q", body["error"])
	}
}

func TestRateLimitWindowRollsOver(t *testing.T) {
	served := 0
	h := RateLimit(1, 20*time.Millisecond, countingHandler(&served))

	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/oauth/register", nil))
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/oauth/register", nil))
	if rr.Code != http.StatusTooManyRequests {
		t.Fatalf("expected the second request to be refused, got %d", rr.Code)
	}

	time.Sleep(30 * time.Millisecond)

	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/oauth/register", nil))
	if rr.Code != http.StatusCreated {
		t.Fatalf("expected the window to roll over, got %d", rr.Code)
	}
	if served != 2 {
		t.Fatalf("handler ran %d times, want 2", served)
	}
}

// Preflight writes nothing, so counting it would let a browser's own CORS check
// exhaust the budget before the real request arrives.
func TestRateLimitIgnoresPreflight(t *testing.T) {
	served := 0
	h := RateLimit(1, time.Hour, countingHandler(&served))

	for i := 0; i < 5; i++ {
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, httptest.NewRequest(http.MethodOptions, "/oauth/register", nil))
		if rr.Code == http.StatusTooManyRequests {
			t.Fatalf("preflight %d was rate limited", i+1)
		}
	}

	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/oauth/register", nil))
	if rr.Code != http.StatusCreated {
		t.Fatalf("the real request was refused after preflights: %d", rr.Code)
	}
}

// A zero limit means "unconfigured", not "refuse everything" -- that would take
// registration offline for anyone who left the knob unset.
func TestRateLimitDisabledWhenUnconfigured(t *testing.T) {
	served := 0
	h := RateLimit(0, time.Hour, countingHandler(&served))

	for i := 0; i < 50; i++ {
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/oauth/register", nil))
		if rr.Code != http.StatusCreated {
			t.Fatalf("request %d refused with %d", i+1, rr.Code)
		}
	}
	if served != 50 {
		t.Fatalf("handler ran %d times, want 50", served)
	}
}

// The counter is shared state on a path several clients can hit at once.
func TestRateLimitIsConcurrencySafe(t *testing.T) {
	const limit = 25
	served := 0
	var mu sync.Mutex
	h := RateLimit(limit, time.Hour, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		served++
		mu.Unlock()
	}))

	var wg sync.WaitGroup
	for i := 0; i < 200; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/oauth/register", nil))
		}()
	}
	wg.Wait()

	if served != limit {
		t.Fatalf("handler ran %d times, want exactly %d", served, limit)
	}
}
