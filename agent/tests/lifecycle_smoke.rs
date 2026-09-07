//! Real Podman round trip for the workspace lifecycle. Gated: HEARTH_PODMAN_IT=1.
//!
//! Requires a working rootless podman socket and `docker.io/library/busybox:stable`
//! reachable. Run with:
//!   HEARTH_PODMAN_IT=1 HEARTH_PODMAN_SOCKET=/run/user/1001/podman/podman.sock \
//!     cargo test -p hearth-agent --test lifecycle_smoke -- --nocapture
//!
//! Set `HEARTH_PODMAN_SOCKET` to target a specific socket; the host also runs
//! Docker, so the explicit socket is required.

use std::sync::Arc;

use hearth_agent::engine::PodmanEngine;
use hearth_agent::lifecycle;
use hearth_proto::hearth::v1::{CreateWorkspaceRequest, ResourceLimits, WorkspaceState};

fn podman() -> Option<PodmanEngine> {
    if std::env::var("HEARTH_PODMAN_IT").as_deref() != Ok("1") {
        return None;
    }
    let socket = std::env::var("HEARTH_PODMAN_SOCKET")
        .ok()
        .filter(|s| !s.is_empty());
    PodmanEngine::connect(socket.as_deref()).ok()
}

#[tokio::test]
async fn create_get_stop_destroy_busybox() {
    let Some(engine) = podman() else {
        eprintln!("skipped: set HEARTH_PODMAN_IT=1 (and HEARTH_PODMAN_SOCKET) to run");
        return;
    };
    let lc = lifecycle::new(Arc::new(engine), "it-host".into());
    let id = format!("it-{}", std::process::id());

    let ws = lc
        .create(CreateWorkspaceRequest {
            workspace_id: id.clone(),
            image: "docker.io/library/busybox:stable".into(),
            limits: Some(ResourceLimits {
                cpu_millis: 1000,
                memory_bytes: 256 << 20,
                pids: 64,
                disk_bytes: 0,
            }),
            network: "none".into(),
            userns: "keep-id".into(),
        })
        .await;
    assert_eq!(ws.state, WorkspaceState::Running as i32, "{}", ws.message);

    assert_eq!(lc.get(&id).await.state, WorkspaceState::Running as i32);
    assert_eq!(lc.stop(&id).await.state, WorkspaceState::Stopped as i32);
    assert_eq!(lc.get(&id).await.state, WorkspaceState::Stopped as i32);

    lc.destroy(&id).await.expect("destroy");
    assert_eq!(lc.get(&id).await.state, WorkspaceState::Error as i32); // container gone
}
