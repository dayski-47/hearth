use anyhow::{Context, Result};
use tonic::transport::{Certificate, Identity, ServerTlsConfig};

use crate::config::TlsPaths;

// NOTE: tonic 0.12's `ServerTlsConfig` exposes no minimum-TLS-version knob
// (only `identity` / `client_ca_root` / `client_auth_optional`), so the agent's
// inbound listener cannot pin TLS 1.3 the way the gateway does in both
// directions. This asymmetry is acceptable within a single trust domain: every
// peer is minted from the same private CA and the gateway->agent channel is
// already 1.3-pinned on the client side. Revisit if tonic gains the setter.
pub fn server_config(paths: &TlsPaths) -> Result<ServerTlsConfig> {
    let cert = std::fs::read(&paths.cert).context("read agent tls cert")?;
    let key = std::fs::read(&paths.key).context("read agent tls key")?;
    let ca = std::fs::read(&paths.ca).context("read ca")?;
    Ok(ServerTlsConfig::new()
        .identity(Identity::from_pem(cert, key))
        .client_ca_root(Certificate::from_pem(ca)))
}
