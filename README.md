# README

This repository now ships a Go API with a React (Vite + TypeScript) frontend. Rails/Devise, SendGrid, and related artifacts have been removed.

## Running locally
- API: `GOFLAGS=-mod=mod go run ./cmd/api` (env: `HOST`, `PORT`, `DATABASE_URL`, `CORS_ALLOWED_ORIGINS`, `JWT_SECRET`, `STATIC_DIR`, `RELYING_PARTY_ID`, `RELYING_PARTY_NAME`, `DB_CONNECT_RETRIES`, `DB_CONNECT_INTERVAL_MS`). Default `DATABASE_URL` now points to the legacy `budget` database: `postgres://postgres:budgetpass@localhost:5432/budget?sslmode=disable`.
- Frontend (dev): `cd frontend && npm install && npm run dev` (proxies `/api` to `localhost:8080`).
- Docker: `docker-compose up --build api db`. The API container serves the built React app from `/app/static`.
  - DB wait knobs: `DB_CONNECT_RETRIES` (default 10) and `DB_CONNECT_INTERVAL_MS` (default 500).
  - Passkeys: set `RELYING_PARTY_ID` to the hostname users will register from (defaults to `localhost`) and `RELYING_PARTY_NAME` to change the RP display name.

## Database
`db/schema.sql` contains a minimal Postgres schema aligned to the legacy dump (users, budgets, transactions, users_budgets, passkeys). Apply it to your DB (e.g., `psql -f db/schema.sql`). The script creates (if missing) and connects to the `budget` database so tables aren't created in the default `postgres` database. Running it against an existing restored dump will add the passkeys table and ensure the users/budgets join table has the primary key the Go API expects.

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

Auth: set `JWT_SECRET` to enable JWT issuance. Health checks remain open.

Dockerfile: `Dockerfile.go-app` is multi-stage (frontend build → Go build → scratch runtime) and serves the React assets directly from `/app/static`.
