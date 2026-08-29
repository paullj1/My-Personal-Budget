package store

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"testing"
	"time"

	"my-personal-budget/internal/oauth"

	_ "github.com/jackc/pgx/v5/stdlib"
)

// The OAuth flow is mostly SQL -- single-use codes, rotating refresh tokens,
// revocation that cascades -- and those are the parts a hand-written fake cannot
// vouch for. This exercises them against a real Postgres when one is offered.
//
//	TEST_DATABASE_URL=postgres://... go test ./internal/store/
func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("set TEST_DATABASE_URL to run store integration tests")
	}
	db, err := sql.Open("pgx", url)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := db.Ping(); err != nil {
		t.Fatalf("ping: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

// testUser creates a user that is removed with everything it owns.
func testUser(t *testing.T, db *sql.DB) int64 {
	t.Helper()
	var id int64
	email := "oauth-test-" + time.Now().Format("150405.000000000") + "@example.com"
	if err := db.QueryRow(`INSERT INTO users (email) VALUES ($1) RETURNING id`, email).Scan(&id); err != nil {
		t.Fatalf("create user: %v", err)
	}
	t.Cleanup(func() { db.Exec(`DELETE FROM users WHERE id = $1`, id) })
	return id
}

func testClient(t *testing.T, s *Store) OAuthClient {
	t.Helper()
	client, _, err := s.RegisterOAuthClient(context.Background(), OAuthClientInput{
		ClientName:              "Integration Client",
		RedirectURIs:            []string{"https://claude.ai/cb"},
		GrantTypes:              []string{"authorization_code", "refresh_token"},
		ResponseTypes:           []string{"code"},
		TokenEndpointAuthMethod: "none",
		Scope:                   oauth.DefaultScope,
	})
	if err != nil {
		t.Fatalf("register client: %v", err)
	}
	t.Cleanup(func() { s.db.Exec(`DELETE FROM oauth_clients WHERE client_id = $1`, client.ClientID) })
	return client
}

func grantCode(t *testing.T, s *Store, userID int64, clientID string) string {
	t.Helper()
	code, err := s.GrantAuthorizationCode(context.Background(), OAuthAuthCodeInput{
		UserID: userID, ClientID: clientID, RedirectURI: "https://claude.ai/cb",
		CodeChallenge: "challenge", CodeChallengeMethod: "S256",
		Resource: "https://budget.example/mcp", Scope: oauth.DefaultScope,
		TTL: 5 * time.Minute,
	})
	if err != nil {
		t.Fatalf("grant code: %v", err)
	}
	return code
}

func TestIntegrationOAuthClientRoundTrip(t *testing.T) {
	db := openTestDB(t)
	s := New(db)
	client := testClient(t, s)

	loaded, err := s.GetOAuthClient(context.Background(), client.ClientID)
	if err != nil {
		t.Fatalf("get client: %v", err)
	}
	if loaded.ClientName != "Integration Client" {
		t.Fatalf("client_name: got %q", loaded.ClientName)
	}
	// JSONB columns have to survive the round trip as slices, not as raw JSON.
	if len(loaded.RedirectURIs) != 1 || loaded.RedirectURIs[0] != "https://claude.ai/cb" {
		t.Fatalf("redirect_uris: got %v", loaded.RedirectURIs)
	}
	if len(loaded.GrantTypes) != 2 {
		t.Fatalf("grant_types: got %v", loaded.GrantTypes)
	}
	if loaded.IsConfidential() {
		t.Fatal("a public client came back marked confidential")
	}

	if _, err := s.GetOAuthClient(context.Background(), "mpbc_nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestIntegrationOAuthConfidentialClientSecret(t *testing.T) {
	db := openTestDB(t)
	s := New(db)

	client, secret, err := s.RegisterOAuthClient(context.Background(), OAuthClientInput{
		ClientName:              "Confidential",
		RedirectURIs:            []string{"https://example.com/cb"},
		TokenEndpointAuthMethod: "client_secret_post",
	})
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	t.Cleanup(func() { db.Exec(`DELETE FROM oauth_clients WHERE client_id = $1`, client.ClientID) })

	if secret == "" {
		t.Fatal("no secret issued to a confidential client")
	}
	if err := s.VerifyOAuthClientSecret(context.Background(), client.ClientID, secret); err != nil {
		t.Fatalf("correct secret rejected: %v", err)
	}
	if err := s.VerifyOAuthClientSecret(context.Background(), client.ClientID, "wrong"); err == nil {
		t.Fatal("a wrong secret was accepted")
	}
	// Only the hash is stored, so the plaintext must not be recoverable.
	var stored string
	if err := db.QueryRow(`SELECT client_secret_hash FROM oauth_clients WHERE client_id = $1`,
		client.ClientID).Scan(&stored); err != nil {
		t.Fatalf("read hash: %v", err)
	}
	if stored == secret {
		t.Fatal("the secret was stored in plaintext")
	}
}

func TestIntegrationOAuthCodeIsSingleUse(t *testing.T) {
	db := openTestDB(t)
	s := New(db)
	userID := testUser(t, db)
	client := testClient(t, s)
	code := grantCode(t, s, userID, client.ClientID)

	granted, err := s.ConsumeAuthorizationCode(context.Background(), code)
	if err != nil {
		t.Fatalf("consume: %v", err)
	}
	if granted.UserID != userID || granted.ClientID != client.ClientID {
		t.Fatalf("code resolved to the wrong grant: %+v", granted)
	}
	if granted.Scope != oauth.DefaultScope {
		t.Fatalf("scope: got %q", granted.Scope)
	}

	// The replay guard lives in the UPDATE's WHERE clause, not in Go.
	if _, err := s.ConsumeAuthorizationCode(context.Background(), code); !errors.Is(err, ErrNotFound) {
		t.Fatalf("replayed code: expected ErrNotFound, got %v", err)
	}
}

func TestIntegrationOAuthExpiredCodeIsRefused(t *testing.T) {
	db := openTestDB(t)
	s := New(db)
	userID := testUser(t, db)
	client := testClient(t, s)

	code, err := s.GrantAuthorizationCode(context.Background(), OAuthAuthCodeInput{
		UserID: userID, ClientID: client.ClientID, RedirectURI: "https://claude.ai/cb",
		CodeChallenge: "challenge", Scope: oauth.DefaultScope, TTL: -time.Second,
	})
	if err != nil {
		t.Fatalf("grant: %v", err)
	}
	if _, err := s.ConsumeAuthorizationCode(context.Background(), code); !errors.Is(err, ErrOAuthExpired) {
		t.Fatalf("expected ErrOAuthExpired, got %v", err)
	}
}

// Re-authorizing an app the user already connected must update that connection,
// not stack a second one the Connections screen would list twice.
func TestIntegrationOAuthReauthorizationReusesTheConnection(t *testing.T) {
	db := openTestDB(t)
	s := New(db)
	userID := testUser(t, db)
	client := testClient(t, s)

	grantCode(t, s, userID, client.ClientID)
	grantCode(t, s, userID, client.ClientID)

	conns, err := s.ListOAuthConnections(context.Background(), userID)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(conns) != 1 {
		t.Fatalf("expected 1 connection, got %d", len(conns))
	}
	if conns[0].ExpiresAt != nil {
		t.Fatalf("a new connection should not expire on its own, got %v", conns[0].ExpiresAt)
	}
}

func TestIntegrationOAuthTokenLifecycle(t *testing.T) {
	db := openTestDB(t)
	s := New(db)
	userID := testUser(t, db)
	client := testClient(t, s)
	code := grantCode(t, s, userID, client.ClientID)
	granted, err := s.ConsumeAuthorizationCode(context.Background(), code)
	if err != nil {
		t.Fatalf("consume: %v", err)
	}

	pair, err := s.IssueOAuthTokens(context.Background(), granted.AuthorizationID,
		granted.Scope, granted.Resource, time.Hour)
	if err != nil {
		t.Fatalf("issue: %v", err)
	}

	info, err := s.LookupOAuthAccessToken(context.Background(), pair.AccessToken)
	if err != nil {
		t.Fatalf("lookup: %v", err)
	}
	if info.UserID != userID {
		t.Fatalf("token belongs to %d, want %d", info.UserID, userID)
	}
	if info.ClientName != "Integration Client" {
		t.Fatalf("client_name: got %q", info.ClientName)
	}
	// A lookup stamps the connection so the UI can show when it was last used.
	conns, _ := s.ListOAuthConnections(context.Background(), userID)
	if conns[0].LastUsedAt == nil {
		t.Fatal("last_used_at was not stamped")
	}

	// A refresh token is not an access token, and vice versa.
	if _, err := s.LookupOAuthAccessToken(context.Background(), pair.RefreshToken); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a refresh token authenticated as an access token: %v", err)
	}

	rotated, err := s.RedeemRefreshToken(context.Background(), pair.RefreshToken, client.ClientID)
	if err != nil {
		t.Fatalf("redeem: %v", err)
	}
	if rotated.AuthorizationID != granted.AuthorizationID {
		t.Fatalf("refresh resolved to the wrong connection")
	}
	// Rotation is mandatory: a spent refresh token must not work twice.
	if _, err := s.RedeemRefreshToken(context.Background(), pair.RefreshToken, client.ClientID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a spent refresh token was accepted: %v", err)
	}
	// Nor may another client redeem it.
	other := testClient(t, s)
	code2 := grantCode(t, s, userID, other.ClientID)
	granted2, _ := s.ConsumeAuthorizationCode(context.Background(), code2)
	pair2, _ := s.IssueOAuthTokens(context.Background(), granted2.AuthorizationID, granted2.Scope, "", time.Hour)
	if _, err := s.RedeemRefreshToken(context.Background(), pair2.RefreshToken, client.ClientID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("another client redeemed the refresh token: %v", err)
	}
}

func TestIntegrationOAuthExpiredAccessTokenIsDistinguishable(t *testing.T) {
	db := openTestDB(t)
	s := New(db)
	userID := testUser(t, db)
	client := testClient(t, s)
	code := grantCode(t, s, userID, client.ClientID)
	granted, _ := s.ConsumeAuthorizationCode(context.Background(), code)

	pair, err := s.IssueOAuthTokens(context.Background(), granted.AuthorizationID, granted.Scope, "", -time.Second)
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	// Expired, not unknown: the client should refresh rather than give up.
	if _, err := s.LookupOAuthAccessToken(context.Background(), pair.AccessToken); !errors.Is(err, ErrOAuthExpired) {
		t.Fatalf("expected ErrOAuthExpired, got %v", err)
	}
}

// Opaque tokens exist so that disconnecting takes effect on the next request. A
// JWT could not be withdrawn.
func TestIntegrationOAuthDisconnectKillsLiveTokens(t *testing.T) {
	db := openTestDB(t)
	s := New(db)
	userID := testUser(t, db)
	client := testClient(t, s)
	code := grantCode(t, s, userID, client.ClientID)
	granted, _ := s.ConsumeAuthorizationCode(context.Background(), code)
	pair, _ := s.IssueOAuthTokens(context.Background(), granted.AuthorizationID, granted.Scope, "", time.Hour)

	if err := s.DeleteOAuthConnection(context.Background(), userID, granted.AuthorizationID); err != nil {
		t.Fatalf("disconnect: %v", err)
	}
	if _, err := s.LookupOAuthAccessToken(context.Background(), pair.AccessToken); !errors.Is(err, ErrNotFound) {
		t.Fatalf("the access token survived disconnection: %v", err)
	}
	if _, err := s.RedeemRefreshToken(context.Background(), pair.RefreshToken, client.ClientID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("the refresh token survived disconnection: %v", err)
	}

	// Someone else's connection is not theirs to delete.
	other := testUser(t, db)
	code2 := grantCode(t, s, other, client.ClientID)
	granted2, _ := s.ConsumeAuthorizationCode(context.Background(), code2)
	if err := s.DeleteOAuthConnection(context.Background(), userID, granted2.AuthorizationID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-user disconnect: expected ErrNotFound, got %v", err)
	}
}

func TestIntegrationOAuthConnectionExpiryStopsAccess(t *testing.T) {
	db := openTestDB(t)
	s := New(db)
	userID := testUser(t, db)
	client := testClient(t, s)
	code := grantCode(t, s, userID, client.ClientID)
	granted, _ := s.ConsumeAuthorizationCode(context.Background(), code)
	pair, _ := s.IssueOAuthTokens(context.Background(), granted.AuthorizationID, granted.Scope, "", time.Hour)

	past := time.Now().Add(-time.Hour)
	if err := s.SetOAuthConnectionExpiry(context.Background(), userID, granted.AuthorizationID, &past); err != nil {
		t.Fatalf("set expiry: %v", err)
	}
	// The token itself is still within its own hour; the connection's expiry is
	// what has to stop it.
	if _, err := s.LookupOAuthAccessToken(context.Background(), pair.AccessToken); !errors.Is(err, ErrOAuthExpired) {
		t.Fatalf("expected the connection expiry to stop the token, got %v", err)
	}
	if _, err := s.RedeemRefreshToken(context.Background(), pair.RefreshToken, client.ClientID); !errors.Is(err, ErrOAuthExpired) {
		t.Fatalf("expected refresh to be refused, got %v", err)
	}

	// Clearing it brings the connection back.
	if err := s.SetOAuthConnectionExpiry(context.Background(), userID, granted.AuthorizationID, nil); err != nil {
		t.Fatalf("clear expiry: %v", err)
	}
	if _, err := s.LookupOAuthAccessToken(context.Background(), pair.AccessToken); err != nil {
		t.Fatalf("clearing the expiry did not restore access: %v", err)
	}
}

// RFC 7009: revoking a refresh token takes the connection with it; revoking an
// access token leaves the client able to refresh.
func TestIntegrationOAuthRevocationScope(t *testing.T) {
	db := openTestDB(t)
	s := New(db)
	userID := testUser(t, db)
	client := testClient(t, s)

	code := grantCode(t, s, userID, client.ClientID)
	granted, _ := s.ConsumeAuthorizationCode(context.Background(), code)
	pair, _ := s.IssueOAuthTokens(context.Background(), granted.AuthorizationID, granted.Scope, "", time.Hour)

	if err := s.RevokeOAuthToken(context.Background(), pair.AccessToken); err != nil {
		t.Fatalf("revoke access: %v", err)
	}
	if _, err := s.LookupOAuthAccessToken(context.Background(), pair.AccessToken); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a revoked access token still works: %v", err)
	}
	if _, err := s.RedeemRefreshToken(context.Background(), pair.RefreshToken, client.ClientID); err != nil {
		t.Fatalf("revoking an access token should leave refresh working: %v", err)
	}

	// Now the refresh path, on a fresh grant.
	code = grantCode(t, s, userID, client.ClientID)
	granted, _ = s.ConsumeAuthorizationCode(context.Background(), code)
	pair, _ = s.IssueOAuthTokens(context.Background(), granted.AuthorizationID, granted.Scope, "", time.Hour)

	if err := s.RevokeOAuthToken(context.Background(), pair.RefreshToken); err != nil {
		t.Fatalf("revoke refresh: %v", err)
	}
	if _, err := s.LookupOAuthAccessToken(context.Background(), pair.AccessToken); !errors.Is(err, ErrNotFound) {
		t.Fatalf("revoking the refresh token left the access token alive: %v", err)
	}
	conns, _ := s.ListOAuthConnections(context.Background(), userID)
	if len(conns) != 0 {
		t.Fatalf("the connection survived refresh-token revocation: %d", len(conns))
	}

	// Revoking an unknown token is not an error.
	if err := s.RevokeOAuthToken(context.Background(), "mpbat_never-existed"); err != nil {
		t.Fatalf("revoking an unknown token errored: %v", err)
	}
}

// The sweep is what keeps open registration from accumulating rows forever.
func TestIntegrationPurgeStaleOAuth(t *testing.T) {
	db := openTestDB(t)
	s := New(db)
	userID := testUser(t, db)

	// A client that registered and never came back for consent.
	abandoned := testClient(t, s)
	// A client the user actually connected.
	connected := testClient(t, s)
	grantCode(t, s, userID, connected.ClientID)

	// Nothing is stale yet, so a sweep with a real grace period must not touch
	// either -- a client swept mid-consent would fail as invalid_client.
	if _, err := s.PurgeStaleOAuth(context.Background(), time.Hour); err != nil {
		t.Fatalf("purge: %v", err)
	}
	if _, err := s.GetOAuthClient(context.Background(), abandoned.ClientID); err != nil {
		t.Fatalf("an in-flight registration was swept: %v", err)
	}

	// Age everything past the grace period.
	if _, err := db.Exec(`UPDATE oauth_clients SET created_at = NOW() - INTERVAL '48 hours'
		WHERE client_id IN ($1, $2)`, abandoned.ClientID, connected.ClientID); err != nil {
		t.Fatalf("age clients: %v", err)
	}

	result, err := s.PurgeStaleOAuth(context.Background(), time.Hour)
	if err != nil {
		t.Fatalf("purge: %v", err)
	}
	if result.Clients < 1 {
		t.Fatalf("the abandoned registration survived: %+v", result)
	}
	if _, err := s.GetOAuthClient(context.Background(), abandoned.ClientID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected the abandoned client to be gone, got %v", err)
	}
	// A client with a live connection is not debris, however old its registration.
	if _, err := s.GetOAuthClient(context.Background(), connected.ClientID); err != nil {
		t.Fatalf("a connected client was swept: %v", err)
	}
	conns, _ := s.ListOAuthConnections(context.Background(), userID)
	if len(conns) != 1 {
		t.Fatalf("the live connection was swept: %d remain", len(conns))
	}
}

func TestIntegrationPurgeStaleOAuthClearsRevokedAndExpired(t *testing.T) {
	db := openTestDB(t)
	s := New(db)
	userID := testUser(t, db)
	client := testClient(t, s)
	code := grantCode(t, s, userID, client.ClientID)
	granted, err := s.ConsumeAuthorizationCode(context.Background(), code)
	if err != nil {
		t.Fatalf("consume: %v", err)
	}
	pair, err := s.IssueOAuthTokens(context.Background(), granted.AuthorizationID, granted.Scope, "", time.Hour)
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	if err := s.RevokeOAuthToken(context.Background(), pair.RefreshToken); err != nil {
		t.Fatalf("revoke: %v", err)
	}

	if _, err := db.Exec(`UPDATE oauth_authorizations SET revoked_at = NOW() - INTERVAL '48 hours'
		WHERE id = $1`, granted.AuthorizationID); err != nil {
		t.Fatalf("age revocation: %v", err)
	}
	if _, err := db.Exec(`UPDATE oauth_auth_codes SET expires_at = NOW() - INTERVAL '48 hours'
		WHERE authorization_id = $1`, granted.AuthorizationID); err != nil {
		t.Fatalf("age code: %v", err)
	}

	result, err := s.PurgeStaleOAuth(context.Background(), time.Hour)
	if err != nil {
		t.Fatalf("purge: %v", err)
	}
	if result.Authorizations < 1 {
		t.Fatalf("the revoked connection survived: %+v", result)
	}
	if result.Codes < 1 {
		t.Fatalf("the expired code survived: %+v", result)
	}

	var tokens int
	if err := db.QueryRow(`SELECT COUNT(*) FROM oauth_tokens WHERE authorization_id = $1`,
		granted.AuthorizationID).Scan(&tokens); err != nil {
		t.Fatalf("count tokens: %v", err)
	}
	if tokens != 0 {
		t.Fatalf("%d tokens outlived their connection", tokens)
	}
}

// A quiet sweep is the steady state, and must not report work it did not do.
func TestIntegrationPurgeStaleOAuthIsQuietWhenClean(t *testing.T) {
	db := openTestDB(t)
	s := New(db)
	result, err := s.PurgeStaleOAuth(context.Background(), time.Hour)
	if err != nil {
		t.Fatalf("purge: %v", err)
	}
	if !result.Empty() {
		t.Logf("swept leftovers from earlier tests: %+v", result)
	}
	result, err = s.PurgeStaleOAuth(context.Background(), time.Hour)
	if err != nil {
		t.Fatalf("second purge: %v", err)
	}
	if !result.Empty() {
		t.Fatalf("a second sweep still found work: %+v", result)
	}
}

// The commit path has to record which pipeline produced the extraction.
func TestIntegrationCommitReceiptRecordsExtractionSource(t *testing.T) {
	db := openTestDB(t)
	s := New(db)
	userID := testUser(t, db)

	var budgetID int64
	if err := db.QueryRow(`INSERT INTO budgets (name) VALUES ('Groceries') RETURNING id`).Scan(&budgetID); err != nil {
		t.Fatalf("create budget: %v", err)
	}
	t.Cleanup(func() { db.Exec(`DELETE FROM budgets WHERE id = $1`, budgetID) })
	if _, err := db.Exec(`INSERT INTO users_budgets (user_id, budget_id) VALUES ($1, $2)`, userID, budgetID); err != nil {
		t.Fatalf("share budget: %v", err)
	}

	for _, source := range []string{SourceClientSupplied, ""} {
		result, err := s.CommitReceipt(context.Background(), &userID, ReceiptInput{
			Merchant: "Target", CatchAllBudgetID: budgetID, Reconciled: true,
			ExtractionSource: source,
			Items: []ReceiptItemInput{
				{Position: 1, Description: "Gatorade", NormKey: "GATORADE", AmountCents: 699, TaxCents: 42},
			},
		})
		if err != nil {
			t.Fatalf("commit (%q): %v", source, err)
		}
		t.Cleanup(func() { db.Exec(`DELETE FROM receipts WHERE id = $1`, result.Receipt.ID) })

		want := source
		if want == "" {
			// An unset source is the app's own pipeline, which is every row that
			// predates the column.
			want = SourceServerOCR
		}
		if result.Receipt.ExtractionSource != want {
			t.Fatalf("extraction_source: got %q want %q", result.Receipt.ExtractionSource, want)
		}
		reloaded, _, err := s.GetReceipt(context.Background(), result.Receipt.ID, &userID)
		if err != nil {
			t.Fatalf("get receipt: %v", err)
		}
		if reloaded.ExtractionSource != want {
			t.Fatalf("reloaded extraction_source: got %q want %q", reloaded.ExtractionSource, want)
		}
	}
}

// The date-range SQL is the part the in-package fakes cannot vouch for.
func TestIntegrationListTransactionsFiltered(t *testing.T) {
	db := openTestDB(t)
	s := New(db)
	userID := testUser(t, db)

	var budgetID int64
	if err := db.QueryRow(`INSERT INTO budgets (name) VALUES ('Filter Test') RETURNING id`).Scan(&budgetID); err != nil {
		t.Fatalf("create budget: %v", err)
	}
	t.Cleanup(func() { db.Exec(`DELETE FROM budgets WHERE id = $1`, budgetID) })
	if _, err := db.Exec(`INSERT INTO users_budgets (user_id, budget_id) VALUES ($1, $2)`, userID, budgetID); err != nil {
		t.Fatalf("share budget: %v", err)
	}

	mk := func(desc string, credit bool, amt float64, daysAgo int) {
		if _, err := db.Exec(`
			INSERT INTO transacts (budget_id, user_id, description, credit, amount, created_at, updated_at)
			VALUES ($1,$2,$3,$4,$5, NOW() - make_interval(days => $6), NOW())`,
			budgetID, userID, desc, credit, amt, daysAgo); err != nil {
			t.Fatalf("insert %s: %v", desc, err)
		}
	}
	mk("Coffee", false, 4.25, 40)
	mk("Groceries", false, 42.50, 10)
	mk("Payday", true, 1000, 5)
	mk("Book", false, 15.00, 1)

	ctx := context.Background()

	// No filter: everything, newest first, with totals.
	all, sum, err := s.ListTransactionsFiltered(ctx, budgetID, &userID, TransactionFilter{})
	if err != nil {
		t.Fatalf("unfiltered: %v", err)
	}
	if len(all) != 4 || sum.Count != 4 {
		t.Fatalf("expected 4, got %d (summary %d)", len(all), sum.Count)
	}
	if all[0].Description != "Book" {
		t.Fatalf("not newest-first: %s", all[0].Description)
	}
	if sum.CreditTotal != 1000 || sum.DebitTotal != 61.75 || sum.Net != 938.25 {
		t.Fatalf("totals wrong: %+v", sum)
	}

	// Date range excludes the 40-day-old row.
	from := time.Now().AddDate(0, 0, -20)
	ranged, rsum, err := s.ListTransactionsFiltered(ctx, budgetID, &userID, TransactionFilter{From: &from})
	if err != nil {
		t.Fatalf("ranged: %v", err)
	}
	if len(ranged) != 3 {
		t.Fatalf("expected 3 in range, got %d", len(ranged))
	}
	if rsum.DebitTotal != 57.50 {
		t.Fatalf("range totals should exclude the old row: %+v", rsum)
	}

	// A closed window on both ends.
	lo, hi := time.Now().AddDate(0, 0, -12), time.Now().AddDate(0, 0, -3)
	win, _, err := s.ListTransactionsFiltered(ctx, budgetID, &userID, TransactionFilter{From: &lo, To: &hi})
	if err != nil {
		t.Fatalf("window: %v", err)
	}
	if len(win) != 2 {
		t.Fatalf("expected 2 inside the window, got %d", len(win))
	}

	// Search is case-insensitive on the description.
	found, _, err := s.ListTransactionsFiltered(ctx, budgetID, &userID, TransactionFilter{Search: "groc"})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(found) != 1 || found[0].Description != "Groceries" {
		t.Fatalf("search returned %+v", found)
	}

	// A limit that cuts the range says so, rather than passing off a partial sum
	// as a complete one.
	cut, csum, err := s.ListTransactionsFiltered(ctx, budgetID, &userID, TransactionFilter{Limit: 2})
	if err != nil {
		t.Fatalf("limited: %v", err)
	}
	if len(cut) != 2 || !csum.Truncated {
		t.Fatalf("expected 2 rows and truncated=true, got %d %+v", len(cut), csum)
	}
	if csum.Count != 2 {
		t.Fatalf("summary count should match rows returned: %+v", csum)
	}

	// Access is still enforced.
	other := testUser(t, db)
	if _, _, err := s.ListTransactionsFiltered(ctx, budgetID, &other, TransactionFilter{}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a user without access got %v, want ErrNotFound", err)
	}
}
