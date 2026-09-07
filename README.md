# Hearth

A self-hostable, browser-accessible development environment. Point it at a
server you control, open a URL from any device, log in, and get a real Linux
terminal and code editor running inside an isolated container on that server —
no toolchain to install on the machine you're sitting at.

## How it fits together

Hearth is three services rather than one, split along a control-plane /
data-plane line:

- **`hearth-gateway`** (Go) — the only service a browser talks to. It owns the
  Postgres database, handles login and sessions, tracks which worker hosts are
  alive, exposes the REST API, and proxies terminal traffic to the data
  plane.
- **`hearth-agent`** (Rust) — runs on every worker host and is the only thing
  that creates, starts, stops, and destroys workspace containers there, under a
  hardened rootless-Podman profile.
- **`hearth-workspace`** (Rust) — also per worker host; handles the
  high-frequency work inside a running container: terminal sessions and file
  I/O. Terminal sessions work today; file I/O is not built yet.

The gateway talks to the data-plane services over gRPC secured with mutual TLS
from a local certificate authority.

## What works today

- One admin account; login, sessions, CSRF, per-IP rate limiting.
- Create / list / inspect / start / stop / destroy a workspace over the REST
  API — each is a real container with dropped capabilities, a read-only root,
  a user-namespace mapping, CPU / memory / PID limits, an egress-only network,
  and a persistent named volume.
- A reconciliation loop that keeps the database converged with what the
  containers are actually doing, and flags a silent host's workspaces.
- The full terminal transport: a WebSocket on the gateway, authenticated
  and origin-checked, bridged to a `podman exec` PTY in the workspace
  container. Proven end to end by a gated test — but there is no terminal
  page in the browser yet, so you can't sit down and use it.

## What's next

A bare `xterm.js` terminal page wired to that WebSocket, then a file tree
and editor with live file-change events, reconnect / resume, a
`docker-compose` + Caddy deploy with a pinned workspace base image, and
CI with an end-to-end golden path.

**Status:** early development, in progress. Not yet usable.
