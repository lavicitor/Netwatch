# Netwatch

A concurrent network scanner written in Go: discovers hosts and open ports
on a target subnet, streams results live to a small web GUI, and (optionally)
keeps history in Postgres -- including detecting when a new device joins the
network or a port opens/closes since the last scan.

## Architecture

- `cmd/netwatch` -- entrypoint: wires config, store, scanner, and the HTTP server together.
- `internal/scanner` -- concurrent host/port discovery. Not yet implemented (see Status below).
- `internal/store` -- Postgres persistence via `pgx`, migrations embedded and applied automatically via `goose` on startup. Entirely optional -- see below.
- `internal/api` -- REST endpoints + a Server-Sent Events stream (`/api/stream`) for pushing live results to the GUI, plus serves `web/static`.
- `internal/model` -- shared types.
- `migrations/` -- embedded SQL schema (hosts, host_ports, scans, scan_events).
- `web/static` -- a minimal vanilla-JS GUI: submits a scan target, renders hosts grouped by subnet with their open ports as they stream in.
- `deploy/scan-network/compose.yml` -- a Compose stack of fake "devices" to scan during development/demos, so this never needs to touch a real LAN.
- `.devcontainer` -- VS Code Dev Container config; builds and runs the app itself from a plain Dockerfile. Works the same way under Docker or Podman.

## Running with or without a database

Persistence is entirely optional and off by default. Set all five
`NETWATCH_DB_*` variables (host, port, user, password, database name -- see
`.env.example`) and Netwatch tries to connect and apply migrations on
startup. Leave them unset and Netwatch just runs with in-memory results --
no error, nothing logged, that's a normal supported mode. If they're set
but the connection or migration fails, Netwatch logs a warning and falls
back to in-memory mode rather than refusing to start.

## Prerequisites

- Docker or Podman + Compose (`docker compose` / `podman-compose`)
- VS Code with the **Dev Containers** extension. If you're on Podman, point VS Code at it: Settings -> `dev.containers.dockerPath` -> `podman`.
- Optionally, a Postgres instance reachable from the devcontainer, if you want history/persistence (see above and `.env.example`).

## Setup order

The app and the demo scan-network are independent of each other -- the
devcontainer doesn't require the scan-network to exist, and it starts the
same way whether or not you ever touch it.

**To just run the app:**

1. Copy `.env.example` to `.env` and fill in the `NETWATCH_DB_*` values if you want persistence, or leave them blank to run in-memory.
2. Open this folder in VS Code -> **Dev Containers: Reopen in Container**. It builds the image from `.devcontainer/Dockerfile` and runs `go mod tidy` automatically.
3. Inside the devcontainer: `make run` (or the VS Code Go debugger). GUI at <http://localhost:8080>.

**To also scan the fake demo devices** (run from your host terminal, not the devcontainer's -- substitute `docker compose`/`docker network` if you're on Docker instead of Podman):

1. `podman-compose -f deploy/scan-network/compose.yml up -d` -- brings up `scan-net` and the fake devices.
2. `podman network connect scan-net Devcontainer-Netwatch` -- joins the already-running devcontainer to `scan-net`. No restart needed.
3. Scan `10.89.0.0/24` from the GUI or `POST /api/scan`.
4. `podman network disconnect scan-net Devcontainer-Netwatch` when done, and/or `podman-compose -f deploy/scan-network/compose.yml down` to tear the fake devices down entirely.

## Status

Scaffolding (config, storage/migrations, HTTP routing, GUI shell,
devcontainer, scan-network) works end to end. Not yet implemented:

- [ ] `Scanner.Scan` in `internal/scanner/scanner.go` -- the actual concurrent host/port discovery
- [ ] Broadcast hub wiring `/api/scan` -> `/api/stream` in `internal/api/api.go`
- [ ] `store.Store` read/write methods, including new-device/port-change diffing
- [ ] `/api/hosts` in-memory fallback when no database is configured
