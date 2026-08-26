-- Minimal schema for Go API (aligned to legacy dump.sql), scoped to the budget database.
SELECT 'CREATE DATABASE budget'
WHERE NOT EXISTS (SELECT FROM pg_database WHERE datname = 'budget')\gexec

\connect budget

CREATE TABLE IF NOT EXISTS users (
  id SERIAL PRIMARY KEY,
  email VARCHAR NOT NULL DEFAULT '',
  encrypted_password VARCHAR NOT NULL DEFAULT '',
  reset_password_token VARCHAR,
  reset_password_sent_at TIMESTAMP,
  remember_created_at TIMESTAMP,
  sign_in_count INTEGER NOT NULL DEFAULT 0,
  current_sign_in_at TIMESTAMP,
  last_sign_in_at TIMESTAMP,
  current_sign_in_ip INET,
  last_sign_in_ip INET,
  created_at TIMESTAMP NOT NULL DEFAULT NOW(),
  updated_at TIMESTAMP NOT NULL DEFAULT NOW(),
  failed_attempts INTEGER NOT NULL DEFAULT 0,
  unlock_token VARCHAR,
  locked_at TIMESTAMP,
  confirmation_token VARCHAR,
  confirmed_at TIMESTAMP,
  confirmation_sent_at TIMESTAMP,
  unconfirmed_email VARCHAR
);
CREATE UNIQUE INDEX IF NOT EXISTS index_users_on_email ON users (email);

CREATE TABLE IF NOT EXISTS api_keys (
  id SERIAL PRIMARY KEY,
  user_id INTEGER REFERENCES users(id) ON DELETE CASCADE,
  name VARCHAR NOT NULL DEFAULT '',
  token_hash TEXT NOT NULL UNIQUE,
  token_prefix VARCHAR NOT NULL DEFAULT '',
  created_at TIMESTAMP NOT NULL DEFAULT NOW(),
  last_used_at TIMESTAMP
);
CREATE INDEX IF NOT EXISTS index_api_keys_on_user_id ON api_keys (user_id);

CREATE TABLE IF NOT EXISTS budgets (
  id SERIAL PRIMARY KEY,
  name VARCHAR,
  created_at TIMESTAMP NOT NULL DEFAULT NOW(),
  updated_at TIMESTAMP NOT NULL DEFAULT NOW(),
  payroll DOUBLE PRECISION DEFAULT 0,
  payroll_run_at TIMESTAMP,
  auto_balance_enabled BOOLEAN NOT NULL DEFAULT FALSE
);

ALTER TABLE budgets
  ADD COLUMN IF NOT EXISTS auto_balance_enabled BOOLEAN NOT NULL DEFAULT FALSE;

CREATE TABLE IF NOT EXISTS budget_auto_balance_sources (
  budget_id INTEGER REFERENCES budgets(id) ON DELETE CASCADE,
  source_budget_id INTEGER REFERENCES budgets(id) ON DELETE CASCADE,
  weight INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY (budget_id, source_budget_id)
);

CREATE TABLE IF NOT EXISTS transacts (
  id SERIAL PRIMARY KEY,
  description VARCHAR,
  budget_id INTEGER REFERENCES budgets(id) ON DELETE CASCADE,
  user_id INTEGER REFERENCES users(id) ON DELETE SET NULL,
  credit BOOLEAN NOT NULL DEFAULT FALSE,
  amount DOUBLE PRECISION,
  created_at TIMESTAMP NOT NULL DEFAULT NOW(),
  updated_at TIMESTAMP NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS users_budgets (
  user_id INTEGER REFERENCES users(id) ON DELETE CASCADE,
  budget_id INTEGER REFERENCES budgets(id) ON DELETE CASCADE
);
CREATE INDEX IF NOT EXISTS index_users_budgets_on_user_id ON users_budgets (user_id);
CREATE INDEX IF NOT EXISTS index_users_budgets_on_budget_id ON users_budgets (budget_id);
DO $$
BEGIN
  IF NOT EXISTS (
    SELECT 1 FROM pg_constraint WHERE conname = 'users_budgets_pkey'
  ) THEN
    -- Collapse accidental duplicates before adding a PK so ON CONFLICT works.
    DELETE FROM users_budgets ub
    USING users_budgets dup
    WHERE ub.ctid < dup.ctid
      AND ub.user_id = dup.user_id
      AND ub.budget_id = dup.budget_id;

    ALTER TABLE users_budgets
      ADD CONSTRAINT users_budgets_pkey PRIMARY KEY (user_id, budget_id);
  END IF;
END$$;

-- Passkeys persist WebAuthn credentials per user (one credential per user for now).
CREATE TABLE IF NOT EXISTS passkeys (
  id SERIAL PRIMARY KEY,
  user_id INTEGER UNIQUE REFERENCES users(id) ON DELETE CASCADE,
  credential_id TEXT UNIQUE NOT NULL,
  public_key TEXT NOT NULL DEFAULT '',
  sign_count INTEGER NOT NULL DEFAULT 0,
  backup_eligible BOOLEAN NOT NULL DEFAULT FALSE,
  backup_state BOOLEAN NOT NULL DEFAULT FALSE,
  created_at TIMESTAMP NOT NULL DEFAULT NOW(),
  updated_at TIMESTAMP NOT NULL DEFAULT NOW()
);

-- Receipts persist the parsed extraction, not the photo. Keeping the JSON gives
-- an audit trail and the corpus for budget suggestions without the retention
-- concerns of storing images. See docs/receipt-scan-design.md.
CREATE TABLE IF NOT EXISTS receipts (
  id SERIAL PRIMARY KEY,
  user_id INTEGER REFERENCES users(id) ON DELETE SET NULL,
  merchant VARCHAR NOT NULL DEFAULT '',
  purchased_at TIMESTAMP,
  currency VARCHAR NOT NULL DEFAULT 'USD',
  subtotal_cents INTEGER NOT NULL DEFAULT 0,
  tax_cents INTEGER NOT NULL DEFAULT 0,
  total_cents INTEGER NOT NULL DEFAULT 0,
  tax_evidence VARCHAR NOT NULL DEFAULT 'unknown',
  tax_basis VARCHAR NOT NULL DEFAULT '',
  reconciled BOOLEAN NOT NULL DEFAULT TRUE,
  parsed JSONB NOT NULL DEFAULT '{}'::jsonb,
  model VARCHAR NOT NULL DEFAULT '',
  created_at TIMESTAMP NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS index_receipts_on_user_id ON receipts (user_id);

-- One row per receipt line. norm_key is the match key that drives budget
-- suggestions; amounts are integer cents because tax proration must be exact.
CREATE TABLE IF NOT EXISTS receipt_items (
  id SERIAL PRIMARY KEY,
  receipt_id INTEGER NOT NULL REFERENCES receipts(id) ON DELETE CASCADE,
  budget_id INTEGER REFERENCES budgets(id) ON DELETE SET NULL,
  transact_id INTEGER REFERENCES transacts(id) ON DELETE SET NULL,
  line_text VARCHAR NOT NULL DEFAULT '',
  norm_key VARCHAR NOT NULL DEFAULT '',
  description VARCHAR NOT NULL DEFAULT '',
  marker VARCHAR NOT NULL DEFAULT '',
  amount_cents INTEGER NOT NULL DEFAULT 0,
  tax_cents INTEGER NOT NULL DEFAULT 0,
  adjust_cents INTEGER NOT NULL DEFAULT 0,
  taxable BOOLEAN,
  position INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS index_receipt_items_on_norm_key ON receipt_items (norm_key);
CREATE INDEX IF NOT EXISTS index_receipt_items_on_receipt_id ON receipt_items (receipt_id);
CREATE INDEX IF NOT EXISTS index_receipt_items_on_budget_id ON receipt_items (budget_id);

-- Lets a transaction link back to the receipt that produced it.
ALTER TABLE transacts
  ADD COLUMN IF NOT EXISTS receipt_id INTEGER REFERENCES receipts(id) ON DELETE SET NULL;
CREATE INDEX IF NOT EXISTS index_transacts_on_receipt_id ON transacts (receipt_id);

-- Where an extraction came from, so the two paths can be compared later without
-- guessing. Deliberately not the model's name: the server-side model is already
-- in receipts.model, and the client-side one is not something this app can see
-- or trust to stay put.
--   server_ocr      -- came through this app's own pipeline, hand entry included
--   client_supplied -- arrived already structured from an MCP client
ALTER TABLE receipts
  ADD COLUMN IF NOT EXISTS extraction_source VARCHAR NOT NULL DEFAULT 'server_ocr';
CREATE INDEX IF NOT EXISTS index_receipts_on_extraction_source ON receipts (extraction_source);

-- Timestamps here are TIMESTAMPTZ, unlike the legacy tables above.
--
-- These are the first columns this app compares against a Go-side time.Now()
-- rather than against the database's own NOW(). A bare TIMESTAMP drops the
-- offset and keeps the wall clock, so on a host that is not on UTC an authorization
-- code minted five minutes ahead reads back hours in the past and every exchange
-- fails as invalid_grant. TIMESTAMPTZ round-trips the instant instead.
-- OAuth 2.1 authorization server, so remote MCP clients can connect without a
-- pasted API key. Clients register themselves (RFC 7591); nothing is
-- pre-provisioned.
CREATE TABLE IF NOT EXISTS oauth_clients (
  id SERIAL PRIMARY KEY,
  client_id TEXT NOT NULL UNIQUE,
  -- Null for public clients, which is what MCP clients using PKCE are.
  client_secret_hash TEXT,
  client_name VARCHAR NOT NULL DEFAULT '',
  client_uri VARCHAR NOT NULL DEFAULT '',
  logo_uri VARCHAR NOT NULL DEFAULT '',
  redirect_uris JSONB NOT NULL DEFAULT '[]'::jsonb,
  grant_types JSONB NOT NULL DEFAULT '[]'::jsonb,
  response_types JSONB NOT NULL DEFAULT '[]'::jsonb,
  token_endpoint_auth_method VARCHAR NOT NULL DEFAULT 'none',
  scope VARCHAR NOT NULL DEFAULT '',
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- One row per (user, client) consent: the "connection" the UI lists and can
-- disconnect. expires_at NULL means the connection does not expire on its own,
-- which is the default; revoking is an explicit act.
CREATE TABLE IF NOT EXISTS oauth_authorizations (
  id SERIAL PRIMARY KEY,
  user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  client_id TEXT NOT NULL REFERENCES oauth_clients(client_id) ON DELETE CASCADE,
  scope VARCHAR NOT NULL DEFAULT '',
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  last_used_at TIMESTAMPTZ,
  expires_at TIMESTAMPTZ,
  revoked_at TIMESTAMPTZ
);
CREATE UNIQUE INDEX IF NOT EXISTS index_oauth_authorizations_on_user_client
  ON oauth_authorizations (user_id, client_id);

-- Authorization codes are single-use and short-lived. Only the hash is stored,
-- for the same reason api_keys stores only a hash.
CREATE TABLE IF NOT EXISTS oauth_auth_codes (
  id SERIAL PRIMARY KEY,
  code_hash TEXT NOT NULL UNIQUE,
  authorization_id INTEGER NOT NULL REFERENCES oauth_authorizations(id) ON DELETE CASCADE,
  redirect_uri TEXT NOT NULL DEFAULT '',
  code_challenge TEXT NOT NULL DEFAULT '',
  code_challenge_method VARCHAR NOT NULL DEFAULT 'S256',
  -- RFC 8707 resource indicator: the audience the issued token is bound to.
  resource TEXT NOT NULL DEFAULT '',
  scope VARCHAR NOT NULL DEFAULT '',
  expires_at TIMESTAMPTZ NOT NULL,
  consumed_at TIMESTAMPTZ,
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS index_oauth_auth_codes_on_expires_at ON oauth_auth_codes (expires_at);

-- Access and refresh tokens are opaque and stored hashed, so that disconnecting
-- a client takes effect on the next request. A JWT could not be withdrawn.
CREATE TABLE IF NOT EXISTS oauth_tokens (
  id SERIAL PRIMARY KEY,
  authorization_id INTEGER NOT NULL REFERENCES oauth_authorizations(id) ON DELETE CASCADE,
  kind VARCHAR NOT NULL,
  token_hash TEXT NOT NULL UNIQUE,
  resource TEXT NOT NULL DEFAULT '',
  scope VARCHAR NOT NULL DEFAULT '',
  -- Null means no expiry of its own; refresh tokens live as long as the
  -- connection does.
  expires_at TIMESTAMPTZ,
  last_used_at TIMESTAMPTZ,
  revoked_at TIMESTAMPTZ,
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS index_oauth_tokens_on_authorization_id ON oauth_tokens (authorization_id);
CREATE INDEX IF NOT EXISTS index_oauth_tokens_on_expires_at ON oauth_tokens (expires_at);
