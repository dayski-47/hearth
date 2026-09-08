#!/usr/bin/env bash
# Bootstrap a Hearth deployment: certs, session secret, admin credential, .env.
set -euo pipefail
cd "$(dirname "$0")/.." || exit 1   # repo root

FORCE=${1:-}
if [[ -f .env && "$FORCE" != "--force" ]]; then
  echo ".env already exists. Re-run with --force to overwrite." >&2
  exit 1
fi

command -v docker >/dev/null || { echo "docker is required" >&2; exit 1; }
docker compose version >/dev/null 2>&1 || { echo "docker compose v2 is required" >&2; exit 1; }
command -v podman >/dev/null || echo "warning: podman not found - the host data plane needs rootless podman" >&2

echo "== Hearth setup =="
read -rp "Admin username [admin]: " admin_user
admin_user=${admin_user:-admin}
read -rsp "Admin password: " admin_pw; echo
[[ -n "$admin_pw" ]] || { echo "password cannot be empty" >&2; exit 1; }
read -rp "Domain (or 'localhost' for a local trial) [localhost]: " domain
domain=${domain:-localhost}

echo "-- generating the mTLS CA"
bash deploy/certs/gen-ca.sh >/dev/null

echo "-- building the gateway image (for password hashing)"
docker compose -f deploy/docker-compose.yml build gateway >/dev/null

echo "-- hashing the admin password"
admin_hash=$(printf '%s\n' "$admin_pw" | docker run --rm -i hearth-gateway:local hash-password)
[[ $admin_hash == \$argon2id\$* ]] || { echo "password hashing failed" >&2; exit 1; }

secret=$(openssl rand -base64 36)
sock="${XDG_RUNTIME_DIR:-/run/user/$(id -u)}/podman/podman.sock"

# Caddy serves TLS for a real domain (Let's Encrypt) and for localhost (internal CA).
scheme=https

# Fill .env.example line by line. Most values (base64 secret, paths, URLs) hold
# no '$', so they are written bare - both compose env_file and systemd
# EnvironmentFile read them literally. The argon2id hash is the exception: it is
# '$'-delimited, and compose's env_file interpolation would eat every "$word".
# Single quotes stop that; compose and systemd both strip the surrounding quotes.
while IFS= read -r line || [[ -n "$line" ]]; do
  case $line in
    HEARTH_DOMAIN=*)              printf '%s\n' "HEARTH_DOMAIN=${domain}" ;;
    HEARTH_PUBLIC_URL=*)          printf '%s\n' "HEARTH_PUBLIC_URL=${scheme}://${domain}" ;;
    HEARTH_ALLOWED_ORIGIN=*)      printf '%s\n' "HEARTH_ALLOWED_ORIGIN=${scheme}://${domain}" ;;
    HEARTH_SESSION_SECRET=*)      printf '%s\n' "HEARTH_SESSION_SECRET=${secret}" ;;
    HEARTH_ADMIN_USER=*)          printf '%s\n' "HEARTH_ADMIN_USER=${admin_user}" ;;
    HEARTH_ADMIN_PASSWORD_HASH=*) printf "%s\n" "HEARTH_ADMIN_PASSWORD_HASH='${admin_hash}'" ;;
    HEARTH_PODMAN_SOCKET=*)       printf '%s\n' "HEARTH_PODMAN_SOCKET=${sock}" ;;
    *)                            printf '%s\n' "$line" ;;
  esac
done < .env.example > .env

chmod 600 .env
echo
echo "Wrote .env. Next:"
echo "  docker compose -f deploy/docker-compose.yml up -d"
echo "  just install-services"
