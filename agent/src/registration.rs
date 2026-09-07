use std::time::Duration;

use anyhow::Result;
use hearth_proto::hearth::v1::gateway_control_client::GatewayControlClient;
use hearth_proto::hearth::v1::{HeartbeatRequest, RegisterRequest};
use tokio::time::{interval, sleep, MissedTickBehavior};
use tonic::transport::Channel;

use crate::{config::Config, gateway_client};

/// Upper bound on the reconnect/re-register backoff.
const MAX_BACKOFF: Duration = Duration::from_secs(30);

pub async fn run(cfg: Config) -> Result<()> {
    // Block here until the gateway is reachable and has accepted our
    // registration. Agent-before-gateway is the normal systemd/compose boot
    // order, so a missing gateway must not kill this task.
    let (mut client, period) = connect_and_register(&cfg).await;

    let mut tick = interval(period);
    tick.set_missed_tick_behavior(MissedTickBehavior::Delay);
    loop {
        tick.tick().await;
        if let Err(e) = client
            .agent_heartbeat(HeartbeatRequest {
                host_id: cfg.host_id.clone(),
                running_workspaces: 0,
            })
            .await
        {
            tracing::warn!(error = format!("{e:#}"), "heartbeat failed; reconnecting");
            match gateway_client::connect(&cfg).await {
                Ok(c) => client = c,
                Err(e) => tracing::error!(error = format!("{e:#}"), "reconnect failed"),
            }
        }
    }
}

/// Connect to the gateway and register, retrying forever with exponential
/// backoff (1s, 2s, 4s, ... capped at 30s). The agent is a long-lived daemon;
/// there is no failure here worth giving up on.
async fn connect_and_register(cfg: &Config) -> (GatewayControlClient<Channel>, Duration) {
    let mut backoff = Duration::from_secs(1);
    loop {
        match try_connect_and_register(cfg).await {
            Ok((client, period)) => {
                tracing::info!(?period, "registered with gateway");
                return (client, period);
            }
            Err(e) => {
                tracing::warn!(
                    error = format!("{e:#}"),
                    backoff_secs = backoff.as_secs(),
                    "gateway registration attempt failed; retrying"
                );
                sleep(backoff).await;
                backoff = (backoff * 2).min(MAX_BACKOFF);
            }
        }
    }
}

async fn try_connect_and_register(
    cfg: &Config,
) -> Result<(GatewayControlClient<Channel>, Duration)> {
    let mut client = gateway_client::connect(cfg).await?;
    let resp = client
        .register_agent(RegisterRequest {
            host_id: cfg.host_id.clone(),
            advertise_addr: cfg.advertise_addr.clone(),
            capacity: None,
            workspace_addr: cfg.workspace_addr.clone(),
        })
        .await?
        .into_inner();
    let period = Duration::from_secs(resp.heartbeat_interval_seconds.max(1) as u64);
    Ok((client, period))
}
