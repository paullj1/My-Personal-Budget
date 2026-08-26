package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"runtime/debug"
	"syscall"
	"time"

	"my-personal-budget/internal/config"
	"my-personal-budget/internal/database"
	"my-personal-budget/internal/oauthsweep"
	"my-personal-budget/internal/payroll"
	"my-personal-budget/internal/server"
	"my-personal-budget/internal/store"
)

func main() {
	cfg := config.FromEnv()

	// Bound the heap before serving anything. Receipt scanning allocates in large
	// transient bursts -- an image costs roughly 13MB per megapixel across decode
	// and the full-frame RGBA copies -- and on a 1GB host shared with Postgres and
	// Caddy that burst is what ends the process. Under a soft limit the collector
	// runs harder as the ceiling approaches instead of growing into an OOM kill.
	//
	// GOMEMLIMIT, if set, is the runtime's own knob and already applied, so leave it
	// alone rather than overriding what an operator asked for.
	if cfg.MemoryLimitBytes > 0 && os.Getenv("GOMEMLIMIT") == "" {
		debug.SetMemoryLimit(cfg.MemoryLimitBytes)
		log.Printf("Go memory limit: %dMB (soft)", cfg.MemoryLimitBytes>>20)
	} else if limit := os.Getenv("GOMEMLIMIT"); limit != "" {
		log.Printf("Go memory limit: GOMEMLIMIT=%s (set by the environment)", limit)
	} else {
		log.Printf("Go memory limit: none -- a large scan can grow until the kernel intervenes")
	}

	db, err := database.Connect(cfg.DBURL, cfg.DBConnectRetries, cfg.DBConnectInterval)
	if err != nil {
		log.Fatalf("connect db: %v", err)
	}
	defer db.Close()

	if err := database.ApplyMigrations(context.Background(), db, "db/schema.sql"); err != nil {
		log.Fatalf("apply migrations: %v", err)
	}

	store := store.New(db)

	bgCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	payroll.StartScheduler(bgCtx, store, log.Default())
	if cfg.OAuthEnabled() {
		// Open registration needs something clearing up after it; see
		// store.PurgeStaleOAuth.
		oauthsweep.StartScheduler(bgCtx, store, log.Default(), oauthsweep.Options{
			Interval: cfg.OAuthSweepInterval,
			Retain:   cfg.OAuthStaleAfter,
		})
		log.Printf("OAuth enabled: issuer=%s registration limit=%d/%s sweep=%s (stale after %s)",
			cfg.PublicBaseURL, cfg.OAuthRegistrationLimit, cfg.OAuthRegistrationWindow,
			cfg.OAuthSweepInterval, cfg.OAuthStaleAfter)
	}

	router := server.NewRouter(cfg, store)

	// A synchronous receipt scan legitimately runs for tens of seconds while the
	// vision model reads the photo. With the default 10s write timeout the
	// connection is torn down before the handler can answer, and the client sees
	// an empty reply while the server logs a successful request. Scale the
	// body timeouts off the configured inference timeout, and keep the header
	// timeout short so slow-header protection is unaffected.
	readTimeout, writeTimeout := 10*time.Second, 10*time.Second
	if cfg.ReceiptScanEnabled() {
		budget := cfg.ReceiptOCRTimeout + 30*time.Second
		if budget > readTimeout {
			readTimeout = budget
		}
		if budget > writeTimeout {
			writeTimeout = budget
		}
	}

	srv := &http.Server{
		Addr:              cfg.Host + ":" + cfg.Port,
		Handler:           router,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       readTimeout,
		WriteTimeout:      writeTimeout,
		IdleTimeout:       60 * time.Second,
	}
	if cfg.ReceiptScanEnabled() {
		// Pointing the wrong client at a server fails in confusing ways -- Ollama's
		// OpenAI shim accepts the request and ignores the schema -- so say plainly
		// which backend is in use.
		log.Printf("receipt scanning: api=%s url=%s model=%s",
			cfg.ReceiptOCRAPI, cfg.ReceiptOCRURL, cfg.ReceiptOCRModel)
	}
	log.Printf("HTTP timeouts: read=%s write=%s (receipt scanning %s)",
		readTimeout, writeTimeout,
		map[bool]string{true: "enabled", false: "disabled"}[cfg.ReceiptScanEnabled()])

	log.Printf("Go API listening on %s", srv.Addr)

	idleConnsClosed := make(chan struct{})
	go func() {
		sigint := make(chan os.Signal, 1)
		signal.Notify(sigint, syscall.SIGINT, syscall.SIGTERM)
		<-sigint

		cancel()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		if err := srv.Shutdown(ctx); err != nil {
			log.Printf("HTTP server Shutdown: %v", err)
		}
		close(idleConnsClosed)
	}()

	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatalf("ListenAndServe: %v", err)
	}

	<-idleConnsClosed
}
