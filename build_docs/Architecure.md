# React/Vite/Typescript/Golang

A React single-page app talks to a Go REST API over JSON; the API persists to a relational database via GORM.

## Frontend

- **React 18 + TypeScript**, built with **Vite**.
- **react-router-dom** for routing; **@tanstack/react-query** for server state (queries/mutations with cache invalidation).
- **Tailwind CSS** with a small shadcn-style component kit (`components/ui`: `Card`, `Button`, `Input`, `Markdown`, `MarkdownEditor`, …).
- A thin REST client (`web/src/lib/api.ts`) using `fetch` with JWT credentials; typed API objects per domain area.
- Shipped as static assets served by Apache under the `/` base path.

## Backend

- **Go**, HTTP routing with **chi**, persistence with **GORM** (MySQL).
- Layered by responsibility:
  - `internal/domain` — GORM models (embedding a shared `Base` with a UUID id, timestamps, soft delete) and `AllModels()` for AutoMigrate.
  - `internal/<area>` — service packages holding business logic (`auth`, `workflow`, `search`, `notifications`, `reports`, `automation`, `devlinks`, `architectures`). Each exposes a `NewService(db, …)` and its own `Err*` sentinels.
  - `internal/httpapi` — the chi router, request/response helpers, and per-area handlers that decode JSON, call services, and map sentinel errors to HTTP status codes.
- `cmd/server/main.go` wires config → DB (open + AutoMigrate + idempotent seeds) → services → HTTP server.

## Authentication & authorization

- **Session cookies** (HTTP-only) for the web app, and **personal access tokens** (`c2_pat_…`, stored only as SHA-256 hashes) for API clients and the MCP server. Tokens inherit their owner's projects and permissions and can be read-only or read-write.
- Org roles enforced in the service layer; middleware provides `RequireAuth`.

## Deployment

`deployment/deploy.sh` cross-compiles the Go API for linux, builds the Vite SPA (base `/<project_name>/`), and rsync/scp's both to the server. The API runs as a **systemd** unit (`<project_name>-api`) behind **Apache**, which serves the SPA and reverse-proxies `/<project_name>/api` to the service (stripping the `/<project_name>` prefix). A `/healthz` endpoint backs the deploy health check.
