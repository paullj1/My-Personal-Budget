package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"my-personal-budget/internal/oauth"
)

// ErrOAuthExpired distinguishes a credential that was valid once from one that
// never existed, so the token endpoint can say which.
var ErrOAuthExpired = errors.New("oauth credential expired")

// OAuthClient is a dynamically registered client (RFC 7591).
type OAuthClient struct {
	ID                      int64     `json:"-"`
	ClientID                string    `json:"client_id"`
	ClientName              string    `json:"client_name"`
	ClientURI               string    `json:"client_uri,omitempty"`
	LogoURI                 string    `json:"logo_uri,omitempty"`
	RedirectURIs            []string  `json:"redirect_uris"`
	GrantTypes              []string  `json:"grant_types"`
	ResponseTypes           []string  `json:"response_types"`
	TokenEndpointAuthMethod string    `json:"token_endpoint_auth_method"`
	Scope                   string    `json:"scope"`
	CreatedAt               time.Time `json:"-"`

	// hasSecret is set on load so the token endpoint knows whether to demand
	// client authentication without exposing the hash.
	hasSecret bool
}

// IsConfidential reports whether the client authenticates at the token endpoint
// with a secret.
//
// Derived from the registered auth method rather than from whether a secret hash
// happens to exist, because this is the property callers actually depend on: a
// client that registered as client_secret_* will be made to prove itself at the
// token endpoint, and VerifyOAuthClientSecret refuses any client whose stored
// hash is NULL. So claiming secret auth without having a secret cannot buy
// anything -- the exchange still fails.
func (c OAuthClient) IsConfidential() bool {
	return c.TokenEndpointAuthMethod != "" && c.TokenEndpointAuthMethod != "none"
}

// HasStoredSecret reports whether a secret hash exists for this client, which is
// a fact about the row rather than about the registration.
func (c OAuthClient) HasStoredSecret() bool { return c.hasSecret }

// OAuthClientInput is a registration request, already validated by the caller.
type OAuthClientInput struct {
	ClientName              string
	ClientURI               string
	LogoURI                 string
	RedirectURIs            []string
	GrantTypes              []string
	ResponseTypes           []string
	TokenEndpointAuthMethod string
	Scope                   string
}

// OAuthConnection is one client a user has connected, as the UI lists it.
type OAuthConnection struct {
	ID         int64      `json:"id"`
	ClientID   string     `json:"client_id"`
	ClientName string     `json:"client_name"`
	ClientURI  string     `json:"client_uri,omitempty"`
	Scope      string     `json:"scope"`
	CreatedAt  time.Time  `json:"created_at"`
	LastUsedAt *time.Time `json:"last_used_at,omitempty"`
	// ExpiresAt nil means the connection does not expire on its own.
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
}

// OAuthAuthCode is a consumed authorization code with everything the token
// endpoint needs to validate the exchange.
type OAuthAuthCode struct {
	AuthorizationID     int64
	UserID              int64
	ClientID            string
	RedirectURI         string
	CodeChallenge       string
	CodeChallengeMethod string
	Resource            string
	Scope               string
}

// OAuthTokenInfo is the result of presenting a bearer token.
type OAuthTokenInfo struct {
	AuthorizationID int64
	UserID          int64
	ClientID        string
	ClientName      string
	Scope           string
	Resource        string
}

// RegisterOAuthClient stores a registration and returns the client, plus the
// generated secret when one was issued (confidential clients only).
func (s *Store) RegisterOAuthClient(ctx context.Context, in OAuthClientInput) (OAuthClient, string, error) {
	clientID, err := oauth.GenerateClientID()
	if err != nil {
		return OAuthClient{}, "", err
	}

	var secret string
	var secretHash sql.NullString
	if in.TokenEndpointAuthMethod != "none" {
		secret, secretHash.String, err = oauth.GenerateSecret("mpbs_")
		if err != nil {
			return OAuthClient{}, "", err
		}
		secretHash.Valid = true
	}

	redirects, _ := json.Marshal(in.RedirectURIs)
	grants, _ := json.Marshal(in.GrantTypes)
	responses, _ := json.Marshal(in.ResponseTypes)

	const q = `
		INSERT INTO oauth_clients (client_id, client_secret_hash, client_name, client_uri, logo_uri,
		                           redirect_uris, grant_types, response_types,
		                           token_endpoint_auth_method, scope)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)
		RETURNING id, created_at;
	`
	client := OAuthClient{
		ClientID:                clientID,
		ClientName:              in.ClientName,
		ClientURI:               in.ClientURI,
		LogoURI:                 in.LogoURI,
		RedirectURIs:            in.RedirectURIs,
		GrantTypes:              in.GrantTypes,
		ResponseTypes:           in.ResponseTypes,
		TokenEndpointAuthMethod: in.TokenEndpointAuthMethod,
		Scope:                   in.Scope,
		hasSecret:               secretHash.Valid,
	}
	if err := s.db.QueryRowContext(ctx, q, clientID, secretHash, in.ClientName, in.ClientURI, in.LogoURI,
		redirects, grants, responses, in.TokenEndpointAuthMethod, in.Scope,
	).Scan(&client.ID, &client.CreatedAt); err != nil {
		return OAuthClient{}, "", fmt.Errorf("insert oauth client: %w", err)
	}
	return client, secret, nil
}

// GetOAuthClient loads a registration by its public client_id.
func (s *Store) GetOAuthClient(ctx context.Context, clientID string) (OAuthClient, error) {
	const q = `
		SELECT id, client_id, client_secret_hash IS NOT NULL, client_name, client_uri, logo_uri,
		       redirect_uris, grant_types, response_types, token_endpoint_auth_method, scope, created_at
		FROM oauth_clients WHERE client_id = $1;
	`
	var (
		client                       OAuthClient
		redirects, grants, responses []byte
	)
	err := s.db.QueryRowContext(ctx, q, clientID).Scan(
		&client.ID, &client.ClientID, &client.hasSecret, &client.ClientName, &client.ClientURI, &client.LogoURI,
		&redirects, &grants, &responses, &client.TokenEndpointAuthMethod, &client.Scope, &client.CreatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return OAuthClient{}, ErrNotFound
	}
	if err != nil {
		return OAuthClient{}, err
	}
	_ = json.Unmarshal(redirects, &client.RedirectURIs)
	_ = json.Unmarshal(grants, &client.GrantTypes)
	_ = json.Unmarshal(responses, &client.ResponseTypes)
	return client, nil
}

// VerifyOAuthClientSecret checks a presented secret against the stored hash.
func (s *Store) VerifyOAuthClientSecret(ctx context.Context, clientID, secret string) error {
	const q = `SELECT client_secret_hash FROM oauth_clients WHERE client_id = $1;`
	var hash sql.NullString
	if err := s.db.QueryRowContext(ctx, q, clientID).Scan(&hash); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		return err
	}
	if !hash.Valid {
		return ErrNotFound
	}
	if oauth.HashSecret(secret) != hash.String {
		return ErrNotFound
	}
	return nil
}

// OAuthAuthCodeInput is a granted consent awaiting exchange.
type OAuthAuthCodeInput struct {
	UserID              int64
	ClientID            string
	RedirectURI         string
	CodeChallenge       string
	CodeChallengeMethod string
	Resource            string
	Scope               string
	TTL                 time.Duration
}

// GrantAuthorizationCode records the user's consent and mints a one-use code.
//
// The consent row is upserted rather than inserted: re-authorizing a client the
// user already connected must update that connection, not accumulate duplicates
// the UI would then list side by side.
func (s *Store) GrantAuthorizationCode(ctx context.Context, in OAuthAuthCodeInput) (string, error) {
	code, hash, err := oauth.GenerateSecret("mpbac_")
	if err != nil {
		return "", err
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()

	var authorizationID int64
	const upsert = `
		INSERT INTO oauth_authorizations (user_id, client_id, scope)
		VALUES ($1, $2, $3)
		ON CONFLICT (user_id, client_id) DO UPDATE
		  SET scope = EXCLUDED.scope, revoked_at = NULL
		RETURNING id;
	`
	if err := tx.QueryRowContext(ctx, upsert, in.UserID, in.ClientID, in.Scope).Scan(&authorizationID); err != nil {
		return "", fmt.Errorf("upsert authorization: %w", err)
	}

	const insert = `
		INSERT INTO oauth_auth_codes (code_hash, authorization_id, redirect_uri, code_challenge,
		                              code_challenge_method, resource, scope, expires_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8);
	`
	if _, err := tx.ExecContext(ctx, insert, hash, authorizationID, in.RedirectURI, in.CodeChallenge,
		in.CodeChallengeMethod, in.Resource, in.Scope, time.Now().Add(in.TTL),
	); err != nil {
		return "", fmt.Errorf("insert auth code: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return "", err
	}
	return code, nil
}

// ConsumeAuthorizationCode marks a code used and returns what it authorized.
//
// The UPDATE ... WHERE consumed_at IS NULL is the replay guard: two concurrent
// exchanges of the same code cannot both match, so only one gets a token.
func (s *Store) ConsumeAuthorizationCode(ctx context.Context, code string) (OAuthAuthCode, error) {
	const q = `
		UPDATE oauth_auth_codes c
		SET consumed_at = NOW()
		FROM oauth_authorizations a
		WHERE c.code_hash = $1
		  AND c.consumed_at IS NULL
		  AND c.authorization_id = a.id
		RETURNING c.authorization_id, a.user_id, a.client_id, c.redirect_uri, c.code_challenge,
		          c.code_challenge_method, c.resource, c.scope, c.expires_at;
	`
	var out OAuthAuthCode
	var expiresAt time.Time
	err := s.db.QueryRowContext(ctx, q, oauth.HashSecret(code)).Scan(
		&out.AuthorizationID, &out.UserID, &out.ClientID, &out.RedirectURI, &out.CodeChallenge,
		&out.CodeChallengeMethod, &out.Resource, &out.Scope, &expiresAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return OAuthAuthCode{}, ErrNotFound
	}
	if err != nil {
		return OAuthAuthCode{}, err
	}
	if time.Now().After(expiresAt) {
		return OAuthAuthCode{}, ErrOAuthExpired
	}
	return out, nil
}

// OAuthTokenPair is what the token endpoint hands back.
type OAuthTokenPair struct {
	AccessToken  string
	RefreshToken string
	ExpiresIn    int
	Scope        string
}

// IssueOAuthTokens mints an access/refresh pair against an existing consent.
//
// The refresh token carries no expiry of its own: the connection's lifetime is
// the one the user controls from the Connections screen, and a silently dead
// refresh token would look like a bug rather than a decision.
func (s *Store) IssueOAuthTokens(ctx context.Context, authorizationID int64, scope, resource string, accessTTL time.Duration) (OAuthTokenPair, error) {
	access, accessHash, err := oauth.GenerateSecret("mpbat_")
	if err != nil {
		return OAuthTokenPair{}, err
	}
	refresh, refreshHash, err := oauth.GenerateSecret("mpbrt_")
	if err != nil {
		return OAuthTokenPair{}, err
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return OAuthTokenPair{}, err
	}
	defer tx.Rollback()

	const insert = `
		INSERT INTO oauth_tokens (authorization_id, kind, token_hash, resource, scope, expires_at)
		VALUES ($1,$2,$3,$4,$5,$6);
	`
	expiry := time.Now().Add(accessTTL)
	if _, err := tx.ExecContext(ctx, insert, authorizationID, "access", accessHash, resource, scope, expiry); err != nil {
		return OAuthTokenPair{}, fmt.Errorf("insert access token: %w", err)
	}
	if _, err := tx.ExecContext(ctx, insert, authorizationID, "refresh", refreshHash, resource, scope, nil); err != nil {
		return OAuthTokenPair{}, fmt.Errorf("insert refresh token: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return OAuthTokenPair{}, err
	}

	return OAuthTokenPair{
		AccessToken:  access,
		RefreshToken: refresh,
		ExpiresIn:    int(accessTTL.Seconds()),
		Scope:        scope,
	}, nil
}

// RedeemRefreshToken revokes a refresh token and returns the consent it belongs
// to, so the caller can issue a fresh pair. Rotation is mandatory in OAuth 2.1
// for public clients, and revoking here means a stolen token is usable once at
// most -- and its reuse shows up as a failed grant rather than silent access.
func (s *Store) RedeemRefreshToken(ctx context.Context, token, clientID string) (OAuthTokenInfo, error) {
	const q = `
		UPDATE oauth_tokens t
		SET revoked_at = NOW()
		FROM oauth_authorizations a
		WHERE t.token_hash = $1
		  AND t.kind = 'refresh'
		  AND t.revoked_at IS NULL
		  AND t.authorization_id = a.id
		  AND a.revoked_at IS NULL
		  AND a.client_id = $2
		RETURNING t.authorization_id, a.user_id, a.client_id, t.scope, t.resource, a.expires_at;
	`
	var (
		info      OAuthTokenInfo
		expiresAt sql.NullTime
	)
	err := s.db.QueryRowContext(ctx, q, oauth.HashSecret(token), clientID).Scan(
		&info.AuthorizationID, &info.UserID, &info.ClientID, &info.Scope, &info.Resource, &expiresAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return OAuthTokenInfo{}, ErrNotFound
	}
	if err != nil {
		return OAuthTokenInfo{}, err
	}
	if expiresAt.Valid && time.Now().After(expiresAt.Time) {
		return OAuthTokenInfo{}, ErrOAuthExpired
	}
	return info, nil
}

// LookupOAuthAccessToken resolves a bearer token to its user, or reports why not.
//
// The connection's own expiry is checked alongside the token's: an expired or
// disconnected connection must stop working immediately, which is the whole
// reason these tokens are opaque rather than self-contained.
func (s *Store) LookupOAuthAccessToken(ctx context.Context, token string) (OAuthTokenInfo, error) {
	const q = `
		SELECT t.authorization_id, a.user_id, a.client_id, c.client_name, t.scope, t.resource,
		       t.expires_at, t.revoked_at, a.expires_at, a.revoked_at
		FROM oauth_tokens t
		JOIN oauth_authorizations a ON a.id = t.authorization_id
		JOIN oauth_clients c ON c.client_id = a.client_id
		WHERE t.token_hash = $1 AND t.kind = 'access';
	`
	var (
		info                      OAuthTokenInfo
		tokenExpires, authExpires sql.NullTime
		tokenRevoked, authRevoked sql.NullTime
	)
	err := s.db.QueryRowContext(ctx, q, oauth.HashSecret(token)).Scan(
		&info.AuthorizationID, &info.UserID, &info.ClientID, &info.ClientName, &info.Scope, &info.Resource,
		&tokenExpires, &tokenRevoked, &authExpires, &authRevoked,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return OAuthTokenInfo{}, ErrNotFound
	}
	if err != nil {
		return OAuthTokenInfo{}, err
	}
	if tokenRevoked.Valid || authRevoked.Valid {
		return OAuthTokenInfo{}, ErrNotFound
	}
	now := time.Now()
	if tokenExpires.Valid && now.After(tokenExpires.Time) {
		return OAuthTokenInfo{}, ErrOAuthExpired
	}
	if authExpires.Valid && now.After(authExpires.Time) {
		return OAuthTokenInfo{}, ErrOAuthExpired
	}
	_, _ = s.db.ExecContext(ctx, `UPDATE oauth_authorizations SET last_used_at = NOW() WHERE id = $1`, info.AuthorizationID)
	return info, nil
}

// RevokeOAuthToken honours RFC 7009. A refresh token takes its whole connection
// with it; an access token revokes only itself, so the client can recover by
// refreshing.
func (s *Store) RevokeOAuthToken(ctx context.Context, token string) error {
	hash := oauth.HashSecret(token)
	const q = `
		UPDATE oauth_tokens SET revoked_at = NOW()
		WHERE token_hash = $1 AND revoked_at IS NULL
		RETURNING kind, authorization_id;
	`
	var kind string
	var authorizationID int64
	err := s.db.QueryRowContext(ctx, q, hash).Scan(&kind, &authorizationID)
	if errors.Is(err, sql.ErrNoRows) {
		// RFC 7009: revoking an unknown token is not an error.
		return nil
	}
	if err != nil {
		return err
	}
	if kind == "refresh" {
		_, err = s.db.ExecContext(ctx,
			`UPDATE oauth_authorizations SET revoked_at = NOW() WHERE id = $1`, authorizationID)
	}
	return err
}

// ListOAuthConnections returns the user's live connections, newest first.
func (s *Store) ListOAuthConnections(ctx context.Context, userID int64) ([]OAuthConnection, error) {
	const q = `
		SELECT a.id, a.client_id, c.client_name, c.client_uri, a.scope, a.created_at, a.last_used_at, a.expires_at
		FROM oauth_authorizations a
		JOIN oauth_clients c ON c.client_id = a.client_id
		WHERE a.user_id = $1 AND a.revoked_at IS NULL
		ORDER BY a.created_at DESC;
	`
	rows, err := s.db.QueryContext(ctx, q, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []OAuthConnection
	for rows.Next() {
		var conn OAuthConnection
		if err := rows.Scan(&conn.ID, &conn.ClientID, &conn.ClientName, &conn.ClientURI,
			&conn.Scope, &conn.CreatedAt, &conn.LastUsedAt, &conn.ExpiresAt); err != nil {
			return nil, err
		}
		if strings.TrimSpace(conn.ClientName) == "" {
			conn.ClientName = conn.ClientID
		}
		out = append(out, conn)
	}
	return out, rows.Err()
}

// DeleteOAuthConnection disconnects a client. The tokens go with it via the
// foreign key's ON DELETE CASCADE, so nothing is left that could still
// authenticate.
func (s *Store) DeleteOAuthConnection(ctx context.Context, userID, connectionID int64) error {
	res, err := s.db.ExecContext(ctx,
		`DELETE FROM oauth_authorizations WHERE id = $1 AND user_id = $2`, connectionID, userID)
	if err != nil {
		return err
	}
	if affected, _ := res.RowsAffected(); affected == 0 {
		return ErrNotFound
	}
	return nil
}

// SetOAuthConnectionExpiry sets or clears a connection's expiry. A nil time
// means it never expires, which is the default a new connection is created with.
func (s *Store) SetOAuthConnectionExpiry(ctx context.Context, userID, connectionID int64, expiresAt *time.Time) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE oauth_authorizations SET expires_at = $3 WHERE id = $1 AND user_id = $2 AND revoked_at IS NULL`,
		connectionID, userID, expiresAt)
	if err != nil {
		return err
	}
	if affected, _ := res.RowsAffected(); affected == 0 {
		return ErrNotFound
	}
	return nil
}

// OAuthPurgeResult reports what a sweep removed, so the log line says something
// rather than just that it ran.
type OAuthPurgeResult struct {
	Codes          int64 `json:"codes"`
	Clients        int64 `json:"clients"`
	Authorizations int64 `json:"authorizations"`
	Tokens         int64 `json:"tokens"`
}

// Empty reports whether the sweep found nothing to do, which is the steady state.
func (r OAuthPurgeResult) Empty() bool {
	return r.Codes == 0 && r.Clients == 0 && r.Authorizations == 0 && r.Tokens == 0
}

// PurgeStaleOAuth clears the debris of authorizations that never completed.
//
// Registration is necessarily open -- a remote MCP client cannot be
// pre-provisioned -- so anyone who can reach the server can add an
// oauth_clients row. Registration grants nothing without a passkey-backed
// consent, but the rows would otherwise accumulate forever. This is what keeps
// that bounded, alongside the rate limit on the endpoint itself.
//
// retain is the grace period before something is considered abandoned. It has
// to comfortably exceed the time between registering and consenting, since a
// client swept mid-flow would see its authorization fail as invalid_client.
func (s *Store) PurgeStaleOAuth(ctx context.Context, retain time.Duration) (OAuthPurgeResult, error) {
	if retain <= 0 {
		retain = 24 * time.Hour
	}
	cutoff := time.Now().Add(-retain)
	var out OAuthPurgeResult

	// Codes past their grace period. Consumed ones are kept for a while so a
	// replay is still refused as "already used" rather than as "never existed".
	res, err := s.db.ExecContext(ctx,
		`DELETE FROM oauth_auth_codes WHERE expires_at < $1`, cutoff)
	if err != nil {
		return out, fmt.Errorf("purge auth codes: %w", err)
	}
	out.Codes, _ = res.RowsAffected()

	// Connections the user or the client revoked. The row is already inert; this
	// only reclaims it. Tokens follow via ON DELETE CASCADE.
	res, err = s.db.ExecContext(ctx,
		`DELETE FROM oauth_authorizations WHERE revoked_at IS NOT NULL AND revoked_at < $1`, cutoff)
	if err != nil {
		return out, fmt.Errorf("purge revoked authorizations: %w", err)
	}
	out.Authorizations, _ = res.RowsAffected()

	// Access tokens long past expiry, and anything revoked. Refresh tokens have a
	// NULL expires_at and live as long as their connection, so they are only
	// caught here once revoked.
	res, err = s.db.ExecContext(ctx, `
		DELETE FROM oauth_tokens
		WHERE (expires_at IS NOT NULL AND expires_at < $1)
		   OR (revoked_at IS NOT NULL AND revoked_at < $1)`, cutoff)
	if err != nil {
		return out, fmt.Errorf("purge tokens: %w", err)
	}
	out.Tokens, _ = res.RowsAffected()

	// Clients that registered and never got consent -- the ones an abusive
	// registration loop would leave behind. A client whose only connection was
	// later disconnected lands here too, which is right: its registration is dead
	// weight, and reconnecting registers afresh.
	res, err = s.db.ExecContext(ctx, `
		DELETE FROM oauth_clients c
		WHERE c.created_at < $1
		  AND NOT EXISTS (SELECT 1 FROM oauth_authorizations a WHERE a.client_id = c.client_id)`, cutoff)
	if err != nil {
		return out, fmt.Errorf("purge unused clients: %w", err)
	}
	out.Clients, _ = res.RowsAffected()

	return out, nil
}
