// Package oauthsweep runs the periodic cleanup of abandoned OAuth state.
//
// It lives outside internal/oauth because that package is the pure-function half
// of the authorization server and internal/store already imports it; a sweeper
// there would close the loop into an import cycle.
package oauthsweep

import (
	"context"
	"log"
	"time"

	"my-personal-budget/internal/store"
)

// Sweeper is the store surface this needs, kept narrow so a test does not have
// to stand up a database.
type Sweeper interface {
	PurgeStaleOAuth(ctx context.Context, retain time.Duration) (store.OAuthPurgeResult, error)
}

// Options configures the loop.
type Options struct {
	// Interval is how often to sweep.
	Interval time.Duration
	// Retain is the grace period before state counts as abandoned.
	Retain time.Duration
	// StartDelay holds the first sweep back so it does not compete with the
	// connection pool warming up. Negative disables the wait, which is what tests
	// want; zero takes the default.
	StartDelay time.Duration
}

const (
	defaultInterval   = time.Hour
	defaultRetain     = 24 * time.Hour
	defaultStartDelay = 5 * time.Second
)

// StartScheduler runs a sweep shortly after startup and then on Interval, until
// ctx is cancelled. It mirrors payroll.StartScheduler so there is one shape for
// background work in this process.
func StartScheduler(ctx context.Context, s Sweeper, logger *log.Logger, opts Options) {
	if logger == nil {
		logger = log.Default()
	}
	if opts.Interval <= 0 {
		opts.Interval = defaultInterval
	}
	if opts.Retain <= 0 {
		opts.Retain = defaultRetain
	}
	if opts.StartDelay == 0 {
		opts.StartDelay = defaultStartDelay
	}
	go run(ctx, s, logger, opts)
}

func run(ctx context.Context, s Sweeper, logger *log.Logger, opts Options) {
	// Let the pool warm up before the first query, as the payroll loop does.
	if opts.StartDelay > 0 {
		select {
		case <-ctx.Done():
			return
		case <-time.After(opts.StartDelay):
		}
	}

	for {
		sweep(ctx, s, logger, opts.Retain)

		timer := time.NewTimer(opts.Interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

func sweep(ctx context.Context, s Sweeper, logger *log.Logger, retain time.Duration) {
	runCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	result, err := s.PurgeStaleOAuth(runCtx, retain)
	if err != nil {
		// Housekeeping failing is not fatal: nothing a user does depends on it,
		// and the next tick tries again. It is logged because a sweep that never
		// succeeds is how the table grows unnoticed.
		logger.Printf("oauth sweep: %v", err)
		return
	}
	// Silent when there is nothing to do, which is the steady state. A log line
	// per hour saying "0" trains you to stop reading them.
	if result.Empty() {
		return
	}
	logger.Printf("oauth sweep: removed %d code(s), %d revoked connection(s), %d token(s), %d unused client registration(s)",
		result.Codes, result.Authorizations, result.Tokens, result.Clients)
}
