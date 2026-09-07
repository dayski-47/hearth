//! The inbound gRPC surface the gateway dials for terminal traffic.

use std::future::Future;
use std::sync::Arc;

use anyhow::{Context, Result};
use hearth_proto::hearth::v1::{
    workspace_io_server::{WorkspaceIo, WorkspaceIoServer},
    CreateNodeRequest, DeleteNodeRequest, DeleteNodeResponse, FileChunk, FileEvent, ListDirRequest,
    ListDirResponse, Node, ReadFileRequest, RenameNodeRequest, TerminalClientFrame, WatchRequest,
    WriteFileFrame, WriteFileResponse,
};
use tokio_stream::wrappers::ReceiverStream;
use tonic::{transport::Server, Request, Response, Status};

use crate::{config::Config, engine::PodmanExec, terminal, tls};

pub struct WorkspaceSvc {
    exec: Arc<PodmanExec>,
}

impl WorkspaceSvc {
    pub fn new(exec: Arc<PodmanExec>) -> Self {
        Self { exec }
    }
}

// The `WorkspaceIo` trait mirrors tonic 0.12's generated signatures, whose
// `Result` error variant (`tonic::Status`) trips clippy's `result_large_err`
// lint. The shape is not ours to change, so silence the lint for this impl only.
#[allow(clippy::result_large_err)]
#[tonic::async_trait]
impl WorkspaceIo for WorkspaceSvc {
    type OpenTerminalStream = terminal::TerminalStream;
    type ReadFileStream = ReceiverStream<Result<FileChunk, Status>>;
    type WatchChangesStream = ReceiverStream<Result<FileEvent, Status>>;

    async fn open_terminal(
        &self,
        req: Request<tonic::Streaming<TerminalClientFrame>>,
    ) -> Result<Response<Self::OpenTerminalStream>, Status> {
        terminal::open(self.exec.clone(), req.into_inner()).await
    }

    async fn list_dir(
        &self,
        _r: Request<ListDirRequest>,
    ) -> Result<Response<ListDirResponse>, Status> {
        Err(Status::unimplemented("files: a later phase"))
    }

    async fn read_file(
        &self,
        _r: Request<ReadFileRequest>,
    ) -> Result<Response<Self::ReadFileStream>, Status> {
        Err(Status::unimplemented("files: a later phase"))
    }

    async fn write_file(
        &self,
        _r: Request<tonic::Streaming<WriteFileFrame>>,
    ) -> Result<Response<WriteFileResponse>, Status> {
        Err(Status::unimplemented("files: a later phase"))
    }

    async fn create_node(&self, _r: Request<CreateNodeRequest>) -> Result<Response<Node>, Status> {
        Err(Status::unimplemented("files: a later phase"))
    }

    async fn delete_node(
        &self,
        _r: Request<DeleteNodeRequest>,
    ) -> Result<Response<DeleteNodeResponse>, Status> {
        Err(Status::unimplemented("files: a later phase"))
    }

    async fn rename_node(&self, _r: Request<RenameNodeRequest>) -> Result<Response<Node>, Status> {
        Err(Status::unimplemented("files: a later phase"))
    }

    async fn watch_changes(
        &self,
        _r: Request<WatchRequest>,
    ) -> Result<Response<Self::WatchChangesStream>, Status> {
        Err(Status::unimplemented("files: a later phase"))
    }
}

pub async fn serve<S>(cfg: Config, exec: PodmanExec, shutdown: S) -> Result<()>
where
    S: Future<Output = ()> + Send + 'static,
{
    let addr = cfg
        .grpc_listen_addr
        .parse()
        .context("parse grpc listen addr")?;
    let tls_config = tls::server_config(&cfg.tls)?;
    tracing::info!(%addr, "workspace grpc listening");
    Server::builder()
        .tls_config(tls_config)?
        .add_service(WorkspaceIoServer::new(WorkspaceSvc::new(Arc::new(exec))))
        .serve_with_shutdown(addr, shutdown)
        .await
        .context("grpc serve")?;
    Ok(())
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::sync::Arc;

    // The unimplemented RPCs never touch the exec, but WorkspaceSvc needs one.
    // Build a real PodmanExec only when the gated socket is available.
    fn svc_or_skip() -> Option<WorkspaceSvc> {
        if std::env::var("HEARTH_PODMAN_IT").as_deref() != Ok("1") {
            return None;
        }
        let exec = crate::engine::PodmanExec::connect(
            std::env::var("HEARTH_PODMAN_SOCKET").ok().as_deref(),
        )
        .ok()?;
        Some(WorkspaceSvc::new(Arc::new(exec)))
    }

    #[tokio::test]
    async fn list_dir_is_unimplemented() {
        let Some(svc) = svc_or_skip() else {
            eprintln!("skipped: set HEARTH_PODMAN_IT=1");
            return;
        };
        let err = svc
            .list_dir(tonic::Request::new(
                hearth_proto::hearth::v1::ListDirRequest {
                    workspace_id: "w1".into(),
                    path: "/".into(),
                },
            ))
            .await
            .unwrap_err();
        assert_eq!(err.code(), tonic::Code::Unimplemented);
    }
}
