use anyhow::{Context, Result};
use hearth_proto::hearth::v1::gateway_control_client::GatewayControlClient;
use tonic::transport::{Certificate, Channel, ClientTlsConfig, Identity};

use crate::config::{Config, TlsPaths};

fn client_tls(paths: &TlsPaths) -> Result<ClientTlsConfig> {
    let ca = std::fs::read(&paths.ca).context("read ca")?;
    let cert = std::fs::read(&paths.cert).context("read agent cert")?;
    let key = std::fs::read(&paths.key).context("read agent key")?;
    Ok(ClientTlsConfig::new()
        .ca_certificate(Certificate::from_pem(ca))
        .identity(Identity::from_pem(cert, key))
        .domain_name("hearth-gateway"))
}

pub async fn connect(cfg: &Config) -> Result<GatewayControlClient<Channel>> {
    let tls = client_tls(&cfg.tls)?;
    let channel = Channel::from_shared(cfg.gateway_grpc_addr.clone())
        .context("parse gateway grpc addr")?
        .tls_config(tls)?
        .connect()
        .await
        .context("dial gateway")?;
    Ok(GatewayControlClient::new(channel))
}
