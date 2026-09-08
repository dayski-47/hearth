//! The inbound gRPC surface the gateway dials for terminal and file traffic.

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

use crate::{config::Config, engine::PodmanExec, files::Files, terminal, tls};

/// Largest file `read_file` will stream back. A workspace is for source, not
/// for shipping build artefacts down the wire.
const READ_CAP: u64 = 10 * 1024 * 1024;

pub struct WorkspaceSvc {
    exec: Arc<PodmanExec>,
    files: Arc<Files>,
}

impl WorkspaceSvc {
    pub fn new(exec: Arc<PodmanExec>, files: Arc<Files>) -> Self {
        Self { exec, files }
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
        r: Request<ListDirRequest>,
    ) -> Result<Response<ListDirResponse>, Status> {
        let r = r.into_inner();
        let entries = self.files.list_dir(&r.workspace_id, &r.path).await?;
        Ok(Response::new(ListDirResponse { entries }))
    }

    async fn read_file(
        &self,
        r: Request<ReadFileRequest>,
    ) -> Result<Response<Self::ReadFileStream>, Status> {
        let r = r.into_inner();
        let stream = self.files.read_file(&r.workspace_id, &r.path).await?;
        Ok(Response::new(stream))
    }

    async fn write_file(
        &self,
        r: Request<tonic::Streaming<WriteFileFrame>>,
    ) -> Result<Response<WriteFileResponse>, Status> {
        let bytes_written = self.files.write_file(r.into_inner()).await?;
        Ok(Response::new(WriteFileResponse { bytes_written }))
    }

    async fn create_node(&self, r: Request<CreateNodeRequest>) -> Result<Response<Node>, Status> {
        let r = r.into_inner();
        let node = self
            .files
            .create_node(&r.workspace_id, &r.path, r.is_dir)
            .await?;
        Ok(Response::new(node))
    }

    async fn delete_node(
        &self,
        r: Request<DeleteNodeRequest>,
    ) -> Result<Response<DeleteNodeResponse>, Status> {
        let r = r.into_inner();
        self.files.delete_node(&r.workspace_id, &r.path).await?;
        Ok(Response::new(DeleteNodeResponse {}))
    }

    async fn rename_node(&self, r: Request<RenameNodeRequest>) -> Result<Response<Node>, Status> {
        let r = r.into_inner();
        let node = self
            .files
            .rename_node(&r.workspace_id, &r.from, &r.to)
            .await?;
        Ok(Response::new(node))
    }

    async fn watch_changes(
        &self,
        r: Request<WatchRequest>,
    ) -> Result<Response<Self::WatchChangesStream>, Status> {
        let r = r.into_inner();
        let stream = self.files.watch(&r.workspace_id).await?;
        Ok(Response::new(stream))
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
    let exec = Arc::new(exec);
    let files = Arc::new(Files::new(exec.docker().clone(), READ_CAP));
    tracing::info!(%addr, "workspace grpc listening");
    Server::builder()
        .tls_config(tls_config)?
        .add_service(WorkspaceIoServer::new(WorkspaceSvc::new(exec, files)))
        .serve_with_shutdown(addr, shutdown)
        .await
        .context("grpc serve")?;
    Ok(())
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::sync::Arc;

    // The file RPCs need a real Files (and so a real container-engine socket) to
    // reach a volume. Build the whole service only when the gated socket is
    // available.
    fn svc_or_skip() -> Option<WorkspaceSvc> {
        if std::env::var("HEARTH_PODMAN_IT").as_deref() != Ok("1") {
            return None;
        }
        let exec = crate::engine::PodmanExec::connect(
            std::env::var("HEARTH_PODMAN_SOCKET").ok().as_deref(),
        )
        .ok()?;
        let exec = Arc::new(exec);
        let files = Arc::new(Files::new(exec.docker().clone(), READ_CAP));
        Some(WorkspaceSvc::new(exec, files))
    }

    #[tokio::test]
    async fn list_dir_needs_a_workspace() {
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
        // No volume named `hearth-ws-w1` exists, so the inspect 404s.
        assert_eq!(err.code(), tonic::Code::NotFound);
    }
}
