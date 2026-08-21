package server

import (
	"log"
	"net/http"
	"time"

	"my-personal-budget/internal/config"
	"my-personal-budget/internal/passkey"
	"my-personal-budget/internal/server/handlers"
	"my-personal-budget/internal/server/middleware"
	"my-personal-budget/internal/store"
)

const (
	// defaultHandlerTimeout bounds every route other than receipt scanning.
	defaultHandlerTimeout = 20 * time.Second

	// apiPrefix is where the versioned API is mounted.
	apiPrefix = "/api/v1"
)

// scanFullPath is the absolute path of the scan endpoint, derived from the
// handler's own constant so renaming the route cannot silently drop scans back to
// the default deadline.
var scanFullPath = apiPrefix + handlers.ScanPath

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
	mux.Handle(apiPrefix+"/", protected)

	// Serve frontend assets (SPA fallback).
	mux.Handle("/", newSPAHandler(cfg.StaticDir))

	// Handler-level deadlines, so raising the server's write timeout for receipt
	// scanning does not loosen every other route.
	var handler http.Handler = mux
	if cfg.ReceiptScanEnabled() {
		handler = withRouteTimeouts(handler, scanFullPath,
			cfg.ReceiptOCRTimeout+15*time.Second, defaultHandlerTimeout)
	} else {
		handler = withRouteTimeouts(handler, "", 0, defaultHandlerTimeout)
	}
	handler = requestLogger(handler)
	if len(cfg.AllowedOrigins) > 0 {
		handler = withCORS(handler, cfg.AllowedOrigins)
	}

	log.Printf("Allowed origins: %v", cfg.AllowedOrigins)
	return handler
}
