//! Maps the Agent gRPC surface onto container-engine operations.

use std::sync::Arc;

use anyhow::Result;
use hearth_common::workspace_container_name;
use hearth_proto::hearth::v1::{CreateWorkspaceRequest, Workspace, WorkspaceState};

use crate::engine::{
    ContainerEngine, ContainerRunState, MountSource, NetworkMode, WorkspaceContainerSpec,
    EGRESS_NETWORK,
};

pub struct Lifecycle<E: ContainerEngine> {
    engine: Arc<E>,
    host_id: String,
    host_mounts: Vec<String>,
}

pub fn new<E: ContainerEngine>(
    engine: Arc<E>,
    host_id: String,
    host_mounts: Vec<String>,
) -> Lifecycle<E> {
    Lifecycle {
        engine,
        host_id,
        host_mounts,
    }
}

impl<E: ContainerEngine> Lifecycle<E> {
    fn ok(&self, id: &str, container_id: String, state: WorkspaceState) -> Workspace {
        Workspace {
            workspace_id: id.to_string(),
            container_id,
            state: state as i32,
            host_id: self.host_id.clone(),
            message: String::new(),
        }
    }
    fn errored(&self, id: &str, e: anyhow::Error) -> Workspace {
        tracing::error!(workspace_id = %id, error = format!("{e:#}"), "workspace operation failed");
        Workspace {
            workspace_id: id.to_string(),
            container_id: String::new(),
            state: WorkspaceState::Error as i32,
            host_id: self.host_id.clone(),
            message: format!("{e:#}"),
        }
    }

    pub async fn create(&self, req: CreateWorkspaceRequest) -> Workspace {
        let id = req.workspace_id.clone();
        match self.create_inner(&req).await {
            Ok(cid) => self.ok(&id, cid, WorkspaceState::Running),
            Err(e) => self.errored(&id, e),
        }
    }

    async fn create_inner(&self, req: &CreateWorkspaceRequest) -> Result<String> {
        let id = &req.workspace_id;
        let network = match req.network.as_str() {
            "none" => NetworkMode::None,
            _ => NetworkMode::Egress,
        };
        let limits = req.limits.unwrap_or_default();
        let mount = if req.host_mount_path.is_empty() {
            MountSource::Volume(workspace_container_name(id))
        } else if self.host_mounts.iter().any(|p| p == &req.host_mount_path) {
            MountSource::Bind(req.host_mount_path.clone())
        } else {
            anyhow::bail!(
                "host_mount_path {:?} is not in the configured allowlist",
                req.host_mount_path
            );
        };
        let is_bind = matches!(&mount, MountSource::Bind(_));
        let spec = WorkspaceContainerSpec {
            name: workspace_container_name(id),
            image: req.image.clone(),
            mount,
            network: network.clone(),
            userns: req.userns.clone(),
            cpu_millis: limits.cpu_millis,
            memory_bytes: limits.memory_bytes,
            pids: limits.pids,
        };
        self.engine.ensure_image(&req.image).await?;
        if matches!(network, NetworkMode::Egress) {
            self.engine.ensure_network(EGRESS_NETWORK).await?;
        }
        if !is_bind {
            self.engine
                .create_volume(&workspace_container_name(id))
                .await?;
        }
        let cid = self.engine.create_container(spec).await?;
        self.engine.start(&workspace_container_name(id)).await?;
        tracing::info!(workspace_id = %id, container_id = %cid, "workspace running");
        Ok(cid)
    }

    pub async fn start(&self, id: &str) -> Workspace {
        match self.engine.start(&workspace_container_name(id)).await {
            Ok(()) => self.get(id).await,
            Err(e) => self.errored(id, e),
        }
    }

    pub async fn stop(&self, id: &str) -> Workspace {
        match self.engine.stop(&workspace_container_name(id)).await {
            Ok(()) => {
                tracing::info!(workspace_id = %id, "workspace stopped");
                self.ok(id, String::new(), WorkspaceState::Stopped)
            }
            Err(e) => self.errored(id, e),
        }
    }

    pub async fn destroy(&self, id: &str) -> Result<()> {
        self.engine.remove(&workspace_container_name(id)).await?;
        self.engine
            .remove_volume(&workspace_container_name(id))
            .await?;
        tracing::info!(workspace_id = %id, "workspace destroyed");
        Ok(())
    }

    pub async fn get(&self, id: &str) -> Workspace {
        match self
            .engine
            .inspect_state(&workspace_container_name(id))
            .await
        {
            Ok(ContainerRunState::Running) => self.ok(id, String::new(), WorkspaceState::Running),
            Ok(ContainerRunState::Stopped) => self.ok(id, String::new(), WorkspaceState::Stopped),
            Ok(ContainerRunState::Missing) => Workspace {
                workspace_id: id.to_string(),
                container_id: String::new(),
                state: WorkspaceState::Error as i32,
                host_id: self.host_id.clone(),
                message: "container not found on host".to_string(),
            },
            Err(e) => self.errored(id, e),
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::engine::{ContainerEngine, ContainerRunState, WorkspaceContainerSpec};
    use std::sync::Mutex;

    #[derive(Default)]
    struct FakeEngine {
        calls: Mutex<Vec<String>>,
        fail: Option<&'static str>,
        state: Mutex<ContainerRunState>,
    }
    impl FakeEngine {
        fn new(state: ContainerRunState) -> Self {
            Self {
                state: Mutex::new(state),
                ..Default::default()
            }
        }
        fn record(&self, s: &str) -> anyhow::Result<()> {
            self.calls.lock().unwrap().push(s.to_string());
            if self.fail == Some(s) {
                anyhow::bail!("boom in {s}")
            } else {
                Ok(())
            }
        }
    }
    #[tonic::async_trait]
    impl ContainerEngine for FakeEngine {
        async fn ping(&self) -> anyhow::Result<()> {
            Ok(())
        }
        async fn ensure_image(&self, _: &str) -> anyhow::Result<()> {
            self.record("ensure_image")
        }
        async fn ensure_network(&self, _: &str) -> anyhow::Result<()> {
            self.record("ensure_network")
        }
        async fn create_volume(&self, _: &str) -> anyhow::Result<()> {
            self.record("create_volume")
        }
        async fn remove_volume(&self, _: &str) -> anyhow::Result<()> {
            self.record("remove_volume")
        }
        async fn create_container(&self, _: WorkspaceContainerSpec) -> anyhow::Result<String> {
            self.record("create_container")?;
            Ok("cid-1".into())
        }
        async fn start(&self, _: &str) -> anyhow::Result<()> {
            self.record("start")
        }
        async fn stop(&self, _: &str) -> anyhow::Result<()> {
            self.record("stop")
        }
        async fn remove(&self, _: &str) -> anyhow::Result<()> {
            self.record("remove")
        }
        async fn inspect_state(&self, _: &str) -> anyhow::Result<ContainerRunState> {
            Ok(*self.state.lock().unwrap())
        }
    }

    fn req(id: &str) -> CreateWorkspaceRequest {
        CreateWorkspaceRequest {
            workspace_id: id.into(),
            image: "busybox:stable".into(),
            limits: Some(hearth_proto::hearth::v1::ResourceLimits {
                cpu_millis: 1000,
                memory_bytes: 1 << 30,
                pids: 128,
                disk_bytes: 0,
            }),
            network: "egress".into(),
            userns: "keep-id".into(),
            host_mount_path: String::new(),
        }
    }

    #[tokio::test]
    async fn create_pulls_makes_volume_and_starts_running() {
        let eng = Arc::new(FakeEngine::new(ContainerRunState::Running));
        let lc = new(eng.clone(), "h1".into(), Vec::new());
        let ws = lc.create(req("w1")).await;
        assert_eq!(ws.state, WorkspaceState::Running as i32);
        assert_eq!(ws.container_id, "cid-1");
        assert_eq!(ws.host_id, "h1");
        let calls = eng.calls.lock().unwrap().clone();
        assert_eq!(
            calls,
            [
                "ensure_image",
                "ensure_network",
                "create_volume",
                "create_container",
                "start"
            ]
        );
    }

    #[tokio::test]
    async fn create_failure_yields_error_state_with_message() {
        let eng = Arc::new(FakeEngine {
            fail: Some("create_container"),
            ..FakeEngine::new(ContainerRunState::Missing)
        });
        let lc = new(eng, "h1".into(), Vec::new());
        let ws = lc.create(req("w2")).await;
        assert_eq!(ws.state, WorkspaceState::Error as i32);
        assert!(ws.message.contains("create_container"));
    }

    #[tokio::test]
    async fn get_maps_run_state() {
        for (rs, want) in [
            (ContainerRunState::Running, WorkspaceState::Running),
            (ContainerRunState::Stopped, WorkspaceState::Stopped),
            (ContainerRunState::Missing, WorkspaceState::Error),
        ] {
            let lc = new(Arc::new(FakeEngine::new(rs)), "h1".into(), Vec::new());
            let ws = lc.get("w1").await;
            assert_eq!(ws.state, want as i32, "{rs:?}");
        }
    }

    #[tokio::test]
    async fn destroy_removes_container_then_volume() {
        let eng = Arc::new(FakeEngine::new(ContainerRunState::Stopped));
        let lc = new(eng.clone(), "h1".into(), Vec::new());
        lc.destroy("w1").await.unwrap();
        let calls = eng.calls.lock().unwrap().clone();
        assert_eq!(calls, ["remove", "remove_volume"]);
    }

    #[tokio::test]
    async fn create_rejects_a_host_mount_path_outside_the_allowlist() {
        let eng = Arc::new(FakeEngine::new(ContainerRunState::Running));
        let lc = new(
            eng.clone(),
            "h1".into(),
            vec!["/home/dayson/homelab".into()],
        );
        let mut r = req("w1");
        r.host_mount_path = "/etc".into();

        let ws = lc.create(r).await;

        assert_eq!(ws.state, WorkspaceState::Error as i32);
        assert!(
            eng.calls.lock().unwrap().is_empty(),
            "no engine call should happen before allowlist validation"
        );
    }

    #[tokio::test]
    async fn create_with_an_allowed_host_mount_path_skips_create_volume() {
        let eng = Arc::new(FakeEngine::new(ContainerRunState::Running));
        let lc = new(
            eng.clone(),
            "h1".into(),
            vec!["/home/dayson/homelab".into()],
        );
        let mut r = req("w1");
        r.host_mount_path = "/home/dayson/homelab".into();

        let ws = lc.create(r).await;

        assert_eq!(ws.state, WorkspaceState::Running as i32, "{}", ws.message);
        let calls = eng.calls.lock().unwrap().clone();
        assert!(
            !calls.contains(&"create_volume".to_string()),
            "calls = {calls:?}, want no create_volume"
        );
        assert!(calls.contains(&"create_container".to_string()));
    }
}
