package oauthsweep

import (
	"bytes"
	"context"
	"errors"
	"log"
	"strings"
	"sync"
	"testing"
	"time"

	"my-personal-budget/internal/store"
)

type fakeSweeper struct {
	mu      sync.Mutex
	calls   int
	retains []time.Duration
	result  store.OAuthPurgeResult
	err     error
	done    chan struct{}
}

func (f *fakeSweeper) PurgeStaleOAuth(_ context.Context, retain time.Duration) (store.OAuthPurgeResult, error) {
	f.mu.Lock()
	f.calls++
	f.retains = append(f.retains, retain)
	calls := f.calls
	f.mu.Unlock()
	if f.done != nil && calls == 1 {
		close(f.done)
	}
	return f.result, f.err
}

func (f *fakeSweeper) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

func TestSweepLogsWhatItRemoved(t *testing.T) {
	var buf bytes.Buffer
	logger := log.New(&buf, "", 0)
	s := &fakeSweeper{result: store.OAuthPurgeResult{Codes: 2, Clients: 3, Authorizations: 1, Tokens: 4}}

	sweep(context.Background(), s, logger, time.Hour)

	out := buf.String()
	for _, want := range []string{"2 code", "1 revoked connection", "4 token", "3 unused client"} {
		if !strings.Contains(out, want) {
			t.Fatalf("log is missing %q: %s", want, out)
		}
	}
}

// An hourly line saying "removed 0" trains you to stop reading the log.
func TestSweepIsSilentWhenThereIsNothingToDo(t *testing.T) {
	var buf bytes.Buffer
	s := &fakeSweeper{}

	sweep(context.Background(), s, log.New(&buf, "", 0), time.Hour)

	if buf.Len() != 0 {
		t.Fatalf("a clean sweep logged: %s", buf.String())
	}
}

// Housekeeping failing is not fatal, but a sweep that never succeeds is how the
// table grows unnoticed -- so it has to say so.
func TestSweepLogsFailures(t *testing.T) {
	var buf bytes.Buffer
	s := &fakeSweeper{err: errors.New("connection refused")}

	sweep(context.Background(), s, log.New(&buf, "", 0), time.Hour)

	if !strings.Contains(buf.String(), "connection refused") {
		t.Fatalf("failure not logged: %s", buf.String())
	}
}

func TestStartSchedulerSweepsAndStops(t *testing.T) {
	s := &fakeSweeper{done: make(chan struct{})}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	StartScheduler(ctx, s, log.New(&bytes.Buffer{}, "", 0), Options{
		Interval:   10 * time.Millisecond,
		Retain:     2 * time.Hour,
		StartDelay: -1,
	})

	select {
	case <-s.done:
	case <-time.After(5 * time.Second):
		t.Fatal("the scheduler never ran a sweep")
	}

	// The configured grace period is what reaches the store, not a default.
	s.mu.Lock()
	retain := s.retains[0]
	s.mu.Unlock()
	if retain != 2*time.Hour {
		t.Fatalf("retain: got %s want 2h", retain)
	}

	cancel()
	settled := s.callCount()
	time.Sleep(60 * time.Millisecond)
	if grown := s.callCount(); grown > settled+1 {
		t.Fatalf("the loop kept sweeping after cancellation: %d -> %d", settled, grown)
	}
}

// Zero values mean "unconfigured", so the loop has to supply its own defaults
// rather than spin at zero interval or sweep with no grace period at all.
func TestStartSchedulerFallsBackToDefaults(t *testing.T) {
	s := &fakeSweeper{done: make(chan struct{})}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	StartScheduler(ctx, s, nil, Options{StartDelay: -1})

	select {
	case <-s.done:
	case <-time.After(5 * time.Second):
		t.Fatal("the scheduler never ran a sweep")
	}

	s.mu.Lock()
	retain := s.retains[0]
	s.mu.Unlock()
	if retain != defaultRetain {
		t.Fatalf("retain: got %s want %s", retain, defaultRetain)
	}
}
