#!/usr/bin/env bash
# Issues (or renews) the Tailscale HTTPS cert Caddy serves for the tailnet
# hostname. `tailscale cert` needs root to read Tailscale's daemon state;
# run this via sudo (the systemd unit that calls it does).
set -euo pipefail
cd "$(dirname "$0")"   # deploy/certs

domain=$(tailscale status --json | python3 -c 'import json,sys; print(json.load(sys.stdin)["Self"]["DNSName"].rstrip("."))')
mkdir -p tailscale
tailscale cert --cert-file "tailscale/${domain}.crt" --key-file "tailscale/${domain}.key" "$domain"
chmod 644 "tailscale/${domain}.crt"
chmod 640 "tailscale/${domain}.key"
echo "issued/renewed cert for $domain"

# Make sure the running Caddy actually picks up a renewed cert rather than
# relying on it noticing the file changed on its own.
docker exec hearth-caddy-1 caddy reload --config /etc/caddy/Caddyfile 2>/dev/null \
  && echo "reloaded caddy" \
  || echo "caddy not running, nothing to reload"
