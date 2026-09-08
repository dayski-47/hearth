#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")"
DAYS=3650

gen_key() { openssl genpkey -algorithm ED25519 -out "$1"; }

if [[ ! -f ca.pem ]]; then
  gen_key ca-key.pem
  openssl req -x509 -new -key ca-key.pem -days $DAYS -subj "/CN=hearth-local-ca" -out ca.pem
fi

# server/client leaf for a given name + SAN
leaf() {
  local name=$1 cn=$2 san=$3
  gen_key "${name}-key.pem"
  openssl req -new -key "${name}-key.pem" -subj "/CN=${cn}" -out "${name}.csr"
  openssl x509 -req -in "${name}.csr" -CA ca.pem -CAkey ca-key.pem -CAcreateserial \
    -days $DAYS -extfile <(printf "subjectAltName=%s\nextendedKeyUsage=serverAuth,clientAuth\n" "$san") \
    -out "${name}.pem"
  rm -f "${name}.csr"
}

leaf gateway hearth-gateway "DNS:localhost,DNS:hearth-gateway,IP:127.0.0.1"
leaf agent    hearth-agent   "DNS:localhost,DNS:hearth-agent,DNS:host.containers.internal,DNS:host.docker.internal,IP:127.0.0.1"
leaf workspace hearth-workspace "DNS:localhost,DNS:hearth-workspace,DNS:host.containers.internal,DNS:host.docker.internal,IP:127.0.0.1"
echo "certs written to $(pwd)"
