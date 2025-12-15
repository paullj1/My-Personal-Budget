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

## Coding Style
- Go: idiomatic Go 1.22+, run `gofmt`.
- Frontend: TypeScript/React with functional components; keep styling in `frontend/src/styles.css`.
- Keep handlers thin; put DB logic in `internal/store`, config in `internal/config`.

## Auth & Security
- JWT auth; tokens issued when passkey login finishes. `JWT_SECRET` must be set for protected routes.
- Passkey endpoints are demo-grade (no attestation/signature verification); set `RELYING_PARTY_ID` to your host.
- Database connection retries configurable via `DB_CONNECT_RETRIES` and `DB_CONNECT_INTERVAL_MS`.
