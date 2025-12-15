package payroll

import (
	"context"
	"database/sql/driver"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"my-personal-budget/internal/store"
)

// StartScheduler kicks off a background loop that ensures payroll transactions
// are created once per month. It runs immediately on startup, then schedules
// the next run for the first moment of the next month. The provided context
// cancels the loop.
func StartScheduler(ctx context.Context, s *store.Store, logger *log.Logger) {
	if logger == nil {
		logger = log.Default()
	}
	go run(ctx, s, logger)
}

func run(ctx context.Context, s *store.Store, logger *log.Logger) {
	// Small delay to let the DB warm up after process start.
	select {
	case <-ctx.Done():
		return
	case <-time.After(2 * time.Second):
	}

	next := time.Now()
	for {
		wait := time.Until(next)
		if wait < 0 {
			wait = 0
		}
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
			count, err := runWithRetry(ctx, s, logger)
			if err != nil {
				logger.Printf("payroll: failed to run monthly payroll: %v", err)
				next = time.Now().Add(15 * time.Second)
				continue
			}
			logger.Printf("payroll: created %d payroll transaction(s) for %s", count, time.Now().Format("2006-01"))
			next = nextMonthStart(time.Now())
		}
	}
}

func runWithRetry(ctx context.Context, s *store.Store, logger *log.Logger) (int, error) {
	backoffs := []time.Duration{0, 750 * time.Millisecond, 2 * time.Second}
	var lastErr error

	for i, delay := range backoffs {
		if delay > 0 {
			select {
			case <-ctx.Done():
				return 0, ctx.Err()
			case <-time.After(delay):
			}
		}
		// Ensure the pool has at least one live connection before running payroll.
		pingCtx, cancelPing := context.WithTimeout(ctx, 3*time.Second)
		pingErr := s.Ping(pingCtx)
		cancelPing()
		if pingErr != nil {
			lastErr = fmt.Errorf("ping db: %w", pingErr)
			if i == len(backoffs)-1 {
				break
			}
			continue
		}

		runCtx, cancelRun := context.WithTimeout(ctx, 10*time.Second)
		count, err := s.RunMonthlyPayroll(runCtx, time.Now())
		cancelRun()
		if err == nil {
			return count, nil
		}
		lastErr = err
		logger.Printf("payroll attempt %d/%d failed: %v", i+1, len(backoffs), err)
		if !isBadConn(err) || i == len(backoffs)-1 {
			break
		}
	}
	return 0, lastErr
}

func isBadConn(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, driver.ErrBadConn) {
		return true
	}
	return strings.Contains(strings.ToLower(err.Error()), "bad connection")
}

func nextMonthStart(now time.Time) time.Time {
	startOfMonth := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, now.Location())
	return startOfMonth.AddDate(0, 1, 0)
}
