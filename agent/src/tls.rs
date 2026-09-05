use anyhow::{Context, Result};
use tonic::transport::{Certificate, Identity, ServerTlsConfig};

use crate::config::TlsPaths;

pub fn server_config(paths: &TlsPaths) -> Result<ServerTlsConfig> {
    let cert = std::fs::read(&paths.cert).context("read agent tls cert")?;
    let key = std::fs::read(&paths.key).context("read agent tls key")?;
    let ca = std::fs::read(&paths.ca).context("read ca")?;
    Ok(ServerTlsConfig::new()
        .identity(Identity::from_pem(cert, key))
        .client_ca_root(Certificate::from_pem(ca)))
}
