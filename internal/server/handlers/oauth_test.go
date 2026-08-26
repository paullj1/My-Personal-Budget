package handlers

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"my-personal-budget/internal/auth"
	"my-personal-budget/internal/config"
	"my-personal-budget/internal/oauth"
	"my-personal-budget/internal/store"
)

// fakeOAuthStore mirrors the real store's semantics closely enough to exercise
// the handler: single-use codes, rotating refresh tokens, and revocation that
// takes the whole connection with it.
type fakeOAuthStore struct {
	mu sync.Mutex

	clients map[string]store.OAuthClient
	secrets map[string]string

	nextAuthID int64
	auths      map[int64]*store.OAuthConnection
	authUser   map[int64]int64

	codes  map[string]*fakeCode
	tokens map[string]*fakeToken
}

type fakeCode struct {
	granted   store.OAuthAuthCode
	expiresAt time.Time
	consumed  bool
}

type fakeToken struct {
	info    store.OAuthTokenInfo
	kind    string
	expires time.Time
	revoked bool
}

func newFakeOAuthStore() *fakeOAuthStore {
	return &fakeOAuthStore{
		clients:  map[string]store.OAuthClient{},
		secrets:  map[string]string{},
		auths:    map[int64]*store.OAuthConnection{},
		authUser: map[int64]int64{},
		codes:    map[string]*fakeCode{},
		tokens:   map[string]*fakeToken{},
	}
}

func (f *fakeOAuthStore) RegisterOAuthClient(_ context.Context, in store.OAuthClientInput) (store.OAuthClient, string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	clientID, err := oauth.GenerateClientID()
	if err != nil {
		return store.OAuthClient{}, "", err
	}
	client := store.OAuthClient{
		ClientID:                clientID,
		ClientName:              in.ClientName,
		ClientURI:               in.ClientURI,
		RedirectURIs:            in.RedirectURIs,
		GrantTypes:              in.GrantTypes,
		ResponseTypes:           in.ResponseTypes,
		TokenEndpointAuthMethod: in.TokenEndpointAuthMethod,
		Scope:                   in.Scope,
		CreatedAt:               time.Now(),
	}
	var secret string
	if in.TokenEndpointAuthMethod != "none" {
		secret, _, _ = oauth.GenerateSecret("mpbs_")
		f.secrets[clientID] = secret
	}
	f.clients[clientID] = client
	return client, secret, nil
}

func (f *fakeOAuthStore) GetOAuthClient(_ context.Context, clientID string) (store.OAuthClient, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	client, ok := f.clients[clientID]
	if !ok {
		return store.OAuthClient{}, store.ErrNotFound
	}
	return client, nil
}

func (f *fakeOAuthStore) VerifyOAuthClientSecret(_ context.Context, clientID, secret string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if known, ok := f.secrets[clientID]; !ok || known != secret {
		return store.ErrNotFound
	}
	return nil
}

func (f *fakeOAuthStore) GrantAuthorizationCode(_ context.Context, in store.OAuthAuthCodeInput) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	var authID int64
	for id, conn := range f.auths {
		if conn.ClientID == in.ClientID && f.authUser[id] == in.UserID {
			authID = id
			break
		}
	}
	if authID == 0 {
		f.nextAuthID++
		authID = f.nextAuthID
		f.auths[authID] = &store.OAuthConnection{
			ID: authID, ClientID: in.ClientID, ClientName: f.clients[in.ClientID].ClientName,
			Scope: in.Scope, CreatedAt: time.Now(),
		}
		f.authUser[authID] = in.UserID
	}
	f.auths[authID].Scope = in.Scope

	code, _, err := oauth.GenerateSecret("mpbac_")
	if err != nil {
		return "", err
	}
	f.codes[code] = &fakeCode{
		granted: store.OAuthAuthCode{
			AuthorizationID: authID, UserID: in.UserID, ClientID: in.ClientID,
			RedirectURI: in.RedirectURI, CodeChallenge: in.CodeChallenge,
			CodeChallengeMethod: in.CodeChallengeMethod, Resource: in.Resource, Scope: in.Scope,
		},
		expiresAt: time.Now().Add(in.TTL),
	}
	return code, nil
}

func (f *fakeOAuthStore) ConsumeAuthorizationCode(_ context.Context, code string) (store.OAuthAuthCode, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	entry, ok := f.codes[code]
	if !ok || entry.consumed {
		return store.OAuthAuthCode{}, store.ErrNotFound
	}
	entry.consumed = true
	if time.Now().After(entry.expiresAt) {
		return store.OAuthAuthCode{}, store.ErrOAuthExpired
	}
	return entry.granted, nil
}

func (f *fakeOAuthStore) IssueOAuthTokens(_ context.Context, authorizationID int64, scope, resource string, ttl time.Duration) (store.OAuthTokenPair, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	access, _, _ := oauth.GenerateSecret("mpbat_")
	refresh, _, _ := oauth.GenerateSecret("mpbrt_")
	conn := f.auths[authorizationID]
	info := store.OAuthTokenInfo{
		AuthorizationID: authorizationID, UserID: f.authUser[authorizationID],
		ClientID: conn.ClientID, ClientName: conn.ClientName, Scope: scope, Resource: resource,
	}
	f.tokens[access] = &fakeToken{info: info, kind: "access", expires: time.Now().Add(ttl)}
	f.tokens[refresh] = &fakeToken{info: info, kind: "refresh"}
	return store.OAuthTokenPair{
		AccessToken: access, RefreshToken: refresh, ExpiresIn: int(ttl.Seconds()), Scope: scope,
	}, nil
}

func (f *fakeOAuthStore) RedeemRefreshToken(_ context.Context, token, clientID string) (store.OAuthTokenInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	entry, ok := f.tokens[token]
	if !ok || entry.kind != "refresh" || entry.revoked || entry.info.ClientID != clientID {
		return store.OAuthTokenInfo{}, store.ErrNotFound
	}
	if conn, ok := f.auths[entry.info.AuthorizationID]; !ok || conn == nil {
		return store.OAuthTokenInfo{}, store.ErrNotFound
	}
	entry.revoked = true
	return entry.info, nil
}

func (f *fakeOAuthStore) RevokeOAuthToken(_ context.Context, token string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	entry, ok := f.tokens[token]
	if !ok {
		return nil
	}
	entry.revoked = true
	if entry.kind == "refresh" {
		delete(f.auths, entry.info.AuthorizationID)
	}
	return nil
}

func (f *fakeOAuthStore) LookupOAuthAccessToken(_ context.Context, token string) (store.OAuthTokenInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	entry, ok := f.tokens[token]
	if !ok || entry.kind != "access" || entry.revoked {
		return store.OAuthTokenInfo{}, store.ErrNotFound
	}
	if _, ok := f.auths[entry.info.AuthorizationID]; !ok {
		return store.OAuthTokenInfo{}, store.ErrNotFound
	}
	if !entry.expires.IsZero() && time.Now().After(entry.expires) {
		return store.OAuthTokenInfo{}, store.ErrOAuthExpired
	}
	return entry.info, nil
}

func (f *fakeOAuthStore) ListOAuthConnections(_ context.Context, userID int64) ([]store.OAuthConnection, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []store.OAuthConnection
	for id, conn := range f.auths {
		if f.authUser[id] == userID {
			out = append(out, *conn)
		}
	}
	return out, nil
}

func (f *fakeOAuthStore) DeleteOAuthConnection(_ context.Context, userID, connectionID int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.auths[connectionID]; !ok || f.authUser[connectionID] != userID {
		return store.ErrNotFound
	}
	delete(f.auths, connectionID)
	return nil
}

func (f *fakeOAuthStore) SetOAuthConnectionExpiry(_ context.Context, userID, connectionID int64, expiresAt *time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	conn, ok := f.auths[connectionID]
	if !ok || f.authUser[connectionID] != userID {
		return store.ErrNotFound
	}
	conn.ExpiresAt = expiresAt
	return nil
}

// --- fixtures -------------------------------------------------------------

const testBaseURL = "https://budget.example"

func testOAuthConfig() config.Config {
	return config.Config{
		PublicBaseURL:       testBaseURL,
		JWTSecret:           "test-secret",
		RelyingPartyName:    "My Personal Budget",
		OAuthAccessTokenTTL: time.Hour,
		OAuthAuthCodeTTL:    5 * time.Minute,
	}
}

func newOAuthFixture(t *testing.T) (*OAuthHandler, *fakeOAuthStore) {
	t.Helper()
	fs := newFakeOAuthStore()
	return NewOAuthHandler(testOAuthConfig(), fs), fs
}

// registerClient runs a real registration through the handler.
func registerClient(t *testing.T, h *OAuthHandler, redirectURI string) string {
	t.Helper()
	body, _ := json.Marshal(map[string]any{
		"client_name":   "Test Client",
		"redirect_uris": []string{redirectURI},
	})
	rr := httptest.NewRecorder()
	h.Register(rr, httptest.NewRequest(http.MethodPost, "/oauth/register", strings.NewReader(string(body))))
	if rr.Code != http.StatusCreated {
		t.Fatalf("register: status %d body %s", rr.Code, rr.Body.String())
	}
	var out map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatalf("register decode: %v", err)
	}
	return out["client_id"].(string)
}

func pkce(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// authorizeAndConsent walks the browser half of the flow and returns the code.
func authorizeAndConsent(t *testing.T, h *OAuthHandler, clientID, redirectURI, challenge string, userID int64) (code, state string) {
	t.Helper()
	state = "opaque-state"
	query := url.Values{
		"client_id":             {clientID},
		"redirect_uri":          {redirectURI},
		"response_type":         {"code"},
		"state":                 {state},
		"code_challenge":        {challenge},
		"code_challenge_method": {"S256"},
		"resource":              {testBaseURL + "/mcp"},
	}
	rr := httptest.NewRecorder()
	h.Authorize(rr, httptest.NewRequest(http.MethodGet, "/oauth/authorize?"+query.Encode(), nil))
	if rr.Code != http.StatusFound {
		t.Fatalf("authorize: status %d body %s", rr.Code, rr.Body.String())
	}
	location, err := url.Parse(rr.Header().Get("Location"))
	if err != nil {
		t.Fatalf("authorize redirect: %v", err)
	}
	if location.Path != ConsentPath {
		t.Fatalf("expected a redirect to %s, got %s", ConsentPath, location.Path)
	}
	requestID := location.Query().Get("request_id")
	if requestID == "" {
		t.Fatal("consent redirect carried no request_id")
	}

	body, _ := json.Marshal(map[string]any{"request_id": requestID, "approve": true})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/oauth/consent", strings.NewReader(string(body)))
	req = req.WithContext(auth.WithUserID(req.Context(), userID))
	rr = httptest.NewRecorder()
	h.ConsentHandler().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("consent: status %d body %s", rr.Code, rr.Body.String())
	}
	var decided struct {
		RedirectTo string `json:"redirect_to"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &decided); err != nil {
		t.Fatalf("consent decode: %v", err)
	}
	back, err := url.Parse(decided.RedirectTo)
	if err != nil {
		t.Fatalf("consent redirect: %v", err)
	}
	if got := back.Query().Get("state"); got != state {
		t.Fatalf("state not returned: got %q want %q", got, state)
	}
	return back.Query().Get("code"), state
}

func postToken(t *testing.T, h *OAuthHandler, form url.Values) (int, map[string]any) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/oauth/token", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rr := httptest.NewRecorder()
	h.Token(rr, req)
	var out map[string]any
	_ = json.Unmarshal(rr.Body.Bytes(), &out)
	return rr.Code, out
}

// --- tests ----------------------------------------------------------------

func TestOAuthMetadataDocuments(t *testing.T) {
	h, _ := newOAuthFixture(t)

	rr := httptest.NewRecorder()
	h.ProtectedResourceMetadata(rr, httptest.NewRequest(http.MethodGet, "/.well-known/oauth-protected-resource", nil))
	var resource map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &resource); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got := resource["resource"]; got != testBaseURL+"/mcp" {
		t.Fatalf("resource: got %v", got)
	}
	servers := resource["authorization_servers"].([]any)
	if len(servers) != 1 || servers[0] != testBaseURL {
		t.Fatalf("authorization_servers: got %v", servers)
	}

	rr = httptest.NewRecorder()
	h.AuthorizationServerMetadata(rr, httptest.NewRequest(http.MethodGet, "/.well-known/oauth-authorization-server", nil))
	var server map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &server); err != nil {
		t.Fatalf("decode: %v", err)
	}
	// The issuer has to equal the base the client discovered, or validation fails.
	if got := server["issuer"]; got != testBaseURL {
		t.Fatalf("issuer: got %v", got)
	}
	if got := server["registration_endpoint"]; got != testBaseURL+"/oauth/register" {
		t.Fatalf("registration_endpoint: got %v", got)
	}
	methods := server["code_challenge_methods_supported"].([]any)
	if len(methods) != 1 || methods[0] != "S256" {
		t.Fatalf("expected S256 only, got %v", methods)
	}
}

func TestOAuthRegisterValidatesRedirectURIs(t *testing.T) {
	h, _ := newOAuthFixture(t)

	for _, body := range []string{
		`{"client_name":"x"}`,
		`{"client_name":"x","redirect_uris":["http://evil.example/cb"]}`,
		`{"client_name":"x","redirect_uris":["/relative"]}`,
	} {
		rr := httptest.NewRecorder()
		h.Register(rr, httptest.NewRequest(http.MethodPost, "/oauth/register", strings.NewReader(body)))
		if rr.Code != http.StatusBadRequest {
			t.Fatalf("expected 400 for %s, got %d", body, rr.Code)
		}
	}

	// A public client gets no secret to leak.
	rr := httptest.NewRecorder()
	h.Register(rr, httptest.NewRequest(http.MethodPost, "/oauth/register",
		strings.NewReader(`{"client_name":"x","redirect_uris":["https://claude.ai/cb"]}`)))
	if rr.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rr.Code, rr.Body.String())
	}
	var out map[string]any
	_ = json.Unmarshal(rr.Body.Bytes(), &out)
	if _, ok := out["client_secret"]; ok {
		t.Fatal("a public client was issued a secret")
	}
	if out["token_endpoint_auth_method"] != "none" {
		t.Fatalf("token_endpoint_auth_method: got %v", out["token_endpoint_auth_method"])
	}
}

func TestOAuthAuthorizationCodeFlow(t *testing.T) {
	h, fs := newOAuthFixture(t)
	const redirectURI = "https://claude.ai/api/mcp/auth_callback"
	clientID := registerClient(t, h, redirectURI)

	verifier := "verifier-that-is-long-enough-to-be-realistic-1234"
	code, _ := authorizeAndConsent(t, h, clientID, redirectURI, pkce(verifier), 42)
	if code == "" {
		t.Fatal("consent produced no code")
	}

	status, out := postToken(t, h, url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"client_id":     {clientID},
		"redirect_uri":  {redirectURI},
		"code_verifier": {verifier},
		"resource":      {testBaseURL + "/mcp"},
	})
	if status != http.StatusOK {
		t.Fatalf("token: status %d body %v", status, out)
	}
	access, _ := out["access_token"].(string)
	if access == "" {
		t.Fatalf("no access token: %v", out)
	}
	if out["token_type"] != "Bearer" {
		t.Fatalf("token_type: got %v", out["token_type"])
	}
	if out["scope"] != oauth.DefaultScope {
		t.Fatalf("scope: got %v want %q", out["scope"], oauth.DefaultScope)
	}

	// The token resolves to the user who consented, not to whoever registered.
	info, err := fs.LookupOAuthAccessToken(context.Background(), access)
	if err != nil {
		t.Fatalf("access token does not authenticate: %v", err)
	}
	if info.UserID != 42 {
		t.Fatalf("token belongs to user %d, want 42", info.UserID)
	}
}

func TestOAuthCodeIsSingleUse(t *testing.T) {
	h, _ := newOAuthFixture(t)
	const redirectURI = "https://claude.ai/cb"
	clientID := registerClient(t, h, redirectURI)
	verifier := "verifier-that-is-long-enough-to-be-realistic-1234"
	code, _ := authorizeAndConsent(t, h, clientID, redirectURI, pkce(verifier), 1)

	form := url.Values{
		"grant_type": {"authorization_code"}, "code": {code}, "client_id": {clientID},
		"redirect_uri": {redirectURI}, "code_verifier": {verifier},
	}
	if status, out := postToken(t, h, form); status != http.StatusOK {
		t.Fatalf("first exchange failed: %d %v", status, out)
	}
	status, out := postToken(t, h, form)
	if status != http.StatusBadRequest || out["error"] != "invalid_grant" {
		t.Fatalf("replayed code was accepted: %d %v", status, out)
	}
}

func TestOAuthCodeRequiresMatchingVerifier(t *testing.T) {
	h, _ := newOAuthFixture(t)
	const redirectURI = "https://claude.ai/cb"
	clientID := registerClient(t, h, redirectURI)
	code, _ := authorizeAndConsent(t, h, clientID, redirectURI, pkce("the-real-verifier-0123456789abcdef"), 1)

	status, out := postToken(t, h, url.Values{
		"grant_type": {"authorization_code"}, "code": {code}, "client_id": {clientID},
		"redirect_uri": {redirectURI}, "code_verifier": {"not-the-real-verifier"},
	})
	if status != http.StatusBadRequest || out["error"] != "invalid_grant" {
		t.Fatalf("a mismatched verifier was accepted: %d %v", status, out)
	}
}

// A code intercepted in a redirect must be useless to a different registered
// client, even one that authenticates correctly as itself.
func TestOAuthCodeIsBoundToItsClient(t *testing.T) {
	h, _ := newOAuthFixture(t)
	const redirectURI = "https://claude.ai/cb"
	clientID := registerClient(t, h, redirectURI)
	otherID := registerClient(t, h, "https://other.example/cb")
	verifier := "verifier-that-is-long-enough-to-be-realistic-1234"
	code, _ := authorizeAndConsent(t, h, clientID, redirectURI, pkce(verifier), 1)

	status, out := postToken(t, h, url.Values{
		"grant_type": {"authorization_code"}, "code": {code}, "client_id": {otherID},
		"code_verifier": {verifier},
	})
	if status != http.StatusBadRequest || out["error"] != "invalid_grant" {
		t.Fatalf("another client redeemed the code: %d %v", status, out)
	}
}

func TestOAuthAuthorizeRefusesUnregisteredRedirect(t *testing.T) {
	h, _ := newOAuthFixture(t)
	clientID := registerClient(t, h, "https://claude.ai/cb")

	query := url.Values{
		"client_id": {clientID}, "redirect_uri": {"https://evil.example/cb"},
		"response_type": {"code"}, "code_challenge": {pkce("v")}, "code_challenge_method": {"S256"},
	}
	rr := httptest.NewRecorder()
	h.Authorize(rr, httptest.NewRequest(http.MethodGet, "/oauth/authorize?"+query.Encode(), nil))
	// Must not redirect: bouncing to an unregistered URI is the attack.
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d (Location=%q)", rr.Code, rr.Header().Get("Location"))
	}
}

func TestOAuthAuthorizeRequiresPKCE(t *testing.T) {
	h, _ := newOAuthFixture(t)
	const redirectURI = "https://claude.ai/cb"
	clientID := registerClient(t, h, redirectURI)

	query := url.Values{
		"client_id": {clientID}, "redirect_uri": {redirectURI},
		"response_type": {"code"}, "state": {"s"},
	}
	rr := httptest.NewRecorder()
	h.Authorize(rr, httptest.NewRequest(http.MethodGet, "/oauth/authorize?"+query.Encode(), nil))
	// The redirect URI is known good here, so the error goes back to the client.
	if rr.Code != http.StatusFound {
		t.Fatalf("expected a redirect, got %d", rr.Code)
	}
	back, _ := url.Parse(rr.Header().Get("Location"))
	if back.Query().Get("error") != "invalid_request" {
		t.Fatalf("expected invalid_request, got %q", back.Query().Get("error"))
	}
	if back.Query().Get("state") != "s" {
		t.Fatal("state was not echoed on the error redirect")
	}
}

func TestOAuthConsentDenialRedirectsWithAccessDenied(t *testing.T) {
	h, _ := newOAuthFixture(t)
	const redirectURI = "https://claude.ai/cb"
	clientID := registerClient(t, h, redirectURI)

	query := url.Values{
		"client_id": {clientID}, "redirect_uri": {redirectURI}, "response_type": {"code"},
		"state": {"s"}, "code_challenge": {pkce("v")}, "code_challenge_method": {"S256"},
	}
	rr := httptest.NewRecorder()
	h.Authorize(rr, httptest.NewRequest(http.MethodGet, "/oauth/authorize?"+query.Encode(), nil))
	location, _ := url.Parse(rr.Header().Get("Location"))
	requestID := location.Query().Get("request_id")

	body, _ := json.Marshal(map[string]any{"request_id": requestID, "approve": false})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/oauth/consent", strings.NewReader(string(body)))
	req = req.WithContext(auth.WithUserID(req.Context(), 1))
	rr = httptest.NewRecorder()
	h.ConsentHandler().ServeHTTP(rr, req)

	var out struct {
		RedirectTo string `json:"redirect_to"`
	}
	_ = json.Unmarshal(rr.Body.Bytes(), &out)
	back, _ := url.Parse(out.RedirectTo)
	if back.Query().Get("error") != "access_denied" {
		t.Fatalf("expected access_denied, got %q", back.Query().Get("error"))
	}
	if back.Query().Get("code") != "" {
		t.Fatal("a denial produced a code")
	}
}

func TestOAuthConsentRequiresLogin(t *testing.T) {
	h, _ := newOAuthFixture(t)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/oauth/consent?request_id=x", nil)
	rr := httptest.NewRecorder()
	h.ConsentHandler().ServeHTTP(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rr.Code)
	}
}

// One consent yields one code; the request id cannot be spent twice.
func TestOAuthConsentIsSingleUse(t *testing.T) {
	h, _ := newOAuthFixture(t)
	const redirectURI = "https://claude.ai/cb"
	clientID := registerClient(t, h, redirectURI)

	query := url.Values{
		"client_id": {clientID}, "redirect_uri": {redirectURI}, "response_type": {"code"},
		"code_challenge": {pkce("v")}, "code_challenge_method": {"S256"},
	}
	rr := httptest.NewRecorder()
	h.Authorize(rr, httptest.NewRequest(http.MethodGet, "/oauth/authorize?"+query.Encode(), nil))
	location, _ := url.Parse(rr.Header().Get("Location"))
	requestID := location.Query().Get("request_id")

	body, _ := json.Marshal(map[string]any{"request_id": requestID, "approve": true})
	for i, wantStatus := range []int{http.StatusOK, http.StatusNotFound} {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/oauth/consent", strings.NewReader(string(body)))
		req = req.WithContext(auth.WithUserID(req.Context(), 1))
		rr := httptest.NewRecorder()
		h.ConsentHandler().ServeHTTP(rr, req)
		if rr.Code != wantStatus {
			t.Fatalf("consent %d: got %d want %d", i+1, rr.Code, wantStatus)
		}
	}
}

func TestOAuthRefreshTokenRotates(t *testing.T) {
	h, _ := newOAuthFixture(t)
	const redirectURI = "https://claude.ai/cb"
	clientID := registerClient(t, h, redirectURI)
	verifier := "verifier-that-is-long-enough-to-be-realistic-1234"
	code, _ := authorizeAndConsent(t, h, clientID, redirectURI, pkce(verifier), 7)

	_, first := postToken(t, h, url.Values{
		"grant_type": {"authorization_code"}, "code": {code}, "client_id": {clientID},
		"redirect_uri": {redirectURI}, "code_verifier": {verifier},
	})
	refresh := first["refresh_token"].(string)

	status, second := postToken(t, h, url.Values{
		"grant_type": {"refresh_token"}, "refresh_token": {refresh}, "client_id": {clientID},
	})
	if status != http.StatusOK {
		t.Fatalf("refresh failed: %d %v", status, second)
	}
	if second["refresh_token"] == refresh {
		t.Fatal("the refresh token was not rotated")
	}

	// Re-using the spent refresh token must fail, so a stolen one is good once
	// at most and its reuse surfaces as a failed grant.
	status, out := postToken(t, h, url.Values{
		"grant_type": {"refresh_token"}, "refresh_token": {refresh}, "client_id": {clientID},
	})
	if status != http.StatusBadRequest || out["error"] != "invalid_grant" {
		t.Fatalf("a spent refresh token was accepted: %d %v", status, out)
	}
}

func TestOAuthRefreshCannotWidenScope(t *testing.T) {
	h, fs := newOAuthFixture(t)
	const redirectURI = "https://claude.ai/cb"
	clientID := registerClient(t, h, redirectURI)
	verifier := "verifier-that-is-long-enough-to-be-realistic-1234"

	// Consent to read only.
	query := url.Values{
		"client_id": {clientID}, "redirect_uri": {redirectURI}, "response_type": {"code"},
		"scope": {oauth.ScopeBudgetsRead}, "code_challenge": {pkce(verifier)},
		"code_challenge_method": {"S256"},
	}
	rr := httptest.NewRecorder()
	h.Authorize(rr, httptest.NewRequest(http.MethodGet, "/oauth/authorize?"+query.Encode(), nil))
	location, _ := url.Parse(rr.Header().Get("Location"))
	body, _ := json.Marshal(map[string]any{"request_id": location.Query().Get("request_id"), "approve": true})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/oauth/consent", strings.NewReader(string(body)))
	req = req.WithContext(auth.WithUserID(req.Context(), 1))
	rr = httptest.NewRecorder()
	h.ConsentHandler().ServeHTTP(rr, req)
	var decided struct {
		RedirectTo string `json:"redirect_to"`
	}
	_ = json.Unmarshal(rr.Body.Bytes(), &decided)
	back, _ := url.Parse(decided.RedirectTo)

	_, first := postToken(t, h, url.Values{
		"grant_type": {"authorization_code"}, "code": {back.Query().Get("code")},
		"client_id": {clientID}, "redirect_uri": {redirectURI}, "code_verifier": {verifier},
	})
	if first["scope"] != oauth.ScopeBudgetsRead {
		t.Fatalf("scope: got %v want %q", first["scope"], oauth.ScopeBudgetsRead)
	}

	status, out := postToken(t, h, url.Values{
		"grant_type": {"refresh_token"}, "refresh_token": {first["refresh_token"].(string)},
		"client_id": {clientID}, "scope": {oauth.ScopeBudgetsWrite},
	})
	if status != http.StatusBadRequest || out["error"] != "invalid_scope" {
		t.Fatalf("refresh widened the grant: %d %v", status, out)
	}
	_ = fs
}

func TestOAuthConnectionsListAndDisconnect(t *testing.T) {
	h, fs := newOAuthFixture(t)
	const redirectURI = "https://claude.ai/cb"
	clientID := registerClient(t, h, redirectURI)
	verifier := "verifier-that-is-long-enough-to-be-realistic-1234"
	code, _ := authorizeAndConsent(t, h, clientID, redirectURI, pkce(verifier), 5)
	_, tokens := postToken(t, h, url.Values{
		"grant_type": {"authorization_code"}, "code": {code}, "client_id": {clientID},
		"redirect_uri": {redirectURI}, "code_verifier": {verifier},
	})
	access := tokens["access_token"].(string)

	listReq := httptest.NewRequest(http.MethodGet, "/api/v1/connections", nil)
	listReq.URL.Path = "/connections"
	listReq = listReq.WithContext(auth.WithUserID(listReq.Context(), 5))
	rr := httptest.NewRecorder()
	h.ConnectionsHandler().ServeHTTP(rr, listReq)
	if rr.Code != http.StatusOK {
		t.Fatalf("list: status %d body %s", rr.Code, rr.Body.String())
	}
	var listed struct {
		Data []store.OAuthConnection `json:"data"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &listed); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(listed.Data) != 1 {
		t.Fatalf("expected 1 connection, got %d", len(listed.Data))
	}
	conn := listed.Data[0]
	if conn.ClientName != "Test Client" {
		t.Fatalf("client_name: got %q", conn.ClientName)
	}
	// A new connection does not expire on its own.
	if conn.ExpiresAt != nil {
		t.Fatalf("expected no expiry by default, got %v", conn.ExpiresAt)
	}

	// Another user must not see it.
	otherReq := httptest.NewRequest(http.MethodGet, "/connections", nil)
	otherReq = otherReq.WithContext(auth.WithUserID(otherReq.Context(), 6))
	rr = httptest.NewRecorder()
	h.ConnectionsHandler().ServeHTTP(rr, otherReq)
	_ = json.Unmarshal(rr.Body.Bytes(), &listed)
	if len(listed.Data) != 0 {
		t.Fatalf("another user saw %d connections", len(listed.Data))
	}

	// Disconnecting kills the token that was already issued.
	delReq := httptest.NewRequest(http.MethodDelete, "/connections/"+itoa(conn.ID), nil)
	delReq = delReq.WithContext(auth.WithUserID(delReq.Context(), 5))
	rr = httptest.NewRecorder()
	h.ConnectionsHandler().ServeHTTP(rr, delReq)
	if rr.Code != http.StatusNoContent {
		t.Fatalf("disconnect: status %d body %s", rr.Code, rr.Body.String())
	}
	if _, err := fs.LookupOAuthAccessToken(context.Background(), access); err == nil {
		t.Fatal("the access token still works after disconnecting")
	}
}

func TestOAuthConnectionExpiryCanBeSetAndCleared(t *testing.T) {
	h, fs := newOAuthFixture(t)
	const redirectURI = "https://claude.ai/cb"
	clientID := registerClient(t, h, redirectURI)
	verifier := "verifier-that-is-long-enough-to-be-realistic-1234"
	code, _ := authorizeAndConsent(t, h, clientID, redirectURI, pkce(verifier), 5)
	postToken(t, h, url.Values{
		"grant_type": {"authorization_code"}, "code": {code}, "client_id": {clientID},
		"redirect_uri": {redirectURI}, "code_verifier": {verifier},
	})

	conns, _ := fs.ListOAuthConnections(context.Background(), 5)
	id := conns[0].ID

	when := time.Now().Add(30 * 24 * time.Hour).UTC().Format(time.RFC3339)
	patch := func(body string) int {
		req := httptest.NewRequest(http.MethodPatch, "/connections/"+itoa(id), strings.NewReader(body))
		req = req.WithContext(auth.WithUserID(req.Context(), 5))
		rr := httptest.NewRecorder()
		h.ConnectionsHandler().ServeHTTP(rr, req)
		return rr.Code
	}

	if status := patch(`{"expires_at":"` + when + `"}`); status != http.StatusNoContent {
		t.Fatalf("set expiry: got %d", status)
	}
	conns, _ = fs.ListOAuthConnections(context.Background(), 5)
	if conns[0].ExpiresAt == nil {
		t.Fatal("expiry was not set")
	}

	// Null means never, which is how connections start out.
	if status := patch(`{"expires_at":null}`); status != http.StatusNoContent {
		t.Fatalf("clear expiry: got %d", status)
	}
	conns, _ = fs.ListOAuthConnections(context.Background(), 5)
	if conns[0].ExpiresAt != nil {
		t.Fatalf("expiry was not cleared: %v", conns[0].ExpiresAt)
	}

	if status := patch(`{"expires_at":"next tuesday"}`); status != http.StatusBadRequest {
		t.Fatalf("expected 400 for an unparseable timestamp, got %d", status)
	}
}

func TestOAuthConnectionsRequireLogin(t *testing.T) {
	h, _ := newOAuthFixture(t)
	rr := httptest.NewRecorder()
	h.ConnectionsHandler().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/connections", nil))
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rr.Code)
	}
}

func itoa(v int64) string {
	return strconv.FormatInt(v, 10)
}
