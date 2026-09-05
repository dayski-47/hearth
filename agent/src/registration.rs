use std::time::Duration;

use anyhow::Result;
use hearth_proto::hearth::v1::{HeartbeatRequest, RegisterRequest};
use tokio::time::{interval, MissedTickBehavior};

use crate::{config::Config, gateway_client};

pub async fn run(cfg: Config) -> Result<()> {
    let mut client = gateway_client::connect(&cfg).await?;
    let resp = client
        .register_agent(RegisterRequest {
            host_id: cfg.host_id.clone(),
            advertise_addr: cfg.advertise_addr.clone(),
            capacity: None,
        })
        .await?
        .into_inner();
    let period = Duration::from_secs(resp.heartbeat_interval_seconds.max(1) as u64);
    tracing::info!(?period, "registered with gateway");

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
            tracing::warn!(error = %e, "heartbeat failed; reconnecting");
            match gateway_client::connect(&cfg).await {
                Ok(c) => client = c,
                Err(e) => tracing::error!(error = %e, "reconnect failed"),
            }
        }
    }
}
