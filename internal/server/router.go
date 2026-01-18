package server

import (
	"log"
	"net/http"

	"my-personal-budget/internal/config"
	"my-personal-budget/internal/passkey"
	"my-personal-budget/internal/server/handlers"
	"my-personal-budget/internal/server/middleware"
	"my-personal-budget/internal/store"
)

// NewRouter wires HTTP routes and middleware.
func NewRouter(cfg config.Config, store *store.Store) http.Handler {
	mux := http.NewServeMux()

	health := handlers.NewHealthHandler()
	passkeyStore := passkey.NewChallengeStore()
	api, err := handlers.NewAPIHandler(cfg, store, passkeyStore)
	if err != nil {
		log.Fatalf("init api handler: %v", err)
	}

	mux.HandleFunc("/healthz", health.Handle)
	mux.Handle("/api/v1/auth/passkeys/begin", api.PasskeyBeginHandler())
	mux.Handle("/api/v1/auth/passkeys/finish", api.PasskeyFinishHandler())
	mux.Handle("/api/v1/auth/passkeys/login/begin", api.PasskeyLoginBeginHandler())
	mux.Handle("/api/v1/auth/passkeys/login/finish", api.PasskeyLoginFinishHandler())

	mcp := handlers.NewMCPHandler(store)
	mux.Handle("/mcp", middleware.APIKeyAuth(store, mcp))

	protected := http.StripPrefix("/api/v1", api.Router())
	if cfg.JWTSecret != "" {
		protected = middleware.JWTAuth(cfg.JWTSecret, protected)
	} else if cfg.APIToken != "" {
		protected = middleware.RequireAPIToken(cfg.APIToken, protected)
	}
	mux.Handle("/api/v1/", protected)

	// Serve frontend assets (SPA fallback).
	mux.Handle("/", newSPAHandler(cfg.StaticDir))

	handler := requestLogger(mux)
	if len(cfg.AllowedOrigins) > 0 {
		handler = withCORS(handler, cfg.AllowedOrigins)
	}

	log.Printf("Allowed origins: %v", cfg.AllowedOrigins)
	return handler
}
