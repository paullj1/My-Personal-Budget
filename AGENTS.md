# Repository Guidelines

## Project Structure
- Go API under `cmd/api` and `internal/...` (config, database, store, server, passkey, auth).
- React + Vite frontend in `frontend/` with TypeScript.
- Docker: `Dockerfile.go-app` (multi-stage builds frontend then API) and `docker-compose.yml` (api + postgres db).
- Database schema lives in `db/schema.sql`.

## Build, Test, and Development
- Run API locally: `GOFLAGS=-mod=mod go run ./cmd/api` (env: `HOST`, `PORT`, `DATABASE_URL`, `JWT_SECRET`, `RELYING_PARTY_ID`, `RELYING_PARTY_NAME`, etc.).
- Frontend dev server: `cd frontend && npm install && npm run dev` (proxies `/api` to `localhost:8080`).
- Docker dev: `docker-compose up --build api db`.
- Tests: `GOFLAGS=-mod=mod go test ./...` (set `GOCACHE` if needed).
- Store integration tests (real SQL: single-use codes, token rotation, cascading revocation) skip
  unless a database is offered: `TEST_DATABASE_URL=postgres://... go test ./internal/store/`. Apply
  `db/schema.sql` to a throwaway database first. Worth running after touching `internal/store`; the
  in-package fakes cannot catch things like a timestamp column with the wrong type.

## Coding Style
- Go: idiomatic Go 1.22+, run `gofmt`.
- Frontend: TypeScript/React with functional components; keep styling in `frontend/src/styles.css`.
- Keep handlers thin; put DB logic in `internal/store`, config in `internal/config`.

## Auth & Security
- JWT auth; tokens issued when passkey login finishes. `JWT_SECRET` must be set for protected routes.
- The OAuth authorization server (`internal/oauth`, `internal/server/handlers/oauth.go`) runs only
  when `PUBLIC_BASE_URL` and `JWT_SECRET` are both set; consent is a logged-in browser action, so it
  rides the passkey session.
- OAuth tokens are opaque and stored hashed, not JWTs, so that disconnecting a client from the
  Connections screen takes effect on its next request.
- Client registration is open by necessity. `middleware.RateLimit` caps the inflow and
  `oauthsweep.StartScheduler` clears registrations that never became connections; both are needed,
  neither is sufficient alone.
- Passkey endpoints are demo-grade (no attestation/signature verification); set `RELYING_PARTY_ID` to your host.
- Database connection retries configurable via `DB_CONNECT_RETRIES` and `DB_CONNECT_INTERVAL_MS`.
