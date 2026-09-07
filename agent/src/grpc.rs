use std::future::Future;
use std::sync::Arc;

use anyhow::{Context, Result};
use hearth_proto::hearth::v1::{
    agent_server::{Agent, AgentServer},
    CreateWorkspaceRequest, DestroyResponse, Workspace, WorkspaceRef,
};
use tonic::{transport::Server, Request, Response, Status};

use crate::{config::Config, engine::ContainerEngine, tls};

pub struct AgentSvc<E: ContainerEngine> {
    lifecycle: Arc<crate::lifecycle::Lifecycle<E>>,
}

impl<E: ContainerEngine> AgentSvc<E> {
    pub fn new(engine: Arc<E>, host_id: String) -> Self {
        Self {
            lifecycle: Arc::new(crate::lifecycle::new(engine, host_id)),
        }
    }
}

// The `Agent` trait mirrors tonic 0.12's generated signatures, whose `Result`
// error variant (`tonic::Status`) trips clippy's `result_large_err` lint. The
// shape is not ours to change, so silence the lint for this impl only.
#[allow(clippy::result_large_err)]
#[tonic::async_trait]
impl<E: ContainerEngine> Agent for AgentSvc<E> {
    async fn create_workspace(
        &self,
        r: Request<CreateWorkspaceRequest>,
    ) -> Result<Response<Workspace>, Status> {
        Ok(Response::new(self.lifecycle.create(r.into_inner()).await))
    }

    async fn start_workspace(
        &self,
        r: Request<WorkspaceRef>,
    ) -> Result<Response<Workspace>, Status> {
        Ok(Response::new(
            self.lifecycle.start(&r.into_inner().workspace_id).await,
        ))
    }

    async fn stop_workspace(
        &self,
        r: Request<WorkspaceRef>,
    ) -> Result<Response<Workspace>, Status> {
        Ok(Response::new(
            self.lifecycle.stop(&r.into_inner().workspace_id).await,
        ))
    }

    async fn destroy_workspace(
        &self,
        r: Request<WorkspaceRef>,
    ) -> Result<Response<DestroyResponse>, Status> {
        match self.lifecycle.destroy(&r.into_inner().workspace_id).await {
            Ok(()) => Ok(Response::new(DestroyResponse {})),
            Err(e) => Err(Status::internal(format!("{e:#}"))),
        }
    }

    async fn get_workspace(&self, r: Request<WorkspaceRef>) -> Result<Response<Workspace>, Status> {
        Ok(Response::new(
            self.lifecycle.get(&r.into_inner().workspace_id).await,
        ))
    }
}

pub async fn serve<E, S>(cfg: Config, engine: E, shutdown: S) -> Result<()>
where
    E: ContainerEngine,
    S: Future<Output = ()> + Send + 'static,
{
    let addr = cfg
        .grpc_listen_addr
        .parse()
        .context("parse grpc listen addr")?;
    let svc = AgentSvc::new(Arc::new(engine), cfg.host_id.clone());
    let tls_config = tls::server_config(&cfg.tls)?;
    tracing::info!(%addr, "agent grpc listening");
    Server::builder()
        .tls_config(tls_config)?
        .add_service(AgentServer::new(svc))
        .serve_with_shutdown(addr, shutdown)
        .await
        .context("grpc serve")?;
    Ok(())
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::engine::{ContainerEngine, ContainerRunState, WorkspaceContainerSpec};
    use hearth_proto::hearth::v1::WorkspaceState;
    use std::sync::Mutex;

    // A local copy of `lifecycle.rs`'s test engine: the two `#[cfg(test)]`
    // modules compile into separate `--lib` builds, so there is no collision.
    #[derive(Default)]
    struct FakeEngine {
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

    #[tokio::test]
    async fn create_workspace_returns_running() {
        let eng = Arc::new(FakeEngine::new(ContainerRunState::Running));
        let svc = AgentSvc::new(eng, "h1".into());
        let ws = svc
            .create_workspace(Request::new(CreateWorkspaceRequest {
                workspace_id: "w1".into(),
                image: "busybox:stable".into(),
                limits: None,
                network: "none".into(),
                userns: "auto".into(),
            }))
            .await
            .unwrap()
            .into_inner();
        assert_eq!(ws.state, WorkspaceState::Running as i32);
    }

    #[tokio::test]
    async fn destroy_workspace_maps_engine_error_to_internal() {
        let eng = Arc::new(FakeEngine {
            fail: Some("remove"),
            ..FakeEngine::new(ContainerRunState::Stopped)
        });
        let svc = AgentSvc::new(eng, "h1".into());
        let err = svc
            .destroy_workspace(Request::new(WorkspaceRef {
                workspace_id: "w1".into(),
            }))
            .await
            .unwrap_err();
        assert_eq!(err.code(), tonic::Code::Internal);
    }
}
