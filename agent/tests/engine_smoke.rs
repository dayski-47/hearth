//! Requires a working podman socket and `docker.io/library/busybox:latest`
//! pulled locally. Run with:
//!   HEARTH_PODMAN_IT=1 cargo test -p hearth-agent --test engine_smoke -- --nocapture
//!
//! Set `HEARTH_PODMAN_SOCKET` to target a specific socket (e.g.
//! `/run/user/1001/podman/podman.sock`); otherwise the bollard socket defaults
//! are used.

use hearth_agent::engine::{
    ContainerEngine, ContainerRunState, MountSource, NetworkMode, PodmanEngine,
    WorkspaceContainerSpec,
};

#[tokio::test]
async fn create_start_inspect_remove_busybox() {
    if std::env::var("HEARTH_PODMAN_IT").is_err() {
        eprintln!("skipping: set HEARTH_PODMAN_IT=1 to run");
        return;
    }

    let socket = std::env::var("HEARTH_PODMAN_SOCKET")
        .ok()
        .filter(|s| !s.is_empty());
    let engine = PodmanEngine::connect(socket.as_deref()).expect("connect to podman");
    engine.ping().await.expect("podman ping");

    let name = format!("hearth-smoke-{}", std::process::id());
    let volume = format!("hearth-smoke-vol-{}", std::process::id());

    engine.create_volume(&volume).await.expect("create volume");

    let spec = WorkspaceContainerSpec {
        name: name.clone(),
        image: "docker.io/library/busybox:latest".into(),
        mount: MountSource::Volume(volume.clone()),
        network: NetworkMode::None,
        userns: "auto".into(),
        cpu_millis: 1000,
        memory_bytes: 1 << 30,
        pids: 128,
    };

    let id = engine
        .create_container(spec)
        .await
        .expect("create container");
    eprintln!("created container {id}");

    engine.start(&id).await.expect("start container");
    let state = engine
        .inspect_state(&id)
        .await
        .expect("inspect after start");
    eprintln!("state after start: {state:?}");
    assert_eq!(state, ContainerRunState::Running);

    engine.remove(&id).await.expect("remove container");
    let state = engine
        .inspect_state(&id)
        .await
        .expect("inspect after remove");
    eprintln!("state after remove: {state:?}");
    assert_eq!(state, ContainerRunState::Missing);

    engine.remove_volume(&volume).await.expect("remove volume");
    // Removing an already-gone volume is a no-op success.
    engine
        .remove_volume(&volume)
        .await
        .expect("remove missing volume is success");
}
