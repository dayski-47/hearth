#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/../.."   # repo root
REPO=$(pwd)
UNIT_DIR="${XDG_CONFIG_HOME:-$HOME/.config}/systemd/user"

if [[ "${1:-}" == "--uninstall" ]]; then
  systemctl --user disable --now hearth-agent hearth-workspace 2>/dev/null || true
  rm -f "$UNIT_DIR"/hearth-agent.service "$UNIT_DIR"/hearth-workspace.service
  systemctl --user daemon-reload
  echo "removed hearth-agent + hearth-workspace units"
  exit 0
fi

[[ -f .env ]] || { echo "run 'just setup' first" >&2; exit 1; }
command -v cargo >/dev/null || { echo "cargo (rust toolchain) required to build the data plane" >&2; exit 1; }

echo "-- building the data plane (release)"
cargo build --release -p hearth-agent -p hearth-workspace

mkdir -p "$UNIT_DIR"
for svc in hearth-agent hearth-workspace; do
  sed "s#@REPO@#${REPO}#g" "deploy/systemd/${svc}.service.tmpl" > "$UNIT_DIR/${svc}.service"
done
loginctl enable-linger "$USER" >/dev/null 2>&1 || true
systemctl --user daemon-reload
systemctl --user enable --now hearth-agent hearth-workspace
echo "-- units installed; check with: systemctl --user status hearth-agent hearth-workspace"
