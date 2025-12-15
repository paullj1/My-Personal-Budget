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

	store := store.New(db)

	bgCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	payroll.StartScheduler(bgCtx, store, log.Default())

	router := server.NewRouter(cfg, store)
	srv := &http.Server{
		Addr:         cfg.Host + ":" + cfg.Port,
		Handler:      router,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 10 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

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
