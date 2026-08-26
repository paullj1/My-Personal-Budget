# README

This repository now ships a Go API with a React (Vite + TypeScript) frontend. Rails/Devise, SendGrid, and related artifacts have been removed.

## Running locally
- API: `GOFLAGS=-mod=mod go run ./cmd/api` (env: `HOST`, `PORT`, `DATABASE_URL`, `CORS_ALLOWED_ORIGINS`, `JWT_SECRET`, `STATIC_DIR`, `RELYING_PARTY_ID`, `RELYING_PARTY_NAME`, `PUBLIC_BASE_URL`, `DB_CONNECT_RETRIES`, `DB_CONNECT_INTERVAL_MS`). Default `DATABASE_URL` now points to the legacy `budget` database: `postgres://postgres:budgetpass@localhost:5432/budget?sslmode=disable`.
- Frontend (dev): `cd frontend && npm install && npm run dev` (proxies `/api` to `localhost:8080`).
- Docker: `docker-compose up --build api db`. The API container serves the built React app from `/app/static`.
  - DB wait knobs: `DB_CONNECT_RETRIES` (default 10) and `DB_CONNECT_INTERVAL_MS` (default 500).
  - Passkeys: set `RELYING_PARTY_ID` to the hostname users will register from (defaults to `localhost`) and `RELYING_PARTY_NAME` to change the RP display name.

## Database
`db/schema.sql` contains a minimal Postgres schema aligned to the legacy dump (users, budgets, transactions, users_budgets, passkeys, receipts, oauth_*). Apply it to your DB (e.g., `psql -f db/schema.sql`). The `oauth_*` tables use `TIMESTAMPTZ` rather
than the legacy `TIMESTAMP`: they are the first columns compared against a Go-side clock, and a naive
timestamp drops the offset, which on a non-UTC host expires every authorization code before it can be
used. Store integration tests cover this — see `AGENTS.md`. The script creates (if missing) and connects to the `budget` database so tables aren't created in the default `postgres` database. Running it against an existing restored dump will add the passkeys table and ensure the users/budgets join table has the primary key the Go API expects.

## Go API endpoints (v1)
- `GET /api/v1/healthz` – health check.
- Passkeys (WebAuthn; relies on RP ID/origin, but no email verification):
  - `POST /api/v1/auth/passkeys/begin` – start registration, returns creation options.
  - `POST /api/v1/auth/passkeys/finish` – finish registration, stores credential id/public key.
  - `POST /api/v1/auth/passkeys/login/begin` – start assertion for login.
  - `POST /api/v1/auth/passkeys/login/finish` – finish assertion, returns JWT.
  - Passkeys are persisted to the `passkeys` table and one credential is allowed per email. Email ownership is not verified—first to register an email wins—so configure RP ID/origin correctly and add your own email verification if needed.
- Budgets/transactions:
  - `GET /api/v1/budgets`
  - `POST /api/v1/budgets`
  - `GET /api/v1/budgets/{id}`
  - `PUT/PATCH /api/v1/budgets/{id}`
  - `DELETE /api/v1/budgets/{id}`
  - `GET /api/v1/budgets/{id}/transactions?limit=100&offset=0&q=`
  - `POST /api/v1/budgets/{id}/transactions`
  - `GET/POST/DELETE /api/v1/budgets/{id}/shares`

## OAuth
Set `PUBLIC_BASE_URL` (and `JWT_SECRET`) to run the built-in authorization server, which is what
lets web clients such as the Claude app connect without a pasted key. Clients register themselves;
nothing is pre-provisioned.

- `GET /.well-known/oauth-protected-resource` (and `…/mcp`) – RFC 9728 resource metadata.
- `GET /.well-known/oauth-authorization-server` – RFC 8414 server metadata.
- `POST /oauth/register` – RFC 7591 dynamic client registration (open; grants nothing on its own).
- `GET /oauth/authorize` – validates the request, then hands the browser to the consent screen.
- `POST /oauth/token` – `authorization_code` and `refresh_token` grants.
- `POST /oauth/revoke` – RFC 7009.

PKCE with S256 is required, refresh tokens rotate on use, and access tokens are opaque and stored
hashed so disconnecting takes effect on the next request. Consent is granted by a logged-in browser
session, so it is backed by the same passkey used to sign in.

Registration is open, because a remote MCP client cannot be pre-provisioned, so two things bound what
an unauthenticated caller can leave behind:

- **Rate limit** on `POST /oauth/register` — `OAUTH_REGISTRATION_LIMIT` per `OAUTH_REGISTRATION_WINDOW_MS`
  (default 20/hour), answering 429 with `Retry-After`. The cap is global, not per-address: the server
  sits behind a proxy, so `X-Forwarded-For` is the only thing telling callers apart and a caller sets
  that itself. A global cap bounds total rows regardless of who is asking.
- **Background sweep** every `OAUTH_SWEEP_INTERVAL_MS` (default hourly) clearing anything abandoned for
  longer than `OAUTH_STALE_AFTER_MS` (default 24h): registrations that never got consent, revoked
  connections, expired codes, and dead tokens. Clients with a live connection are never swept, however
  old. `OAUTH_STALE_AFTER_MS` must comfortably exceed the time between registering and consenting — a
  client swept mid-flow fails as `invalid_client`.

Connections are managed at `/connections` in the UI, backed by `GET /api/v1/connections`,
`DELETE /api/v1/connections/{id}` (disconnect) and `PATCH /api/v1/connections/{id}` (set or clear an
expiry; connections never expire by default). API keys live on the same screen and are unchanged.

Auth: set `JWT_SECRET` to enable JWT issuance. Health checks remain open.

Dockerfile: `Dockerfile.go-app` is multi-stage (frontend build → Go build → scratch runtime) and serves the React assets directly from `/app/static`.
