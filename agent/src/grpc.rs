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
    #[allow(dead_code)]
    host_id: String,
    #[allow(dead_code)]
    engine: Arc<E>,
}

// The `Agent` trait mirrors tonic 0.12's generated signatures, whose `Result`
// error variant (`tonic::Status`) trips clippy's `result_large_err` lint. The
// shape is not ours to change, so silence the lint for this impl only.
#[allow(clippy::result_large_err)]
#[tonic::async_trait]
impl<E: ContainerEngine> Agent for AgentSvc<E> {
    async fn create_workspace(
        &self,
        _r: Request<CreateWorkspaceRequest>,
    ) -> Result<Response<Workspace>, Status> {
        Err(Status::unimplemented("create_workspace: Plan 2"))
    }

    async fn start_workspace(
        &self,
        _r: Request<WorkspaceRef>,
    ) -> Result<Response<Workspace>, Status> {
        Err(Status::unimplemented("Plan 2"))
    }

    async fn stop_workspace(
        &self,
        _r: Request<WorkspaceRef>,
    ) -> Result<Response<Workspace>, Status> {
        Err(Status::unimplemented("Plan 2"))
    }

    async fn destroy_workspace(
        &self,
        _r: Request<WorkspaceRef>,
    ) -> Result<Response<DestroyResponse>, Status> {
        Err(Status::unimplemented("Plan 2"))
    }

    async fn get_workspace(
        &self,
        _r: Request<WorkspaceRef>,
    ) -> Result<Response<Workspace>, Status> {
        Err(Status::unimplemented("Plan 2"))
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
    let svc = AgentSvc {
        host_id: cfg.host_id.clone(),
        engine: Arc::new(engine),
    };
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
    use crate::engine::{ContainerEngine, ContainerRunState, ContainerSpec};

    struct FakeEngine;

    #[tonic::async_trait]
    impl ContainerEngine for FakeEngine {
        async fn ping(&self) -> anyhow::Result<()> {
            Ok(())
        }
        async fn create_container(&self, _s: ContainerSpec) -> anyhow::Result<String> {
            Ok("c1".into())
        }
        async fn start(&self, _id: &str) -> anyhow::Result<()> {
            Ok(())
        }
        async fn stop(&self, _id: &str) -> anyhow::Result<()> {
            Ok(())
        }
        async fn remove(&self, _id: &str) -> anyhow::Result<()> {
            Ok(())
        }
        async fn inspect_state(&self, _id: &str) -> anyhow::Result<ContainerRunState> {
            Ok(ContainerRunState::Running)
        }
    }

    fn svc() -> AgentSvc<FakeEngine> {
        AgentSvc {
            host_id: "h1".into(),
            engine: Arc::new(FakeEngine),
        }
    }

    #[tokio::test]
    async fn create_workspace_is_unimplemented() {
        let err = svc()
            .create_workspace(Request::new(CreateWorkspaceRequest {
                workspace_id: "w1".into(),
                image: "img".into(),
                limits: None,
                network: "none".into(),
                userns: "auto".into(),
            }))
            .await
            .unwrap_err();
        assert_eq!(err.code(), tonic::Code::Unimplemented);
    }

    #[tokio::test]
    async fn get_workspace_is_unimplemented() {
        let err = svc()
            .get_workspace(Request::new(WorkspaceRef {
                workspace_id: "w1".into(),
            }))
            .await
            .unwrap_err();
        assert_eq!(err.code(), tonic::Code::Unimplemented);
    }
}
