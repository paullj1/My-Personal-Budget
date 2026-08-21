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
