# Self-hosting Hearth

Hearth runs on a single Linux host you control. This guide takes you from a
fresh server to a working login in four commands, then covers the knobs you are
most likely to reach for: workspace limits, a custom base image, and network
lockdown.

For what Hearth is and how the services fit together, see the README.

## What you need

- A Linux host with **rootless Podman** installed and its user socket enabled:
  ```
  systemctl --user enable --now podman.socket
  loginctl enable-linger $USER
  ```
  The socket is how the host data plane drives Podman. `enable-linger` keeps
  the host services running after you log out (`just install-services` runs it
  too, but enabling the Podman socket is yours to do).
- **Docker** and **Compose v2** (`docker compose version` should succeed). The
  gateway, Postgres, and Caddy run as containers. The stack is tested only with
  Docker and Compose v2; `podman-compose` mostly works but differs on
  `extra_hosts: host-gateway` and healthcheck syntax, so treat it as
  best-effort.
- The **Rust toolchain** (`cargo`), used to build the two host binaries.
- Either a **domain** pointed at the host, or acceptance of a browser
  certificate warning on `localhost`. With a real domain Caddy fetches a
  Let's Encrypt certificate automatically. With `localhost` it self-signs, and
  your browser will warn you on the first visit.

### Networking

The two host services listen on all interfaces: `hearth-agent` on
`0.0.0.0:9091` and `hearth-workspace` on `0.0.0.0:9092`. They bind `0.0.0.0`
rather than loopback because the gateway runs in a container and reaches them
across the Podman bridge at `host.containers.internal`. Every call is gated by
mutual TLS: the gateway must present a client certificate from the local
certificate authority that `just setup` generates.

The gateway container reaches 9091 and 9092 across the Compose bridge, whose
subnet is pinned to `10.7.0.0/24`. A host with a default-deny firewall must
allow that subnet to those ports, or workspace operations time out (`docker
compose` login and `/healthz` still work, so the symptom is a 502 only when a
workspace is created):

```
sudo ufw allow from 10.7.0.0/24 to any port 9091 proto tcp
sudo ufw allow from 10.7.0.0/24 to any port 9092 proto tcp
```

On a host with a public interface, close 9091 and 9092 to everything else so
they are reachable only from that subnet. The gateway's own gRPC port is
published on `127.0.0.1:9090` for the host agent to register against, and only
Caddy publishes 80 and 443.

## Why two of the five services run on the host

A full deployment is five services: `postgres`, the gateway, and Caddy as
containers, plus `hearth-agent` and `hearth-workspace` as `systemd --user`
units on the host. The two Rust services stay on the host because they sit
directly on Podman. `hearth-agent` drives the Podman socket to create and
destroy workspace containers, and `hearth-workspace` reads and writes workspace
files at Podman's on-disk volume path. Running them inside a container would
mean nesting a container runtime in a container for no benefit. This is the
same split a GitLab Runner or a Nomad client uses: a small agent on the metal,
the control plane anywhere.

## Quickstart

```
git clone https://github.com/dayski-47/hearth && cd hearth
just setup
docker compose -f deploy/docker-compose.yml up -d
just install-services
```

`just setup` prompts for an admin username, an admin password, and your domain.
It generates the mTLS CA under `deploy/certs/`, builds the gateway image, hashes
the password, and writes `.env` from `.env.example`. It will not overwrite an
existing `.env`; re-run with `just setup --force` if you need to start over.

`docker compose ... up -d` starts Postgres, the gateway, and Caddy.
`just install-services` builds the release binaries, renders the systemd unit
files with your repo path, and enables `hearth-agent` and `hearth-workspace`.

Then open `https://<your-domain>` (or `https://localhost`) and log in with the
admin credential you set during setup. Check the host services with:

```
systemctl --user status hearth-agent hearth-workspace
```

## Changing workspace limits

The per-workspace resource limits live in `.env` as `HEARTH_WORKSPACE_*`:

| Variable | Meaning | Default |
| --- | --- | --- |
| `HEARTH_WORKSPACE_CPU` | CPU quota in millicores | `2000` |
| `HEARTH_WORKSPACE_MEMORY` | Memory limit in bytes | `2147483648` (2 GiB) |
| `HEARTH_WORKSPACE_PIDS` | Max process count | `512` |
| `HEARTH_WORKSPACE_DISK` | Disk quota in bytes | `5368709120` (5 GiB) |
| `HEARTH_WORKSPACE_IDLE_TIMEOUT` | Idle time before a workspace is paused | `30m` |

These values are read by the gateway alone; it passes the limits to the host
agent per workspace. After editing `.env`, restart the gateway container so it
re-reads its environment:

```
docker compose -f deploy/docker-compose.yml up -d --force-recreate gateway
```

New limits apply to workspaces created after the restart.

The Postgres password is a separate setting. The compose default is `hearth`,
and Postgres is never published outside the compose network. Set
`POSTGRES_PASSWORD` in `deploy/.env` (Compose reads that file when it parses the
stack) before the first `docker compose up`. Changing it on a running stack does
not work: `pgdata` keeps the password from its first launch and Postgres ignores
the variable afterward, so the gateway's connection string would change while
the database password would not. To change it later you have to delete the
`pgdata` volume as well, which wipes the database.

## Using your own base image

Every workspace starts from one OCI image. Set `HEARTH_WORKSPACE_IMAGE` in
`.env` to any image that has a shell, then restart the gateway container as
above. A workspace is a hardened container (all capabilities dropped, read-only
root, user-namespace mapping), so the image only needs a shell and whatever
tools you want available by default.

No workspace image is published yet, though a fresh `.env` carries one as the
default, so for now point `HEARTH_WORKSPACE_IMAGE` at an image you can pull, for
example a stock `docker.io/library/debian:stable` or one you build yourself. A
published default image and a `just workspace-image` helper to build it are
coming with the CI work.

## Locking networking down

By default workspaces attach to the `egress` Podman network, which allows
outbound traffic (so `git clone` and `npm install` work inside a workspace) but
no inbound connections. To cut off egress entirely, set:

```
HEARTH_WORKSPACE_NETWORK=none
```

in `.env` and restart the gateway container as above. Workspaces then have no
network at all. The trade-off is that nothing inside a workspace can reach the
internet: no `git clone`, no package installs, no outbound API calls. Use this
when a workspace should only ever touch code you put in its volume yourself.

## A second host

Multi-host scheduling is post-v1. The registry seam exists: hosts register with
the gateway and it tracks which ones are alive. The placement logic that would
pick a host for a new workspace does not, so today every deployment is a single
host.

## Updating

```
git pull
docker compose -f deploy/docker-compose.yml up -d --build
just install-services
```

`--build` rebuilds the gateway image from the new source. `just install-services`
rebuilds the host binaries and restarts the units. Your `.env`, the CA under
`deploy/certs/`, and the Postgres volume are left alone.

## Uninstalling

```
just uninstall-services
docker compose -f deploy/docker-compose.yml down -v
```

`just uninstall-services` disables and removes the two systemd units.
`down -v` stops the containers and deletes their volumes, including the Postgres
database and Caddy's certificate cache; on a real domain the next `up` re-fetches
certificates from Let's Encrypt, so repeated teardowns can run into its rate
limits. Remove the repository checkout to finish.
