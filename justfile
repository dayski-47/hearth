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

migrate DIR="up":
    goose -dir gateway/internal/store/migrations postgres "$DATABASE_URL" {{DIR}}

certs:
    bash deploy/certs/gen-ca.sh

setup *ARGS:
    bash deploy/setup.sh {{ARGS}}

dev:
    docker compose -f deploy/docker-compose.yml up

install-services:
    bash deploy/systemd/install.sh

uninstall-services:
    bash deploy/systemd/install.sh --uninstall
