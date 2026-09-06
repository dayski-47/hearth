use anyhow::Result;
use hearth_agent::engine::ContainerEngine;
use hearth_agent::{config, engine, grpc};

#[tokio::main]
async fn main() -> Result<()> {
    tracing_subscriber::fmt()
        .json()
        .with_env_filter(
            tracing_subscriber::EnvFilter::try_from_default_env()
                .unwrap_or_else(|_| tracing_subscriber::EnvFilter::new("info")),
        )
        .init();

    let cfg = config::load()?;
    let podman = engine::PodmanEngine::connect(cfg.podman_socket.as_deref())?;
    podman.ping().await?;
    tracing::info!(host_id = %cfg.host_id, "podman reachable");

    let reg_cfg = cfg.clone();
    tokio::spawn(async move {
        if let Err(e) = hearth_agent::registration::run(reg_cfg).await {
            tracing::error!(error = format!("{e:#}"), "registration loop exited");
        }
    });

    let shutdown = async {
        let _ = tokio::signal::ctrl_c().await;
    };
    grpc::serve(cfg, podman, shutdown).await
}
