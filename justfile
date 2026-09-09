set shell := ["bash", "-uc"]

build:
    buf generate
    cd gateway/internal/store && sqlc generate
    cd gateway && go build ./...
    cargo build

test:
    cd gateway && go test ./...
    cargo test

lint:
    cd gateway && go vet ./... && test -z "$(gofmt -l .)"
    cargo fmt --check
    cargo clippy --all-targets -- -D warnings
    buf lint

proto:
    buf generate
    cargo build -p hearth-proto

deny:
    cargo deny check

migrate DIR="up":
    goose -dir gateway/internal/store/migrations postgres "$DATABASE_URL" {{DIR}}

certs:
    bash deploy/certs/gen-ca.sh

setup *ARGS:
    bash deploy/setup.sh {{ARGS}}

dev:
    docker compose -f deploy/docker-compose.yml up

web:
    npm --prefix web ci
    npm --prefix web run build

web-dev:
    npm --prefix web run dev

workspace-image tag="dev":
    docker build -t hearth-workspace-base:{{tag}} deploy/images/workspace-base

install-services:
    bash deploy/systemd/install.sh

uninstall-services:
    bash deploy/systemd/install.sh --uninstall

e2e-web:
    #!/usr/bin/env bash
    set -euo pipefail
    printf 'admin\nhearthdeploy\nlocalhost\n' | just setup --force
    docker compose -f deploy/docker-compose.yml up -d --build
    sudo ufw allow from 10.7.0.0/24 to any port 9091,9092 proto tcp 2>/dev/null || true
    just install-services
    cleanup() {
      podman rm -f $(podman ps -aq --filter name=hearth-ws-) 2>/dev/null || true
      podman volume rm $(podman volume ls -q --filter name=hearth-ws-) 2>/dev/null || true
      just uninstall-services
      docker compose -f deploy/docker-compose.yml down -v
    }
    trap cleanup EXIT
    npx --prefix web playwright install --with-deps chromium
    HEARTH_E2E_URL=https://localhost npm --prefix web run e2e
