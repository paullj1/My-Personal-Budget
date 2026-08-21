package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"my-personal-budget/internal/config"
	"my-personal-budget/internal/database"
	"my-personal-budget/internal/payroll"
	"my-personal-budget/internal/server"
	"my-personal-budget/internal/store"
)

func main() {
	cfg := config.FromEnv()

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
