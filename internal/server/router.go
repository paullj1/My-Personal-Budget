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

	// protectedResourceMetadataPath is fixed by RFC 9728; clients look here
	// after a 401 from /mcp.
	protectedResourceMetadataPath = "/.well-known/oauth-protected-resource"
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

	// The OAuth authorization server, when a public origin is configured. Without
	// it /mcp still works, but only with a pasted API key: a web client has no way
	// to discover an issuer that cannot name itself.
	var oauthHandler *handlers.OAuthHandler
	resourceMetadataURL := ""
	if cfg.OAuthEnabled() {
		oauthHandler = handlers.NewOAuthHandler(cfg, store)
		resourceMetadataURL = cfg.PublicBaseURL + protectedResourceMetadataPath

		mux.HandleFunc(protectedResourceMetadataPath, oauthHandler.ProtectedResourceMetadata)
		// Clients built against RFC 9728's path-insertion rule look for the
		// resource's own path appended, so /mcp is served alongside the bare form.
		mux.HandleFunc(protectedResourceMetadataPath+handlers.MCPResourcePath, oauthHandler.ProtectedResourceMetadata)
		mux.HandleFunc("/.well-known/oauth-authorization-server", oauthHandler.AuthorizationServerMetadata)
		// Registration is the one endpoint an unauthenticated caller can use to
		// write a row, so it is the one that needs a cap. See middleware.RateLimit
		// for why the cap is global rather than per-address.
		mux.Handle("/oauth/register", middleware.RateLimit(
			cfg.OAuthRegistrationLimit, cfg.OAuthRegistrationWindow,
			http.HandlerFunc(oauthHandler.Register)))
		mux.HandleFunc("/oauth/authorize", oauthHandler.Authorize)
		mux.HandleFunc("/oauth/token", oauthHandler.Token)
		mux.HandleFunc("/oauth/revoke", oauthHandler.Revoke)
	} else {
		log.Print("OAuth disabled: set PUBLIC_BASE_URL and JWT_SECRET to let web clients connect to /mcp")
	}

	mcp := handlers.NewMCPHandler(store)
	mux.Handle("/mcp", middleware.MCPAuth(store, resourceMetadataURL, mcp))

	apiMux := http.NewServeMux()
	apiMux.Handle("/", api.Router())
	if oauthHandler != nil {
		// Consent and connection management ride the JWT-protected API, because
		// both are the logged-in user acting in their own browser.
		apiMux.Handle("/oauth/consent", oauthHandler.ConsentHandler())
		apiMux.Handle("/connections", oauthHandler.ConnectionsHandler())
		apiMux.Handle("/connections/", oauthHandler.ConnectionsHandler())
	}

	protected := http.StripPrefix("/api/v1", apiMux)
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
	// Applied unconditionally: OAuth discovery and /mcp are cross-origin by
	// nature, and the wrapper is inert on a request with no Origin header.
	handler = withCORS(handler, cfg.AllowedOrigins)

	log.Printf("Allowed origins: %v", cfg.AllowedOrigins)
	return handler
}
