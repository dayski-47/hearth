# Hearth

Hearth is a self-hostable, browser-accessible development environment. Point it
at a server you control, open a URL from any device, and log in to a real Linux
terminal and editor running inside an isolated container on that server. There
is nothing to install on the machine you are sitting at.

> **Status:** early development, in progress. The pieces listed below work and
> there is a React UI that logs in, manages workspaces, and opens a terminal,
> but the file tree and editor are not built and Hearth is not ready for daily
> use.

## Key features

- **Isolated workspaces.** Every workspace is a rootless-Podman container with
  all capabilities dropped, a read-only root filesystem, a user-namespace
  mapping, CPU, memory and PID limits, an egress-only network, and a persistent
  volume.
- **Browser terminal.** A `podman exec` PTY streamed to a real xterm.js
  terminal in the browser over one authenticated, origin-checked WebSocket.
- **File access.** List, read, write, create, rename, and delete files in a
  workspace over REST, with every path confined to the workspace volume by
  the kernel (`openat2` with `RESOLVE_BENEATH`), and atomic saves.
- **Control plane and data plane are separate.** A stateless Go gateway holds
  the database and the API; small per-host Rust services own the container
  runtime. A bug in terminal handling cannot take down auth or the database.
- **Mutual TLS everywhere.** Every call from the gateway to a host is gRPC over
  mTLS from a local certificate authority, and the gateway verifies the client
  certificate.
- **Self-healing state.** A reconciliation loop keeps the database converged
  with what the containers are actually doing, and flags a host that goes
  silent.

## Tech stack

- **Gateway:** Go, chi, pgx, sqlc, goose, Postgres, `log/slog`.
- **Data plane:** Rust, tokio, tonic, bollard, Podman.
- **Between services:** gRPC and protobuf over mutual TLS.
- **Frontend:** React, TypeScript, Vite, Zustand, xterm.js.

## How it fits together

Hearth is three services rather than one, split along a control-plane and
data-plane line:

- **`hearth-gateway`** (Go) is the only service a browser talks to. It owns the
  Postgres database, handles login and sessions, tracks which worker hosts are
  alive, serves the web page, exposes the REST API, and proxies terminal
  traffic to the data plane.
- **`hearth-agent`** (Rust) runs on every worker host. It is the only thing that
  creates, starts, stops, and destroys workspace containers there, under a
  hardened Podman profile.
- **`hearth-workspace`** (Rust) also runs on every worker host. It handles the
  high-frequency work inside a running container: terminal sessions, file
  operations, and a filesystem watch that streams change events out.

## Self-hosting

Hearth runs on a single Linux host: `docker compose` brings up the gateway,
Postgres, and Caddy, and two rootless `systemd --user` services run the
container data plane. See [docs/self-hosting.md](docs/self-hosting.md) for the
four command setup and the configuration knobs (workspace limits, a custom base
image, network lockdown).

## Roadmap

- [x] Service skeleton, mutual TLS, agent registration
- [x] Admin login, sessions, CSRF, per-IP login rate limiting
- [x] Workspace lifecycle over the REST API, with a reconciliation loop
- [x] Terminal transport: a gateway WebSocket bridged to a container PTY
- [x] A browser page: log in, pick a workspace, get a terminal
- [x] File operations: list, read, write, create, rename, delete, over REST
- [x] Live file-change events streamed to the browser over a WebSocket
- [x] `docker-compose` and Caddy deploy on a single host
- [x] A pinned workspace base image and dependency, secret, and image scanning in CI
- [x] A React workspace UI with lifecycle controls and the terminal
- [ ] A file tree and an editor
- [ ] Reconnect and session resume
