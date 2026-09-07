use anyhow::Result;
use hearth_workspace::{config, engine, grpc};

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
    let exec = engine::PodmanExec::connect(cfg.podman_socket.as_deref())?;
    exec.ping().await?;
    tracing::info!("podman reachable");

    let shutdown = async {
        let _ = tokio::signal::ctrl_c().await;
    };
    grpc::serve(cfg, exec, shutdown).await
}
