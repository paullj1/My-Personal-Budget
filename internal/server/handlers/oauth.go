package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"my-personal-budget/internal/auth"
	"my-personal-budget/internal/config"
	"my-personal-budget/internal/oauth"
	"my-personal-budget/internal/store"
)

// OAuthStore is the persistence the authorization server needs.
type OAuthStore interface {
	RegisterOAuthClient(ctx context.Context, in store.OAuthClientInput) (store.OAuthClient, string, error)
	GetOAuthClient(ctx context.Context, clientID string) (store.OAuthClient, error)
	VerifyOAuthClientSecret(ctx context.Context, clientID, secret string) error
	GrantAuthorizationCode(ctx context.Context, in store.OAuthAuthCodeInput) (string, error)
	ConsumeAuthorizationCode(ctx context.Context, code string) (store.OAuthAuthCode, error)
	IssueOAuthTokens(ctx context.Context, authorizationID int64, scope, resource string, accessTTL time.Duration) (store.OAuthTokenPair, error)
	RedeemRefreshToken(ctx context.Context, token, clientID string) (store.OAuthTokenInfo, error)
	RevokeOAuthToken(ctx context.Context, token string) error
	ListOAuthConnections(ctx context.Context, userID int64) ([]store.OAuthConnection, error)
	DeleteOAuthConnection(ctx context.Context, userID, connectionID int64) error
	SetOAuthConnectionExpiry(ctx context.Context, userID, connectionID int64, expiresAt *time.Time) error
}

// OAuthHandler implements the authorization server endpoints.
type OAuthHandler struct {
	cfg     config.Config
	store   OAuthStore
	pending *oauth.PendingStore
}

// ConsentPath is where the browser is sent to approve a request. The SPA owns
// this route; the server only ever redirects to it.
const ConsentPath = "/oauth/consent"

// MCPResourcePath is the protected resource this server issues tokens for.
const MCPResourcePath = "/mcp"

// Fallbacks for a Config assembled by hand rather than by config.FromEnv. A
// zero TTL would mint codes and tokens that are already expired, which fails as
// an unexplained invalid_grant rather than as a misconfiguration.
const (
	defaultAuthCodeTTL    = 5 * time.Minute
	defaultAccessTokenTTL = time.Hour
)

func NewOAuthHandler(cfg config.Config, store OAuthStore) *OAuthHandler {
	if cfg.OAuthAuthCodeTTL <= 0 {
		cfg.OAuthAuthCodeTTL = defaultAuthCodeTTL
	}
	if cfg.OAuthAccessTokenTTL <= 0 {
		cfg.OAuthAccessTokenTTL = defaultAccessTokenTTL
	}
	return &OAuthHandler{
		cfg:   cfg,
		store: store,
		// Twice the code TTL: the window covers a passkey prompt and a consent
		// click, not just the exchange that follows.
		pending: oauth.NewPendingStore(cfg.OAuthAuthCodeTTL * 2),
	}
}

// resourceURL is the canonical identifier of the MCP endpoint, and the audience
// tokens are bound to.
func (h *OAuthHandler) resourceURL() string {
	return h.cfg.PublicBaseURL + MCPResourcePath
}

// --- Metadata -------------------------------------------------------------

// ProtectedResourceMetadata implements RFC 9728. A client that gets a 401 from
// /mcp reads this to find out which authorization server to talk to.
func (h *OAuthHandler) ProtectedResourceMetadata(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w, http.MethodGet)
		return
	}
	respondJSON(w, http.StatusOK, map[string]any{
		"resource":                 h.resourceURL(),
		"authorization_servers":    []string{h.cfg.PublicBaseURL},
		"scopes_supported":         oauth.SupportedScopes,
		"bearer_methods_supported": []string{"header"},
		"resource_name":            h.cfg.RelyingPartyName,
		"resource_documentation":   h.cfg.PublicBaseURL,
	})
}

// AuthorizationServerMetadata implements RFC 8414.
func (h *OAuthHandler) AuthorizationServerMetadata(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w, http.MethodGet)
		return
	}
	base := h.cfg.PublicBaseURL
	respondJSON(w, http.StatusOK, map[string]any{
		"issuer":                                base,
		"authorization_endpoint":                base + "/oauth/authorize",
		"token_endpoint":                        base + "/oauth/token",
		"registration_endpoint":                 base + "/oauth/register",
		"revocation_endpoint":                   base + "/oauth/revoke",
		"scopes_supported":                      oauth.SupportedScopes,
		"response_types_supported":              []string{"code"},
		"grant_types_supported":                 []string{"authorization_code", "refresh_token"},
		"token_endpoint_auth_methods_supported": []string{"none", "client_secret_post", "client_secret_basic"},
		// S256 only: OAuth 2.1 drops "plain", and advertising it would invite a
		// client to use it.
		"code_challenge_methods_supported":           []string{"S256"},
		"revocation_endpoint_auth_methods_supported": []string{"none", "client_secret_post", "client_secret_basic"},
		"resource_indicators_supported":              true,
	})
}

// --- Dynamic client registration -----------------------------------------

type registrationRequest struct {
	ClientName              string   `json:"client_name"`
	ClientURI               string   `json:"client_uri"`
	LogoURI                 string   `json:"logo_uri"`
	RedirectURIs            []string `json:"redirect_uris"`
	GrantTypes              []string `json:"grant_types"`
	ResponseTypes           []string `json:"response_types"`
	TokenEndpointAuthMethod string   `json:"token_endpoint_auth_method"`
	Scope                   string   `json:"scope"`
}

// Register implements RFC 7591. Registration is open, because a remote MCP
// client has no way to be pre-provisioned. It grants nothing on its own: a
// registered client still has to send the user through /oauth/authorize and get
// a passkey-backed consent.
func (h *OAuthHandler) Register(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w, http.MethodPost)
		return
	}
	var req registrationRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 64<<10)).Decode(&req); err != nil {
		oauthError(w, http.StatusBadRequest, "invalid_client_metadata", "invalid JSON payload")
		return
	}
	if len(req.RedirectURIs) == 0 {
		oauthError(w, http.StatusBadRequest, "invalid_redirect_uri", "redirect_uris is required")
		return
	}
	if len(req.RedirectURIs) > 10 {
		oauthError(w, http.StatusBadRequest, "invalid_redirect_uri", "too many redirect_uris")
		return
	}
	for _, uri := range req.RedirectURIs {
		if err := oauth.CheckRedirectURI(uri); err != nil {
			oauthError(w, http.StatusBadRequest, "invalid_redirect_uri", err.Error())
			return
		}
	}

	if len(req.GrantTypes) == 0 {
		req.GrantTypes = []string{"authorization_code", "refresh_token"}
	}
	for _, g := range req.GrantTypes {
		if g != "authorization_code" && g != "refresh_token" {
			oauthError(w, http.StatusBadRequest, "invalid_client_metadata",
				"only authorization_code and refresh_token are supported")
			return
		}
	}
	if len(req.ResponseTypes) == 0 {
		req.ResponseTypes = []string{"code"}
	}
	for _, rt := range req.ResponseTypes {
		if rt != "code" {
			oauthError(w, http.StatusBadRequest, "invalid_client_metadata", "only the code response type is supported")
			return
		}
	}
	if req.TokenEndpointAuthMethod == "" {
		req.TokenEndpointAuthMethod = "none"
	}
	switch req.TokenEndpointAuthMethod {
	case "none", "client_secret_post", "client_secret_basic":
	default:
		oauthError(w, http.StatusBadRequest, "invalid_client_metadata", "unsupported token_endpoint_auth_method")
		return
	}

	name := strings.TrimSpace(req.ClientName)
	if name == "" {
		name = "Unnamed client"
	}
	if len(name) > 200 {
		name = name[:200]
	}

	client, secret, err := h.store.RegisterOAuthClient(r.Context(), store.OAuthClientInput{
		ClientName:              name,
		ClientURI:               strings.TrimSpace(req.ClientURI),
		LogoURI:                 strings.TrimSpace(req.LogoURI),
		RedirectURIs:            req.RedirectURIs,
		GrantTypes:              req.GrantTypes,
		ResponseTypes:           req.ResponseTypes,
		TokenEndpointAuthMethod: req.TokenEndpointAuthMethod,
		Scope:                   oauth.NormalizeScope(req.Scope),
	})
	if err != nil {
		log.Printf("oauth: client registration failed: %v", err)
		oauthError(w, http.StatusInternalServerError, "server_error", "could not register client")
		return
	}

	out := map[string]any{
		"client_id":                  client.ClientID,
		"client_name":                client.ClientName,
		"redirect_uris":              client.RedirectURIs,
		"grant_types":                client.GrantTypes,
		"response_types":             client.ResponseTypes,
		"token_endpoint_auth_method": client.TokenEndpointAuthMethod,
		"scope":                      client.Scope,
		"client_id_issued_at":        client.CreatedAt.Unix(),
	}
	if secret != "" {
		out["client_secret"] = secret
		// 0 means the secret does not expire, per RFC 7591.
		out["client_secret_expires_at"] = 0
	}
	respondJSON(w, http.StatusCreated, out)
}

// --- Authorization --------------------------------------------------------

// Authorize validates the request, then hands the browser to the SPA's consent
// screen. Nothing is granted here: this endpoint's only job is to make sure that
// by the time a human sees a prompt, the parameters behind it are already known
// good.
func (h *OAuthHandler) Authorize(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w, http.MethodGet)
		return
	}
	q := r.URL.Query()
	clientID := strings.TrimSpace(q.Get("client_id"))
	if clientID == "" {
		oauthError(w, http.StatusBadRequest, "invalid_request", "client_id is required")
		return
	}
	client, err := h.store.GetOAuthClient(r.Context(), clientID)
	if errors.Is(err, store.ErrNotFound) {
		oauthError(w, http.StatusBadRequest, "invalid_client", "unknown client_id")
		return
	}
	if err != nil {
		oauthError(w, http.StatusInternalServerError, "server_error", "could not load client")
		return
	}

	// Redirect URI is validated before anything else can be redirected to it.
	// Errors past this point go back to the client; errors here must not, or an
	// attacker could use this endpoint to bounce a code anywhere.
	redirectURI := strings.TrimSpace(q.Get("redirect_uri"))
	if err := oauth.ValidateRedirectURI(client.RedirectURIs, redirectURI); err != nil {
		oauthError(w, http.StatusBadRequest, "invalid_request", "redirect_uri does not match a registered URI")
		return
	}
	if redirectURI == "" {
		redirectURI = client.RedirectURIs[0]
	}

	state := q.Get("state")
	if rt := q.Get("response_type"); rt != "code" {
		redirectWithError(w, r, redirectURI, state, "unsupported_response_type", "only the code response type is supported")
		return
	}

	challenge := strings.TrimSpace(q.Get("code_challenge"))
	method := strings.TrimSpace(q.Get("code_challenge_method"))
	// PKCE is mandatory for a public client, because nothing else binds the code
	// to whoever asked for it -- there is no secret to prove identity with at the
	// token endpoint. A confidential client has that secret, which is the
	// protection OAuth 2.0 was built around, so PKCE is defence in depth there
	// rather than the only defence.
	//
	// Requiring it from everyone locks out Home Assistant, whose MCP integration
	// goes through application_credentials -> AuthImplementation, a subclass of
	// LocalOAuth2Implementation that sends no code_challenge at all. It would
	// arrive with a valid client_secret and be turned away before reaching the
	// consent screen.
	if challenge == "" && !client.IsConfidential() {
		redirectWithError(w, r, redirectURI, state, "invalid_request",
			"code_challenge is required for clients without a client_secret")
		return
	}
	if method != "" && method != "S256" {
		redirectWithError(w, r, redirectURI, state, "invalid_request", "code_challenge_method must be S256")
		return
	}

	// RFC 8707. A token minted for this server must not be replayable against a
	// different resource, so an explicit mismatch is refused rather than ignored.
	resource := strings.TrimSpace(q.Get("resource"))
	if resource != "" && !sameResource(resource, h.resourceURL()) {
		redirectWithError(w, r, redirectURI, state, "invalid_target", "resource is not served here")
		return
	}

	id, err := h.pending.Put(oauth.PendingRequest{
		ClientID:            client.ClientID,
		ClientName:          client.ClientName,
		ClientURI:           client.ClientURI,
		RedirectURI:         redirectURI,
		State:               state,
		CodeChallenge:       challenge,
		CodeChallengeMethod: "S256",
		Scope:               oauth.NormalizeScope(q.Get("scope")),
		Resource:            h.resourceURL(),
	})
	if err != nil {
		redirectWithError(w, r, redirectURI, state, "server_error", "could not start authorization")
		return
	}

	dest := ConsentPath + "?request_id=" + url.QueryEscape(id)
	http.Redirect(w, r, dest, http.StatusFound)
}

// consentView is what the SPA renders. It deliberately does not include the
// redirect URI's contents beyond its host: the user is being asked about an
// application, not a URL.
type consentView struct {
	RequestID  string   `json:"request_id"`
	ClientName string   `json:"client_name"`
	ClientURI  string   `json:"client_uri,omitempty"`
	RedirectTo string   `json:"redirect_to"`
	Scopes     []string `json:"scopes"`
}

// ConsentHandler serves the logged-in half of the flow: GET describes the
// pending request, POST decides it. Both require a JWT, so consent is always an
// authenticated user's act.
func (h *OAuthHandler) ConsentHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		userID := auth.UserIDFromContext(r.Context())
		if userID == nil {
			respondError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		switch r.Method {
		case http.MethodGet:
			h.describeConsent(w, r)
		case http.MethodPost:
			h.decideConsent(w, r, *userID)
		default:
			methodNotAllowed(w, http.MethodGet, http.MethodPost)
		}
	})
}

func (h *OAuthHandler) describeConsent(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.URL.Query().Get("request_id"))
	req, ok := h.pending.Peek(id)
	if !ok {
		respondError(w, http.StatusNotFound, "this authorization request expired; start again from the app")
		return
	}
	host := req.RedirectURI
	if parsed, err := url.Parse(req.RedirectURI); err == nil && parsed.Host != "" {
		host = parsed.Host
	}
	respondJSON(w, http.StatusOK, consentView{
		RequestID:  id,
		ClientName: req.ClientName,
		ClientURI:  req.ClientURI,
		RedirectTo: host,
		Scopes:     strings.Fields(req.Scope),
	})
}

func (h *OAuthHandler) decideConsent(w http.ResponseWriter, r *http.Request, userID int64) {
	var body struct {
		RequestID string `json:"request_id"`
		Approve   bool   `json:"approve"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 8<<10)).Decode(&body); err != nil {
		respondError(w, http.StatusBadRequest, "invalid JSON payload")
		return
	}
	req, ok := h.pending.Consume(strings.TrimSpace(body.RequestID))
	if !ok {
		respondError(w, http.StatusNotFound, "this authorization request expired; start again from the app")
		return
	}

	if !body.Approve {
		respondJSON(w, http.StatusOK, map[string]any{
			"redirect_to": errorRedirectURL(req.RedirectURI, req.State, "access_denied", "the user declined"),
		})
		return
	}

	code, err := h.store.GrantAuthorizationCode(r.Context(), store.OAuthAuthCodeInput{
		UserID:              userID,
		ClientID:            req.ClientID,
		RedirectURI:         req.RedirectURI,
		CodeChallenge:       req.CodeChallenge,
		CodeChallengeMethod: req.CodeChallengeMethod,
		Resource:            req.Resource,
		Scope:               req.Scope,
		TTL:                 h.cfg.OAuthAuthCodeTTL,
	})
	if err != nil {
		log.Printf("oauth: granting code failed: %v", err)
		respondError(w, http.StatusInternalServerError, "could not complete authorization")
		return
	}
	target, _ := url.Parse(req.RedirectURI)
	q := target.Query()
	q.Set("code", code)
	if req.State != "" {
		q.Set("state", req.State)
	}
	target.RawQuery = q.Encode()
	respondJSON(w, http.StatusOK, map[string]any{"redirect_to": target.String()})
}

// --- Token ----------------------------------------------------------------

// Token exchanges an authorization code, or rotates a refresh token.
func (h *OAuthHandler) Token(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w, http.MethodPost)
		return
	}
	if err := r.ParseForm(); err != nil {
		oauthError(w, http.StatusBadRequest, "invalid_request", "could not parse form body")
		return
	}

	clientID, clientSecret := clientCredentials(r)
	if clientID == "" {
		oauthError(w, http.StatusBadRequest, "invalid_request", "client_id is required")
		return
	}
	client, err := h.store.GetOAuthClient(r.Context(), clientID)
	if errors.Is(err, store.ErrNotFound) {
		oauthError(w, http.StatusUnauthorized, "invalid_client", "unknown client_id")
		return
	}
	if err != nil {
		oauthError(w, http.StatusInternalServerError, "server_error", "could not load client")
		return
	}
	if client.IsConfidential() {
		if err := h.store.VerifyOAuthClientSecret(r.Context(), clientID, clientSecret); err != nil {
			oauthError(w, http.StatusUnauthorized, "invalid_client", "client authentication failed")
			return
		}
	}

	switch r.PostForm.Get("grant_type") {
	case "authorization_code":
		h.exchangeCode(w, r, client)
	case "refresh_token":
		h.refreshToken(w, r, client)
	default:
		oauthError(w, http.StatusBadRequest, "unsupported_grant_type", "unsupported grant_type")
	}
}

func (h *OAuthHandler) exchangeCode(w http.ResponseWriter, r *http.Request, client store.OAuthClient) {
	code := r.PostForm.Get("code")
	if code == "" {
		oauthError(w, http.StatusBadRequest, "invalid_request", "code is required")
		return
	}
	granted, err := h.store.ConsumeAuthorizationCode(r.Context(), code)
	switch {
	case errors.Is(err, store.ErrNotFound):
		oauthError(w, http.StatusBadRequest, "invalid_grant", "code is invalid or already used")
		return
	case errors.Is(err, store.ErrOAuthExpired):
		oauthError(w, http.StatusBadRequest, "invalid_grant", "code expired")
		return
	case err != nil:
		oauthError(w, http.StatusInternalServerError, "server_error", "could not redeem code")
		return
	}

	// A code issued to one client must not be redeemable by another, even though
	// both authenticated successfully as themselves.
	if granted.ClientID != client.ClientID {
		oauthError(w, http.StatusBadRequest, "invalid_grant", "code was not issued to this client")
		return
	}
	// The redirect URI is replayed so a code cannot be exchanged after being
	// delivered to a different registered URI than the one it was bound to.
	if sent := r.PostForm.Get("redirect_uri"); sent != "" && sent != granted.RedirectURI {
		oauthError(w, http.StatusBadRequest, "invalid_grant", "redirect_uri does not match the authorization request")
		return
	}
	// Verify only when the authorization request carried a challenge. A confidential
	// client may omit PKCE, but one that used it must still satisfy it -- otherwise
	// an attacker holding a stolen code could simply drop the verifier and be let
	// through, which would make the protection worthless for the clients that do
	// rely on it.
	if granted.CodeChallenge != "" {
		if err := oauth.VerifyPKCE(granted.CodeChallenge, granted.CodeChallengeMethod, r.PostForm.Get("code_verifier")); err != nil {
			oauthError(w, http.StatusBadRequest, "invalid_grant", "code_verifier is invalid")
			return
		}
	}
	if resource := r.PostForm.Get("resource"); resource != "" && !sameResource(resource, granted.Resource) {
		oauthError(w, http.StatusBadRequest, "invalid_target", "resource does not match the authorization request")
		return
	}

	h.issue(w, r, granted.AuthorizationID, granted.Scope, granted.Resource)
}

func (h *OAuthHandler) refreshToken(w http.ResponseWriter, r *http.Request, client store.OAuthClient) {
	token := r.PostForm.Get("refresh_token")
	if token == "" {
		oauthError(w, http.StatusBadRequest, "invalid_request", "refresh_token is required")
		return
	}
	info, err := h.store.RedeemRefreshToken(r.Context(), token, client.ClientID)
	switch {
	case errors.Is(err, store.ErrNotFound):
		oauthError(w, http.StatusBadRequest, "invalid_grant", "refresh token is invalid or already used")
		return
	case errors.Is(err, store.ErrOAuthExpired):
		oauthError(w, http.StatusBadRequest, "invalid_grant", "this connection has expired; reconnect from the app")
		return
	case err != nil:
		oauthError(w, http.StatusInternalServerError, "server_error", "could not refresh token")
		return
	}

	scope := info.Scope
	// A refresh may narrow the granted scope but never widen it.
	if asked := r.PostForm.Get("scope"); asked != "" {
		narrowed := make([]string, 0, len(strings.Fields(asked)))
		for _, s := range strings.Fields(asked) {
			if oauth.HasScope(info.Scope, s) {
				narrowed = append(narrowed, s)
			}
		}
		if len(narrowed) == 0 {
			oauthError(w, http.StatusBadRequest, "invalid_scope", "requested scope exceeds the original grant")
			return
		}
		scope = strings.Join(narrowed, " ")
	}
	h.issue(w, r, info.AuthorizationID, scope, info.Resource)
}

func (h *OAuthHandler) issue(w http.ResponseWriter, r *http.Request, authorizationID int64, scope, resource string) {
	pair, err := h.store.IssueOAuthTokens(r.Context(), authorizationID, scope, resource, h.cfg.OAuthAccessTokenTTL)
	if err != nil {
		log.Printf("oauth: issuing tokens failed: %v", err)
		oauthError(w, http.StatusInternalServerError, "server_error", "could not issue tokens")
		return
	}
	// Tokens must never be cached by an intermediary.
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Pragma", "no-cache")
	respondJSON(w, http.StatusOK, map[string]any{
		"access_token":  pair.AccessToken,
		"token_type":    "Bearer",
		"expires_in":    pair.ExpiresIn,
		"refresh_token": pair.RefreshToken,
		"scope":         pair.Scope,
	})
}

// Revoke implements RFC 7009.
func (h *OAuthHandler) Revoke(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w, http.MethodPost)
		return
	}
	if err := r.ParseForm(); err != nil {
		oauthError(w, http.StatusBadRequest, "invalid_request", "could not parse form body")
		return
	}
	token := r.PostForm.Get("token")
	if token == "" {
		oauthError(w, http.StatusBadRequest, "invalid_request", "token is required")
		return
	}
	if err := h.store.RevokeOAuthToken(r.Context(), token); err != nil {
		log.Printf("oauth: revocation failed: %v", err)
		oauthError(w, http.StatusInternalServerError, "server_error", "could not revoke token")
		return
	}
	w.WriteHeader(http.StatusOK)
}

// --- Connections (first-party UI) -----------------------------------------

// ConnectionsHandler backs the Connections screen: list what is connected, set
// when a connection lapses, or disconnect it outright.
func (h *OAuthHandler) ConnectionsHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		userID := auth.UserIDFromContext(r.Context())
		if userID == nil {
			respondError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		rest := strings.Trim(strings.TrimPrefix(r.URL.Path, "/connections"), "/")
		if rest == "" {
			if r.Method != http.MethodGet {
				methodNotAllowed(w, http.MethodGet)
				return
			}
			h.listConnections(w, r, *userID)
			return
		}
		id, err := strconv.ParseInt(rest, 10, 64)
		if err != nil {
			respondError(w, http.StatusBadRequest, "invalid connection id")
			return
		}
		switch r.Method {
		case http.MethodDelete:
			h.deleteConnection(w, r, *userID, id)
		case http.MethodPatch, http.MethodPut:
			h.updateConnection(w, r, *userID, id)
		default:
			methodNotAllowed(w, http.MethodDelete, http.MethodPatch)
		}
	})
}

func (h *OAuthHandler) listConnections(w http.ResponseWriter, r *http.Request, userID int64) {
	conns, err := h.store.ListOAuthConnections(r.Context(), userID)
	if err != nil {
		log.Printf("oauth: listing connections failed: %v", err)
		respondError(w, http.StatusInternalServerError, "failed to load connections")
		return
	}
	if conns == nil {
		conns = []store.OAuthConnection{}
	}
	respondJSON(w, http.StatusOK, map[string]any{
		"data": conns,
		"meta": map[string]any{"count": len(conns)},
	})
}

func (h *OAuthHandler) deleteConnection(w http.ResponseWriter, r *http.Request, userID, id int64) {
	if err := h.store.DeleteOAuthConnection(r.Context(), userID, id); errors.Is(err, store.ErrNotFound) {
		respondError(w, http.StatusNotFound, "connection not found")
		return
	} else if err != nil {
		log.Printf("oauth: disconnecting failed: %v", err)
		respondError(w, http.StatusInternalServerError, "failed to disconnect")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *OAuthHandler) updateConnection(w http.ResponseWriter, r *http.Request, userID, id int64) {
	var body struct {
		// ExpiresAt absent or null means the connection never expires, which is
		// how connections are created.
		ExpiresAt *string `json:"expires_at"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 8<<10)).Decode(&body); err != nil {
		respondError(w, http.StatusBadRequest, "invalid JSON payload")
		return
	}
	var expiresAt *time.Time
	if body.ExpiresAt != nil && strings.TrimSpace(*body.ExpiresAt) != "" {
		parsed, err := time.Parse(time.RFC3339, strings.TrimSpace(*body.ExpiresAt))
		if err != nil {
			respondError(w, http.StatusBadRequest, "expires_at must be an RFC 3339 timestamp")
			return
		}
		expiresAt = &parsed
	}
	if err := h.store.SetOAuthConnectionExpiry(r.Context(), userID, id, expiresAt); errors.Is(err, store.ErrNotFound) {
		respondError(w, http.StatusNotFound, "connection not found")
		return
	} else if err != nil {
		log.Printf("oauth: setting connection expiry failed: %v", err)
		respondError(w, http.StatusInternalServerError, "failed to update connection")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// --- helpers --------------------------------------------------------------

// clientCredentials reads client authentication from either the Basic header or
// the form body, since RFC 6749 permits both and clients differ.
func clientCredentials(r *http.Request) (string, string) {
	if id, secret, ok := r.BasicAuth(); ok && id != "" {
		return id, secret
	}
	return strings.TrimSpace(r.PostForm.Get("client_id")), r.PostForm.Get("client_secret")
}

// sameResource compares RFC 8707 resource indicators. A trailing slash is the
// one difference clients produce that carries no meaning.
func sameResource(a, b string) bool {
	return strings.TrimSuffix(a, "/") == strings.TrimSuffix(b, "/")
}

func oauthError(w http.ResponseWriter, status int, code, description string) {
	w.Header().Set("Cache-Control", "no-store")
	respondJSON(w, status, map[string]string{
		"error":             code,
		"error_description": description,
	})
}

func errorRedirectURL(redirectURI, state, code, description string) string {
	target, err := url.Parse(redirectURI)
	if err != nil {
		return redirectURI
	}
	q := target.Query()
	q.Set("error", code)
	q.Set("error_description", description)
	if state != "" {
		q.Set("state", state)
	}
	target.RawQuery = q.Encode()
	return target.String()
}

func redirectWithError(w http.ResponseWriter, r *http.Request, redirectURI, state, code, description string) {
	http.Redirect(w, r, errorRedirectURL(redirectURI, state, code, description), http.StatusFound)
}
