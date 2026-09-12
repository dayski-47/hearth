//! Real Podman round trip for the workspace lifecycle. Gated: HEARTH_PODMAN_IT=1.
//!
//! Requires a working podman socket and `docker.io/library/busybox:stable`
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

fn request(id: &str, network: &str) -> CreateWorkspaceRequest {
    CreateWorkspaceRequest {
        workspace_id: id.to_string(),
        image: "docker.io/library/busybox:stable".into(),
        limits: Some(ResourceLimits {
            cpu_millis: 1000,
            memory_bytes: 256 << 20,
            pids: 64,
            disk_bytes: 0,
        }),
        network: network.to_string(),
        userns: "keep-id".into(),
        host_mount_path: String::new(),
    }
}

#[tokio::test]
async fn create_get_stop_destroy_busybox() {
    let Some(engine) = podman() else {
        eprintln!("skipped: set HEARTH_PODMAN_IT=1 (and HEARTH_PODMAN_SOCKET) to run");
        return;
    };
    let engine = Arc::new(engine);
    let lc = lifecycle::new(engine.clone(), "it-host".into(), Vec::new());
    let id = format!("it-{}", std::process::id());

    let ws = lc.create(request(&id, "none")).await;
    assert_eq!(ws.state, WorkspaceState::Running as i32, "{}", ws.message);

    // The section 6 model has to survive the trip through Podman, not just be
    // populated on the struct the unit test checks.
    let info = engine
        .docker()
        .inspect_container(&format!("hearth-ws-{id}"), None)
        .await
        .expect("inspect container");
    let hc = info.host_config.expect("host config");

    assert_eq!(
        hc.readonly_rootfs,
        Some(true),
        "root filesystem is writable"
    );
    assert_eq!(hc.pids_limit, Some(64));
    assert_eq!(hc.memory, Some(256 << 20));
    assert_eq!(hc.memory_swap, hc.memory, "swap not pinned to memory");
    assert_eq!(hc.nano_cpus, Some(1_000_000_000));
    // Podman answers a `cap_drop: ["ALL"]` create by echoing back the expanded
    // default set, so accept either shape: what has to hold is that nothing was
    // added back and every privileged default is gone.
    assert!(
        hc.cap_add.as_ref().is_none_or(|c| c.is_empty()),
        "cap_add = {:?}, want nothing added back",
        hc.cap_add
    );
    let dropped = hc.cap_drop.clone().unwrap_or_default();
    let dropped_all = dropped.iter().any(|c| c.eq_ignore_ascii_case("ALL"));
    for want in [
        "CHOWN",
        "DAC_OVERRIDE",
        "FOWNER",
        "MKNOD",
        "NET_RAW",
        "SETGID",
        "SETPCAP",
        "SETUID",
        "SYS_CHROOT",
    ] {
        assert!(
            dropped_all || dropped.iter().any(|c| c.eq_ignore_ascii_case(want)),
            "cap_drop = {dropped:?}, want ALL (or at least {want}) dropped"
        );
    }
    assert!(
        hc.security_opt
            .as_ref()
            .is_some_and(|o| o.iter().any(|x| x.contains("no-new-privileges"))),
        "security_opt = {:?}, want no-new-privileges",
        hc.security_opt
    );
    // No live ulimit assertion: Podman 4.3.1's Docker-compat create endpoint
    // silently discards HostConfig.Ulimits (its own --ulimit flag honours it,
    // the API does not), so the nofile limit we ask for never reaches the
    // container and inspect always answers Ulimits: []. The mapping itself is
    // covered by host_config_applies_the_security_model in engine.rs; this
    // assertion becomes meaningful once the host runs a Podman that honours it.

    // The container engine socket must never be reachable from a workspace.
    let sources = hc
        .mounts
        .iter()
        .flatten()
        .filter_map(|m| m.source.clone())
        .chain(hc.binds.iter().flatten().cloned());
    for src in sources {
        assert!(
            !src.contains("podman.sock") && !src.contains("docker.sock"),
            "engine socket mounted into the workspace: {src}"
        );
    }

    assert_eq!(lc.get(&id).await.state, WorkspaceState::Running as i32);
    assert_eq!(lc.stop(&id).await.state, WorkspaceState::Stopped as i32);
    assert_eq!(lc.get(&id).await.state, WorkspaceState::Stopped as i32);

    lc.destroy(&id).await.expect("destroy");
    assert_eq!(lc.get(&id).await.state, WorkspaceState::Error as i32); // container gone
}

async fn exec_output(docker: &bollard::Docker, container: &str, cmd: &[&str]) -> String {
    use bollard::exec::{CreateExecOptions, StartExecResults};
    use futures_util::StreamExt;
    let exec = docker
        .create_exec(
            container,
            CreateExecOptions {
                attach_stdout: Some(true),
                attach_stderr: Some(true),
                cmd: Some(cmd.iter().map(|s| s.to_string()).collect()),
                ..Default::default()
            },
        )
        .await
        .expect("create exec");
    let mut out = Vec::new();
    if let StartExecResults::Attached { mut output, .. } =
        docker.start_exec(&exec.id, None).await.expect("start exec")
    {
        while let Some(Ok(chunk)) = output.next().await {
            out.extend_from_slice(&chunk.into_bytes());
        }
    }
    String::from_utf8_lossy(&out).to_string()
}

#[tokio::test]
async fn create_with_a_host_mount_path_bind_mounts_the_real_directory() {
    let Some(engine) = podman() else {
        eprintln!("skipped: set HEARTH_PODMAN_IT=1 (and HEARTH_PODMAN_SOCKET) to run");
        return;
    };
    let engine = Arc::new(engine);
    let tmp = std::env::temp_dir().join(format!("hearth-host-mount-it-{}", std::process::id()));
    std::fs::create_dir_all(&tmp).expect("create temp dir");
    std::fs::write(tmp.join("marker.txt"), b"from-the-real-host\n").expect("write marker");

    let host_path = tmp.to_str().expect("utf8 path").to_string();
    let lc = lifecycle::new(engine.clone(), "it-host".into(), vec![host_path.clone()]);
    let id = format!("it-bind-{}", std::process::id());

    let mut req = request(&id, "none");
    req.host_mount_path = host_path.clone();
    let ws = lc.create(req).await;
    assert_eq!(ws.state, WorkspaceState::Running as i32, "{}", ws.message);

    let container = format!("hearth-ws-{id}");
    let content = exec_output(
        engine.docker(),
        &container,
        &["cat", "/workspace/marker.txt"],
    )
    .await;
    assert_eq!(content, "from-the-real-host\n");

    // Vice versa: something the container writes lands on the real host.
    exec_output(
        engine.docker(),
        &container,
        &[
            "sh",
            "-c",
            "echo from-the-container > /workspace/from-container.txt",
        ],
    )
    .await;
    let seen = std::fs::read_to_string(tmp.join("from-container.txt")).expect("read back on host");
    assert_eq!(seen, "from-the-container\n");

    lc.destroy(&id).await.expect("destroy");
    assert!(
        tmp.join("marker.txt").exists(),
        "destroy must not touch the host directory"
    );
    assert!(tmp.join("from-container.txt").exists());
    std::fs::remove_dir_all(&tmp).ok();
}

/// The production default is `network=egress`, which means every create calls
/// `ensure_network` on an already-existing network. Two back-to-back creates is
/// the smallest thing that catches a non-idempotent implementation.
#[tokio::test]
async fn two_egress_workspaces_can_be_created() {
    let Some(engine) = podman() else {
        eprintln!("skipped: set HEARTH_PODMAN_IT=1 (and HEARTH_PODMAN_SOCKET) to run");
        return;
    };
    let lc = lifecycle::new(Arc::new(engine), "it-host".into(), Vec::new());
    let base = format!("egress-{}", std::process::id());
    let (first, second) = (format!("{base}-a"), format!("{base}-b"));

    let a = lc.create(request(&first, "egress")).await;
    let b = lc.create(request(&second, "egress")).await;

    // Tear both down before asserting, so a failure cannot leak containers.
    let _ = lc.destroy(&first).await;
    let _ = lc.destroy(&second).await;

    assert_eq!(
        a.state,
        WorkspaceState::Running as i32,
        "first: {}",
        a.message
    );
    assert_eq!(
        b.state,
        WorkspaceState::Running as i32,
        "second create on an existing egress network: {}",
        b.message
    );
}
